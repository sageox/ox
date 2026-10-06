package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractTeamIDFromRepoName_TeamUnderscorePrefix(t *testing.T) {
	assert.Equal(t, "team_abc", extractTeamIDFromRepoName("team_abc"))
	assert.Equal(t, "team_abc", extractTeamIDFromRepoName("team_abc-context"))
	assert.Equal(t, "team_abc", extractTeamIDFromRepoName("team_abc-team-context"))
}

func TestExtractTeamIDFromRepoName_TeamDashPrefix(t *testing.T) {
	assert.Equal(t, "team_abc", extractTeamIDFromRepoName("team-abc"))
	assert.Equal(t, "team_abc", extractTeamIDFromRepoName("team-abc-context"))
}

func TestExtractTeamIDFromRepoName_TeamContextSuffix(t *testing.T) {
	assert.Equal(t, "team_myteam", extractTeamIDFromRepoName("myteam-team-context"))
}

func TestExtractTeamIDFromRepoName_Empty(t *testing.T) {
	assert.Equal(t, "", extractTeamIDFromRepoName(""))
}

func TestExtractTeamIDFromRepoName_NoPattern(t *testing.T) {
	assert.Equal(t, "", extractTeamIDFromRepoName("some-random-repo"))
	assert.Equal(t, "", extractTeamIDFromRepoName("myproject"))
}

func TestExtractTeamIDFromRepoName_CaseInsensitive(t *testing.T) {
	assert.Equal(t, "team_abc", extractTeamIDFromRepoName("Team_ABC"))
	assert.Equal(t, "team_abc", extractTeamIDFromRepoName("TEAM-ABC"))
}

func TestHasLocalGitChanges_CleanRepo(t *testing.T) {
	tmp := t.TempDir()
	cmd := exec.Command("git", "init", tmp)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null") // safe: isolating git init in temp dir
	require.NoError(t, cmd.Run())

	// configure git identity in the temp repo
	for _, args := range [][]string{
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		c := exec.Command("git", append([]string{"-C", tmp}, args...)...)
		require.NoError(t, c.Run())
	}

	// create initial commit so HEAD exists
	emptyFile := filepath.Join(tmp, ".gitkeep")
	require.NoError(t, os.WriteFile(emptyFile, []byte(""), 0644))
	c := exec.Command("git", "-C", tmp, "add", ".gitkeep")
	require.NoError(t, c.Run())
	c = exec.Command("git", "-C", tmp, "commit", "-m", "init")
	require.NoError(t, c.Run())

	assert.False(t, hasLocalGitChanges(tmp))
}

func TestHasLocalGitChanges_DirtyRepo(t *testing.T) {
	tmp := t.TempDir()
	cmd := exec.Command("git", "init", tmp)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null") // safe: isolating git init in temp dir
	require.NoError(t, cmd.Run())

	for _, args := range [][]string{
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		c := exec.Command("git", append([]string{"-C", tmp}, args...)...)
		require.NoError(t, c.Run())
	}

	emptyFile := filepath.Join(tmp, ".gitkeep")
	require.NoError(t, os.WriteFile(emptyFile, []byte(""), 0644))
	c := exec.Command("git", "-C", tmp, "add", ".gitkeep")
	require.NoError(t, c.Run())
	c = exec.Command("git", "-C", tmp, "commit", "-m", "init")
	require.NoError(t, c.Run())

	// create uncommitted file
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "dirty.txt"), []byte("changes"), 0644))
	assert.True(t, hasLocalGitChanges(tmp))
}

func TestHasLocalGitChanges_NotAGitRepo(t *testing.T) {
	tmp := t.TempDir()
	// not a git repo - should return true (conservative default)
	assert.True(t, hasLocalGitChanges(tmp))
}

