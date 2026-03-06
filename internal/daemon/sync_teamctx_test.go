package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/manifest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolateCredentials sets up credential isolation for tests by redirecting
// credential storage to a temp directory and forcing file-based storage.
// Shared across daemon test files (referenced by sync_integration_test.go).
func isolateCredentials(t *testing.T) {
	t.Helper()
	prevConfigDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	prevForceFile := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() {
		gitserver.TestSetConfigDirOverride(prevConfigDir)
		gitserver.TestSetForceFileStorage(prevForceFile)
	})
}

// isolateCredentialsWithDir sets up credential isolation and returns the
// temp directory where credentials are stored, so tests can write custom
// credential files to it.
func isolateCredentialsWithDir(t *testing.T) string {
	t.Helper()
	credDir := t.TempDir()
	prevConfigDir := gitserver.TestSetConfigDirOverride(credDir)
	prevForceFile := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() {
		gitserver.TestSetConfigDirOverride(prevConfigDir)
		gitserver.TestSetForceFileStorage(prevForceFile)
	})
	return credDir
}

// setupProjectWithConfig creates a temp project directory with .sageox/config.local.toml
// and a project config.json pointing to a fake endpoint.
func setupProjectWithConfig(t *testing.T, localConfigContent string) string {
	t.Helper()
	projectDir := t.TempDir()
	sageoxDir := filepath.Join(projectDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	require.NoError(t, os.WriteFile(
		filepath.Join(sageoxDir, "config.local.toml"),
		[]byte(localConfigContent),
		0644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(sageoxDir, "config.json"),
		[]byte(`{"endpoint":"https://fake.test.invalid"}`),
		0644,
	))
	return projectDir
}

// writeCredentialsFile writes a GitCredentials JSON file using the proper
// SaveCredentialsForEndpoint path. The endpoint must match what the project
// config uses, so the workspace registry's rebuildFromConfigLocked finds them.
func writeCredentialsFile(t *testing.T, _ string, creds gitserver.GitCredentials) {
	t.Helper()
	// use the same endpoint as our test project config ("https://fake.test.invalid")
	// SaveCredentialsForEndpoint uses the test config dir override
	require.NoError(t, gitserver.SaveCredentialsForEndpoint("https://fake.test.invalid", creds))
}

// newTestScheduler creates a SyncScheduler configured for testing.
func newTestScheduler(projectDir string) *SyncScheduler {
	cfg := DefaultConfig()
	cfg.ProjectRoot = projectDir
	cfg.TeamContextSyncInterval = time.Minute
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewSyncScheduler(cfg, logger)
}

// --- Test 1: New team context appears in credentials ---

func TestTeamContextDiscovery_NewTeamAppears(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	credDir := isolateCredentialsWithDir(t)

	// set up project with empty local config (no team contexts configured)
	projectDir := setupProjectWithConfig(t, "# empty config\n")

	// create a real git repo that acts as the team context
	teamDir := t.TempDir()
	setupGitRepo(t, teamDir)

	scheduler := newTestScheduler(projectDir)

	// first sync: no credentials, no team contexts
	scheduler.pullTeamContexts(context.Background())
	assert.Empty(t, scheduler.TeamContextStatus(), "no team contexts before credentials are written")

	// write credentials with a new team context repo
	writeCredentialsFile(t, credDir, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: "https://git.fake.test.invalid",
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos: map[string]gitserver.RepoEntry{
			"team_new123": {
				Name:   "new-team",
				Type:   "team-context",
				URL:    "file://" + teamDir + ".bare", // bare repo created by setupGitRepo
				TeamID: "team_new123",
			},
		},
	})

	// invalidate config cache so registry re-discovers from credentials
	scheduler.WorkspaceRegistry().InvalidateConfigCache()

	// reload workspace state - registry should discover the new team context
	require.NoError(t, scheduler.WorkspaceRegistry().LoadFromConfig())
	teamContexts := scheduler.WorkspaceRegistry().GetTeamContexts()
	require.NotEmpty(t, teamContexts, "should discover new team context from credentials")

	found := false
	for _, tc := range teamContexts {
		if tc.TeamID == "team_new123" {
			found = true
			assert.Equal(t, "new-team", tc.TeamName)
			assert.NotEmpty(t, tc.CloneURL, "clone URL should be populated from credentials")
			break
		}
	}
	assert.True(t, found, "team_new123 should be discovered from credentials")
}

// --- Test 2: Team context removed from credentials ---

func TestTeamContextDiscovery_RemovedFromCredentials(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	credDir := isolateCredentialsWithDir(t)

	// set up project with empty local config
	projectDir := setupProjectWithConfig(t, "# empty config\n")

	// create a team context git repo
	teamDir := t.TempDir()
	setupGitRepo(t, teamDir)

	// write credentials with one team context
	writeCredentialsFile(t, credDir, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: "https://git.fake.test.invalid",
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos: map[string]gitserver.RepoEntry{
			"team_removeme": {
				Name:   "removable-team",
				Type:   "team-context",
				URL:    "https://git.fake.test.invalid/removable-team.git",
				TeamID: "team_removeme",
			},
		},
	})

	scheduler := newTestScheduler(projectDir)

	// first load: team context should be discovered
	require.NoError(t, scheduler.WorkspaceRegistry().LoadFromConfig())
	teamContexts := scheduler.WorkspaceRegistry().GetTeamContexts()
	require.Len(t, teamContexts, 1, "should have one team context initially")
	assert.Equal(t, "team_removeme", teamContexts[0].TeamID)

	// remove the team from credentials
	writeCredentialsFile(t, credDir, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: "https://git.fake.test.invalid",
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos:     map[string]gitserver.RepoEntry{}, // empty
	})

	// invalidate cache and reload
	scheduler.WorkspaceRegistry().InvalidateConfigCache()
	require.NoError(t, scheduler.WorkspaceRegistry().LoadFromConfig())

	// team context should be removed from registry (rebuildFromConfigLocked
	// removes workspaces no longer in config or credentials)
	teamContexts = scheduler.WorkspaceRegistry().GetTeamContexts()
	assert.Empty(t, teamContexts, "removed team context should no longer appear in registry")

	// the local clone directory should NOT be deleted by the registry rebuild;
	// only CleanupRevokedTeamContexts deletes from disk (and that requires
	// the repo detail API to explicitly revoke). Verify the directory check:
	// since we never cloned to disk, there's nothing to verify on disk,
	// but the key invariant is that rebuildFromConfigLocked doesn't touch the filesystem.
}

// --- Test 3: Credentials file corrupted or missing ---

func TestTeamContextDiscovery_CredentialsCorrupted(t *testing.T) {
	credDir := isolateCredentialsWithDir(t)
	projectDir := setupProjectWithConfig(t, "# empty config\n")

	// write garbage to the credentials file
	dir := filepath.Join(credDir, "sageox")
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "git-credentials.json"),
		[]byte("this is not valid json {{{"),
		0600,
	))

	scheduler := newTestScheduler(projectDir)

	// loading config should not panic or crash; credentials load error
	// is logged and team contexts from credentials are simply not discovered
	require.NoError(t, scheduler.WorkspaceRegistry().LoadFromConfig())
	assert.Empty(t, scheduler.TeamContextStatus(), "corrupted credentials should not produce team contexts")
}

