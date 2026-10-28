package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/require"
)

// startTestRecording starts a recording for the given agent and returns the state.
func startTestRecording(t *testing.T, projectRoot, agentID, adapterName string) *session.RecordingState {
	t.Helper()
	state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID:     agentID,
		AdapterName: adapterName,
		Username:    "testuser",
	})
	require.NoError(t, err)
	require.NotNil(t, state)
	return state
}

// skipIntegration skips tests that intentionally exercise process, git, or
// filesystem boundaries. It lives in an untagged helper file because both
// fast and full test files use it; hiding the helper behind !short makes the
// fast package fail to compile before tests can declare their own tier.
func skipIntegration(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
}

// testGitRepo initializes a temporary git repository for integration tests.
func testGitRepo(t *testing.T) string {
	t.Helper()
	skipIntegration(t)

	tmpDir := t.TempDir()
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.name", "Test User"},
		{"git", "config", "user.email", "test@example.com"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = tmpDir
		cmd.Env = isolatedGitEnv(tmpDir)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "failed: %v: %s", args, string(out))
	}

	readmePath := filepath.Join(tmpDir, "README.md")
	require.NoError(t, os.WriteFile(readmePath, []byte("# Test\n"), 0o644), "failed to create README")
	cmd := exec.Command("git", "add", "README.md")
	cmd.Dir = tmpDir
	cmd.Env = isolatedGitEnv(tmpDir)
	require.NoError(t, cmd.Run(), "failed to git add")
	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = tmpDir
	cmd.Env = isolatedGitEnv(tmpDir)
	require.NoError(t, cmd.Run(), "failed to git commit")
	return tmpDir
}

// runIsolatedGit always pins cmd.Dir and scrubs per-user git configuration.
// Tests must never alter the host's git identity.
func runIsolatedGit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = isolatedGitEnv(dir)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// isolatedGitEnv removes repository-selection variables inherited from the
// developer shell before applying the test repository's hermetic settings.
func isolatedGitEnv(dir string) []string {
	overrides := map[string]string{
		"HOME":                dir,
		"XDG_CONFIG_HOME":     filepath.Join(dir, ".config"),
		"GIT_CONFIG_GLOBAL":   "/dev/null",
		"GIT_CONFIG_SYSTEM":   "/dev/null",
		"GIT_AUTHOR_NAME":     "Test",
		"GIT_AUTHOR_EMAIL":    "test@example.com",
		"GIT_COMMITTER_NAME":  "Test",
		"GIT_COMMITTER_EMAIL": "test@example.com",
	}
	blocked := map[string]bool{
		"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_COMMON_DIR": true, "GIT_INDEX_FILE": true,
	}
	for key := range overrides {
		blocked[key] = true
	}
	baseEnv := os.Environ() // safe: repository selectors are filtered below before any test subprocess starts
	env := make([]string, 0, len(baseEnv)+len(overrides))
	for _, entry := range baseEnv {
		key, _, _ := strings.Cut(entry, "=")
		if !blocked[key] {
			env = append(env, entry)
		}
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func mustRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := runIsolatedGit(t, dir, args...)
	require.NoErrorf(t, err, "git %s: %s", strings.Join(args, " "), out)
}

// noBackgroundGitMaintenance stops git from forking a detached
// `git maintenance run --auto` (or `gc --auto`) into a fixture repository.
// The *.auto keys keep it from triggering; the autoDetach keys make anything
// that still triggers finish before the git command that started it returns.
const noBackgroundGitMaintenance = `[gc]
	auto = 0
	autoDetach = false
[maintenance]
	auto = false
	autoDetach = false
[receive]
	autogc = false
`

// disableBackgroundGitMaintenance appends noBackgroundGitMaintenance to the
// repository's own config. gitDir is a bare repository, or a clone's .git.
//
// Repository config is the only place these settings reliably reach a bare
// origin. A push to a local path starts receive-pack with GIT_CONFIG_COUNT and
// GIT_CONFIG_PARAMETERS removed from its environment, so the Makefile's
// TEST_GIT_ISOLATION never arrives there. receive-pack then runs
// `git maintenance run --auto --detach` with git's defaults, the push returns
// while that process is still writing into the origin, and t.TempDir()
// cleanup fails with "unlinkat <origin>: directory not empty".
func disableBackgroundGitMaintenance(t *testing.T, gitDir string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(gitDir, "config"), os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err, "open git config in %s", gitDir)
	_, writeErr := f.WriteString(noBackgroundGitMaintenance)
	require.NoError(t, f.Close())
	require.NoError(t, writeErr)
}

