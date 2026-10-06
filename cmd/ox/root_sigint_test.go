//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

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

	started := make(chan struct{})
	probe := &cobra.Command{
		Use:    "sigint-probe",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			close(started)
			select {
			case <-cmd.Context().Done():
				return cmd.Context().Err()
			case <-time.After(3 * time.Second):
				return nil
			}
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
}