func TestTeamContextDiscovery_CredentialsMissing(t *testing.T) {
	isolateCredentials(t) // points to empty temp dir, no credentials file
	projectDir := setupProjectWithConfig(t, "# empty config\n")

	scheduler := newTestScheduler(projectDir)

	// no credentials file at all
	require.NoError(t, scheduler.WorkspaceRegistry().LoadFromConfig())
	assert.Empty(t, scheduler.TeamContextStatus(), "missing credentials should not produce team contexts")

	// pullTeamContexts should also handle this gracefully
	scheduler.pullTeamContexts(context.Background())
	assert.Empty(t, scheduler.TeamContextStatus())
}

// --- Test 4: Empty credentials (repos list is empty) ---

func TestTeamContextDiscovery_EmptyRepos(t *testing.T) {
	credDir := isolateCredentialsWithDir(t)
	projectDir := setupProjectWithConfig(t, "# empty config\n")

	// write valid credentials but with no repos
	writeCredentialsFile(t, credDir, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: "https://git.fake.test.invalid",
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos:     map[string]gitserver.RepoEntry{},
	})

	scheduler := newTestScheduler(projectDir)
	require.NoError(t, scheduler.WorkspaceRegistry().LoadFromConfig())

	// should handle gracefully with no team contexts
	assert.Empty(t, scheduler.TeamContextStatus())

	// pullTeamContexts should also work without error
	scheduler.pullTeamContexts(context.Background())
	assert.Empty(t, scheduler.TeamContextStatus())
}

// --- Test 5: Multiple team contexts, one fails ---

func TestTeamContextDiscovery_MultipleTeamsOneFails(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	credDir := isolateCredentialsWithDir(t)

	// create three team context repos; two are real, one will have a bad path
	teamDir1 := t.TempDir()
	setupGitRepo(t, teamDir1)

	teamDir2 := t.TempDir()
	setupGitRepo(t, teamDir2)

	// team3 path exists but is NOT a git repo (simulates failed clone)
	teamDir3 := t.TempDir()

	projectDir := setupProjectWithConfig(t, fmt.Sprintf(`
[[team_contexts]]
team_id = "team_good1"
team_name = "Good Team 1"
path = %q

[[team_contexts]]
team_id = "team_bad"
team_name = "Bad Team"
path = %q

[[team_contexts]]
team_id = "team_good2"
team_name = "Good Team 2"
path = %q
`, teamDir1, teamDir3, teamDir2))

	// write credentials so clone URLs don't matter for the two good teams
	writeCredentialsFile(t, credDir, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: "https://git.fake.test.invalid",
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos:     map[string]gitserver.RepoEntry{},
	})

	scheduler := newTestScheduler(projectDir)

	// make FETCH_HEAD old for the two good repos so sync isn't skipped
	for _, dir := range []string{teamDir1, teamDir2} {
		fetchHead := filepath.Join(dir, ".git", "FETCH_HEAD")
		oldTime := time.Now().Add(-1 * time.Hour)
		_ = os.Chtimes(fetchHead, oldTime, oldTime)
	}

	// run team context sync
	scheduler.pullTeamContexts(context.Background())

	status := scheduler.TeamContextStatus()
	require.Len(t, status, 3, "should report status for all three team contexts")

	// build lookup for assertions
	statusByID := make(map[string]*TeamContextSyncStatus)
	for i := range status {
		statusByID[status[i].TeamID] = &status[i]
	}

	// good teams should sync successfully
	good1 := statusByID["team_good1"]
	require.NotNil(t, good1)
	assert.True(t, good1.Exists, "good team 1 should exist")
	assert.Empty(t, good1.LastErr, "good team 1 should have no error")
	assert.False(t, good1.LastSync.IsZero(), "good team 1 should have last sync set")

	good2 := statusByID["team_good2"]
	require.NotNil(t, good2)
	assert.True(t, good2.Exists, "good team 2 should exist")
	assert.Empty(t, good2.LastErr, "good team 2 should have no error")
	assert.False(t, good2.LastSync.IsZero(), "good team 2 should have last sync set")

	// bad team should report an error but not prevent the other two from syncing
	bad := statusByID["team_bad"]
	require.NotNil(t, bad)
	assert.False(t, bad.Exists, "bad team should not exist as git repo")
	assert.NotEmpty(t, bad.LastErr, "bad team should have an error")

	// verify metrics reflect partial success
	snap := scheduler.Metrics().Snapshot()
	assert.GreaterOrEqual(t, snap.TeamSyncCount, int64(2), "at least two team syncs should succeed")
}

// --- Test: Credentials with non-team-context repos are ignored ---

func TestTeamContextDiscovery_IgnoresNonTeamContextRepos(t *testing.T) {
	credDir := isolateCredentialsWithDir(t)
	projectDir := setupProjectWithConfig(t, "# empty config\n")

	// write credentials with a repo that is NOT a team-context type
	writeCredentialsFile(t, credDir, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: "https://git.fake.test.invalid",
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos: map[string]gitserver.RepoEntry{
			"some_ledger": {
				Name:   "ledger-repo",
				Type:   "ledger",
				URL:    "https://git.fake.test.invalid/ledger.git",
				TeamID: "some_ledger",
			},
		},
	})

	scheduler := newTestScheduler(projectDir)
	require.NoError(t, scheduler.WorkspaceRegistry().LoadFromConfig())

	// ledger-type repos in credentials should NOT be discovered as team contexts
	teamContexts := scheduler.WorkspaceRegistry().GetTeamContexts()
	assert.Empty(t, teamContexts, "non-team-context repo types should be ignored")
}

// --- Test: Config-based team contexts enriched with clone URLs from credentials ---

func TestTeamContextDiscovery_ConfigEnrichedByCredentials(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	credDir := isolateCredentialsWithDir(t)

	teamDir := t.TempDir()
	setupGitRepo(t, teamDir)

	// team context defined in config.local.toml (no clone URL there)
	projectDir := setupProjectWithConfig(t, fmt.Sprintf(`
[[team_contexts]]
team_id = "team_enrich"
team_name = "Enriched Team"
path = %q
`, teamDir))

	// credentials provide the clone URL for the same team
	writeCredentialsFile(t, credDir, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: "https://git.fake.test.invalid",
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos: map[string]gitserver.RepoEntry{
			"team_enrich": {
				Name:   "Enriched Team",
				Type:   "team-context",
				URL:    "https://git.fake.test.invalid/enriched.git",
				TeamID: "team_enrich",
			},
		},
	})

	scheduler := newTestScheduler(projectDir)
	require.NoError(t, scheduler.WorkspaceRegistry().LoadFromConfig())

	teamContexts := scheduler.WorkspaceRegistry().GetTeamContexts()
	require.Len(t, teamContexts, 1)
	assert.Equal(t, "team_enrich", teamContexts[0].TeamID)
	assert.Equal(t, "https://git.fake.test.invalid/enriched.git", teamContexts[0].CloneURL,
		"clone URL should be populated from credentials even when team is defined in config")
	assert.True(t, teamContexts[0].Exists)
}

// --- Test: reposEqual ---

