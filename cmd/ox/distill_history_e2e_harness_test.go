//go:build slow

// distill_history_e2e_harness_test.go — hermetic E2E harness for the ox
// distill history read surface. Owns the types, the per-test setup function,
// the workspace + team-context primitives, the git init helper, the
// fake-auth writer, and the small assertion helpers.
//
// Fixture stagers and the 12 recipes live in
// distill_history_e2e_fixtures_test.go. The sanity tests that prove the
// whole thing wires up live in distill_history_e2e_test.go. The JR-*
// skeleton test cases live in the list/show/since files.
//
// See docs/specs/distill-history-read-test-plan.md §1.

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

// distillHistoryE2E is the bundle of paths and environment that a JR-* test uses
// to drive one ox subprocess against a staged team context. Every field
// lives under a t.TempDir(); nothing in here points at the developer's
// real $HOME or their real XDG data.
type distillHistoryE2E struct {
	oxBin string // built via testguard.BuildOxBinary

	// projectRoot is the ox repo root (where go.mod lives). Captured so the
	// test file never changes process cwd — it is only used by buildOxBinary.
	projectRoot string

	// workspace is the cwd the ox subprocess runs in. It is a git-init'd
	// repo with a single commit and a .sageox/ directory containing
	// config.json + config.local.toml. Corresponds to W in the test plan.
	workspace string

	// primaryTeam is the team-context root that holds memory/daily and the
	// fact-file subdirectories. Corresponds to T (or T1 for multi-team).
	primaryTeam teamContext

	// home / XDG dirs are rerouted under a fresh tempdir so nothing the
	// subprocess does can touch the developer's real ~/.sageox/ or
	// ~/.local/share/sageox/.
	home       string
	configHome string
	dataHome   string
	stateHome  string
	cacheHome  string
	runtimeDir string

	// env is the full slice of KEY=VALUE strings to pass into
	// testguard.RunOx. It includes XDG reroute, HOME, OX_XDG_ENABLE, and
	// SAGEOX_ENDPOINT=https://test.sageox.ai. testguard.MinimalEnv
	// allowlists the rest.
	env []string

	// now is the single reference instant the test captured at t.Run
	// entry. Every helper that needs a clock value reads from this field
	// rather than calling time.Now() again — see plan §4.3. Stamped once
	// by setupDistillHistoryE2E from its parameter and never mutated afterward.
	now time.Time
}

// teamContext represents one staged team-context root. slug + id match
// the [[team_contexts]] row written into config.local.toml; path is the
// absolute directory containing memory/daily, memory/weekly, etc.
type teamContext struct {
	id   string // team_id (e.g., "team_read_e2e")
	name string // team_name (e.g., "Journal Read E2E")
	slug string // slug for user-facing identifiers
	path string // absolute team-context root
}

// stagedDaily is a receipt for one daily file staged by a recipe. Recipes
// return a slice of these so the sanity test can assert the expected
// files exist with the expected metadata, and so JR-* tests can reference
// the exact IDs/dates without recomputing them.
type stagedDaily struct {
	Path    string    // absolute path on disk
	Rel     string    // memory/daily/<name>
	ID      string    // stem (filename without ".md")
	Date    string    // YYYY-MM-DD — the filename date prefix
	Team    string    // team slug this file belongs to
	Mtime   time.Time // explicit mtime set via os.Chtimes
	Sources []string  // source files listed in frontmatter
}

// stagedFact is a receipt for one fact JSONL file staged by a recipe.
type stagedFact struct {
	Path     string // absolute path on disk
	Rel      string // memory/.<kind>-facts/<name>
	Team     string // team slug
	FactType string // ".github-facts", ".session-facts", ".discussion-facts"
	Facts    int    // number of body lines (0 for empty-marker files)
}

// stagedFixture is what every recipe returns. Recipes can stage zero
// files of either kind; the sanity test treats zero-count recipes
// correctly (see EmptyMarkerOnly).
type stagedFixture struct {
	Dailies []stagedDaily
	Facts   []stagedFact
	// ExtraFiles are any other files the recipe staged that are not daily
	// .md files — e.g., malformed filenames, bad-frontmatter siblings.
	// Recorded so the sanity test can assert they exist.
	ExtraFiles []string
}

// ---------------------------------------------------------------------------
// Binary build
// ---------------------------------------------------------------------------

// findDistillHistoryProjectRoot walks up from cwd until it finds a go.mod whose
// module path is github.com/sageox/ox. Mirrors the discovery pattern
// used by buildOxBinary in incremental_e2e_test.go. Suffixed to avoid
// collision with the production findProjectRoot in cmd/ox/agent.go.
func findDistillHistoryProjectRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err, "getwd")
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

