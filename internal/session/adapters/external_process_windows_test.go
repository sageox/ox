//go:build windows

package adapters

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const (
	windowsJobHelperEnv   = "OX_TEST_WINDOWS_JOB_HELPER"
	windowsJobStartedEnv  = "OX_TEST_WINDOWS_JOB_STARTED"
	windowsJobSurvivedEnv = "OX_TEST_WINDOWS_JOB_SURVIVED"
)

func TestRunOneShotCommand_WindowsJobKillsDescendants(t *testing.T) {
	if mode := os.Getenv(windowsJobHelperEnv); mode != "" {
		runWindowsJobHelper(t, mode)
		return
	}

	dir := t.TempDir()
	startedPath := filepath.Join(dir, "descendant-started")
	survivedPath := filepath.Join(dir, "descendant-survived")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunOneShotCommand_WindowsJobKillsDescendants$")
	cmd.Env = append(
		os.Environ(), // safe: re-executes this test binary only
		windowsJobHelperEnv+"=parent",
		windowsJobStartedEnv+"="+startedPath,
		windowsJobSurvivedEnv+"="+survivedPath,
	)
	cmd.WaitDelay = 100 * time.Millisecond

	err := runOneShotCommand(cmd)
	if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("run error = %v, context error = %v; want deadline cancellation", err, ctx.Err())
	}
	if _, err := os.Stat(startedPath); err != nil {
		t.Fatalf("adapter parent never started its immediate descendant: %v", err)
	}
	time.Sleep(2 * time.Second)
	if _, err := os.Stat(survivedPath); !os.IsNotExist(err) {
		t.Fatalf("adapter descendant survived Job Object cancellation: %v", err)
	}
}

func runWindowsJobHelper(t *testing.T, mode string) {
	t.Helper()
	switch mode {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestRunOneShotCommand_WindowsJobKillsDescendants$")
		child.Env = append(os.Environ(), windowsJobHelperEnv+"=child") // safe: re-executes this test binary only
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Getenv(windowsJobStartedEnv), []byte("started"), 0o600); err != nil {
			os.Exit(3)
		}
		time.Sleep(30 * time.Second)
	case "child":
		time.Sleep(3 * time.Second)
		if err := os.WriteFile(os.Getenv(windowsJobSurvivedEnv), []byte("alive"), 0o600); err != nil {
			os.Exit(4)
		}
		os.Exit(0)
	default:
		os.Exit(5)
	}
}
