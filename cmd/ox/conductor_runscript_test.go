package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

	// Nor does doctor ask for a script by hand when the repository has run
	// scripts of its own: the line is off, there is nothing to set up.
	writeConductorSettings(t, repo, conductorSharedSettings, "[scripts.run.dev]\ncommand = \"pnpm dev\"\n")
	res = checkConductorRunScriptAt(repo, true)
	assert.True(t, res.passed, "%+v", res)
	assert.False(t, res.warning, "%+v", res)
	assert.Empty(t, readConductorLocal(t, repo))
}

// TestConductorRunScript_KeepsSettingsPrivate: settings.local.toml can hold
// a coworker's environment values. Failure prevented: installing ox's run
// script turns a 0600 file world-readable, or creates a new one that way.
func TestConductorRunScript_KeepsSettingsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not hold on Windows")
	}
	modeOf := func(repo string) os.FileMode {
		info, err := os.Stat(filepath.Join(repo, ".conductor", conductorLocalSettings))
		require.NoError(t, err)
		return info.Mode().Perm()
	}

	private := conductorRepo(t)
	writeConductorSettings(t, private, conductorLocalSettings, "[env]\nTOKEN = \"x\"\n")
	local := filepath.Join(private, ".conductor", conductorLocalSettings)
	require.NoError(t, os.Chmod(local, 0o600))
	require.Equal(t, os.FileMode(0o600), modeOf(private), "precondition: the file starts private")
	require.True(t, checkConductorRunScriptAt(private, true).passed)
	assert.Contains(t, readConductorLocal(t, private), "[scripts.run.ox]")
	assert.Equal(t, os.FileMode(0o600), modeOf(private))

	fresh := conductorRepo(t)
	require.True(t, checkConductorRunScriptAt(fresh, true).passed)
	assert.Equal(t, os.FileMode(0o600), modeOf(fresh), "a new file is private")

	shared := conductorRepo(t)
	writeConductorSettings(t, shared, conductorLocalSettings, "# mine\n")
	// Set, not assumed: the mode a file is created with depends on the umask.
	require.NoError(t, os.Chmod(filepath.Join(shared, ".conductor", conductorLocalSettings), 0o644))
	require.Equal(t, os.FileMode(0o644), modeOf(shared), "precondition: the file starts shared")
	require.True(t, checkConductorRunScriptAt(shared, true).passed)
	assert.Equal(t, os.FileMode(0o644), modeOf(shared), "a mode the coworker chose is kept")
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

// TestConductorRunScript_LeavesATrackedLocalFileAlone: git ignores no tracked
// file. Failure prevented: ox writing its machine-local run script into a
// settings.local.toml the team committed, to ride along in the next commit.
func TestConductorRunScript_LeavesATrackedLocalFileAlone(t *testing.T) {
	repo := conductorRepo(t)
	writeConductorSettings(t, repo, conductorLocalSettings, "# committed by the team\n")
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	git("add", "-f", ".conductor/"+conductorLocalSettings)
	git("commit", "-q", "-m", "settings")
	require.True(t, conductorLocalTracked(repo), "precondition: the file is tracked")

	res := checkConductorRunScriptAt(repo, true)
	assert.True(t, res.warning, "%+v", res)
	assert.Contains(t, res.detail, "[scripts.run.ox]", "the coworker is told what to add by hand")
	assert.Equal(t, "# committed by the team\n", readConductorLocal(t, repo))

	ensureConductorRunScript(repo, true)
	assert.Equal(t, "# committed by the team\n", readConductorLocal(t, repo), "ox init leaves it alone too")
}

// TestConductorRunScript_ExclusionFailureIsRetried: the file must be kept out
// of git. Failure prevented: the settings written, the exclusion failed, and
// every later run reporting "installed" with the file still open to a commit.
func TestConductorRunScript_ExclusionFailureIsRetried(t *testing.T) {
	ignored := func(repo string) bool {
		return exec.Command("git", "-C", repo, "check-ignore", "-q", ".conductor/"+conductorLocalSettings).Run() == nil
	}

	// The exclude file cannot be written (a directory stands in its place, on
	// every platform): nothing is installed, and the failure is reported.
	repo := conductorRepo(t)
	exclude := filepath.Join(repo, ".git", "info", "exclude")
	require.NoError(t, os.RemoveAll(exclude))
	require.NoError(t, os.MkdirAll(exclude, 0o755))
	res := checkConductorRunScriptAt(repo, true)
	assert.False(t, res.passed, "%+v", res)
	assert.Empty(t, readConductorLocal(t, repo), "no settings are written before the file is kept out of git")

	// Repaired, the next run installs and excludes.
	require.NoError(t, os.RemoveAll(exclude))
	require.True(t, checkConductorRunScriptAt(repo, true).passed)
	assert.Contains(t, readConductorLocal(t, repo), "[scripts.run.ox]")
	assert.True(t, ignored(repo))

	// An install an older ox left without its exclusion is finished by --fix.
	old := conductorRepo(t)
	writeConductorSettings(t, old, conductorLocalSettings, conductorRunScriptBlock)
	require.False(t, ignored(old), "precondition: installed, not excluded")
	require.True(t, checkConductorRunScriptAt(old, true).passed)
	assert.True(t, ignored(old))
}

// TestConductorRunScript_DoesNotWriteThroughALink: an untracked
// settings.local.toml can be a symbolic link to the committed settings.toml.
// Failure prevented: the write following the link and putting ox's
// machine-local run script into the file the whole team shares.
func TestConductorRunScript_DoesNotWriteThroughALink(t *testing.T) {
	repo := conductorRepo(t)
	const shared = "# the team's settings\n"
	writeConductorSettings(t, repo, conductorSharedSettings, shared)
	link := filepath.Join(repo, ".conductor", conductorLocalSettings)
	if err := os.Symlink(conductorSharedSettings, link); err != nil {
		t.Skipf("symbolic links are not available here: %v", err)
	}
	info, err := os.Lstat(link)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink, "precondition: the local settings are a link")

	res := checkConductorRunScriptAt(repo, true)
	assert.True(t, res.warning, "%+v", res)
	ensureConductorRunScript(repo, true)

	data, err := os.ReadFile(filepath.Join(repo, ".conductor", conductorSharedSettings))
	require.NoError(t, err)
	assert.Equal(t, shared, string(data), "the shared settings are untouched")
}