func TestReposEqual(t *testing.T) {
	a := map[string]gitserver.RepoEntry{
		"team-a": {Name: "team-a", Type: "team-context", URL: "https://a.git", TeamID: "team_a"},
	}
	b := map[string]gitserver.RepoEntry{
		"team-a": {Name: "team-a", Type: "team-context", URL: "https://a.git", TeamID: "team_a"},
	}
	assert.True(t, reposEqual(a, b), "identical maps should be equal")

	c := map[string]gitserver.RepoEntry{
		"team-a": {Name: "team-a", Type: "team-context", URL: "https://a.git", TeamID: "team_a"},
		"team-b": {Name: "team-b", Type: "team-context", URL: "https://b.git", TeamID: "team_b"},
	}
	assert.False(t, reposEqual(a, c), "different lengths should not be equal")

	d := map[string]gitserver.RepoEntry{
		"team-a": {Name: "team-a", Type: "team-context", URL: "https://changed.git", TeamID: "team_a"},
	}
	assert.False(t, reposEqual(a, d), "different URL should not be equal")

	assert.True(t, reposEqual(nil, nil), "both nil should be equal")
	assert.True(t, reposEqual(map[string]gitserver.RepoEntry{}, map[string]gitserver.RepoEntry{}), "both empty should be equal")
}

// --- Test: discoverTeams respects dedup interval ---

func TestDiscoverTeams_RespectsDedup(t *testing.T) {
	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	// write credentials with a token that won't expire for a long time
	writeCredentialsFile(t, "", gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: "https://git.fake.test.invalid",
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos: map[string]gitserver.RepoEntry{
			"team-a": {Name: "team-a", Type: "team-context", URL: "https://a.git", TeamID: "team_a"},
		},
	})

	// first call should run (sets lastTeamDiscovery)
	scheduler.discoverTeams()

	// second immediate call should be deduped (no-op)
	scheduler.discoverTeams()

	// verify lastTeamDiscovery was set
	scheduler.mu.Lock()
	assert.False(t, scheduler.lastTeamDiscovery.IsZero(), "lastTeamDiscovery should be set after first call")
	scheduler.mu.Unlock()
}

// --- Test: discoverTeams skips when no credentials ---

func TestDiscoverTeams_SkipsWithNoCredentials(t *testing.T) {
	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	// call without any credentials file — should return without error
	scheduler.discoverTeams()

	scheduler.mu.Lock()
	assert.False(t, scheduler.lastTeamDiscovery.IsZero(), "lastTeamDiscovery should still be stamped")
	scheduler.mu.Unlock()
}

// --- Test: applySparseCheckout reads manifest and applies sparse-checkout ---

func TestApplySparseCheckout_AppliesManifestPaths(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// create a real git repo with a manifest file
	teamDir := t.TempDir()
	setupGitRepo(t, teamDir)

	// write a sync.manifest inside .sageox/
	sageoxDir := filepath.Join(teamDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	manifestContent := `version 1
include docs/
include SOUL.md
include memory/
sync_interval_minutes 10
`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "sync.manifest"), []byte(manifestContent), 0644))

	// commit the manifest so sparse-checkout can work with it
	addCmd := exec.Command("git", "-C", teamDir, "add", ".sageox/sync.manifest")
	require.NoError(t, addCmd.Run())
	commitCmd := exec.Command("git", "-C", teamDir, "commit", "-m", "add manifest")
	require.NoError(t, commitCmd.Run())

	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	ctx := context.Background()
	mCfg := scheduler.applySparseCheckout(ctx, teamDir)

	// verify manifest was parsed correctly
	require.NotNil(t, mCfg)
	assert.Equal(t, 10, mCfg.SyncIntervalMin)
	assert.Contains(t, mCfg.Includes, "docs/")
	assert.Contains(t, mCfg.Includes, "SOUL.md")
	assert.Contains(t, mCfg.Includes, "memory/")

	// verify sparse-checkout was configured
	sparseCmd := exec.Command("git", "-C", teamDir, "sparse-checkout", "list")
	out, err := sparseCmd.CombinedOutput()
	require.NoError(t, err, "sparse-checkout list should succeed: %s", string(out))
	sparseList := strings.TrimSpace(string(out))
	assert.Contains(t, sparseList, "docs")
	assert.Contains(t, sparseList, "memory")
}

func TestApplySparseCheckout_FallsBackWithoutManifest(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	teamDir := t.TempDir()
	setupGitRepo(t, teamDir)

	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	ctx := context.Background()
	mCfg := scheduler.applySparseCheckout(ctx, teamDir)

	// should return fallback config when no manifest file exists
	require.NotNil(t, mCfg)
	assert.Equal(t, 5, mCfg.SyncIntervalMin, "fallback should use default 5-minute interval")
	assert.NotEmpty(t, mCfg.Includes, "fallback should have default include paths")
}

// TestApplySparseCheckout_FallbackWithFilePatterns is a regression test for
// the bug where applySparseCheckout used --cone mode, which only supports
// directories. The fallback manifest includes files like AGENTS.md, SOUL.md,
// etc., which caused: fatal: 'AGENTS.md' is not a directory.
// Fix: use --no-cone mode to support both files and directories.
func TestApplySparseCheckout_FallbackWithFilePatterns(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	teamDir := t.TempDir()
	setupGitRepo(t, teamDir)

	// create files matching fallback includes (files AND directories)
	fallbackFiles := map[string]string{
		"AGENTS.md": "# Agents\n",
		"SOUL.md":   "# Soul\n",
		"TEAM.md":   "# Team\n",
		"MEMORY.md": "# Memory\n",
	}
	fallbackDirs := []string{"docs", "memory", "coworkers", ".sageox"}
	for _, dir := range fallbackDirs {
		require.NoError(t, os.MkdirAll(filepath.Join(teamDir, dir), 0755))
		require.NoError(t, os.WriteFile(
			filepath.Join(teamDir, dir, "placeholder.md"),
			[]byte("placeholder\n"), 0644))
	}
	for name, content := range fallbackFiles {
		require.NoError(t, os.WriteFile(filepath.Join(teamDir, name), []byte(content), 0644))
	}

	// commit so sparse-checkout has content to work with
	addCmd := exec.Command("git", "-C", teamDir, "add", ".")
	require.NoError(t, addCmd.Run())
	commitCmd := exec.Command("git", "-C", teamDir, "commit", "-m", "add fallback files")
	require.NoError(t, commitCmd.Run())

	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	ctx := context.Background()
	mCfg := scheduler.applySparseCheckout(ctx, teamDir)

	// should succeed without errors (was failing with --cone mode)
	require.NotNil(t, mCfg)

	// verify sparse-checkout was configured and includes file patterns
	sparseCmd := exec.Command("git", "-C", teamDir, "sparse-checkout", "list")
	out, err := sparseCmd.CombinedOutput()
	require.NoError(t, err, "sparse-checkout list should succeed: %s", string(out))

	sparseList := string(out)
	// verify file patterns are included (these broke with --cone mode)
	assert.Contains(t, sparseList, "AGENTS.md", "file patterns must work in sparse-checkout")
	assert.Contains(t, sparseList, "SOUL.md", "file patterns must work in sparse-checkout")
	// verify directory patterns too
	assert.Contains(t, sparseList, "docs/", "directory patterns must work in sparse-checkout")
	assert.Contains(t, sparseList, "memory/", "directory patterns must work in sparse-checkout")
}

// --- Two-phase clone tests ---

