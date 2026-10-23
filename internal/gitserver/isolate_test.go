package gitserver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/require"
)

// Failure prevented: on a disposable machine, git that ox runs in its own
// clones runs hooks and asks credential helpers found on the persistent home,
// which a previous job or the machine image may have put there. A hook runs
// with ox's credentials in reach, and another helper is asked for the
// credential first, or given ox's token to store.
func TestIsolateManagedClones(t *testing.T) {
	if testing.Short() {
		t.Skip("short: runs git")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the planted hook and helpers are shell scripts")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	for _, name := range []string{"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"} {
		t.Setenv(name, "") // restores what IsolateManagedClones sets
	}
	t.Setenv("GIT_TERMINAL_PROMPT", "0")

	calls := t.TempDir()
	called := func(name string) bool {
		_, err := os.Stat(filepath.Join(calls, name))
		return err == nil
	}
	reset := func() {
		entries, err := os.ReadDir(calls)
		require.NoError(t, err)
		for _, e := range entries {
			require.NoError(t, os.Remove(filepath.Join(calls, e.Name())))
		}
	}
	global := filepath.Join(home, ".gitconfig")
	require.NoError(t, os.WriteFile(global, fmt.Appendf(nil, "[credential]\n\thelper = \"!f() { touch %s/home-helper; }; f\"\n", calls), 0o644))
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	orig := DefaultHelperCommand()
	t.Cleanup(func() { SetHelperCommand(orig) })
	SetHelperCommand(fmt.Sprintf("!f() { touch %s/ox-helper; }; f", calls))

	newRepo := func(dir string) {
		out, err := exec.Command("git", "init", "-q", dir).CombinedOutput()
		require.NoError(t, err, string(out))
		// post-checkout does not run on an unborn branch
		out, err = exec.Command("git", "-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com",
			"commit", "-q", "--allow-empty", "-m", "first").CombinedOutput()
		require.NoError(t, err, string(out))
		hook := filepath.Join(dir, ".git", "hooks", "post-checkout")
		require.NoError(t, os.WriteFile(hook, fmt.Appendf(nil, "#!/bin/sh\ntouch %s/hook\n", calls), 0o755))
	}
	exercise := func(dir string) {
		_ = exec.Command("git", "-C", dir, "checkout", "-q", "-b", "topic").Run()
		fill := exec.Command("git", "-C", dir, "credential", "fill")
		fill.Stdin = strings.NewReader("protocol=https\nhost=git.example.com\n\n")
		_ = fill.Run() // no helper answers, so git fails after asking them
	}
	managed := filepath.Join(paths.DataDir(), "sageox.ai", "ledgers", "repo_isolated")
	outside := filepath.Join(t.TempDir(), "project")
	newRepo(managed)
	newRepo(outside)

	require.NoError(t, IsolateManagedClones())
	require.NoError(t, IsolateManagedClones(), "a second call, as in a child ox process, must be harmless")

	exercise(managed)
	require.False(t, called("hook"), "a hook ran in ox's clone")
	require.False(t, called("home-helper"), "git asked a credential helper from the home")
	require.True(t, called("ox-helper"), "git must still ask ox's credential helper")

	reset()
	exercise(outside)
	require.True(t, called("hook"), "repositories outside ox's data directory keep their hooks")
	require.True(t, called("home-helper"), "repositories outside ox's data directory keep their helpers")

	require.Equal(t, "1", os.Getenv("GIT_CONFIG_COUNT"), "the second call added a duplicate entry")
}

// Failure prevented: a helper command that spans lines writes its extra lines
// into the include file, where git reads each one as config.
func TestIsolateManagedClones_RefusesAHelperThatSpansLines(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("GIT_CONFIG_COUNT", "")
	orig := DefaultHelperCommand()
	t.Cleanup(func() { SetHelperCommand(orig) })
	SetHelperCommand("!ox git-credential-helper\n[core]\n\thooksPath = /tmp/hooks")

	require.ErrorContains(t, IsolateManagedClones(), "spans lines")
	require.Empty(t, os.Getenv("GIT_CONFIG_COUNT"), "a refused isolation must not point git at an include file")
}
