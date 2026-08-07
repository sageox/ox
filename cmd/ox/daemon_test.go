package main

import (
	"testing"

	"github.com/sageox/ox/internal/cli"
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