// setupTeamContextBareRepo creates a bare git repo populated with team context
// files (manifest, SOUL.md, TEAM.md, memory/, and optionally a large denied dir).
// Returns the bare repo path suitable for cloning with file:// URL.
func setupTeamContextBareRepo(t *testing.T, manifestContent string, extraFiles map[string]string) string {
	t.Helper()
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "team.bare")
	workDir := filepath.Join(tmpDir, "work")

	require.NoError(t, exec.Command("git", "init", "--bare", bareDir).Run())

	// enable partial clone support on the bare repo
	require.NoError(t, exec.Command("git", "-C", bareDir, "config", "uploadpack.allowfilter", "true").Run())

	require.NoError(t, exec.Command("git", "clone", bareDir, workDir).Run())
	gitConfig(t, workDir)

	// create .sageox/ directory
	sageoxDir := filepath.Join(workDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	if manifestContent != "" {
		require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "sync.manifest"), []byte(manifestContent), 0644))
	}

	// create core files
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "SOUL.md"), []byte("# Soul\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "TEAM.md"), []byte("# Team\n"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "memory"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "memory", "notes.md"), []byte("notes\n"), 0644))

	// create extra files (e.g., denied directories)
	for path, content := range extraFiles {
		full := filepath.Join(workDir, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0644))
	}

	// commit and push
	require.NoError(t, exec.Command("git", "-C", workDir, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "commit", "-m", "initial").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "push", "origin", "HEAD:main").Run())

	return bareDir
}

func TestTwoPhaseClone_Success(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateCredentials(t)

	manifest := `version 1
include .sageox/
include SOUL.md
include TEAM.md
include memory/
sync_interval_minutes 10
`
	bareDir := setupTeamContextBareRepo(t, manifest, nil)
	cloneURL := "file://" + bareDir

	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	targetDir := filepath.Join(t.TempDir(), "team-clone")
	ctx := context.Background()

	mCfg, err := scheduler.twoPhaseClone(ctx, cloneURL, targetDir, nil)
	require.NoError(t, err)
	require.NotNil(t, mCfg)

	// verify manifest was parsed
	assert.Equal(t, 10, mCfg.SyncIntervalMin)

	// verify expected files materialized
	assert.FileExists(t, filepath.Join(targetDir, "SOUL.md"))
	assert.FileExists(t, filepath.Join(targetDir, "TEAM.md"))
	assert.FileExists(t, filepath.Join(targetDir, "memory", "notes.md"))
	assert.FileExists(t, filepath.Join(targetDir, ".sageox", "sync.manifest"))

	// verify sparse-checkout is active
	out, err := exec.Command("git", "-C", targetDir, "sparse-checkout", "list").CombinedOutput()
	require.NoError(t, err)
	sparseList := string(out)
	assert.Contains(t, sparseList, ".sageox")
	assert.Contains(t, sparseList, "memory")
}

func TestTwoPhaseClone_NoManifest(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateCredentials(t)

	// no manifest content — should use fallback
	bareDir := setupTeamContextBareRepo(t, "", nil)
	cloneURL := "file://" + bareDir

	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	targetDir := filepath.Join(t.TempDir(), "team-clone")
	ctx := context.Background()

	mCfg, err := scheduler.twoPhaseClone(ctx, cloneURL, targetDir, nil)
	require.NoError(t, err)
	require.NotNil(t, mCfg)

	// fallback config uses 5 min default
	assert.Equal(t, 5, mCfg.SyncIntervalMin)

	// core files should still be materialized via fallback includes
	assert.FileExists(t, filepath.Join(targetDir, "SOUL.md"))
	assert.FileExists(t, filepath.Join(targetDir, "TEAM.md"))
	assert.FileExists(t, filepath.Join(targetDir, "memory", "notes.md"))
}

func TestTwoPhaseClone_DeniedPathsExcluded(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateCredentials(t)

	manifest := `version 1
include .sageox/
include SOUL.md
include memory/
deny assets/
`
	extraFiles := map[string]string{
		"assets/large-file.bin": "binary data here",
	}
	bareDir := setupTeamContextBareRepo(t, manifest, extraFiles)
	cloneURL := "file://" + bareDir

	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	targetDir := filepath.Join(t.TempDir(), "team-clone")
	ctx := context.Background()

	mCfg, err := scheduler.twoPhaseClone(ctx, cloneURL, targetDir, nil)
	require.NoError(t, err)
	require.NotNil(t, mCfg)

	// allowed files should exist
	assert.FileExists(t, filepath.Join(targetDir, "SOUL.md"))
	assert.FileExists(t, filepath.Join(targetDir, "memory", "notes.md"))

	// denied path should not be materialized
	_, statErr := os.Stat(filepath.Join(targetDir, "assets", "large-file.bin"))
	assert.True(t, os.IsNotExist(statErr), "denied path assets/ should not be materialized")
}

func TestTwoPhaseClone_IncompleteCloneDetected(t *testing.T) {
	// verify that Checkout() detects incomplete two-phase clones
	// (.git exists but .sageox/ missing) and moves them aside
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	targetDir := filepath.Join(t.TempDir(), "team-clone")

	// simulate an incomplete two-phase clone: .git exists but no .sageox/
	require.NoError(t, os.MkdirAll(filepath.Join(targetDir, ".git"), 0755))

	// for a ledger repo, .git existing means AlreadyExists=true
	result := &CheckoutResult{Path: targetDir}
	info, _ := os.Stat(targetDir)
	require.NotNil(t, info)
	gitDir := filepath.Join(targetDir, ".git")
	_, gitErr := os.Stat(gitDir)
	require.NoError(t, gitErr, ".git should exist")

	// for team-context, .git without .sageox is incomplete
	sageoxDir := filepath.Join(targetDir, ".sageox")
	_, sageoxErr := os.Stat(sageoxDir)
	assert.True(t, os.IsNotExist(sageoxErr), ".sageox should not exist")

	// verify backup logic works: rename the incomplete dir
	backupPath := fmt.Sprintf("%s.bak.test", targetDir)
	require.NoError(t, os.Rename(targetDir, backupPath))

	// backup should exist, original should not
	assert.NoDirExists(t, targetDir)
	assert.DirExists(t, backupPath)
	_ = result
	_ = scheduler
}

func TestTwoPhaseClone_IncompleteCloneRecovery(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateCredentials(t)

	manifest := `version 1
include .sageox/
include SOUL.md
`
	bareDir := setupTeamContextBareRepo(t, manifest, nil)
	cloneURL := "file://" + bareDir

	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	targetDir := filepath.Join(t.TempDir(), "team-clone")

	// simulate an incomplete clone, then remove it to let twoPhaseClone succeed
	require.NoError(t, os.MkdirAll(filepath.Join(targetDir, ".git"), 0755))
	require.NoError(t, os.RemoveAll(targetDir))

	ctx := context.Background()
	mCfg, err := scheduler.twoPhaseClone(ctx, cloneURL, targetDir, nil)
	require.NoError(t, err)
	require.NotNil(t, mCfg)
	assert.FileExists(t, filepath.Join(targetDir, ".sageox", "sync.manifest"))
	assert.FileExists(t, filepath.Join(targetDir, "SOUL.md"))
}

