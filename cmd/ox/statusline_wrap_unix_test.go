//go:build !windows

package main

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
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

// TestRunUnwrapped_AppKeepsTheGraceToStop: told to stop, the command gets
// wrapStopGrace to finish, whatever the output drain allows after it exits.
// Failure prevented: a command saving its state on SIGTERM killed one second
// in, because the drain timeout had replaced the stop allowance.
func TestRunUnwrapped_AppKeepsTheGraceToStop(t *testing.T) {
	if testing.Short() {
		t.Skip("short: the command takes 2s to stop, by design")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// `wait` returns when the signal arrives; the trap then takes two seconds.
	// The shell says "ready" once its handler is installed; ox is told to stop
	// only then, so the signal always meets the handler.
	cmd := wrappedCommand(ctx, []string{"sh", "-c", `trap 'sleep 2; echo saved; exit 0' TERM; echo ready; sleep 30 & wait`})
	var stdout, stderr syncBuffer
	go func() {
		for !strings.Contains(stdout.String(), "ready") {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	code, err := runUnwrapped(cmd, &stdout, &stderr)
	require.NoError(t, err)
	assert.Equal(t, 0, code, "it exited by itself, after its cleanup")
	assert.Equal(t, "ready\nsaved\n", stdout.String(), "the two-second cleanup ran to its end")
}

// TestRunUnwrapped_TerminalOutputStaysATerminal: with only stdin piped, a
// pager must still see the terminal it draws on. Failure prevented: every
// output replaced by a pipe, so `less` dumps its input instead of paging.
func TestRunUnwrapped_TerminalOutputStaysATerminal(t *testing.T) {
	ptmx, tty, err := pty.Open()
	require.NoError(t, err)
	defer ptmx.Close()
	defer tty.Close()
	go func() { _, _ = io.Copy(io.Discard, ptmx) }()

	// `test -t` asks the command itself what it was given.
	var redirected syncBuffer
	code, err := runUnwrapped(wrappedCommand(context.Background(), []string{"sh", "-c", "test -t 1 && ! test -t 2"}), tty, &redirected)
	require.NoError(t, err)
	assert.Equal(t, 0, code, "stdout is the terminal; redirected stderr is not")

	code, err = runUnwrapped(wrappedCommand(context.Background(), []string{"sh", "-c", "test -t 1"}), &redirected, &redirected)
	require.NoError(t, err)
	assert.Equal(t, 1, code, "a redirected stdout is not presented as a terminal")
}

// TestRunUnwrapped_UndeliveredOutputIsAFailure: the redirect target can
// refuse the output, as a full disk does (failingWriter refuses every write). Failure prevented: ox reporting success after losing
// what the command wrote; or, for output larger than a pipe holds, the
// command blocked forever on a pipe nobody reads.
func TestRunUnwrapped_UndeliveredOutputIsAFailure(t *testing.T) {
	tests := []struct{ name, script string }{
		{"short output", "printf hello"},
		{"more output than a pipe holds", "head -c 1048576 /dev/zero"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr syncBuffer
			type result struct {
				code int
				err  error
			}
			done := make(chan result, 1)
			go func() {
				code, err := runUnwrapped(wrappedCommand(context.Background(), []string{"sh", "-c", tt.script}), failingWriter{}, &stderr)
				done <- result{code, err}
			}()
			select {
			case r := <-done:
				assert.NotEqual(t, 0, r.code, "lost output must not report success (err=%v)", r.err)
			case <-time.After(20 * time.Second):
				t.Fatal("the command hung on output nobody was reading")
			}
		})
	}

	// A command that itself failed keeps its own exit code.
	code, err := runUnwrapped(wrappedCommand(context.Background(), []string{"sh", "-c", "printf hello; exit 7"}), failingWriter{}, failingWriter{})
	require.NoError(t, err)
	assert.Equal(t, 7, code)
}

// blockedWriter never returns from Write until released: a destination whose
// reader has stopped reading.
type blockedWriter struct{ release chan struct{} }

func (w blockedWriter) Write(p []byte) (int, error) {
	<-w.release
	return len(p), nil
}

// TestRunUnwrapped_DoesNotWaitOnADestinationThatStoppedReading: the bound on
// forwarding must hold when the destination blocks, not only when it fails.
// Failure prevented: the command has exited, and ox never returns because a
// copy is stuck in a write nobody will accept.
func TestRunUnwrapped_DoesNotWaitOnADestinationThatStoppedReading(t *testing.T) {
	if testing.Short() {
		t.Skip("short: waits out the output drain grace twice")
	}
	dst := blockedWriter{release: make(chan struct{})}
	t.Cleanup(func() { close(dst.release) }) // let the stuck copy end with the test
	var stderr syncBuffer
	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		code, err := runUnwrapped(wrappedCommand(context.Background(), []string{"sh", "-c", "printf hello"}), dst, &stderr)
		done <- result{code, err}
	}()
	select {
	case r := <-done:
		assert.NotEqual(t, 0, r.code, "output that was never accepted is not a success")
		assert.Error(t, r.err)
	case <-time.After(4*unwrappedDrainGrace + 5*time.Second):
		t.Fatal("ox waited on a destination that will never accept the output")
	}
}

// slowWriter accepts every write, slowly: a reader that is behind but reading.
type slowWriter struct {
	delay time.Duration
	buf   syncBuffer
}

func (w *slowWriter) Write(p []byte) (int, error) {
	time.Sleep(w.delay)
	return w.buf.Write(p)
}

// TestRunUnwrapped_WaitsForASlowReader: the grace after the command exits is
// for a pipe nothing is moving through. Failure prevented: a reader that is
// slow but reading has the rest of the output cut off one second after the
// command exits, and ox reports success.
func TestRunUnwrapped_WaitsForASlowReader(t *testing.T) {
	if testing.Short() {
		t.Skip("short: the reader takes about 3s by design")
	}
	// 200 KiB exits the command at once (it fits the pipe plus one read); the
	// reader then takes well over the one-second grace to take it all.
	dst := &slowWriter{delay: 400 * time.Millisecond}
	var stderr syncBuffer
	code, err := runUnwrapped(wrappedCommand(context.Background(), []string{"sh", "-c", "head -c 204800 /dev/zero"}), dst, &stderr)
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Equal(t, 204800, len(dst.buf.String()), "every byte the command wrote is delivered")
}
