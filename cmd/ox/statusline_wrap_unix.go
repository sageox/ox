//go:build !windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"

	"github.com/sageox/ox/internal/oxactivity"
)

// runStatuslineWrap is `ox statusline --wrap -- <command>`: it runs command
// with ox's row at the bottom of the terminal and returns its exit code.
// Without a terminal on both ends there is no row to hold, so the command is
// simply run.
func runStatuslineWrap(ctx context.Context, argv []string) (int, error) {
	if len(argv) == 0 {
		return 2, errors.New("usage: ox statusline --wrap -- <command> [args]")
	}
	stdinFd, stdoutFd := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	cmd := wrappedCommand(ctx, argv)
	// The app's hooks tell ox which session this terminal holds (see
	// noteWrapSession), so the row shows that session and no other.
	sessionFile := newWrapSessionFile()
	if sessionFile != "" {
		defer os.Remove(sessionFile)
		cmd.Env = append(cmd.Env, envStatuslineWrapSession+"="+sessionFile)
	}
	if !term.IsTerminal(stdinFd) || !term.IsTerminal(stdoutFd) {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return wrappedExitCode(cmd.Run())
	}
	cols, rows, err := term.GetSize(stdoutFd)
	if err != nil || rows < 4 {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return wrappedExitCode(cmd.Run())
	}

	// Free the bottom row before the app starts: if the cursor is on it,
	// scroll once, then step back up so the app begins above ox's row.
	var mu sync.Mutex
	filter := &wrapFilter{rows: rows - 1, dirty: true}
	_, _ = io.WriteString(os.Stdout, "\n\x1b[A\x1b7"+filter.region()+"\x1b8")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows - 1), Cols: uint16(cols)}) //nolint:gosec // terminal sizes fit uint16
	if err != nil {
		_, _ = io.WriteString(os.Stdout, "\x1b7\x1b[r\x1b8")
		return 127, fmt.Errorf("start %s: %w", argv[0], err)
	}
	defer ptmx.Close()

	restore := func() {}
	if old, rawErr := term.MakeRaw(stdinFd); rawErr == nil {
		restore = func() { _ = term.Restore(stdinFd, old) }
	}
	defer restore()

	lastOutput := time.Now()
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	done := make(chan struct{})

	go func() { _, _ = io.Copy(ptmx, os.Stdin) }()
	go func() {
		defer close(done)
		buf := make([]byte, 32<<10)
		for {
			n, readErr := ptmx.Read(buf)
			if n > 0 {
				mu.Lock()
				_, _ = os.Stdout.Write(filter.write(buf[:n]))
				lastOutput = time.Now()
				mu.Unlock()
			}
			if readErr != nil {
				return
			}
		}
	}()

	dir, _ := os.Getwd()
	opts := defaultStatuslineOptions()
	opts.color = oxactivity.ColorAllowedFor(statuslineNoColor)
	var watch flowWatch
	shown := ""
	draw := func() (moving bool) {
		opts.width = cols
		sid := readWrapSession(sessionFile)
		f := buildWatchFrameFor(&watch, dir, sid, opts, 1, time.Now())
		var lane func(int) string
		if len(f.lanes) == 1 {
			// buildWatchFrame drew the lane at full width; redraw it to fit
			// beside the status line.
			lane = func(columns int) string {
				narrow := opts
				narrow.width = columns
				if l := buildWatchFrameFor(&watch, dir, sid, narrow, 1, time.Now()).lanes; len(l) == 1 {
					return l[0]
				}
				return ""
			}
		}
		content := wrapRow(f.status, lane, cols)
		mu.Lock()
		defer mu.Unlock()
		if !filter.idle() || time.Since(lastOutput) < wrapQuiet {
			return f.moving || filter.dirty // the app is mid-draw: try again shortly
		}
		if content != shown || filter.dirty {
			_, _ = io.WriteString(os.Stdout, drawWrapRow(rows, content))
			shown, filter.dirty = content, false
		}
		return f.moving
	}

	wait := statuslineWatchInterval
	timer := time.NewTimer(0)
	defer timer.Stop()
loop:
	for {
		select {
		case <-done:
			break loop
		case <-winch:
			if c, r, sizeErr := term.GetSize(stdoutFd); sizeErr == nil && r >= 4 {
				mu.Lock()
				cols, rows = c, r
				filter.rows, filter.dirty = r-1, true
				_ = pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(r - 1), Cols: uint16(c)}) //nolint:gosec // terminal sizes fit uint16
				_, _ = io.WriteString(os.Stdout, "\x1b7"+filter.region()+"\x1b8")
				mu.Unlock()
			}
			timer.Reset(wrapQuiet)
		case <-timer.C:
			wait = statuslineWatchInterval
			if draw() {
				wait = statuslineWatchMotionInterval
			}
			timer.Reset(wait)
		}
	}

	// Give the terminal back whole: full scroll region, ox's row cleared.
	mu.Lock()
	_, _ = io.WriteString(os.Stdout, "\x1b7\x1b[r"+fmt.Sprintf("\x1b[%d;1H\x1b[2K", rows)+"\x1b8")
	mu.Unlock()
	return wrappedExitCode(cmd.Wait())
}

// wrapStopGrace is how long the wrapped app has to exit after it is asked to
// before it is killed.
const wrapStopGrace = 5 * time.Second

// wrappedCommand is the app ox wraps. When ctx ends (ox was sent SIGTERM or
// SIGINT) the app is asked to stop too, then killed after wrapStopGrace: ox
// must not outlive the request, nor leave the app behind holding a terminal
// ox has not given back. Once the app exits, the caller's own cleanup runs.
func wrappedCommand(ctx context.Context, argv []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = wrappedEnv(os.Environ())
	cmd.Cancel = func() error {
		// In its pseudo-terminal the app leads its own process group: signal
		// the group so its children stop with it. Run without one, it shares
		// ox's group, and only the app itself is signaled.
		if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil && pgid == cmd.Process.Pid {
			return syscall.Kill(-pgid, syscall.SIGTERM)
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = wrapStopGrace
	return cmd
}

// wrappedEnv is the environment the wrapped app runs in: ox's own, with
// NO_COLOR as the coworker had it. main's init may force NO_COLOR=1 for ox's
// own output; inherited, it would strip the app of all its color.
func wrappedEnv(env []string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, "NO_COLOR=") {
			out = append(out, kv)
		}
	}
	if statuslineHasNoC {
		out = append(out, "NO_COLOR="+statuslineNoColor)
	}
	return out
}

func wrappedExitCode(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 1, err
}
