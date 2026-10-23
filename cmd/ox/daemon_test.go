package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/daemon"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDaemonStart_LongRunningClassification wires the real daemonStartCmd
// through the same classification PersistentPreRunE performs, so the fix is
// verified end-to-end rather than only at the helper level.
//
// Failure prevented: the annotation being dropped, renamed, or attached to the
// wrong command — internal/cli's own tests would still pass while `ox daemon
// start --foreground` went right back to reporting its multi-day lifetime as
// command latency.
func TestDaemonStart_LongRunningClassification(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		// This invocation IS the daemon — it blocks until the daemon exits.
		{"foreground is a service process", []string{"--foreground"}, true},
		// This one spawns a child and returns in milliseconds. It is a real
		// command and must keep being measured.
		{"background spawn is a normal command", nil, false},
		{"explicit --foreground=false is a normal command", []string{"--foreground=false"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Fresh flag state per case: daemonStartCmd is a package-level
			// singleton, so a parse from a prior case would leak.
			resetDaemonStartFlags(t)
			require.NoError(t, daemonStartCmd.ParseFlags(tt.args))

			assert.Equal(t, tt.want, cli.IsLongRunning(daemonStartCmd))
		})
	}
}

// TestDecideDaemonStart pins the full truth table, because the interesting cell
// is the one that used to be wrong in a way that looked like success.
//
// Failure prevented: `ox daemon start --foreground` returning nil when a daemon
// already holds the workspace. That flag is how a SUPERVISED service is started
// — a container execs it as PID 1 — so returning 0 tells the supervisor the
// service finished, and it restarts, finds the same incumbent, and returns 0
// again. Observed in the field as 624 restarts across two days with exitCode=0
// on every one of them (sageox-monorepo#2608). A no-op is correct ONLY for a
// background spawn.
func TestDecideDaemonStart(t *testing.T) {
	tests := []struct {
		name           string
		alreadyRunning bool
		foreground     bool
		want           daemonStartAction
	}{
		{"idle workspace, background spawn", false, false, daemonStartSpawn},
		{"idle workspace, we become the daemon", false, true, daemonStartForeground},
		{"incumbent holds it, background spawn is a no-op", true, false, daemonStartNoop},
		// The regression cell. Anything but takeover here is the crashloop.
		{"incumbent holds it, foreground takes over", true, true, daemonStartTakeover},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, decideDaemonStart(tt.alreadyRunning, tt.foreground))
		})
	}
}

// TestDecideDaemonStart_ForegroundNeverNoops states the invariant directly
// rather than leaving it implicit in the table above: whatever else changes,
// --foreground must never resolve to the branch whose RunE arm returns nil
// without running a daemon.
func TestDecideDaemonStart_ForegroundNeverNoops(t *testing.T) {
	for _, alreadyRunning := range []bool{false, true} {
		got := decideDaemonStart(alreadyRunning, true)
		assert.NotEqualf(t, daemonStartNoop, got,
			"--foreground must never no-op (alreadyRunning=%v): a supervised entrypoint that exits 0 is restarted forever",
			alreadyRunning)
	}
}

// TestOrdinaryCommandsAreMeasured guards the default: only commands that
// explicitly opt out are excluded from latency telemetry.
//
// Failure prevented: a broad annotation (or a bug in the default branch)
// silently blinding telemetry for ordinary commands.
func TestOrdinaryCommandsAreMeasured(t *testing.T) {
	for _, cmd := range []*cobra.Command{rootCmd, statusCmd, doctorCmd, daemonStopCmd, daemonStatusCmd} {
		assert.Falsef(t, cli.IsLongRunning(cmd), "%s must stay in command telemetry", cmd.Name())
	}
}

// resetDaemonStartFlags restores daemonStartCmd's flags to their declared
// defaults so each subtest parses from a clean slate.
func resetDaemonStartFlags(t *testing.T) {
	t.Helper()
	fg := daemonStartCmd.Flags().Lookup("foreground")
	require.NotNil(t, fg, "daemon start must declare --foreground")
	require.NoError(t, fg.Value.Set(fg.DefValue))
	fg.Changed = false
}

// Failure prevented: after StabilizeCWD moves to HOME, a restart loses the
// project and its Ledger. Both version upgrades and first-clone restarts must
// return to the original project, regardless of disposable_machine.
func TestReexecDaemon_ReturnsToTheProject(t *testing.T) {
	for _, disposable := range []bool{false, true} {
		t.Run(fmt.Sprintf("disposable_machine=%v", disposable), func(t *testing.T) {
			project, elsewhere := t.TempDir(), t.TempDir()
			t.Chdir(elsewhere)
			var dir string
			execProcess = func(string, []string, []string) error {
				dir, _ = os.Getwd()
				return nil
			}
			t.Cleanup(func() { execProcess = syscall.Exec })

			cfg := &daemon.Config{ProjectRoot: project, DisposableMachine: disposable}
			require.NoError(t, reexecDaemon(cfg, slog.New(slog.DiscardHandler)))

			want, err := filepath.EvalSymlinks(project)
			require.NoError(t, err)
			got, err := filepath.EvalSymlinks(dir)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

// A deleted worktree must fail the restart rather than serve another directory.
func TestReexecDaemon_DeletedProjectDoesNotExec(t *testing.T) {
	called := false
	execProcess = func(string, []string, []string) error {
		called = true
		return nil
	}
	t.Cleanup(func() { execProcess = syscall.Exec })
	cfg := &daemon.Config{ProjectRoot: filepath.Join(t.TempDir(), "deleted")}

	err := reexecDaemon(cfg, slog.New(slog.DiscardHandler))

	require.ErrorIs(t, err, os.ErrNotExist)
	require.False(t, called)
}

// Failure prevented: disposable_machine is on, but `ox daemon start` never
// hands it to the daemon, so none of the daemon's disposable-machine
// behaviors happen.
func TestDaemonConfig_CarriesDisposableMachine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("disposable_machine: true\n"), 0o600))
	t.Setenv("OX_USER_CONFIG", path)

	require.True(t, daemonConfig("/ledger").DisposableMachine)
}

// Failure prevented: with disposable_machine on, git that ox starts in its
// own clones still runs their hooks and credential helpers, because the root
// command never applies the isolation.
func TestRootCommand_DisposableMachineIsolatesManagedClones(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("disposable_machine: true\n"), 0o600))
	t.Setenv("OX_USER_CONFIG", cfgPath)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("DO_NOT_TRACK", "1")
	for _, name := range []string{"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"} {
		t.Setenv(name, "") // restores what the command sets
	}
	oldOut := rootCmd.OutOrStdout()
	rootCmd.SetOut(io.Discard)
	rootCmd.SetArgs([]string{"config", "get", "disposable_machine"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(oldOut)
	})

	require.NoError(t, rootCmd.Execute())

	require.Equal(t, "1", os.Getenv("GIT_CONFIG_COUNT"))
	require.True(t, strings.HasPrefix(os.Getenv("GIT_CONFIG_KEY_0"), "includeIf.gitdir:"), os.Getenv("GIT_CONFIG_KEY_0"))
}