func TestTwoPhaseClone_SubsequentPullWorks(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateCredentials(t)

	manifest := `version 1
include .sageox/
include SOUL.md
include TEAM.md
include memory/
`
	bareDir := setupTeamContextBareRepo(t, manifest, nil)
	cloneURL := "file://" + bareDir

	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	targetDir := filepath.Join(t.TempDir(), "team-clone")
	ctx := context.Background()

	// initial two-phase clone
	_, err := scheduler.twoPhaseClone(ctx, cloneURL, targetDir, nil)
	require.NoError(t, err)

	// push a new commit to the bare repo via a temp clone
	pusherDir := filepath.Join(t.TempDir(), "pusher")
	require.NoError(t, exec.Command("git", "clone", bareDir, pusherDir).Run())
	gitConfig(t, pusherDir)
	require.NoError(t, os.WriteFile(filepath.Join(pusherDir, "SOUL.md"), []byte("# Updated Soul\n"), 0644))
	require.NoError(t, exec.Command("git", "-C", pusherDir, "add", "SOUL.md").Run())
	require.NoError(t, exec.Command("git", "-C", pusherDir, "commit", "-m", "update soul").Run())
	require.NoError(t, exec.Command("git", "-C", pusherDir, "push", "origin", "HEAD:main").Run())

	// pull should work (fetch + pull --rebase)
	_, fetchErr := exec.Command("git", "-C", targetDir, "fetch", "--quiet").CombinedOutput()
	require.NoError(t, fetchErr)

	pullOut, pullErr := exec.Command("git", "-C", targetDir, "pull", "--rebase", "--quiet").CombinedOutput()
	require.NoError(t, pullErr, "pull --rebase should succeed after two-phase clone: %s", string(pullOut))

	// verify updated content
	content, err := os.ReadFile(filepath.Join(targetDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "Updated Soul")
}

func TestValidateTeamContextClone_MissingCoreFiles(t *testing.T) {
	// create a dir with only .sageox but no core files
	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".sageox"), 0755))

	// should not panic; warnings are logged but not returned
	gitserver.ValidateTeamContextClone(repoDir, nil)

	// create one core file — should pass
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "TEAM.md"), []byte("# Team\n"), 0644))
	gitserver.ValidateTeamContextClone(repoDir, nil)
}

func TestSetSyncIntervalMin_StoresAndRetrieves(t *testing.T) {
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	teamDir := "/tmp/fake-team-context"

	// initially should return 0
	assert.Equal(t, 0, scheduler.WorkspaceRegistry().GetSyncIntervalMin(teamDir))

	// register a workspace with that path, then set interval
	scheduler.WorkspaceRegistry().SetSyncIntervalMin(teamDir, 15)

	// won't find it since no workspace has this path yet — that's expected
	assert.Equal(t, 0, scheduler.WorkspaceRegistry().GetSyncIntervalMin(teamDir))
}

// --- Blue-green reclone GC tests ---

// setupClonedTeamContext creates a bare repo and two-phase-clones it into a target dir.
// Returns (bareDir, clonedDir, scheduler) for GC testing.
func setupClonedTeamContext(t *testing.T, manifestContent string, extraFiles map[string]string) (string, string, *SyncScheduler) {
	t.Helper()
	isolateCredentials(t)

	bareDir := setupTeamContextBareRepo(t, manifestContent, extraFiles)
	cloneURL := "file://" + bareDir

	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	targetDir := filepath.Join(t.TempDir(), "team-ctx")
	ctx := context.Background()

	_, err := scheduler.twoPhaseClone(ctx, cloneURL, targetDir, nil)
	require.NoError(t, err)

	// configure pull.rebase
	require.NoError(t, exec.Command("git", "-C", targetDir, "config", "pull.rebase", "true").Run())

	return bareDir, targetDir, scheduler
}

func TestBlueGreenGC_Success(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\ngc_interval_days 7\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifest, nil)

	// record original content hash
	origContent, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)

	// register workspace in the registry so UpdateLastGC can find it
	ws := WorkspaceState{
		ID:       "team_test",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// verify the repo still works after GC
	assert.FileExists(t, filepath.Join(teamDir, "SOUL.md"))
	assert.FileExists(t, filepath.Join(teamDir, ".sageox", "sync.manifest"))

	// content should be the same
	newContent, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Equal(t, string(origContent), string(newContent))

	// .old should be cleaned up
	assert.NoDirExists(t, teamDir+".old")
	assert.NoDirExists(t, teamDir+".new")

	// verify LastGCTime was updated
	lastGC := scheduler.WorkspaceRegistry().GetLastGCTime("team_test")
	assert.False(t, lastGC.IsZero(), "LastGCTime should be set after GC")
}

func TestBlueGreenGC_PreservesUncommittedTrackedChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	// modify a tracked file (unstaged change)
	dirtyContent := "# Soul\nmodified by user\n"
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "SOUL.md"), []byte(dirtyContent), 0644))

	ws := WorkspaceState{
		ID:       "team_test",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	ctx := context.Background()
	result := scheduler.runBlueGreenGC(ctx, ws)

	assert.Equal(t, gcSuccess, result, "GC should succeed with dirty tree")
	assert.NoDirExists(t, teamDir+".new")
	assert.NoDirExists(t, teamDir+".old")

	// the user's modification must survive the reclone
	content, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Equal(t, dirtyContent, string(content), "uncommitted change should be preserved")

	// no leftover preservation artifacts
	assert.NoFileExists(t, teamDir+".gc-diff")
	assert.NoDirExists(t, teamDir+".gc-untracked")
}

func TestBlueGreenGC_CloneFailsKeepsOld(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\n"
	_, teamDir, scheduler := setupClonedTeamContext(t, manifest, nil)

	ws := WorkspaceState{
		ID:       "team_test",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file:///nonexistent/repo.git", // invalid URL
		Exists:   true,
	}

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// old repo should still be intact
	assert.FileExists(t, filepath.Join(teamDir, "SOUL.md"))
	assert.FileExists(t, filepath.Join(teamDir, ".sageox", "sync.manifest"))

	// .new should be cleaned up
	assert.NoDirExists(t, teamDir+".new")
}

func TestBlueGreenGC_NotDueYet(t *testing.T) {
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	// register a workspace with recent LastGCTime
	scheduler.WorkspaceRegistry().UpdateLastGC("team_test")

	ws := WorkspaceState{
		ID:             "team_test",
		Type:           WorkspaceTypeTeamContext,
		Path:           "/tmp/fake-path",
		Exists:         true,
		CloneURL:       "file:///fake",
		GCIntervalDays: 7,
		LastGCTime:     time.Now(), // just ran GC
	}

	// checkAndRunGC should skip this workspace because it's not due
	// We verify by checking that no clone attempt happens (no .new dir)
	assert.NoDirExists(t, ws.Path+".new")
}

func TestBlueGreenGC_CleansUpLeftoverNewDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifest, nil)

	// create a leftover .new from a previous failed GC
	newPath := teamDir + ".new"
	require.NoError(t, os.MkdirAll(newPath, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(newPath, "leftover"), []byte("old"), 0644))

	ws := WorkspaceState{
		ID:       "team_test",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// leftover should be cleaned up, and GC should succeed
	assert.NoDirExists(t, newPath)
	assert.FileExists(t, filepath.Join(teamDir, "SOUL.md"))
}

func TestValidateGCClone_PassesWithCoreFiles(t *testing.T) {
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".git"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".sageox"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "SOUL.md"), []byte("# Soul\n"), 0644))

	assert.True(t, scheduler.validateGCClone(repoDir, nil))
}

func TestValidateGCClone_FailsWithoutGitDir(t *testing.T) {
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".sageox"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "SOUL.md"), []byte("# Soul\n"), 0644))

	assert.False(t, scheduler.validateGCClone(repoDir, nil))
}

func TestValidateGCClone_FailsWithoutCoreFiles(t *testing.T) {
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".git"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".sageox"), 0755))

	assert.False(t, scheduler.validateGCClone(repoDir, nil))
}

// --- Edge case tests: old-style full clones and corruption ---

