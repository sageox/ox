//go:build !windows

package main

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWrappedCommand_StopsWithOx: ox turns its first SIGTERM or SIGINT into a
// canceled context. Failure prevented: the wrapper ignoring it, so ox and the
// app keep running and a second signal kills ox with the terminal still raw.
func TestWrappedCommand_StopsWithOx(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		// ownGroup runs the app as its pseudo-terminal would: leading a group.
		ownGroup bool
	}{
		{name: "an app in its own process group, with a child", argv: []string{"sh", "-c", "sleep 30 & wait"}, ownGroup: true},
		{name: "an app sharing ox's process group", argv: []string{"sleep", "30"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cmd := wrappedCommand(ctx, tt.argv)
			if tt.ownGroup {
				cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			}
			require.NoError(t, cmd.Start())
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				t.Fatalf("the app must still be running before ox is signaled: %v", err)
			case <-time.After(100 * time.Millisecond):
			}

			start := time.Now()
			cancel()
			select {
			case err := <-done:
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit)
				status, ok := exit.Sys().(syscall.WaitStatus)
				require.True(t, ok)
				assert.Equal(t, syscall.SIGTERM, status.Signal(), "asked to stop, not killed")
				assert.Less(t, time.Since(start), wrapStopGrace, "it stopped when asked, without waiting out the grace period")
			case <-time.After(wrapStopGrace + 5*time.Second):
				t.Fatal("the wrapped app outlived ox's stop request")
			}
		})
	}
}

// TestWrappedEnv_AppKeepsItsColor: ox forces NO_COLOR=1 on itself in some
// contexts. Failure prevented: the wrapped app (Codex) inheriting it and
// drawing with no color at all, losing its shaded input band and highlights.
func TestWrappedEnv_AppKeepsItsColor(t *testing.T) {
	prevVal, prevHas := statuslineNoColor, statuslineHasNoC
	t.Cleanup(func() { statuslineNoColor, statuslineHasNoC = prevVal, prevHas })

	statuslineNoColor, statuslineHasNoC = "", false
	assert.Equal(t, []string{"TERM=xterm", "HOME=/h"}, wrappedEnv([]string{"TERM=xterm", "NO_COLOR=1", "HOME=/h"}),
		"the coworker never set NO_COLOR: ox's own is dropped")

	statuslineNoColor, statuslineHasNoC = "1", true
	assert.Equal(t, []string{"TERM=xterm", "NO_COLOR=1"}, wrappedEnv([]string{"NO_COLOR=1", "TERM=xterm"}),
		"the coworker did set it: the app honors it too")
}
