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
	"sync/atomic"
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
		return runUnwrapped(cmd, os.Stdout, os.Stderr)
	}
	cols, rows, err := term.GetSize(stdoutFd)
	if err != nil || rows < 4 {
		return runUnwrapped(cmd, os.Stdout, os.Stderr)
	}

	// Free the bottom row before the app starts: if the cursor is on it,
	// scroll once, then step back up so the app begins above ox's row.
	var mu sync.Mutex
	filter := &wrapFilter{rows: rows - 1, dirty: true}
	_, _ = io.WriteString(os.Stdout, "\n\x1b[A"+filter.setRegion())

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows - 1), Cols: uint16(cols)}) //nolint:gosec // terminal sizes fit uint16
	if err != nil {
		_, _ = io.WriteString(os.Stdout, "\x1b7\x1b[r\x1b8")
		return 127, fmt.Errorf("start %s: %w", argv[0], err)
	}
	defer ptmx.Close()
	// The app's exit is watched on its own: the pseudo-terminal stays open
	// for as long as anything the app started still holds it.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	restore := func() {}
	if old, rawErr := term.MakeRaw(stdinFd); rawErr == nil {
		restore = func() { _ = term.Restore(stdinFd, old) }
	}
	defer restore()

	lastOutput := time.Now()
	var savedSince time.Time // when the app saved the cursor it still holds
	stopped := false         // set under mu once the terminal is being given back
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
				if stopped {
					mu.Unlock()
					return // the terminal has been given back: nothing more is passed through
				}
				_, _ = os.Stdout.Write(filter.write(buf[:n]))
				lastOutput = time.Now()
				switch {
				case !filter.cursorSaved:
					savedSince = time.Time{}
				case savedSince.IsZero():
					savedSince = lastOutput
				}
				_, _ = io.WriteString(os.Stdout, filter.flushRegion(time.Since(savedSince)))
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
		// A resize the app's saved cursor held back is finished here too: the
		// app may write nothing more before it restores.
		_, _ = io.WriteString(os.Stdout, filter.flushRegion(time.Since(savedSince)))
		if !filter.canDraw(time.Since(savedSince)) || time.Since(lastOutput) < wrapQuiet {
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
	var waitErr error
	appExited := false
loop:
	for {
		select {
		case <-done:
			break loop
		case waitErr = <-exited:
			// The app is gone. Take what it last wrote, but do not wait on a
			// background process it left holding the terminal.
			appExited = true
			if ctx.Err() != nil && cmd.Process != nil {
				// ox was told to stop and the app is gone, by itself or
				// killed after the grace period: nothing it started may be
				// left holding the terminal ox is giving back.
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			select {
			case <-done:
			case <-time.After(wrapDrainGrace):
			}
			break loop
		case <-winch:
			if c, r, sizeErr := term.GetSize(stdoutFd); sizeErr == nil && r >= 4 {
				mu.Lock()
				cols, rows = c, r
				_ = pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(r - 1), Cols: uint16(c)}) //nolint:gosec // terminal sizes fit uint16
				_, _ = io.WriteString(os.Stdout, filter.resized(r-1, time.Since(savedSince)))
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

	// Nothing more is passed through: a process still holding the
	// pseudo-terminal must not write over the terminal ox is giving back,
	// and neither may bytes the reader has read but not yet written.
	mu.Lock()
	stopped = true
	mu.Unlock()
	_ = ptmx.Close()
	if !appExited {
		waitErr = <-exited
	}

	// Give the terminal back whole: full scroll region, ox's row cleared.
	mu.Lock()
	_, _ = io.WriteString(os.Stdout, "\x1b7\x1b[r"+fmt.Sprintf("\x1b[%d;1H\x1b[2K", rows)+"\x1b8")
	mu.Unlock()
	return wrappedExitCode(cmd, waitErr)
}

// wrapDrainGrace is how long ox keeps passing output through after the app
// has exited, for what the app wrote last.
const wrapDrainGrace = 250 * time.Millisecond

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

// unwrappedDrainGrace is how long ox keeps forwarding output after a command
// run without a row has exited.
const unwrappedDrainGrace = time.Second

// runUnwrapped runs the command with no row to hold (no terminal on one end,
// or a terminal too short) and returns its exit code.
//
// An output that is a terminal is handed to the command as it is, so a pager
// or a prompt still sees a terminal. Any other output is copied through a
// pipe of ox's own rather than handed over: a background process the command
// leaves behind would otherwise keep ox's stdout or stderr open, and ox could
// not exit until it did. What the command itself wrote is still delivered;
// the copy stops unwrappedDrainGrace after it exits. The time the command has
// to stop when ox is told to (wrapStopGrace) is separate and unchanged.
func runUnwrapped(cmd *exec.Cmd, stdout, stderr io.Writer) (int, error) {
	cmd.Stdin = os.Stdin
	var readers, writers []*os.File
	closeAll := func(files []*os.File) {
		for _, f := range files {
			_ = f.Close()
		}
	}
	var copies sync.WaitGroup
	var copyMu sync.Mutex
	var copyErr error // the first failure to write the command's output on
	var progress copyProgress
	attach := func(dst io.Writer) (io.Writer, error) {
		if f, ok := dst.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			return f, nil
		}
		r, w, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		readers, writers = append(readers, r), append(writers, w)
		copies.Add(1)
		go func() {
			defer copies.Done()
			_, err := io.Copy(progress.watch(dst), r)
			// Closed here, not only at the end: with nobody reading, a command
			// still writing would fill the pipe and never exit. Closed, its
			// next write fails and it stops.
			_ = r.Close()
			// os.ErrClosed is ox ending the copy itself after the drain grace.
			if err != nil && !errors.Is(err, os.ErrClosed) {
				copyMu.Lock()
				if copyErr == nil {
					copyErr = err
				}
				copyMu.Unlock()
			}
		}()
		return w, nil
	}
	var err error
	if cmd.Stdout, err = attach(stdout); err == nil {
		cmd.Stderr, err = attach(stderr)
	}
	if err == nil {
		err = cmd.Start()
	}
	// The command holds its own copies of the write ends now (or never ran).
	closeAll(writers)
	if err != nil {
		closeAll(readers)
		copies.Wait()
		return 127, err
	}
	err = cmd.Wait()

	copied := make(chan struct{})
	go func() {
		copies.Wait()
		close(copied)
	}()
	// The command has exited. What it wrote may still be on its way to a slow
	// reader, and that is waited for: the grace runs only while nothing moves.
	// Then either the pipe is drained and held open by something the command
	// left behind (the copy is ended, nothing is lost), or a write has
	// stopped being accepted (the output is undelivered: a failure).
	stuck := false
drain:
	for last := progress.written.Load(); ; {
		select {
		case <-copied:
			break drain
		case <-time.After(unwrappedDrainGrace):
		}
		now := progress.written.Load()
		if now != last {
			last = now
			continue
		}
		stuck = progress.writing.Load() > 0
		break
	}
	closeAll(readers) // ends a copy still waiting on what the command left behind
	if stuck {
		// Closing ox's read end does not interrupt a blocked write. ox does
		// not wait on it; the stuck copy ends with the process.
		code, exitErr := wrappedExitCode(cmd, err)
		if code == 0 && exitErr == nil {
			return 1, errors.New("write the command's output: the destination stopped accepting it")
		}
		return code, exitErr
	}
	<-copied
	code, err := wrappedExitCode(cmd, err)
	// Output that could not be delivered is a failure even when the command
	// was content: its caller did not get what it wrote.
	if code == 0 && err == nil && copyErr != nil {
		return 1, fmt.Errorf("write the command's output: %w", copyErr)
	}
	return code, err
}

// copyProgress watches output being forwarded: how many writes have been
// accepted, and whether one is waiting to be.
type copyProgress struct {
	written atomic.Int64
	writing atomic.Int32
}

func (p *copyProgress) watch(dst io.Writer) io.Writer {
	return writerFunc(func(b []byte) (int, error) {
		p.writing.Add(1)
		n, err := dst.Write(b)
		p.writing.Add(-1)
		p.written.Add(1)
		return n, err
	})
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }

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

func wrappedExitCode(cmd *exec.Cmd, err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	// Told to stop, the app exited cleanly: os/exec reports the canceled
	// context, but the app's own status is the answer.
	if cmd != nil && cmd.ProcessState != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return cmd.ProcessState.ExitCode(), nil
	}
	return 1, err
}
