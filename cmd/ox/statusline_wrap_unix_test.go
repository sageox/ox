//go:build !windows

package main

import (
	"bytes"
	"context"
	"os/exec"
	"sync"
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

// syncBuffer is a bytes.Buffer safe for the copying goroutines to write to
// while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestRunUnwrapped_DoesNotWaitOnWhatTheCommandLeftBehind: with output
// redirected there is no row to hold and the command is simply run. Failure
// prevented: a background process it started keeps the output open, and ox
// hangs (for as long as that process lives) before reporting the exit code.
func TestRunUnwrapped_DoesNotWaitOnWhatTheCommandLeftBehind(t *testing.T) {
	tests := []struct {
		name, script string
		wantCode     int
	}{
		{"a failure, with a background child", "echo out; echo err >&2; sleep 30 & exit 7", 7},
		{"a success, with a background child", "echo out; echo err >&2; sleep 30 & exit 0", 0},
		{"a plain failure", "echo out; echo err >&2; exit 3", 3},
	}
	if testing.Short() {
		t.Skip("short: waits out the 1s output drain after each command")
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr syncBuffer
			cmd := wrappedCommand(context.Background(), []string{"sh", "-c", tt.script})
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			t.Cleanup(func() {
				if cmd.Process != nil {
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // the sleep left behind
				}
			})
			start := time.Now()
			code, err := runUnwrapped(cmd, &stdout, &stderr)
			require.NoError(t, err)
			assert.Equal(t, tt.wantCode, code)
			assert.Less(t, time.Since(start), 10*time.Second, "ox must not wait for the 30s background child")
			assert.Equal(t, "out\n", stdout.String(), "what the command wrote is delivered")
			assert.Equal(t, "err\n", stderr.String())
		})
	}
}
