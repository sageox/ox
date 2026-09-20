package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Keep stdin open and unanswered: EOF tests cannot detect a command that still
// waits for input when --no-input is set. Exercise the compiled flag wiring too.
func TestNoInputCLI(t *testing.T) {
	skipIntegration(t)
	oxBin := testguard.BuildOxBinary(t, repoPath("..", ".."))

	t.Run("logout", func(t *testing.T) {
		for _, tt := range []struct {
			name      string
			args      []string
			endpoints int
			input     string
			wantError []string
		}{
			{name: "requires consent", args: []string{"--no-input"}, endpoints: 1, wantError: []string{cli.ErrConfirmationRequired.Error()}},
			{name: "ignores piped consent", args: []string{"--no-input"}, endpoints: 1, input: "y\n", wantError: []string{cli.ErrConfirmationRequired.Error()}},
			{name: "requires endpoint choice", args: []string{"--no-input"}, endpoints: 2, wantError: []string{"--no-input requires --endpoint", "--all"}},
			{name: "yes cannot choose endpoint", args: []string{"--no-input", "--yes"}, endpoints: 2, wantError: []string{"--no-input requires --endpoint", "--all"}},
			{name: "yes authorizes single endpoint", args: []string{"--no-input", "--yes"}, endpoints: 1},
			{name: "all and yes authorize every endpoint", args: []string{"--no-input", "--all", "--yes"}, endpoints: 2},
			{name: "force authorizes every endpoint", args: []string{"--no-input", "--force"}, endpoints: 2},
			{name: "no-interactive still accepts piped consent", args: []string{"--no-interactive"}, endpoints: 1, input: "y"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				env := noInputCLIEnv(t)
				var revocations atomic.Int32
				var endpoints []string
				for range tt.endpoints {
					server := testguard.SafeMockServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == auth.RevokeEndpoint && r.Method == http.MethodPost {
							revocations.Add(1)
							w.WriteHeader(http.StatusOK)
							return
						}
						http.NotFound(w, r)
					}))
					endpoints = append(endpoints, server.URL)
					require.NoError(t, auth.SaveTokenForEndpoint(server.URL, &auth.StoredToken{
						AccessToken: "test-access", RefreshToken: "test-refresh", TokenType: "Bearer",
						ExpiresAt: time.Now().Add(time.Hour),
					}))
				}
				authPath, err := auth.GetAuthFilePath()
				require.NoError(t, err)
				before, err := os.ReadFile(authPath)
				require.NoError(t, err)

				var input io.Reader
				if tt.input != "" {
					input = strings.NewReader(tt.input)
				}
				output, runErr := runNoInputCLI(t, oxBin, t.TempDir(), env, input, append([]string{"logout"}, tt.args...)...)
				if len(tt.wantError) > 0 {
					require.Error(t, runErr, "output: %s", output)
					for _, message := range tt.wantError {
						assert.Contains(t, output, message)
					}
					after, err := os.ReadFile(authPath)
					require.NoError(t, err)
					assert.Equal(t, before, after, "missing consent must preserve every stored token")
					assert.Zero(t, revocations.Load(), "missing consent must not revoke any token")
					return
				}

				require.NoError(t, runErr, "output: %s", output)
				assert.EqualValues(t, tt.endpoints, revocations.Load())
				for _, ep := range endpoints {
					token, err := auth.GetTokenForEndpoint(ep)
					require.NoError(t, err)
					assert.Nil(t, token, "authorized logout must remove the stored token")
				}
			})
		}
	})

	t.Run("typed uninstall", func(t *testing.T) {
		for _, tt := range []struct {
			name      string
			args      []string
			input     string
			wantError bool
		}{
			{name: "requires explicit force", args: []string{"--no-input"}, wantError: true},
			{name: "yes cannot replace typed confirmation", args: []string{"--no-input", "--yes"}, wantError: true},
			{name: "force authorizes uninstall", args: []string{"--no-input", "--force"}},
			{name: "no-interactive still accepts piped confirmation", args: []string{"--no-interactive"}, input: "uninstall"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				env := noInputCLIEnv(t)
				repo := testGitRepo(t)
				require.NoError(t, config.SaveProjectConfig(repo, &config.ProjectConfig{RepoID: "repo-no-input"}))
				configPath := filepath.Join(repo, ".sageox", "config.json")
				before, err := os.ReadFile(configPath)
				require.NoError(t, err)

				var input io.Reader
				if tt.input != "" {
					input = strings.NewReader(tt.input)
				}
				output, runErr := runNoInputCLI(t, oxBin, repo, env, input, append([]string{"uninstall", "--local-only"}, tt.args...)...)
				if tt.wantError {
					require.Error(t, runErr, "output: %s", output)
					assert.Contains(t, output, "uninstall requires confirmation")
					assert.Contains(t, output, "--force")
					assert.Contains(t, output, "--no-input")
					after, err := os.ReadFile(configPath)
					require.NoError(t, err)
					assert.Equal(t, before, after, "missing consent must preserve the installation")
					assert.NotContains(t, output, "Uninstalling SageOx...")
					return
				}
				require.NoError(t, runErr, "output: %s", output)
				assert.NoFileExists(t, configPath, "authorized uninstall must remove project configuration")
				assert.Contains(t, output, "SageOx uninstalled successfully")
			})
		}
	})

	t.Run("login requires endpoint trust even with yes", func(t *testing.T) {
		env := noInputCLIEnv(t)
		var requests atomic.Int32
		server := testguard.SafeMockServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			http.NotFound(w, r)
		}))
		output, err := runNoInputCLI(t, oxBin, t.TempDir(), env, nil,
			"login", "--endpoint", server.URL, "--no-input", "--yes")
		require.Error(t, err, "output: %s", output)
		assert.Contains(t, output, "OX_TRUST_ENDPOINT=1")
		assert.Contains(t, output, "--no-input")
		assert.Zero(t, requests.Load(), "an untrusted endpoint must not receive requests")
		trusted, err := loadTrustedEndpoints()
		require.NoError(t, err)
		assert.Empty(t, trusted, "--yes must not persist trust for a new endpoint")
	})

	t.Run("endpoint selection", func(t *testing.T) {
		for _, tt := range []struct {
			name    string
			command string
			unborn  bool
		}{
			{name: "login", command: "login"},
			{name: "init", command: "init"},
			{name: "init unborn", command: "init", unborn: true},
		} {
			t.Run(tt.name, func(t *testing.T) {
				env := noInputCLIEnv(t)
				env = append(env, "SAGEOX_ENDPOINT=") // reach the actual endpoint picker
				repo := t.TempDir()
				mustRunGit(t, repo, "init")
				if !tt.unborn {
					mustRunGit(t, repo, "commit", "--allow-empty", "-m", "Initial commit")
				}
				userFile := filepath.Join(repo, "user.txt")
				userContent := []byte("staged user work\n")
				require.NoError(t, os.WriteFile(userFile, userContent, 0o600))
				mustRunGit(t, repo, "add", "user.txt")
				beforeIndex, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
				require.NoError(t, err)
				beforeHead, _ := runIsolatedGit(t, repo, "rev-parse", "--verify", "HEAD")
				var requests atomic.Int32
				for range 2 {
					server := testguard.SafeMockServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						http.NotFound(w, r)
					}))
					require.NoError(t, auth.SaveTokenForEndpoint(server.URL, &auth.StoredToken{
						AccessToken: "test-access", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour),
					}))
				}
				authPath, err := auth.GetAuthFilePath()
				require.NoError(t, err)
				before, err := os.ReadFile(authPath)
				require.NoError(t, err)

				output, err := runNoInputCLI(t, oxBin, repo, env, nil, tt.command, "--no-input", "--yes")
				require.Error(t, err, "output: %s", output)
				assert.Contains(t, output, "--no-input requires --endpoint <endpoint>")
				after, err := os.ReadFile(authPath)
				require.NoError(t, err)
				assert.Equal(t, before, after, "an unspecified endpoint must not change stored logins")
				assert.Zero(t, requests.Load(), "an unspecified endpoint must not receive requests")
				assert.False(t, config.IsInitialized(repo), "an unspecified endpoint must not initialize the project")
				assert.NoFileExists(t, filepath.Join(repo, ".sageox", "README.md"))
				afterIndex, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
				require.NoError(t, err)
				assert.Equal(t, beforeIndex, afterIndex, "an unspecified endpoint must preserve the index")
				afterUser, err := os.ReadFile(userFile)
				require.NoError(t, err)
				assert.Equal(t, userContent, afterUser)
				afterHead, err := runIsolatedGit(t, repo, "rev-parse", "--verify", "HEAD")
				if tt.unborn {
					assert.Error(t, err, "an unspecified endpoint must leave HEAD unborn")
				} else {
					require.NoError(t, err)
					assert.Equal(t, beforeHead, afterHead)
				}
			})
		}
	})

	t.Run("init without teams explains how to continue", func(t *testing.T) {
		for _, tt := range []struct {
			name         string
			unborn       bool
			explicitTeam bool
			failure      string
		}{
			{name: "committed"},
			{name: "unborn", unborn: true},
			{name: "unborn with explicit team", unborn: true, explicitTeam: true},
			{name: "seed path blocked", unborn: true, explicitTeam: true, failure: "seed"},
			{name: "missing commit object", explicitTeam: true, failure: "fingerprint"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				env := noInputCLIEnv(t)
				repo := t.TempDir()
				mustRunGit(t, repo, "init")
				if !tt.unborn {
					mustRunGit(t, repo, "commit", "--allow-empty", "-m", "Initial commit")
				}
				userFile := filepath.Join(repo, "user.txt")
				userContent := []byte("staged user work\n")
				require.NoError(t, os.WriteFile(userFile, userContent, 0o600))
				mustRunGit(t, repo, "add", "user.txt")
				switch tt.failure {
				case "seed":
					require.NoError(t, os.WriteFile(filepath.Join(repo, ".sageox"), userContent, 0o600))
				case "fingerprint":
					ref, err := runIsolatedGit(t, repo, "symbolic-ref", "HEAD")
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(filepath.Join(repo, ".git", filepath.FromSlash(ref)), []byte(strings.Repeat("1", 40)+"\n"), 0o600))
					_, err = runIsolatedGit(t, repo, "rev-parse", "--verify", "HEAD")
					require.NoError(t, err, "the ref must resolve so seed creation is skipped")
					_, err = runIsolatedGit(t, repo, "rev-list", "HEAD")
					require.Error(t, err, "the missing object must prevent fingerprinting")
				}
				beforeIndex, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
				require.NoError(t, err)
				beforeHead, _ := runIsolatedGit(t, repo, "rev-parse", "--verify", "HEAD")
				var introspections, repoRequests, registrations atomic.Int32
				server := testguard.SafeMockServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case auth.IntrospectEndpoint:
						introspections.Add(1)
						_, _ = io.WriteString(w, `{"active":true,"principal_kind":"user","scope":"*","token_type":"Bearer","user":{"id":"test-user","email":"test@example.com"}}`)
					case "/api/v1/cli/repos":
						repoRequests.Add(1)
						_, _ = io.WriteString(w, `{"repos":{},"teams":[]}`)
					case "/api/v1/repo/init":
						registrations.Add(1)
						_, _ = io.WriteString(w, `{"repo_id":"repo-no-input","team_id":"team-no-input"}`)
					default:
						http.NotFound(w, r)
					}
				}))
				require.NoError(t, auth.SaveTokenForEndpoint(server.URL, &auth.StoredToken{
					AccessToken: "test-access", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour),
				}))

				args := []string{"init", "--endpoint", server.URL, "--no-input"}
				if tt.explicitTeam {
					args = append(args, "--team", "team-no-input", "--agents", "claude-code")
				}
				output, err := runNoInputCLI(t, oxBin, repo, env, nil, args...)
				if tt.explicitTeam && tt.failure == "" {
					require.NoError(t, err, "output: %s", output)
					assert.EqualValues(t, 1, registrations.Load())
					assert.True(t, config.IsInitialized(repo), "explicit choices must still initialize an unborn project")
					_, err = runIsolatedGit(t, repo, "rev-parse", "--verify", "HEAD")
					require.NoError(t, err, "successful initialization must still create its seed commit")
					assert.FileExists(t, filepath.Join(repo, ".sageox", "README.md"))
					return
				}
				require.Error(t, err, "output: %s", output)
				switch tt.failure {
				case "seed":
					assert.Contains(t, output, "failed to create initial commit")
					blockedFile, err := os.ReadFile(filepath.Join(repo, ".sageox"))
					require.NoError(t, err)
					assert.Equal(t, userContent, blockedFile)
				case "fingerprint":
					assert.Contains(t, output, "git repository has no commits")
				default:
					assert.Contains(t, output, "no teams available; create a team first")
					assert.Contains(t, output, "omit --no-input")
				}
				assert.Positive(t, introspections.Load(), "fixture must reach authenticated initialization")
				assert.EqualValues(t, 1, repoRequests.Load(), "fixture must reach the actual zero-team response")
				assert.Zero(t, registrations.Load(), "failed initialization must not register the project")
				assert.False(t, config.IsInitialized(repo))
				if tt.failure != "seed" {
					assert.NoFileExists(t, filepath.Join(repo, ".sageox", "config.json"))
					assert.NoFileExists(t, filepath.Join(repo, ".sageox", "README.md"))
				}
				afterIndex, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
				require.NoError(t, err)
				assert.Equal(t, beforeIndex, afterIndex, "failed initialization must preserve the index")
				afterUser, err := os.ReadFile(userFile)
				require.NoError(t, err)
				assert.Equal(t, userContent, afterUser)
				afterHead, err := runIsolatedGit(t, repo, "rev-parse", "--verify", "HEAD")
				if tt.unborn {
					assert.Error(t, err, "failed initialization must leave HEAD unborn")
				} else {
					require.NoError(t, err)
					assert.Equal(t, beforeHead, afterHead)
				}
			})
		}
	})

	t.Run("dashboard explains no-input conflict", func(t *testing.T) {
		env := append(noInputCLIEnv(t), "FEATURE_TUI=true")
		output, err := runNoInputCLI(t, oxBin, t.TempDir(), env, nil, "dashboard", "--no-input")
		require.Error(t, err, "output: %s", output)
		assert.Contains(t, output, "ox dashboard requires an interactive terminal")
		assert.Contains(t, output, "remove --no-input")
	})

	t.Run("coworker removal requires force", func(t *testing.T) {
		env := noInputCLIEnv(t)
		team := testGitRepo(t)
		project, _ := setupCoworkerProject(t, "team-no-input", []config.TeamContext{{TeamID: "team-no-input", Path: team}})
		coworkerPath := filepath.Join(team, "coworkers", "agents", "reviewer.md")
		content := []byte("---\ndescription: Test reviewer\n---\nReview this team's code.\n")
		require.NoError(t, os.WriteFile(coworkerPath, content, 0o600))
		mustRunGit(t, team, "add", "coworkers/agents/reviewer.md")
		mustRunGit(t, team, "commit", "-m", "Add test coworker")
		beforeHead, err := runIsolatedGit(t, team, "rev-parse", "HEAD")
		require.NoError(t, err)

		output, err := runNoInputCLI(t, oxBin, project, env, nil,
			"coworker", "remove", "reviewer", "--no-input", "--yes")
		require.Error(t, err, "output: %s", output)
		assert.Contains(t, output, "removing a coworker requires confirmation: pass --force")
		after, err := os.ReadFile(coworkerPath)
		require.NoError(t, err)
		assert.Equal(t, content, after)
		afterHead, err := runIsolatedGit(t, team, "rev-parse", "HEAD")
		require.NoError(t, err)
		assert.Equal(t, beforeHead, afterHead, "missing consent must not commit the deletion")
		status, err := runIsolatedGit(t, team, "status", "--porcelain")
		require.NoError(t, err)
		assert.Empty(t, status, "missing consent must leave the worktree and index untouched")

		output, err = runNoInputCLI(t, oxBin, project, env, nil,
			"coworker", "remove", "reviewer", "--no-input", "--force")
		require.NoError(t, err, "output: %s", output)
		assert.Contains(t, output, `Removed coworker "reviewer"`)
		assert.NoFileExists(t, coworkerPath)
		afterHead, err = runIsolatedGit(t, team, "rev-parse", "HEAD")
		require.NoError(t, err)
		assert.NotEqual(t, beforeHead, afterHead, "explicit force must commit the removal")
	})

	t.Run("session redact stops before touching ledger", func(t *testing.T) {
		env := noInputCLIEnv(t)
		ledger := testGitRepo(t)
		mustRunGit(t, ledger, "update-ref", "refs/remotes/origin/main", "HEAD")
		sessionDir := filepath.Join(ledger, "sessions", "leaky")
		require.NoError(t, os.MkdirAll(sessionDir, 0o700))
		rawPath := filepath.Join(sessionDir, "raw.jsonl")
		content := []byte("AKIAIOSFODNN7EXAMPLE\n") // standard fictional AWS key fixture
		require.NoError(t, os.WriteFile(rawPath, content, 0o600))
		mustRunGit(t, ledger, "add", "sessions")
		mustRunGit(t, ledger, "commit", "-m", "Add unpushed test session")
		beforeHead, err := runIsolatedGit(t, ledger, "rev-parse", "HEAD")
		require.NoError(t, err)
		backupDir := filepath.Join(t.TempDir(), "backups")

		output, err := runNoInputCLI(t, oxBin, t.TempDir(), env, nil,
			"session", "redact", "--all", "--ledger-path", ledger, "--backup-dir", backupDir, "--no-input", "--yes")
		require.Error(t, err, "output: %s", output)
		assert.Contains(t, output, "session redact requires interactive input")
		assert.Contains(t, output, "ox session audit")
		assert.NotContains(t, output, "Scanning ledger")
		assert.NoDirExists(t, backupDir, "missing per-file consent must not create a snapshot")
		after, err := os.ReadFile(rawPath)
		require.NoError(t, err)
		assert.Equal(t, content, after)
		afterHead, err := runIsolatedGit(t, ledger, "rev-parse", "HEAD")
		require.NoError(t, err)
		assert.Equal(t, beforeHead, afterHead, "missing per-file consent must not rewrite ledger history")
		status, err := runIsolatedGit(t, ledger, "status", "--porcelain")
		require.NoError(t, err)
		assert.Empty(t, status)
	})

	t.Run("no-input preserves explicit stdin data", func(t *testing.T) {
		env := noInputCLIEnv(t)
		project := t.TempDir()
		require.NoError(t, config.SaveProjectConfig(project, &config.ProjectConfig{RepoID: "repo-stdin"}))
		const payload = "This sample arrived through stdin."
		output, err := runNoInputCLI(t, oxBin, project, env, strings.NewReader(payload),
			"agent", "redact", "test", "-", "--no-input")
		require.NoError(t, err, "output: %s", output)
		var result redactTestJSON
		require.NoError(t, json.Unmarshal([]byte(output), &result), "output: %s", output)
		assert.Equal(t, payload, result.Input)
		assert.Equal(t, payload, result.Output)
	})
}

