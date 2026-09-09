//go:build slow

// Hermetic subprocess and workspace setup for conversation E2E tests.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Harness types
// ---------------------------------------------------------------------------

// conversationE2E holds the paths and environment for one isolated ox process.
// Every writable path lives under t.TempDir().
type conversationE2E struct {
	oxBin       string
	workspace   string
	primaryTeam conversationTeamContext
	configHome  string
	env         []string
}

// conversationTeamContext represents one staged team-context root. slug + id match
// the [[team_contexts]] row written into config.local.toml; path is the
// absolute directory containing the staged discussions.
type conversationTeamContext struct {
	id   string // team_id (e.g., "team_read_e2e")
	name string // team_name (e.g., "Conversation Read E2E")
	slug string // slug for user-facing identifiers
	path string // absolute team-context root
}

// findConversationProjectRoot walks up from the package SOURCE directory
// until it finds a go.mod whose module path is github.com/sageox/ox. Mirrors
// the discovery pattern used by buildOxBinary in incremental_e2e_test.go.
// Suffixed to avoid collision with the production findProjectRoot in
// cmd/ox/agent.go.
//
// It must not start from os.Getwd(): TestMain deliberately moves the process
// out of the ox repository (see cmd/ox/main_test.go).
func findConversationProjectRoot(t *testing.T) string {
	t.Helper()
	dir := packageDir
	for {
		gomod := filepath.Join(dir, "go.mod")
		if data, err := os.ReadFile(gomod); err == nil {
			if strings.Contains(string(data), "github.com/sageox/ox") {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find ox project root walking up from %s", dir)
		}
		dir = parent
	}
}

// ---------------------------------------------------------------------------
// Harness setup
// ---------------------------------------------------------------------------

// setupConversationWorkspace creates an isolated binary, workspace, team
// context, XDG environment, and fake auth using the caller's reference time.
func setupConversationWorkspace(t *testing.T, now time.Time) *conversationE2E {
	t.Helper()

	projectRoot := findConversationProjectRoot(t)
	oxBin := testguard.BuildOxBinary(t, projectRoot)

	// Root tempdir split into labeled subdirs. Each subdir is created
	// explicitly so the XDG reroute is never "auto-created by the first
	// subprocess" — that way a missing dir is an explicit test bug.
	root := t.TempDir()

	workspace := filepath.Join(root, "workspace")
	home := filepath.Join(root, "home")
	teamsRoot := filepath.Join(root, "teams")
	configHome := filepath.Join(root, "xdg", "config")
	dataHome := filepath.Join(root, "xdg", "data")
	stateHome := filepath.Join(root, "xdg", "state")
	cacheHome := filepath.Join(root, "xdg", "cache")
	runtimeDir := filepath.Join(root, "xdg", "run")

	for _, d := range []string{workspace, home, teamsRoot, configHome, dataHome, stateHome, cacheHome, runtimeDir} {
		require.NoError(t, os.MkdirAll(d, 0o755), "mkdir %s", d)
	}

	initConversationGitRepo(t, workspace, home)

	primary := stageConversationTeamContext(t, teamsRoot, conversationTeamContext{
		id:   "team_read_e2e",
		name: "Conversation Read E2E",
		slug: "conversation-read-e2e",
	})

	writeConversationProjectConfig(t, workspace, primary)
	writeConversationLocalConfigTeams(t, workspace, []conversationTeamContext{primary})
	writeConversationFakeAuth(t, configHome, now)

	env := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + configHome,
		"XDG_DATA_HOME=" + dataHome,
		"XDG_STATE_HOME=" + stateHome,
		"XDG_CACHE_HOME=" + cacheHome,
		"XDG_RUNTIME_DIR=" + runtimeDir,
		"OX_XDG_ENABLE=1",
		"SAGEOX_ENDPOINT=https://test.sageox.ai",
	}

	return &conversationE2E{
		oxBin:       oxBin,
		workspace:   workspace,
		primaryTeam: primary,
		configHome:  configHome,
		env:         env,
	}
}

// Run invokes ox under the isolated harness environment.
func (e *conversationE2E) Run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	out, exit, _ := testguard.RunOx(t, e.oxBin, e.workspace, e.env, args...)
	return out, exit
}