// Doctor must retain and verify a PAT's bearer binding, even when the old PAT
// remains accepted by Git. Failed refreshes preserve the cache without probing it.
func TestSaveGitCredentialsFromRepos_BindsBearer(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("real Git liveness probes and CGI backend require Unix process semantics")
	}
	for _, outcome := range []string{"matching bearer", "rotation repaired", "rotation rejected", "offline checkout", "malformed bearer"} {
		t.Run(outcome, func(t *testing.T) {
			gitRoot := t.TempDir()
			hostedTestGit(t, gitRoot, "init", "--bare", filepath.Join(gitRoot, "ledger.git"))
			backend := &cgi.Handler{
				Path: filepath.Join(hostedTestGit(t, gitRoot, "--exec-path"), "git-http-backend"),
				Env:  []string{"GIT_PROJECT_ROOT=" + gitRoot, "GIT_HTTP_EXPORT_ALL=1"},
			}
			var refreshes, oldProbes, freshProbes atomic.Int32
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case auth.IntrospectEndpoint:
					_, _ = w.Write([]byte(`{"active":true,"principal_kind":"team-service","team":{"team_id":"team_test"}}`))
				case "/api/v1/cli/repos":
					refreshes.Add(1)
					assert.Equal(t, "Bearer "+validTeamToken, r.Header.Get("Authorization"))
					if outcome == "rotation rejected" {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					_ = json.NewEncoder(w).Encode(api.ReposResponse{
						Token: "fresh-pat", ServerURL: server.URL, ExpiresAt: time.Now().Add(24 * time.Hour),
						Repos: map[string]api.RepoInfo{"team_test": {Name: "Doctor Team", Type: "team-context", TeamID: "team_test", Slug: "doctor-team", URL: server.URL + "/ledger.git"}},
					})
				default:
					_, pat, _ := r.BasicAuth()
					if strings.HasSuffix(r.URL.Path, "/info/refs") {
						if pat == "doctor-pat" {
							oldProbes.Add(1)
						}
						if pat == "fresh-pat" {
							freshProbes.Add(1)
						}
					}
					if pat == "doctor-pat" || pat == "fresh-pat" {
						backend.ServeHTTP(w, r)
						return
					}
					w.Header().Set("WWW-Authenticate", `Basic realm="ledger"`)
					w.WriteHeader(http.StatusUnauthorized)
				}
			}))
			t.Cleanup(server.Close)
			bearer := validTeamToken
			switch outcome {
			case "offline checkout":
				bearer = ""
			case "malformed bearer":
				bearer = "oxt_truncated"
			}
			setupAuthRenderEnv(t, server.URL, bearer)
			root := createInitializedProjectWithConfig(t, nil)
			hostedTestGit(t, root, "init")
			t.Chdir(root)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			ledgerPath := filepath.Join(gitRoot, "ledger.git")
			const oldURL = "https://oauth2:doctor-pat@127.0.0.1/ledger.git"
			hostedTestGit(t, ledgerPath, "remote", "add", "origin", oldURL)
			require.NoError(t, config.SaveLocalConfig(root, &config.LocalConfig{
				Ledger: &config.LedgerConfig{Path: ledgerPath},
			}))
			boundBearer := validTeamToken
			rotated := strings.HasPrefix(outcome, "rotation")
			if rotated {
				boundBearer = "oxt_rotated_1lKvCA"
			}
			require.NoError(t, saveGitCredentialsFromRepos(nil, server.URL, boundBearer))
			require.NoError(t, saveGitCredentialsFromRepos(&api.ReposResponse{
				Token: "doctor-pat", ServerURL: server.URL, ExpiresAt: time.Now().Add(24 * time.Hour),
				Repos: map[string]api.RepoInfo{"team_test": {Name: "Doctor Team", Type: "team-context", TeamID: "team_test", Slug: "doctor-team", URL: server.URL + "/ledger.git"}},
			}, server.URL, boundBearer))
			cached, err := gitserver.LoadCredentialsForEndpoint(server.URL)
			require.NoError(t, err)
			require.NotNil(t, cached)
			require.Equal(t, gitserver.BearerTokenFingerprint(boundBearer), cached.BearerTokenHash)
			require.Equal(t, "doctor-team", cached.GetRepo("team_test").Slug)
			assert.True(t, checkCredentialIntegrityInDir(config.GetUserConfigDir(), true).passed)

			if outcome == "malformed bearer" {
				for _, result := range []checkResult{
					checkGitCredentials(), checkGitCredentialsFreshness(false),
					checkGitCredentialsFreshness(true), checkGitPATLiveness(false), checkGitPATLiveness(true),
				} {
					assert.False(t, result.passed && !result.warning)
					assert.Contains(t, result.detail, "Rotate or re-mint")
					assert.NotContains(t, result.detail, "ox login")
				}
				require.Zero(t, refreshes.Load())
				require.Zero(t, oldProbes.Load(), "malformed bearers must not probe a cached PAT")
				stored, err := gitserver.LoadCredentialsForEndpoint(server.URL)
				require.NoError(t, err)
				assert.Equal(t, cached, stored)
				assert.Equal(t, oldURL, hostedTestGit(t, ledgerPath, "remote", "get-url", "origin"))
				return
			}

			if !rotated {
				creds, err := auth.RefreshGitCredentialsForEndpoint(context.Background(), server.URL, false)
				require.NoError(t, err)
				require.Equal(t, cached.Token, creds.Token)
				assert.True(t, checkGitCredentials().passed)
				assert.True(t, checkGitCredentialsFreshness(false).passed)
				assert.True(t, checkGitPATLiveness(false).passed)
				require.Zero(t, refreshes.Load(), "matching and offline caches need no API refresh")
				require.EqualValues(t, 1, oldProbes.Load())
				assert.Equal(t, oldURL, hostedTestGit(t, ledgerPath, "remote", "get-url", "origin"))
				return
			}

			assert.True(t, checkAuthentication().passed, "the current bearer is valid")
			assert.True(t, checkGitCredentialsFreshness(false).warning, "freshness alone must not validate another bearer's PAT")
			assert.False(t, checkGitPATLiveness(false).passed, "check-only must flag the binding without probing the old PAT")
			require.Zero(t, refreshes.Load())
			require.Zero(t, oldProbes.Load())
			for _, check := range []struct {
				name string
				run  func() checkResult
			}{
				{"freshness repair", func() checkResult { return checkGitCredentialsFreshness(true) }},
				{"liveness repair", func() checkResult { return checkGitPATLiveness(true) }},
				{"automatic credential repair", checkGitCredentials},
			} {
				t.Run(check.name, func(t *testing.T) {
					require.NoError(t, gitserver.SaveCredentialsForEndpoint(server.URL, *cached))
					hostedTestGit(t, ledgerPath, "remote", "set-url", "origin", oldURL)
					before := refreshes.Load()
					result := check.run()
					assert.Equal(t, outcome == "rotation repaired", result.passed && !result.warning)
					require.Equal(t, before+1, refreshes.Load(), "each repair must resolve the current bearer")
					assert.Zero(t, oldProbes.Load(), "an old bearer's accepted PAT must never be probed")
					stored, err := gitserver.LoadCredentialsForEndpoint(server.URL)
					require.NoError(t, err)
					if outcome == "rotation rejected" {
						assert.Contains(t, result.detail, "Rotate or re-mint")
						assert.Equal(t, cached, stored, "a rejected refresh must preserve the cache")
						assert.Zero(t, freshProbes.Load())
						assert.Equal(t, oldURL, hostedTestGit(t, ledgerPath, "remote", "get-url", "origin"))
					} else {
						if check.name != "liveness repair" {
							assert.Contains(t, result.message, "refreshed, 1 repos (expires in")
						}
						assert.Equal(t, "fresh-pat", stored.Token)
						assert.Equal(t, gitserver.BearerTokenFingerprint(validTeamToken), stored.BearerTokenHash)
						assert.Equal(t, "https://127.0.0.1/ledger.git", hostedTestGit(t, ledgerPath, "remote", "get-url", "origin"))
						assert.Contains(t, hostedTestGit(t, ledgerPath, "config", "--get", "credential.https://127.0.0.1.helper"), "git-credential-helper")
					}
				})
			}
			if outcome == "rotation repaired" {
				assert.EqualValues(t, 1, freshProbes.Load(), "liveness repair must probe the returned fresh PAT")
			}
		})
	}
}

