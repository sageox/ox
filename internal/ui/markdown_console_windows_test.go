//go:build windows

package ui

import (
	"errors"
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
		// ERROR_ACCESS_DENIED means a console is already attached, which is the state wanted
		if r, _, err := syscall.NewLazyDLL("kernel32.dll").NewProc("AllocConsole").Call(); r == 0 && !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
			t.Fatalf("AllocConsole: %v", err)
		}
		// prove the console exists the way lipgloss will find it, or the test proves nothing
		conin, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
		if err != nil {
			t.Fatalf("no console input after AllocConsole: %v", err)
		}
		defer conin.Close()
		var mode uint32
		if err := syscall.GetConsoleMode(syscall.Handle(conin.Fd()), &mode); err != nil {
			t.Fatalf("CONIN$ is not a console: %v", err)
		}

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
