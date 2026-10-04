//go:build windows

package ui

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// allocConsoleEnv makes the re-executed test binary attach a fresh console: the state a
// Git Bash or terminal user is in. GitHub's Windows runners have no console, so without
// one the background query fails fast and the hang fixed in #1110 cannot reproduce.
const allocConsoleEnv = "OX_TEST_ALLOC_CONSOLE"

// TestGetSageOxStyle_ConsoleWithRedirectedStdinDoesNotHang: without the stdin guard, a
// process that owns a console but reads a redirected stdin parks forever in ReadConsole.
func TestGetSageOxStyle_ConsoleWithRedirectedStdinDoesNotHang(t *testing.T) {
	if os.Getenv(allocConsoleEnv) == "1" {
		// fails harmlessly when a console is already attached; either way one exists now
		_, _, _ = syscall.NewLazyDLL("kernel32.dll").NewProc("AllocConsole").Call()
		devNull, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		defer devNull.Close()
		terminalHasDarkBackground(devNull, devNull)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestGetSageOxStyle_ConsoleWithRedirectedStdinDoesNotHang$", "-test.count=1")
	cmd.Env = append(os.Environ(), allocConsoleEnv+"=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("child process failed: %v", err)
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("terminal background query hung on a console it cannot cancel")
	}
}