// TestValidateRepoPath exercises the repo path validator from doctor_git_repos_validate.go
func TestValidateRepoPath_Missing(t *testing.T) {
	result := validateRepoPath("/nonexistent/path/abc123")
	assert.Equal(t, "missing", result)
}

func TestValidateRepoPath_NotDirectory(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "file.txt")
	require.NoError(t, os.WriteFile(f, []byte("data"), 0644))
	assert.Equal(t, "not-directory", validateRepoPath(f))
}

func TestValidateRepoPath_EmptyDir(t *testing.T) {
	tmp := t.TempDir()
	assert.Equal(t, "empty-dir", validateRepoPath(tmp))
}

func TestValidateRepoPath_NotGitRepo(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "file.txt"), []byte("data"), 0644))
	assert.Equal(t, "not-git-repo", validateRepoPath(tmp))
}

func TestValidateRepoPath_ValidGitRepo(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(tmp, ".git"), 0755))
	assert.Equal(t, "", validateRepoPath(tmp))
}

func TestIsValidGitURL_Valid(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"git@github.com:org/repo.git", true},
		{"https://github.com/org/repo.git", true},
		{"http://github.com/org/repo.git", true},
		{"ssh://git@github.com/org/repo.git", true},
		{"git://github.com/org/repo.git", true},
		{"file:///tmp/repo", true},
		{"", false},
		{"ftp://example.com/repo", false},
		{"just-a-name", false},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			assert.Equal(t, tt.want, isValidGitURL(tt.url))
		})
	}
}