// initBareRemote creates a bare repository at path to stand in for a hosted
// origin, with background maintenance disabled. initArgs go to `git init`,
// e.g. "--initial-branch=main". Every bare origin in this package's tests is
// created here or by cloneBareRemote (TestBareRemoteFixturesGoThroughHelpers).
func initBareRemote(t *testing.T, path string, initArgs ...string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(path, 0o755))
	mustRunGit(t, path, append([]string{"init", "-q", "--bare"}, initArgs...)...)
	disableBackgroundGitMaintenance(t, path)
}

// cloneBareRemote is initBareRemote for an origin seeded from source.
func cloneBareRemote(t *testing.T, source, path string) {
	t.Helper()
	mustRunGit(t, filepath.Dir(path), "clone", "-q", "--bare", source, path)
	disableBackgroundGitMaintenance(t, path)
}

// A push into a fixture origin must not leave a git process running there.
// Failure prevented: receive-pack forks a detached maintenance run into the
// origin, the test returns, and t.TempDir() cleanup fails with "directory not
// empty" — the CI-only flake in
// TestRunPlanBackfillTitlesOnLedger_IncompleteCorpusAbortsInDryRun.
func TestInitBareRemote_PushStartsNoBackgroundMaintenance(t *testing.T) {
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	initBareRemote(t, origin)
	work := filepath.Join(root, "work")
	require.NoError(t, os.MkdirAll(work, 0o755))
	initGitRepo(t, work)
	mustRunGit(t, work, "remote", "add", "origin", origin)

	trace := filepath.Join(root, "trace2.jsonl")
	cmd := exec.Command("git", "push", "-q", "origin", "HEAD:refs/heads/main")
	cmd.Dir = work
	cmd.Env = append(isolatedGitEnv(work), "GIT_TRACE2_EVENT="+trace)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git push: %s", out)

	raw, err := os.ReadFile(trace)
	require.NoError(t, err)
	sawReceivePack := false
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var event struct {
			Event string   `json:"event"`
			Argv  []string `json:"argv"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &event), line)
		if event.Event == "start" && strings.Contains(strings.Join(event.Argv, " "), "receive-pack") {
			sawReceivePack = true
		}
		if event.Event == "child_start" && len(event.Argv) > 1 && (event.Argv[1] == "maintenance" || event.Argv[1] == "gc") {
			t.Errorf("push started background maintenance in the fixture origin: %s", strings.Join(event.Argv, " "))
		}
	}
	// Without receive-pack's own events, the absence of a maintenance child
	// above would prove nothing.
	require.True(t, sawReceivePack, "trace2 never reached the receiving side of the push")
}

// Failure prevented: a new fixture runs `git init --bare` directly, keeps
// git's default receive.autogc, and brings back the background-maintenance
// cleanup flake, which appears only on a loaded CI runner.
func TestBareRemoteFixturesGoThroughHelpers(t *testing.T) {
	files, err := filepath.Glob(repoPath("*_test.go"))
	require.NoError(t, err)
	require.NotEmpty(t, files, "no test sources under %s", packageDir)
	for _, file := range files {
		if filepath.Base(file) == "test_helpers_test.go" {
			continue
		}
		data, err := os.ReadFile(file)
		require.NoError(t, err)
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, `"--bare"`) {
				t.Errorf("%s:%d creates a bare repository directly; use initBareRemote or cloneBareRemote: %s",
					filepath.Base(file), i+1, strings.TrimSpace(line))
			}
		}
	}
}

// initGitRepo creates a minimal git repo in the given directory. Keep this
// helper available to every tier; callers decide whether their scenario is
// fast enough to run under testing.Short.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.name", "Dev"},
		{"git", "config", "user.email", "dev@example.com"},
		{"git", "config", "commit.gpgsign", "false"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = isolatedGitEnv(dir)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "failed: %v: %s", args, string(out))
	}

	readme := filepath.Join(dir, "README.md")
	require.NoError(t, os.WriteFile(readme, []byte("repo"), 0o644))
	cmd := exec.Command("git", "add", "README.md")
	cmd.Dir = dir
	cmd.Env = isolatedGitEnv(dir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add: %s", string(out))
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = dir
	cmd.Env = isolatedGitEnv(dir)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit: %s", string(out))
}

func TestIsolatedGitEnvDropsRepositorySelectors(t *testing.T) {
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE"} {
		t.Setenv(key, "/must/not/leak")
	}

	env := isolatedGitEnv(t.TempDir())
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		require.NotContains(t, []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE"}, key)
	}
}