// Project config currently stores one endpoint, so the multi-endpoint chooser
// needs a direct regression. Closed stdin exposes an accidental default without
// hanging the test, and every shared flag is restored afterward.
func TestNoInputUninstallEndpointSelection(t *testing.T) {
	previousNoInput, previousAll, previousForce := cli.NoInput(), uninstallAll, uninstallForce
	cli.SetNoInput(true)
	t.Cleanup(func() {
		cli.SetNoInput(previousNoInput)
		uninstallAll, uninstallForce = previousAll, previousForce
	})
	for _, tt := range []struct {
		name  string
		all   bool
		force bool
	}{
		{name: "missing choice"},
		{name: "explicit all", all: true},
		{name: "explicit force", force: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			uninstallAll, uninstallForce = tt.all, tt.force
			withStdin(t, "", func() {
				selected, all, err := selectEndpointForUninstall(t.TempDir(), []string{"http://127.0.0.1:8101", "http://127.0.0.1:8102"})
				if !tt.all && !tt.force {
					require.ErrorContains(t, err, "pass --all with --no-input")
					assert.Empty(t, selected)
					assert.False(t, all)
					return
				}
				require.NoError(t, err)
				assert.Empty(t, selected)
				assert.True(t, all)
			})
		})
	}
}

// Isolate both the parent-side auth fixture and the child process. A file-backed
// git credential store also prevents logout from touching the real keychain.
func noInputCLIEnv(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	env := []string{
		"HOME=" + dir,
		"NO_COLOR=1", "OX_XDG_ENABLE=1", "OX_XDG_DISABLE=", "SAGEOX_TOKEN=",
		"OX_GIT_CREDENTIALS_FILE=" + filepath.Join(dir, "git-credentials.json"),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		"SAGEOX_ENDPOINT=http://127.0.0.1:1", "SKIP_BROWSER=1",
		"HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1",
		"NO_PROXY=localhost,127.0.0.1",
	}
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
		env = append(env, key+"="+filepath.Join(dir, key))
	}
	for _, pair := range env {
		key, value, _ := strings.Cut(pair, "=")
		t.Setenv(key, value)
	}
	return env
}

func runNoInputCLI(t *testing.T, binary, dir string, env []string, input io.Reader, args ...string) (string, error) {
	t.Helper()
	if input == nil {
		reader, writer, err := os.Pipe()
		require.NoError(t, err)
		defer reader.Close()
		defer writer.Close() // keep stdin open and unanswered until the command exits
		input = reader
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := testguard.OxCmdContext(t, ctx, binary, dir, env, args...)
	cmd.Stdin = input
	output, err := cmd.CombinedOutput()
	require.NoError(t, ctx.Err(), "command waited for stdin: %s", output)
	return string(output), err
}