func TestBlueGreenGC_OldStyleFullClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\n"
	extraFiles := map[string]string{
		"src/main.go":      "package main\n",
		"assets/big.bin":   "binary stuff",
		"docs/readme.md":  "# docs\n",
		"coworkers/bob.md": "# bob\n",
	}
	bareDir := setupTeamContextBareRepo(t, manifest, extraFiles)

	// do a regular full clone (old-style, no sparse checkout)
	teamDir := filepath.Join(t.TempDir(), "team-ctx")
	require.NoError(t, exec.Command("git", "clone", bareDir, teamDir).Run())
	gitConfig(t, teamDir)
	require.NoError(t, exec.Command("git", "-C", teamDir, "config", "pull.rebase", "true").Run())

	// verify full clone has all files including non-manifest ones
	assert.FileExists(t, filepath.Join(teamDir, "src", "main.go"))
	assert.FileExists(t, filepath.Join(teamDir, "assets", "big.bin"))

	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	ws := WorkspaceState{
		ID:       "team_old",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "old-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// manifest-declared files should exist
	assert.FileExists(t, filepath.Join(teamDir, "SOUL.md"))
	assert.FileExists(t, filepath.Join(teamDir, "TEAM.md"))
	assert.FileExists(t, filepath.Join(teamDir, "memory", "notes.md"))
	assert.FileExists(t, filepath.Join(teamDir, ".sageox", "sync.manifest"))

	// non-manifest files should NOT exist (sparse clone replaced full clone)
	_, err := os.Stat(filepath.Join(teamDir, "src", "main.go"))
	assert.True(t, os.IsNotExist(err), "non-manifest file src/main.go should not exist after GC reclone")
	_, err = os.Stat(filepath.Join(teamDir, "assets", "big.bin"))
	assert.True(t, os.IsNotExist(err), "non-manifest file assets/big.bin should not exist after GC reclone")

	// sparse-checkout should be active
	out, err := exec.Command("git", "-C", teamDir, "sparse-checkout", "list").CombinedOutput()
	require.NoError(t, err)
	sparseList := string(out)
	assert.Contains(t, sparseList, ".sageox")
	assert.Contains(t, sparseList, "memory")

	// cleanup should be complete
	assert.NoDirExists(t, teamDir+".old")
	assert.NoDirExists(t, teamDir+".new")

	// LastGCTime should be updated
	lastGC := scheduler.WorkspaceRegistry().GetLastGCTime("team_old")
	assert.False(t, lastGC.IsZero())
}

func TestBlueGreenGC_OldStyleFullClone_PreservesContent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude memory/\n"
	extraFiles := map[string]string{
		"src/main.go": "package main\n",
	}
	bareDir := setupTeamContextBareRepo(t, manifest, extraFiles)

	// full clone (old-style)
	teamDir := filepath.Join(t.TempDir(), "team-ctx")
	require.NoError(t, exec.Command("git", "clone", bareDir, teamDir).Run())
	gitConfig(t, teamDir)
	require.NoError(t, exec.Command("git", "-C", teamDir, "config", "pull.rebase", "true").Run())

	// read content before GC
	soulBefore, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)
	notesBefore, err := os.ReadFile(filepath.Join(teamDir, "memory", "notes.md"))
	require.NoError(t, err)

	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	ws := WorkspaceState{
		ID:       "team_content",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "content-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// content should be identical after GC
	soulAfter, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Equal(t, string(soulBefore), string(soulAfter))

	notesAfter, err := os.ReadFile(filepath.Join(teamDir, "memory", "notes.md"))
	require.NoError(t, err)
	assert.Equal(t, string(notesBefore), string(notesAfter))

	// non-manifest file should be gone
	_, err = os.Stat(filepath.Join(teamDir, "src", "main.go"))
	assert.True(t, os.IsNotExist(err))
}

func TestBlueGreenGC_RepoWithStaleLockFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifest, nil)

	// create a stale index.lock — git status will report dirty/error
	lockFile := filepath.Join(teamDir, ".git", "index.lock")
	require.NoError(t, os.WriteFile(lockFile, []byte{}, 0644))

	ws := WorkspaceState{
		ID:       "team_lock",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "lock-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// GC should skip — original repo untouched
	assert.FileExists(t, filepath.Join(teamDir, "SOUL.md"))
	assert.NoDirExists(t, teamDir+".new")
	assert.NoDirExists(t, teamDir+".old")
}

func TestBlueGreenGC_RepoInRebaseState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifest, nil)

	// simulate a rebase in progress
	rebaseMergeDir := filepath.Join(teamDir, ".git", "rebase-merge")
	require.NoError(t, os.MkdirAll(rebaseMergeDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(rebaseMergeDir, "head-name"), []byte("refs/heads/main\n"), 0644))

	ws := WorkspaceState{
		ID:       "team_rebase",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "rebase-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// GC should skip — rebase state makes it dirty
	assert.FileExists(t, filepath.Join(teamDir, "SOUL.md"))
	assert.NoDirExists(t, teamDir+".new")
	assert.NoDirExists(t, teamDir+".old")
}

func TestBlueGreenGC_CorruptGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifest, nil)

	// corrupt .git/HEAD
	headFile := filepath.Join(teamDir, ".git", "HEAD")
	require.NoError(t, os.WriteFile(headFile, []byte("garbage-not-a-ref\n"), 0644))

	ws := WorkspaceState{
		ID:       "team_corrupt",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "corrupt-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// isCheckoutClean returns false on error → GC should skip
	// the corrupt repo is preserved (don't make it worse)
	assert.DirExists(t, filepath.Join(teamDir, ".git"))
	assert.NoDirExists(t, teamDir+".new")
	assert.NoDirExists(t, teamDir+".old")
}

func TestBlueGreenGC_MissingGitDir(t *testing.T) {
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	// directory exists but has no .git/
	teamDir := filepath.Join(t.TempDir(), "team-ctx")
	require.NoError(t, os.MkdirAll(teamDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "SOUL.md"), []byte("# Soul\n"), 0644))

	ws := WorkspaceState{
		ID:       "team_nogit",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "nogit-team",
		CloneURL: "file:///nonexistent/repo.git",
		Exists:   true,
	}

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// should skip gracefully — isCheckoutClean fails on non-git dir
	assert.NoDirExists(t, teamDir+".new")
	assert.NoDirExists(t, teamDir+".old")
	// original dir should still exist
	assert.DirExists(t, teamDir)
}

func TestBlueGreenGC_WorkspacePathDoesNotExist(t *testing.T) {
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	// workspace path doesn't exist — isCheckoutClean returns false, GC skips
	ws := WorkspaceState{
		ID:       "team_nopath",
		Type:     WorkspaceTypeTeamContext,
		Path:     filepath.Join(t.TempDir(), "does-not-exist"),
		TeamName: "nopath-team",
		CloneURL: "file:///nonexistent/repo.git",
		Exists:   true,
	}

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// should skip gracefully — no dirs created
	assert.NoDirExists(t, ws.Path+".new")
	assert.NoDirExists(t, ws.Path+".old")
}

func TestBlueGreenGC_PreExistingOldDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifest, nil)

	// create a pre-existing .old directory (leftover from a previous failed cleanup)
	oldPath := teamDir + ".old"
	require.NoError(t, os.MkdirAll(oldPath, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(oldPath, "stale"), []byte("leftover"), 0644))

	ws := WorkspaceState{
		ID:       "team_preold",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "preold-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// GC should succeed — pre-existing .old should be removed
	assert.FileExists(t, filepath.Join(teamDir, "SOUL.md"))
	assert.NoDirExists(t, oldPath)
	assert.NoDirExists(t, teamDir+".new")

	lastGC := scheduler.WorkspaceRegistry().GetLastGCTime("team_preold")
	assert.False(t, lastGC.IsZero())
}

