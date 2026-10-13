//go:build !windows

package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/require"
)

const importSignalProbeEnv = "GO_WANT_IMPORT_SIGNAL_PROBE"

// The first interrupt preserves committed work; the second must terminate even
// while a final push uses WithoutCancel. Test in a child to protect the runner.
func TestImportInterrupt_SecondSignalKillsFinalPush(t *testing.T) {
	if os.Getenv(importSignalProbeEnv) == "1" {
		runImportSignalProbe(t)
		return
	}
	if testing.Short() {
		t.Skip("short: real interrupt subprocess and Ledger fixture")
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	probeRoot := t.TempDir()
	cmd := testguard.OxCmdContext(t, ctx, executable, probeRoot, []string{
		importSignalProbeEnv + "=1", "TMPDIR=" + probeRoot,
		"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
	}, "-test.run=^TestImportInterrupt_SecondSignalKillsFinalPush$")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
	})
	phases := make(chan string, 2)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			switch line := scanner.Text(); line {
			case "import-probe: summarizing", "import-probe: final push":
				phases <- line
			}
		}
	}()
	waitPhase := func(want string) {
		t.Helper()
		select {
		case phase := <-phases:
			require.Equal(t, want, phase)
		case err := <-done:
			t.Fatalf("probe exited before %s: %v\n%s", want, err, stderr.String())
		case <-ctx.Done():
			t.Fatalf("probe never reached %s", want)
		}
	}
	waitPhase("import-probe: summarizing")
	require.NoError(t, cmd.Process.Signal(os.Interrupt))
	waitPhase("import-probe: final push")
	require.NoError(t, cmd.Process.Signal(os.Interrupt))
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr, "second interrupt must terminate the final push")
		status, ok := exitErr.Sys().(syscall.WaitStatus)
		require.True(t, ok)
		require.True(t, status.Signaled(), "%s", stderr.String())
		require.Equal(t, syscall.SIGINT, status.Signal())
	case <-time.After(5 * time.Second):
		t.Fatal("second interrupt was swallowed while final push was running")
	}
}

// runImportSignalProbe runs in the child test process and holds final publication
// after the first interrupt. A second interrupt must terminate this process
// instead of remaining captured by the canceled import's signal registration.
func runImportSignalProbe(t *testing.T) {
	t.Helper()
	f := newImportFixture(t)
	ctx, stop := importSignalContext(context.Background())
	defer stop()
	f.ctx = ctx
	f.summarizer.before = func(prompt string) {
		if !strings.Contains(prompt, pushPrompt) {
			return
		}
		fmt.Fprintln(os.Stdout, "import-probe: summarizing")
		<-ctx.Done()
	}
	f.push = func(pushCtx context.Context, _ string) error {
		require.NoError(t, pushCtx.Err(), "first interrupt must permit final publication")
		require.ErrorIs(t, ctx.Err(), context.Canceled)
		fmt.Fprintln(os.Stdout, "import-probe: final push")
		<-pushCtx.Done()
		return pushCtx.Err()
	}
	start := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start, prompt: loginPrompt, reply: "Fixed the cookie."})
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: start.Add(time.Hour), prompt: pushPrompt, reply: "Re-uploaded the object."})
	f.run(t, importOptions{yes: true, jsonOut: true, parallel: 1})
	t.Fatal("final push returned instead of being terminated by the second interrupt")
}
