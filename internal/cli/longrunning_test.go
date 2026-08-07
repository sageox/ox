package cli

import (
	"errors"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/telemetry"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestCmd builds a command with the given annotation and a --foreground
// bool flag, then parses args through cobra so flag state matches what
// PersistentPreRunE actually sees at runtime.
func newTestCmd(t *testing.T, annotation string, args ...string) *cobra.Command {
	t.Helper()

	cmd := &cobra.Command{
		Use:  "start",
		RunE: func(*cobra.Command, []string) error { return nil },
	}
	cmd.Flags().Bool("foreground", false, "run in foreground")
	if annotation != "" {
		cmd.Annotations = map[string]string{AnnotationLongRunning: annotation}
	}
	require.NoError(t, cmd.ParseFlags(args))
	return cmd
}

// TestIsLongRunning classifies invocations as service-or-command.
//
// Failure prevented: a service process (one that blocks for its whole
// lifetime) being treated as a discrete command, so its lifetime lands in
// command-latency aggregates. `ox daemon start --foreground` did exactly
// this — p50 70 minutes, max 8.8 days.
func TestIsLongRunning(t *testing.T) {
	tests := []struct {
		name       string
		annotation string
		args       []string
		want       bool
	}{
		// The discriminator is the flag, not the command: `ox daemon start`
		// spawns a child and returns in milliseconds, so it stays measured.
		{"flag form, flag set", "flag:foreground", []string{"--foreground"}, true},
		{"flag form, flag unset", "flag:foreground", nil, false},
		{"flag form, flag explicitly false", "flag:foreground", []string{"--foreground=false"}, false},

		{"always form", "true", nil, true},
		{"always form ignores flags", "true", []string{"--foreground"}, true},

		// Everything unannotated is a normal command — the default must be
		// "measure it", so adding a command never silently drops telemetry.
		{"no annotation", "", []string{"--foreground"}, false},

		// Malformed annotations degrade to "normal command" rather than
		// silently suppressing telemetry for a real command.
		{"unknown value", "yes", nil, false},
		{"empty value", "", nil, false},
		{"flag form with empty flag name", "flag:", []string{"--foreground"}, false},
		{"flag form naming a nonexistent flag", "flag:nope", []string{"--foreground"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newTestCmd(t, tt.annotation, tt.args...)
			assert.Equal(t, tt.want, IsLongRunning(cmd))
		})
	}
}

func TestIsLongRunning_NilCommand(t *testing.T) {
	assert.False(t, IsLongRunning(nil))
}

// TestTrackCommand_SuppressedForLongRunning is the regression test for the
// poisoned latency data: a service process reaches the post-run hooks only at
// shutdown, so the elapsed time is its lifetime. Recording it corrupts every
// command-duration percentile in the same aggregate.
//
// Failure prevented: an 8.8-day daemon lifetime reported as a command
// duration. Asserted via CommandCount/TotalDuration because TrackCommand
// feeds both the queued event and these stats from the same call.
func TestTrackCommand_SuppressedForLongRunning(t *testing.T) {
	// Simulates a daemon that has been up for days before shutting down.
	const lifetime = 8*24*time.Hour + 19*time.Hour

	tests := []struct {
		name        string
		longRunning bool
		wantRecords int
	}{
		{"service invocation is not recorded", true, 0},
		{"normal command is still recorded", false, 1},
	}

	for _, tt := range tests {
		for _, path := range []string{"completion", "error"} {
			t.Run(tt.name+"/"+path, func(t *testing.T) {
				client := telemetry.NewClient("test-session", telemetry.WithEnabled(true))
				c := &Context{
					Config:           &config.Config{},
					TelemetryClient:  client,
					CommandStartTime: time.Now().Add(-lifetime),
					LongRunning:      tt.longRunning,
				}

				cmd := newTestCmd(t, "")
				if path == "completion" {
					c.TrackCommandCompletion(cmd)
				} else {
					c.TrackCommandError(cmd, errors.New("daemon exited"))
				}

				stats := client.GetStats()
				assert.Equal(t, tt.wantRecords, stats.CommandCount)
				if tt.wantRecords == 0 {
					assert.Zero(t, stats.TotalDuration,
						"service lifetime must never enter command-duration data")
				} else {
					assert.GreaterOrEqual(t, stats.TotalDuration, lifetime,
						"real commands must still be measured")
				}
			})
		}
	}
}