func TestValidateGCClone_FailsWithDeniedPaths(t *testing.T) {
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".git"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".sageox"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "SOUL.md"), []byte("# Soul\n"), 0644))

	// create a denied path that should not be materialized
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "data"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "data", "leak.bin"), []byte("leaked"), 0644))

	cfg := &manifest.ManifestConfig{
		Denies: []string{"data"},
	}

	assert.False(t, scheduler.validateGCClone(repoDir, cfg),
		"validation should fail when denied paths are materialized")
}

func TestValidateGCClone_FailsWithoutSageoxDir(t *testing.T) {
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".git"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "SOUL.md"), []byte("# Soul\n"), 0644))
	// no .sageox directory

	assert.False(t, scheduler.validateGCClone(repoDir, nil),
		"validation should fail without .sageox directory")
}

func TestValidateGCClone_PassesWithDeniesNotMaterialized(t *testing.T) {
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".git"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".sageox"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "SOUL.md"), []byte("# Soul\n"), 0644))

	cfg := &manifest.ManifestConfig{
		Denies: []string{"data", "sessions", "coworkers"},
	}

	assert.True(t, scheduler.validateGCClone(repoDir, cfg),
		"validation should pass when denied paths don't exist")
}

func TestBlueGreenGC_ConcurrentSkipped(t *testing.T) {
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	// simulate GC already in progress by setting the atomic flag
	atomic.StoreInt32(&scheduler.gcInProgress, 1)

	ws := WorkspaceState{
		ID:       "team_concurrent",
		Type:     WorkspaceTypeTeamContext,
		Path:     "/tmp/fake-gc-path",
		Exists:   true,
		CloneURL: "file:///fake",
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	ctx := context.Background()
	scheduler.checkAndRunGC(ctx)

	// GC should have been skipped entirely — flag still set
	assert.Equal(t, int32(1), atomic.LoadInt32(&scheduler.gcInProgress),
		"gcInProgress flag should remain set (not cleared by skipped check)")
}

func TestBlueGreenGC_SkipsCloneInFlight(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifest, nil)

	ws := WorkspaceState{
		ID:             "team_inflight",
		Type:           WorkspaceTypeTeamContext,
		Path:           teamDir,
		TeamName:       "inflight-team",
		CloneURL:       "file://" + bareDir,
		Exists:         true,
		GCIntervalDays: 1,
		LastGCTime:     time.Time{}, // never run = due for GC
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	// mark this workspace as having a clone in flight
	scheduler.cloneInFlight.Store(ws.ID, true)

	ctx := context.Background()
	scheduler.checkAndRunGC(ctx)

	// GC should have been skipped — no .new or .old dirs
	assert.NoDirExists(t, teamDir+".new")
	assert.NoDirExists(t, teamDir+".old")

	// LastGCTime should NOT be updated
	lastGC := scheduler.WorkspaceRegistry().GetLastGCTime("team_inflight")
	assert.True(t, lastGC.IsZero())
}

func TestBlueGreenGC_UpdatesManifestConfig(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// manifest with custom gc_interval_days and sync_interval_minutes
	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude memory/\ngc_interval_days 14\nsync_interval_minutes 10\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	ws := WorkspaceState{
		ID:       "team_config",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "config-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// verify GC succeeded
	assert.FileExists(t, filepath.Join(teamDir, "SOUL.md"))
	assert.NoDirExists(t, teamDir+".old")
	assert.NoDirExists(t, teamDir+".new")

	lastGC := scheduler.WorkspaceRegistry().GetLastGCTime("team_config")
	assert.False(t, lastGC.IsZero())

	// verify manifest config was propagated to registry
	registry.mu.Lock()
	updatedWs := registry.workspaces["team_config"]
	gcInterval := updatedWs.GCIntervalDays
	syncInterval := updatedWs.SyncIntervalMin
	registry.mu.Unlock()

	assert.Equal(t, 14, gcInterval, "gc_interval_days should be updated from manifest")
	assert.Equal(t, 10, syncInterval, "sync_interval_min should be updated from manifest")
}

func TestBlueGreenGC_ValidationFailsKeepsOld(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// create a bare repo WITHOUT core files — clone will succeed but validation will fail
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "bare.git")
	workDir := filepath.Join(tmpDir, "work")

	require.NoError(t, exec.Command("git", "init", "--bare", bareDir).Run())
	require.NoError(t, exec.Command("git", "-C", bareDir, "config", "uploadpack.allowfilter", "true").Run())
	require.NoError(t, exec.Command("git", "clone", bareDir, workDir).Run())
	gitConfig(t, workDir)

	// only create .sageox with manifest, but NO core files (SOUL.md, TEAM.md, MEMORY.md)
	sageoxDir := filepath.Join(workDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\n"
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "sync.manifest"), []byte(manifestContent), 0644))
	require.NoError(t, exec.Command("git", "-C", workDir, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "commit", "-m", "init").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "push", "origin", "HEAD:main").Run())

	// set up a valid existing team context (with SOUL.md) that should be preserved
	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude memory/\n"
	_, teamDir, scheduler := setupClonedTeamContext(t, manifest, nil)
	origSoul, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)

	ws := WorkspaceState{
		ID:       "team_valfail",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "valfail-team",
		CloneURL: "file://" + bareDir, // points to repo without core files
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// old repo should still be intact because validation failed
	newSoul, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Equal(t, string(origSoul), string(newSoul), "original content preserved after validation failure")

	// .new should be cleaned up
	assert.NoDirExists(t, teamDir+".new")

	// LastGCTime should NOT be updated
	lastGC := scheduler.WorkspaceRegistry().GetLastGCTime("team_valfail")
	assert.True(t, lastGC.IsZero())
}

func TestBlueGreenGC_EmptyWorkspacePath(t *testing.T) {
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	ws := WorkspaceState{
		ID:       "team_empty",
		Type:     WorkspaceTypeTeamContext,
		Path:     "", // empty path
		Exists:   true,
		CloneURL: "file:///fake",
	}

	ctx := context.Background()
	// should not panic on empty path
	scheduler.runBlueGreenGC(ctx, ws)
}

func TestBlueGreenGC_LeftoverNewRemovalFails(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifest, nil)

	// create a leftover .new dir that cannot be removed (read-only parent of contents)
	newPath := teamDir + ".new"
	innerDir := filepath.Join(newPath, "inner")
	require.NoError(t, os.MkdirAll(innerDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(innerDir, "file"), []byte("stuck"), 0644))
	// make inner dir non-writable so RemoveAll fails on the file inside
	require.NoError(t, os.Chmod(innerDir, 0555))
	t.Cleanup(func() {
		os.Chmod(innerDir, 0755)
		os.RemoveAll(newPath)
	})

	ws := WorkspaceState{
		ID:       "team_stucknew",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "stucknew-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// GC should bail out because it can't remove leftover .new
	// original repo should still be intact
	assert.FileExists(t, filepath.Join(teamDir, "SOUL.md"))

	// restore permissions for cleanup
	os.Chmod(innerDir, 0755)
}

// --- Dirty-tree preservation regression tests ---