// setupDistillHistoryE2E builds a per-test harness: fresh ox binary, fresh
// workspace with .sageox/config.json, fresh primary team context, fresh
// XDG reroute tempdirs, fake auth.json, and a config.local.toml pointing
// at the primary team. The returned struct is self-contained — call
// runDistillHistoryOx to invoke the binary against it, and call stage* helpers
// with e2e.primaryTeam.path to stage fixture files.
//
// `now` is captured once by the caller and passed through to recipes so
// any time-relative assertions are stable across wall-clock drift (see
// docs/specs/distill-history-read-test-plan.md §4.3). The value is stored on
// the returned *distillHistoryE2E so time-dependent helpers (writeFakeAuth
// today, future ones) never call time.Now() independently.
func setupDistillHistoryE2E(t *testing.T, now time.Time) *distillHistoryE2E {
	t.Helper()

	projectRoot := findDistillHistoryProjectRoot(t)
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

	initDistillHistoryGitRepo(t, workspace, home)

	primary := stageTeamContext(t, teamsRoot, teamContext{
		id:   "team_read_e2e",
		name: "Journal Read E2E",
		slug: "journal-read-e2e",
	})

	writeProjectConfig(t, workspace, primary)
	writeLocalConfigTeams(t, workspace, []teamContext{primary})
	writeFakeAuth(t, configHome, now)

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

	return &distillHistoryE2E{
		oxBin:       oxBin,
		projectRoot: projectRoot,
		workspace:   workspace,
		primaryTeam: primary,
		home:        home,
		configHome:  configHome,
		dataHome:    dataHome,
		stateHome:   stateHome,
		cacheHome:   cacheHome,
		runtimeDir:  runtimeDir,
		env:         env,
		now:         now,
	}
}

// addSecondaryTeam stages a second team-context root and re-writes
// config.local.toml so both teams are registered. Returns the new team
// context. Used by MultiTeamList and any JR-* case that needs --all-teams
// semantics.
func addSecondaryTeam(t *testing.T, e2e *distillHistoryE2E, id, name, slug string) teamContext {
	t.Helper()

	teamsRoot := filepath.Dir(e2e.primaryTeam.path)
	secondary := stageTeamContext(t, teamsRoot, teamContext{
		id:   id,
		name: name,
		slug: slug,
	})

	writeLocalConfigTeams(t, e2e.workspace, []teamContext{e2e.primaryTeam, secondary})
	return secondary
}

// Run invokes the ox subprocess under the harness env. Thin wrapper
// over testguard.RunOx so JR-* tests can call
// e2e.Run(t, "distill", "history", "list", ...) without recomputing env every
// time. testguard.RunOx also returns an elapsed duration; it is
// dropped here because no current caller asserts on it. If a future
// performance-budget assertion needs it, reintroduce it at the same
// time the assertion lands — no call site cares today.
func (e *distillHistoryE2E) Run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	out, exit, _ := testguard.RunOx(t, e.oxBin, e.workspace, e.env, args...)
	return out, exit
}

// ---------------------------------------------------------------------------
// Workspace + team context primitives
// ---------------------------------------------------------------------------

// initDistillHistoryGitRepo initializes a workspace as a git repo with one commit.
// HOME is scoped so git cannot read the developer's ~/.gitconfig.
func initDistillHistoryGitRepo(t *testing.T, workspace, home string) {
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

// stageTeamContext creates a team-context root directory and pre-creates
// the memory subtree the distill history reader expects. Returns a populated
// teamContext value whose path points at the new root.
func stageTeamContext(t *testing.T, teamsRoot string, base teamContext) teamContext {
	t.Helper()
	path := filepath.Join(teamsRoot, base.id)
	subdirs := []string{
		filepath.Join("memory", "daily"),
		filepath.Join("memory", "weekly"),
		filepath.Join("memory", "monthly"),
		filepath.Join("memory", ".github-facts"),
		filepath.Join("memory", ".session-facts"),
		filepath.Join("memory", ".discussion-facts"),
	}
	for _, sub := range subdirs {
		require.NoError(t, os.MkdirAll(filepath.Join(path, sub), 0o755), "mkdir team subdir %s", sub)
	}
	base.path = path
	return base
}

// writeProjectConfig writes workspace/.sageox/config.json with the bits
// the reader needs: config_version, repo_id, team_id, team_name, and
// endpoint. Endpoint is test.sageox.ai so testguard.validateEnv accepts
// the value when the JR-* tests re-read it.
func writeProjectConfig(t *testing.T, workspace string, primary teamContext) {
	t.Helper()
	sageoxDir := filepath.Join(workspace, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0o755), "mkdir .sageox")

	cfg := map[string]any{
		"config_version": "2",
		"repo_id":        "repo_distill_history_read_e2e",
		"team_id":        primary.id,
		"team_name":      primary.name,
		"endpoint":       "https://test.sageox.ai",
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	require.NoError(t, err, "marshal project config")
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), data, 0o644))
}

// writeLocalConfigTeams rewrites workspace/.sageox/config.local.toml with
// one [[team_contexts]] row per supplied team. Each row carries team_id,
// team_name, slug, path, and a last_sync of zero (the go-toml zero-time
// encoding is fine here — only the path is load-bearing for the reader).
//
// Written directly rather than through internal/config.SaveLocalConfig
// because SaveLocalConfig rejects uninitialized projects, and we skip
// "ox init" on purpose (staging is simpler than driving the CLI).
func writeLocalConfigTeams(t *testing.T, workspace string, teams []teamContext) {
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

// writeFakeAuth writes a minimal auth.json under the rerouted XDG config
// home. No network calls happen in this test suite — the file exists
// only so endpoint resolution inside ox does not refuse to run. `now`
// is the harness's reference instant — `expires_at` is derived from it
// so the file's contents are deterministic across wall-clock drift.
func writeFakeAuth(t *testing.T, configHome string, now time.Time) {
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

// ---------------------------------------------------------------------------
// Small assertion helpers (kept local to avoid bloating the shared test
// helper surface in this package).
// ---------------------------------------------------------------------------

// mustExist fails the test if path does not exist on disk.
func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected path to exist: %s: %v", path, err)
	}
}

// envHasPrefix reports whether any KEY=VALUE entry in env starts with
// prefix. Used by the harness shape test to assert the XDG reroute is
// present without caring about specific tempdir paths.
func envHasPrefix(env []string, prefix string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}
