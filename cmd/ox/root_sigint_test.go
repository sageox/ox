//go:build !windows

package main

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/spf13/cobra"
)

// TestExecuteWithSignalContext_SIGINTCancelsCommandContext guards the root
// wiring: subprocesses run in their own process group, so a terminal SIGINT
// reaches them only if every command's context cancels when ox is interrupted.
func TestExecuteWithSignalContext_SIGINTCancelsCommandContext(t *testing.T) {
	// absorb SIGINT so a missing handler fails the test instead of killing it
	sink := make(chan os.Signal, 4)
	signal.Notify(sink, os.Interrupt)
	t.Cleanup(func() { signal.Stop(sink) })

	// fake git forks a long sleeper and records its pid; the sleeper must die
	// when ox is interrupted, not linger holding locks
	binDir := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "sleeper.pid")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	// only the probe's invocation stalls; ox's own startup git calls pass through
	script := "#!/bin/sh\ncase \" $* \" in *\" sigint-probe-stall \"*) ;; *) exec '" + realGit + "' \"$@\" ;; esac\n" +
		"sleep 30 &\necho $! > '" + pidFile + "'\nwait\n"
	if err = os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	started := make(chan struct{})
	probe := &cobra.Command{
		Use:    "sigint-probe",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			close(started)
			_, err := gitutil.RunGit(cmd.Context(), t.TempDir(), "sigint-probe-stall")
			return err
		},
	}
	rootCmd.AddCommand(probe)
	t.Cleanup(func() { rootCmd.RemoveCommand(probe) })

	done := make(chan int, 1)
	go func() { done <- executeWithSignalContext([]string{"sigint-probe"}) }()

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("command never started")
	}
	sleeperPID := waitForPID(t, pidFile)
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT: %v", err)
	}

	select {
	case code := <-done:
		if code != 130 {
			t.Fatalf("exit code = %d, want 130 (context was not canceled by SIGINT)", code)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("command did not finish after SIGINT")
	}

	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(sleeperPID, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(sleeperPID, syscall.SIGKILL)
			t.Fatalf("git grandchild pid %d survived SIGINT", sleeperPID)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func waitForPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("fake git never recorded its sleeper pid")
	return 0
}
