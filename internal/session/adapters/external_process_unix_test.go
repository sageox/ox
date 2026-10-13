//go:build !windows

package adapters

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

func TestExternalAdapter_TimeoutKillsDescendants(t *testing.T) {
	if testing.Short() {
		t.Skip("short: drives an external adapter subprocess")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "ox-adapter-descendant")
	contents := "#!/bin/sh\nsleep 30 &\nchild=$!\nprintf '%s\\n' \"$child\" > \"${0%/*}/child.pid\"\nwait\n"
	if err := os.WriteFile(script, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}

	ea := NewExternalAdapterWithInfo(script, &adapterprotocol.InfoResponse{Name: "descendant"})
	ea.oneShotTimeout = 500 * time.Millisecond
	_, err := ea.Read("session")
	if !errors.Is(err, ErrAdapterTimeout) {
		t.Fatalf("error = %v, want ErrAdapterTimeout", err)
	}

	pidBytes, err := os.ReadFile(filepath.Join(dir, "child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("adapter descendant pid %d remained alive after cancellation: %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestConfigureOneShotCommand_CancelLifecycle(t *testing.T) {
	t.Run("before start", func(t *testing.T) {
		cmd := exec.CommandContext(context.Background(), "true")
		configureOneShotCommand(cmd)
		if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
			t.Fatalf("Cancel error = %v, want os.ErrProcessDone", err)
		}
	})

	t.Run("after exit", func(t *testing.T) {
		cmd := exec.CommandContext(context.Background(), "true")
		configureOneShotCommand(cmd)
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
			t.Fatalf("Cancel error = %v, want os.ErrProcessDone", err)
		}
	})
}

// A descendant that outlives the adapter and keeps the output pipes open for a
// while must not fail an otherwise-complete one-shot read; this is what a
// scheduler stall on a saturated host looks like to cmd.Wait.
func TestExternalAdapter_OneShotToleratesSlowPipeDrain(t *testing.T) {
	if testing.Short() {
		t.Skip("holds the adapter's pipes open for 500ms")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "ox-adapter-slowdrain")
	contents := "#!/bin/sh\nsleep 0.5 &\nprintf '{\"ok\":true}\\n'\nexit 0\n"
	if err := os.WriteFile(script, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}

	ea := NewExternalAdapterWithInfo(script, &adapterprotocol.InfoResponse{Name: "slowdrain"})
	out, err := ea.execOneShot("read-from-offset")
	if err != nil {
		t.Fatalf("one-shot failed despite complete adapter output: %v", err)
	}
	if string(out) != `{"ok":true}` {
		t.Fatalf("output = %q", out)
	}
}

// Cancellation belongs to the caller, even if the adapter deadline has time
// remaining. A pre-canceled call must not spawn; canceling an active call must
// discard partial stdout and reap both the wrapper and its child.
func TestExternalAdapter_ReadHonorsCallerCancellation(t *testing.T) {
	for _, stage := range []string{"before start", "while reading"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			script := filepath.Join(dir, "ox-adapter-cancel")
			contents := "#!/bin/sh\nsleep 30 &\nchild=$!\nprintf '%s %s\\n' \"$$\" \"$child\" > \"${0%/*}/started\"\nprintf '{\"entries\":['\nwait\n"
			if err := os.WriteFile(script, []byte(contents), 0o755); err != nil {
				t.Fatal(err)
			}
			ea := NewExternalAdapterWithInfo(script, &adapterprotocol.InfoResponse{Name: "cancel"})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if stage == "before start" {
				cancel()
				entries, err := ea.ReadWithContext(ctx, "session", 30*time.Second)
				if !errors.Is(err, context.Canceled) || entries != nil {
					t.Fatalf("read = (%v, %v), want (nil, context.Canceled)", entries, err)
				}
				if _, err := os.Stat(filepath.Join(dir, "started")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("pre-canceled call spawned an adapter: %v", err)
				}
				return
			}
			type result struct {
				entries []RawEntry
				err     error
			}
			done := make(chan result, 1)
			go func() {
				entries, err := ea.ReadWithContext(ctx, "session", 30*time.Second)
				done <- result{entries: entries, err: err}
			}()
			deadline := time.Now().Add(5 * time.Second)
			var pids []string
			for len(pids) != 2 {
				if data, err := os.ReadFile(filepath.Join(dir, "started")); err == nil {
					pids = strings.Fields(string(data))
				}
				if time.Now().After(deadline) {
					t.Fatal("adapter did not signal that its child was running")
				}
				time.Sleep(time.Millisecond)
			}
			cancel()
			select {
			case result := <-done:
				if !errors.Is(result.err, context.Canceled) || result.entries != nil {
					t.Fatalf("read = (%v, %v), want (nil, context.Canceled)", result.entries, result.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("canceled adapter did not stop before its 30-second deadline")
			}
			for _, text := range pids {
				pid, err := strconv.Atoi(text)
				if err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(5 * time.Second)
				for {
					if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("canceled read left adapter process %d alive", pid)
					}
					time.Sleep(time.Millisecond)
				}
			}
		})
	}
}