// ---------------------------------------------------------------------------
// Workspace + team context primitives
// ---------------------------------------------------------------------------

// initConversationGitRepo initializes a workspace as a git repo with one commit.
// HOME is scoped so git cannot read the developer's ~/.gitconfig.
func initConversationGitRepo(t *testing.T, workspace, home string) {
	t.Helper()

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		cmd.Env = []string{
			"HOME=" + home,
			"PATH=" + os.Getenv("PATH"),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.local",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.local",
		}
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, string(out))
	}

	run("init", "-q")
	run("config", "user.name", "Test")
	run("config", "user.email", "test@test.local")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# test\n"), 0o644))
	run("add", "README.md")
	run("commit", "-q", "-m", "init")
}

// stageConversationTeamContext creates a team-context root for staged discussions.
func stageConversationTeamContext(t *testing.T, teamsRoot string, base conversationTeamContext) conversationTeamContext {
	t.Helper()
	base.path = filepath.Join(teamsRoot, base.id)
	require.NoError(t, os.MkdirAll(base.path, 0o755))
	return base
}

// writeConversationProjectConfig writes workspace/.sageox/config.json with the bits
// the reader needs: config_version, repo_id, team_id, team_name, and
// endpoint. Endpoint is test.sageox.ai so testguard.validateEnv accepts
// the value when conversation tests re-read it.
func writeConversationProjectConfig(t *testing.T, workspace string, primary conversationTeamContext) {
	t.Helper()
	sageoxDir := filepath.Join(workspace, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0o755), "mkdir .sageox")

	cfg := map[string]any{
		"config_version": "2",
		"repo_id":        "repo_conversation_read_e2e",
		"team_id":        primary.id,
		"team_name":      primary.name,
		"endpoint":       "https://test.sageox.ai",
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	require.NoError(t, err, "marshal project config")
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), data, 0o644))
}

// writeConversationLocalConfigTeams rewrites workspace/.sageox/config.local.toml with
// one [[team_contexts]] row per supplied team. Each row carries team_id,
// team_name, slug, path, and a last_sync of zero (the go-toml zero-time
// encoding is fine here — only the path is load-bearing for the reader).
//
// Written directly rather than through internal/config.SaveLocalConfig
// because SaveLocalConfig rejects uninitialized projects, and we skip
// "ox init" on purpose (staging is simpler than driving the CLI).
func writeConversationLocalConfigTeams(t *testing.T, workspace string, teams []conversationTeamContext) {
	t.Helper()
	sageoxDir := filepath.Join(workspace, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0o755))

	var sb strings.Builder
	for _, tc := range teams {
		sb.WriteString("\n[[team_contexts]]\n")
		fmt.Fprintf(&sb, "team_id = %q\n", tc.id)
		fmt.Fprintf(&sb, "team_name = %q\n", tc.name)
		fmt.Fprintf(&sb, "slug = %q\n", tc.slug)
		fmt.Fprintf(&sb, "path = %q\n", tc.path)
		sb.WriteString("last_sync = 0001-01-01T00:00:00Z\n")
	}
	path := filepath.Join(sageoxDir, "config.local.toml")
	require.NoError(t, os.WriteFile(path, []byte(sb.String()), 0o600))
}

// writeConversationFakeAuth writes a minimal auth.json under the rerouted XDG config
// home. No network calls happen in this test suite — the file exists
// only so endpoint resolution inside ox does not refuse to run. `now`
// is the harness's reference instant — `expires_at` is derived from it
// so the file's contents are deterministic across wall-clock drift.
func writeConversationFakeAuth(t *testing.T, configHome string, now time.Time) {
	t.Helper()
	authDir := filepath.Join(configHome, "sageox")
	require.NoError(t, os.MkdirAll(authDir, 0o700))

	token := map[string]any{
		"tokens": map[string]any{
			"test.sageox.ai": map[string]any{
				"access_token": "test-access-token",
				"token_type":   "Bearer",
				"expires_at":   now.Add(24 * time.Hour).Format(time.RFC3339),
			},
		},
	}
	data, err := json.Marshal(token)
	require.NoError(t, err, "marshal auth")
	require.NoError(t, os.WriteFile(filepath.Join(authDir, "auth.json"), data, 0o600))
}