func TestNormalizeGitURLForCompare_HTTPS(t *testing.T) {
	result := normalizeGitURLForCompare("https://github.com/org/repo.git")
	assert.Equal(t, "github.com/org/repo", result)
}

func TestNormalizeGitURLForCompare_SSH(t *testing.T) {
	result := normalizeGitURLForCompare("git@github.com:org/repo.git")
	assert.Equal(t, "github.com/org/repo", result)
}

func TestNormalizeGitURLForCompare_WithCredentials(t *testing.T) {
	result := normalizeGitURLForCompare("https://oauth2:TOKEN@gitlab.com/org/repo.git")
	assert.Equal(t, "gitlab.com/org/repo", result)
}

func TestNormalizeGitURLForCompare_CaseInsensitive(t *testing.T) {
	result := normalizeGitURLForCompare("HTTPS://GitHub.com/Org/Repo.git")
	assert.Equal(t, "github.com/org/repo", result)
}

func TestNormalizeGitURLForCompare_NoSuffix(t *testing.T) {
	result := normalizeGitURLForCompare("https://github.com/org/repo")
	assert.Equal(t, "github.com/org/repo", result)
}

func TestIsRecentlyInitialized_NoConfigFile(t *testing.T) {
	tmp := t.TempDir()
	assert.False(t, isRecentlyInitialized(tmp))
}