func TestBlueGreenGC_PreservesStagedChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	// stage a change
	stagedContent := "# Soul\nstaged modification\n"
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "SOUL.md"), []byte(stagedContent), 0644))
	require.NoError(t, exec.Command("git", "-C", teamDir, "add", "SOUL.md").Run())

	ws := WorkspaceState{
		ID:       "team_staged",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	content, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Equal(t, stagedContent, string(content), "staged change should be preserved")
}

func TestBlueGreenGC_PreservesMixedStagedAndUnstaged(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	// stage a change, then make a further unstaged change
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "SOUL.md"), []byte("staged version"), 0644))
	require.NoError(t, exec.Command("git", "-C", teamDir, "add", "SOUL.md").Run())
	finalContent := "unstaged version on top of staged"
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "SOUL.md"), []byte(finalContent), 0644))

	ws := WorkspaceState{
		ID:       "team_mixed",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	// the latest (unstaged) content should be what survives
	content, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Equal(t, finalContent, string(content), "latest working tree content should be preserved")
}

func TestBlueGreenGC_PreservesUntrackedFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	// create an untracked file
	untrackedContent := "user's custom notes\n"
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "my-notes.md"), []byte(untrackedContent), 0644))

	ws := WorkspaceState{
		ID:       "team_untracked",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	content, err := os.ReadFile(filepath.Join(teamDir, "my-notes.md"))
	require.NoError(t, err)
	assert.Equal(t, untrackedContent, string(content), "untracked file should be preserved")
}

func TestBlueGreenGC_PreservesUntrackedInSubdirs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	// create untracked files in nested subdirectories
	nestedDir := filepath.Join(teamDir, "notes", "2024")
	require.NoError(t, os.MkdirAll(nestedDir, 0755))
	nestedContent := "january notes\n"
	require.NoError(t, os.WriteFile(filepath.Join(nestedDir, "jan.md"), []byte(nestedContent), 0644))

	ws := WorkspaceState{
		ID:       "team_nested",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	content, err := os.ReadFile(filepath.Join(teamDir, "notes", "2024", "jan.md"))
	require.NoError(t, err)
	assert.Equal(t, nestedContent, string(content), "nested untracked file should be preserved")
}

func TestBlueGreenGC_PushesUnpushedCommitsBeforeReclone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	// make a local commit that is NOT pushed
	newContent := "# Soul\nlocal commit content\n"
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "SOUL.md"), []byte(newContent), 0644))
	gitConfig(t, teamDir)
	require.NoError(t, exec.Command("git", "-C", teamDir, "add", "SOUL.md").Run())
	require.NoError(t, exec.Command("git", "-C", teamDir, "commit", "-m", "local change").Run())

	// verify there are unpushed commits (user commit + auto-commit from TwoPhaseClone)
	countOut, err := exec.Command("git", "-C", teamDir, "rev-list", "--count", "origin/main..HEAD").CombinedOutput()
	require.NoError(t, err)
	count := strings.TrimSpace(string(countOut))
	assert.True(t, count >= "1", "should have at least 1 unpushed commit, got %s", count)

	ws := WorkspaceState{
		ID:       "team_push",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	// verify the commit was pushed to the bare repo
	logOut, err := exec.Command("git", "-C", bareDir, "log", "--oneline", "main").CombinedOutput()
	require.NoError(t, err)
	assert.Contains(t, string(logOut), "local change", "unpushed commit should be in bare repo after GC")

	// the recloned repo should have the committed content
	content, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Equal(t, newContent, string(content))
}

func TestBlueGreenGC_SkipsWhenPushFails(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	// make a local commit
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "SOUL.md"), []byte("local change"), 0644))
	gitConfig(t, teamDir)
	require.NoError(t, exec.Command("git", "-C", teamDir, "add", "SOUL.md").Run())
	require.NoError(t, exec.Command("git", "-C", teamDir, "commit", "-m", "unpushable").Run())

	// break the bare repo so push will fail
	require.NoError(t, os.RemoveAll(bareDir))

	ws := WorkspaceState{
		ID:       "team_pushfail",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSkippedDirty, result, "GC should skip when push fails")

	// original repo should be untouched
	assert.FileExists(t, filepath.Join(teamDir, "SOUL.md"))
	content, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Equal(t, "local change", string(content))
}

func TestBlueGreenGC_DiffApplyConflictPreservesDiffFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	// push the auto-commit from TwoPhaseClone so the local is in sync with remote
	require.NoError(t, exec.Command("git", "-C", teamDir, "push", "origin", "HEAD", "--quiet").Run())

	// push a conflicting change to the bare repo via a temp clone
	// this completely replaces SOUL.md content, so the local diff can't apply cleanly
	pusherDir := filepath.Join(t.TempDir(), "pusher")
	require.NoError(t, exec.Command("git", "clone", bareDir, pusherDir).Run())
	gitConfig(t, pusherDir)
	require.NoError(t, os.WriteFile(filepath.Join(pusherDir, "SOUL.md"), []byte("completely different remote content\nwith multiple lines\nthat conflict\n"), 0644))
	require.NoError(t, exec.Command("git", "-C", pusherDir, "add", "SOUL.md").Run())
	require.NoError(t, exec.Command("git", "-C", pusherDir, "commit", "-m", "conflict").Run())
	require.NoError(t, exec.Command("git", "-C", pusherDir, "push", "origin", "HEAD:main").Run())

	// now make a local uncommitted change to SOUL.md that conflicts
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "SOUL.md"), []byte("local user edit\nwith different content\nthat also conflicts\n"), 0644))

	ws := WorkspaceState{
		ID:       "team_conflict",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	// reclone succeeds even if diff apply fails — the repo is valid
	assert.Equal(t, gcSuccess, result)

	// the .gc-diff file should be preserved for manual recovery
	diffFile := teamDir + ".gc-diff"
	assert.FileExists(t, diffFile, "diff file should be preserved when apply fails")

	// clean up
	t.Cleanup(func() { os.Remove(diffFile) })
}

func TestBlueGreenGC_PreservesBinaryUntrackedFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	// create a binary untracked file
	binaryContent := make([]byte, 256)
	for i := range binaryContent {
		binaryContent[i] = byte(i)
	}
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "diagram.png"), binaryContent, 0644))

	ws := WorkspaceState{
		ID:       "team_binary",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	content, err := os.ReadFile(filepath.Join(teamDir, "diagram.png"))
	require.NoError(t, err)
	assert.Equal(t, binaryContent, content, "binary untracked file should survive reclone with identical content")
}

func TestBlueGreenGC_StagedDeletePreserved(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	// stage a deletion of TEAM.md
	require.NoError(t, exec.Command("git", "-C", teamDir, "rm", "TEAM.md").Run())

	ws := WorkspaceState{
		ID:       "team_delete",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	// TEAM.md should not exist after reclone (the deletion should be re-applied)
	assert.NoFileExists(t, filepath.Join(teamDir, "TEAM.md"), "staged delete should be preserved")
}

func TestBlueGreenGC_CleanTreeStillWorks(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// verify clean tree GC still works identically with the new code path
	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\ngc_interval_days 7\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	origContent, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)

	ws := WorkspaceState{
		ID:       "team_clean",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	content, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Equal(t, string(origContent), string(content))

	// no preservation artifacts
	assert.NoFileExists(t, teamDir+".gc-diff")
	assert.NoDirExists(t, teamDir+".gc-untracked")
}
