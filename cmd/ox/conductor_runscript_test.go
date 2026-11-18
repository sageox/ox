package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/config"
)

// Conductor's chat has no status bar; `ox doctor --fix` puts ox's line in its
// Run pane by adding an "ox" run script to the machine-local
// .conductor/settings.local.toml. These tests drive the real doctor check.

// conductorRepo is a git repository Conductor drives, as its root checkout.
func conductorRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("CONDUCTOR_ROOT_PATH", "")
	stubOrchestrator(t, "conductor")
	repo := t.TempDir()
	require.NoError(t, exec.Command("git", "-C", repo, "init", "-q").Run())
	resolved, err := filepath.EvalSymlinks(repo)
	require.NoError(t, err)
	return resolved
}

func writeConductorSettings(t *testing.T, repo, name, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".conductor"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".conductor", name), []byte(body), 0o644))
}

func readConductorLocal(t *testing.T, repo string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, ".conductor", conductorLocalSettings))
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return string(data)
}

// TestConductorRunScript_DoctorAddsIt is the customer promise: in a
// repository Conductor drives, `ox doctor --fix` puts ox's line in the Run
// pane, keeps the coworker's own local settings, and keeps the file out of
// git. Failure prevented: a Conductor coworker with no way to see the line,
// or ox committing a machine-local file for them.
func TestConductorRunScript_DoctorAddsIt(t *testing.T) {
	repo := conductorRepo(t)
	userSettings := "# my overrides\n[environment_variables]\nFOO = \"bar\"\n"
	writeConductorSettings(t, repo, conductorLocalSettings, userSettings)

	res := checkConductorRunScriptAt(repo, false)
	assert.True(t, res.warning, "missing is reported before --fix: %+v", res)
	assert.Equal(t, userSettings, readConductorLocal(t, repo), "a check without --fix writes nothing")

	res = checkConductorRunScriptAt(repo, true)
	assert.True(t, res.passed, "%+v", res)

	body := readConductorLocal(t, repo)
	assert.True(t, strings.HasPrefix(body, userSettings), "the coworker's settings and comments survive")
	var doc struct {
		Environment map[string]string `toml:"environment_variables"`
		Scripts     struct {
			Run map[string]struct {
				Command     string   `toml:"command"`
				AvailableIn []string `toml:"available_in"`
			} `toml:"run"`
		} `toml:"scripts"`
	}
	require.NoError(t, toml.Unmarshal([]byte(body), &doc))
	assert.Equal(t, "bar", doc.Environment["FOO"])
	assert.Equal(t, "ox statusline --watch", doc.Scripts.Run["ox"].Command)
	assert.Equal(t, []string{"local"}, doc.Scripts.Run["ox"].AvailableIn)

	out, err := exec.Command("git", "-C", repo, "status", "--porcelain", "--untracked-files=all").Output()
	require.NoError(t, err)
	assert.NotContains(t, string(out), conductorLocalSettings, "the machine-local file is never offered for commit")

	res = checkConductorRunScriptAt(repo, true)
	assert.True(t, res.passed)
	assert.Equal(t, body, readConductorLocal(t, repo), "a second --fix changes nothing")
}

// TestConductorRunScript_LeavesSettingsAlone covers the repositories ox must
// not write to. Failure prevented: a local [scripts.run] table hiding the
// team's dev-server script, a coworker's own "ox" script overwritten, a
// broken TOML file made worse, or Conductor settings in a repository that
// does not use Conductor.
func TestConductorRunScript_LeavesSettingsAlone(t *testing.T) {
	tests := []struct {
		name         string
		shared       string
		local        string
		orchestrator string
		wantSkipped  bool
		wantWarning  bool
	}{
		{name: "team run script in shared settings", shared: "[scripts.run.dev]\ncommand = \"pnpm dev\"\n", orchestrator: "conductor", wantWarning: true},
		{name: "legacy string run script", shared: "[scripts]\nrun = \"make dev\"\n", orchestrator: "conductor", wantWarning: true},
		{name: "coworker's own ox script", local: "[scripts.run.ox]\ncommand = \"ox status\"\n", orchestrator: "conductor"},
		{name: "unreadable settings", local: "[scripts\n", orchestrator: "conductor", wantWarning: true},
		{name: "not a Conductor repository", orchestrator: "", wantSkipped: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := conductorRepo(t)
			stubOrchestrator(t, tt.orchestrator)
			if tt.shared != "" {
				writeConductorSettings(t, repo, conductorSharedSettings, tt.shared)
			}
			if tt.local != "" {
				writeConductorSettings(t, repo, conductorLocalSettings, tt.local)
			}

			res := checkConductorRunScriptAt(repo, true)
			assert.Equal(t, tt.wantSkipped, res.skipped, "%+v", res)
			assert.Equal(t, tt.wantWarning, res.warning, "%+v", res)
			assert.Equal(t, tt.local, readConductorLocal(t, repo), "settings.local.toml is untouched")
		})
	}
}

// TestConductorRunScript_StatuslineOff respects `ox config set statusline
// off`. Failure prevented: doctor re-adding a run script the coworker turned
// off.
func TestConductorRunScript_StatuslineOff(t *testing.T) {
	repo := conductorRepo(t)
	setStatusline(t, false)

	res := checkConductorRunScriptAt(repo, true)
	assert.True(t, res.passed, "%+v", res)
	assert.Empty(t, readConductorLocal(t, repo))
}

// TestConductorRepoRoot_FromAWorkspaceWorktree finds the repository root, not
// the workspace worktree, since Conductor keeps .conductor/ in the root.
// Failure prevented: the run script written into a throwaway worktree.
func TestConductorRepoRoot_FromAWorkspaceWorktree(t *testing.T) {
	repo := conductorRepo(t)
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(t.TempDir(), "bangkok")
	run("worktree", "add", "-q", wt)

	assert.Equal(t, repo, conductorRepoRoot(wt))
	t.Setenv("CONDUCTOR_ROOT_PATH", "/conductor/root")
	assert.Equal(t, "/conductor/root", conductorRepoRoot(wt), "Conductor's own answer wins")
}