func TestIsRecentlyInitialized_RecentConfig(t *testing.T) {
	tmp := t.TempDir()
	sageoxDir := filepath.Join(tmp, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	configPath := filepath.Join(sageoxDir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{}"), 0644))
	// file just created, so it's recent
	assert.True(t, isRecentlyInitialized(tmp))
}

// TestDoctorCheckResultConstructors verifies the check result factory functions
func TestDoctorPassedCheck(t *testing.T) {
	c := PassedCheck("test", "all good")
	assert.True(t, c.passed)
	assert.False(t, c.warning)
	assert.False(t, c.skipped)
	assert.Equal(t, "test", c.name)
	assert.Equal(t, "all good", c.message)
}

func TestDoctorFailedCheck(t *testing.T) {
	c := FailedCheck("test", "broken", "run fix")
	assert.False(t, c.passed)
	assert.Equal(t, "broken", c.message)
	assert.Equal(t, "run fix", c.detail)
}

func TestDoctorWarningCheck(t *testing.T) {
	c := WarningCheck("test", "hmm", "might want to check")
	assert.True(t, c.passed)
	assert.True(t, c.warning)
}

func TestDoctorSkippedCheck(t *testing.T) {
	c := SkippedCheck("test", "not applicable", "")
	assert.True(t, c.skipped)
}

func TestDoctorCriticalCheck(t *testing.T) {
	c := CriticalCheck("test", "fatal", "fix now")
	assert.False(t, c.passed)
	assert.Equal(t, "critical", c.priority)
}

func TestDoctorInfoCheck(t *testing.T) {
	c := InfoCheck("test", "fyi", "optional")
	assert.True(t, c.passed)
	assert.True(t, c.warning)
	assert.Equal(t, "info", c.priority)
}

func TestDoctorAgentRequiredCheck(t *testing.T) {
	c := AgentRequiredCheck("test", "needs agent", "run ox agent doctor")
	assert.True(t, c.passed)
	assert.True(t, c.warning)
	assert.True(t, c.requiresAgent)
}

func TestCheckResult_WithFixInfo(t *testing.T) {
	c := FailedCheck("test", "broken", "fix it")
	c = c.WithFixInfo("test-slug", FixLevelSuggested)
	assert.Equal(t, "test-slug", c.slug)
	assert.Equal(t, FixLevelSuggested, c.fixLevel)
}

func TestCheckResult_WithRequiresAgent(t *testing.T) {
	c := WarningCheck("test", "needs help", "")
	c = c.WithRequiresAgent()
	assert.True(t, c.requiresAgent)
}

func TestCountCheck_Pass(t *testing.T) {
	var p, w, f, s int
	countCheck(PassedCheck("x", "ok"), &p, &w, &f, &s)
	assert.Equal(t, 1, p)
	assert.Equal(t, 0, w)
	assert.Equal(t, 0, f)
	assert.Equal(t, 0, s)
}

func TestCountCheck_Warning(t *testing.T) {
	var p, w, f, s int
	countCheck(WarningCheck("x", "warn", ""), &p, &w, &f, &s)
	assert.Equal(t, 0, p)
	assert.Equal(t, 1, w)
}

func TestCountCheck_Fail(t *testing.T) {
	var p, w, f, s int
	countCheck(FailedCheck("x", "bad", ""), &p, &w, &f, &s)
	assert.Equal(t, 1, f)
}

func TestCountCheck_Skip(t *testing.T) {
	var p, w, f, s int
	countCheck(SkippedCheck("x", "skip", ""), &p, &w, &f, &s)
	assert.Equal(t, 1, s)
}

func TestCategorizeCheck_SkippedIgnored(t *testing.T) {
	var crit, attn, opt, agent []checkResult
	hasFailed := false
	categorizeCheck(SkippedCheck("x", "skip", ""), &crit, &attn, &opt, &agent, &hasFailed)
	assert.Empty(t, crit)
	assert.Empty(t, attn)
	assert.Empty(t, opt)
	assert.Empty(t, agent)
	assert.False(t, hasFailed)
}

func TestCategorizeCheck_CriticalFail(t *testing.T) {
	var crit, attn, opt, agent []checkResult
	hasFailed := false
	categorizeCheck(CriticalCheck("x", "fatal", ""), &crit, &attn, &opt, &agent, &hasFailed)
	assert.Len(t, crit, 1)
	assert.True(t, hasFailed)
}

func TestCategorizeCheck_RegularFail(t *testing.T) {
	var crit, attn, opt, agent []checkResult
	hasFailed := false
	categorizeCheck(FailedCheck("x", "bad", ""), &crit, &attn, &opt, &agent, &hasFailed)
	assert.Len(t, attn, 1)
	assert.True(t, hasFailed)
}

func TestCategorizeCheck_InfoWarning(t *testing.T) {
	var crit, attn, opt, agent []checkResult
	hasFailed := false
	categorizeCheck(InfoCheck("x", "fyi", ""), &crit, &attn, &opt, &agent, &hasFailed)
	assert.Len(t, opt, 1)
	assert.False(t, hasFailed)
}

func TestCategorizeCheck_RegularWarning(t *testing.T) {
	var crit, attn, opt, agent []checkResult
	hasFailed := false
	categorizeCheck(WarningCheck("x", "warn", ""), &crit, &attn, &opt, &agent, &hasFailed)
	assert.Len(t, attn, 1)
}

func TestCategorizeCheck_AgentRequired(t *testing.T) {
	var crit, attn, opt, agent []checkResult
	hasFailed := false
	c := AgentRequiredCheck("x", "needs agent", "")
	categorizeCheck(c, &crit, &attn, &opt, &agent, &hasFailed)
	assert.Len(t, agent, 1)
	assert.False(t, hasFailed) // warning-level agent checks don't set hasFailed
}

func TestCategorizeCheck_AgentRequiredFail(t *testing.T) {
	var crit, attn, opt, agent []checkResult
	hasFailed := false
	c := checkResult{name: "x", passed: false, requiresAgent: true, message: "broken"}
	categorizeCheck(c, &crit, &attn, &opt, &agent, &hasFailed)
	assert.Len(t, agent, 1)
	assert.True(t, hasFailed)
}

func TestHasRequiresAgentIssues_Empty(t *testing.T) {
	assert.False(t, hasRequiresAgentIssues(nil))
	assert.False(t, hasRequiresAgentIssues([]checkCategory{}))
}

func TestHasRequiresAgentIssues_NoAgentChecks(t *testing.T) {
	cats := []checkCategory{
		{name: "test", checks: []checkResult{
			PassedCheck("ok", "fine"),
			FailedCheck("bad", "broken", "fix"),
		}},
	}
	assert.False(t, hasRequiresAgentIssues(cats))
}

func TestHasRequiresAgentIssues_HasAgentWarning(t *testing.T) {
	cats := []checkCategory{
		{name: "test", checks: []checkResult{
			AgentRequiredCheck("x", "needs agent", "run ox agent doctor"),
		}},
	}
	assert.True(t, hasRequiresAgentIssues(cats))
}

func TestHasRequiresAgentIssues_AgentCheckInChildren(t *testing.T) {
	parent := PassedCheck("parent", "ok")
	parent.children = []checkResult{
		AgentRequiredCheck("child", "needs agent", ""),
	}
	cats := []checkCategory{
		{name: "test", checks: []checkResult{parent}},
	}
	assert.True(t, hasRequiresAgentIssues(cats))
}

func TestHasRequiresAgentIssues_SkippedAgentCheck(t *testing.T) {
	c := checkResult{name: "x", skipped: true, requiresAgent: true}
	cats := []checkCategory{
		{name: "test", checks: []checkResult{c}},
	}
	assert.False(t, hasRequiresAgentIssues(cats))
}

func TestCollectFixableSlugs_PassedCheck(t *testing.T) {
	var slugs []fixSlugInfo
	collectFixableSlugs(PassedCheck("x", "ok"), &slugs)
	assert.Empty(t, slugs)
}

func TestCollectFixableSlugs_FailedWithSlug(t *testing.T) {
	var slugs []fixSlugInfo
	c := FailedCheck("x", "bad", "").WithFixInfo("my-slug", FixLevelSuggested)
	collectFixableSlugs(c, &slugs)
	assert.Len(t, slugs, 1)
	assert.Equal(t, "my-slug", slugs[0].slug)
	assert.Equal(t, FixLevelSuggested, slugs[0].fixLevel)
}

func TestCollectFixableSlugs_SkippedIgnored(t *testing.T) {
	var slugs []fixSlugInfo
	c := SkippedCheck("x", "skip", "")
	c.slug = "some-slug"
	c.fixLevel = FixLevelSuggested
	collectFixableSlugs(c, &slugs)
	assert.Empty(t, slugs)
}

func TestCollectFixableSlugs_CheckOnlyIgnored(t *testing.T) {
	var slugs []fixSlugInfo
	c := FailedCheck("x", "bad", "").WithFixInfo("slug", FixLevelCheckOnly)
	collectFixableSlugs(c, &slugs)
	assert.Empty(t, slugs)
}

func TestCollectFixableSlugs_WarningWithSlug(t *testing.T) {
	var slugs []fixSlugInfo
	c := WarningCheck("x", "warn", "").WithFixInfo("warn-slug", FixLevelAuto)
	collectFixableSlugs(c, &slugs)
	assert.Len(t, slugs, 1)
}

func TestFixableSlugsHint_Empty(t *testing.T) {
	assert.Equal(t, "", fixableSlugsHint(nil))
}

func TestFixableSlugsHint_NonEmpty(t *testing.T) {
	slugs := []fixSlugInfo{{slug: "a", fixLevel: FixLevelSuggested}}
	hint := fixableSlugsHint(slugs)
	assert.Contains(t, hint, "ox doctor --fix")
}

func TestMessageAnnotation_Empty(t *testing.T) {
	assert.Equal(t, "", messageAnnotation(""))
}

func TestMessageAnnotation_NonEmpty(t *testing.T) {
	result := messageAnnotation("hello")
	assert.Contains(t, result, "hello")
}

func TestCheckResultToJSON_Passed(t *testing.T) {
	c := PassedCheck("test-check", "all good")
	j := checkResultToJSON(c)
	assert.Equal(t, "passed", j.Status)
	assert.Equal(t, "test-check", j.Name)
	assert.Equal(t, "all good", j.Message)
}

func TestCheckResultToJSON_Failed(t *testing.T) {
	c := FailedCheck("test-check", "broken", "fix it")
	j := checkResultToJSON(c)
	assert.Equal(t, "failed", j.Status)
	assert.Equal(t, "fix it", j.Detail)
}

func TestCheckResultToJSON_Warning(t *testing.T) {
	c := WarningCheck("test-check", "hmm", "look into")
	j := checkResultToJSON(c)
	assert.Equal(t, "warning", j.Status)
}

func TestCheckResultToJSON_Skipped(t *testing.T) {
	c := SkippedCheck("test-check", "n/a", "")
	j := checkResultToJSON(c)
	assert.Equal(t, "skipped", j.Status)
}

func TestCheckResultToJSON_WithChildren(t *testing.T) {
	c := PassedCheck("parent", "ok")
	c.children = []checkResult{
		FailedCheck("child1", "bad", ""),
		PassedCheck("child2", "ok"),
	}
	j := checkResultToJSON(c)
	assert.Len(t, j.Children, 2)
	assert.Equal(t, "failed", j.Children[0].Status)
	assert.Equal(t, "passed", j.Children[1].Status)
}

func TestCheckResultToJSON_WithMetadata(t *testing.T) {
	c := FailedCheck("test", "broken", "detail")
	c.slug = "my-slug"
	c.fixLevel = FixLevelAuto
	c.priority = "critical"
	c.requiresAgent = true
	j := checkResultToJSON(c)
	assert.Equal(t, "my-slug", j.Slug)
	assert.Equal(t, "auto", j.FixLevel)
	assert.Equal(t, "critical", j.Priority)
	assert.True(t, j.RequiresAgent)
}

func TestEnrichCheckResult_AlreadyEnriched(t *testing.T) {
	c := PassedCheck("test", "ok")
	c.slug = "already-set"
	enrichCheckResult(&c)
	// slug should remain unchanged
	assert.Equal(t, "already-set", c.slug)
}
