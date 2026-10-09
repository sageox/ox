//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cursorPrimeProcessIdentity struct {
	PID, Group int
}

func TestCursorPrimeSharesOuterHookLifetime(t *testing.T) {
	if testing.Short() {
		t.Skip("short: exercises nested hook processes")
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCursorPrimeLifetimeHelper$")
	cmd.Env = append(os.Environ(), "OX_CURSOR_PROCESS_TEST_ROLE=hook", "OX_CURSOR_PROCESS_TEST_DIR="+dir) // safe: isolated helper never invokes ox commands
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	var prime cursorPrimeProcessIdentity
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(filepath.Join(dir, "ready"))
		return err == nil && json.Unmarshal(data, &prime) == nil
	}, 5*time.Second, 10*time.Millisecond)
	t.Cleanup(func() {
		_ = syscall.Kill(prime.PID, syscall.SIGKILL)
		_ = syscall.Kill(-prime.PID, syscall.SIGKILL)
	})
	assert.Equal(t, cmd.Process.Pid, prime.Group, "prime must remain covered by the adapter's outer group timeout")
	require.NoError(t, syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL))
	require.Error(t, cmd.Wait())
	// An escaped prime would observe this release and write after the hook died.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "release"), nil, 0o600))
	assert.Never(t, func() bool {
		_, err := os.Stat(filepath.Join(dir, "late-write"))
		return err == nil
	}, 500*time.Millisecond, 10*time.Millisecond)
}

func TestCursorPrimeDirectInvocationCancelsItsOwnGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("short: exercises nested prime processes")
	}
	if syscall.Getpgrp() == os.Getpid() {
		t.Skip("test runner itself owns the outer hook group")
	}
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCursorPrimeLifetimeHelper$")
	cmd.Env = append(os.Environ(), "OX_CURSOR_PROCESS_TEST_ROLE=prime", "OX_CURSOR_PROCESS_TEST_DIR="+dir) // safe: isolated helper never invokes ox commands
	configureCursorPrimeProcess(cmd)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	var prime cursorPrimeProcessIdentity
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(filepath.Join(dir, "ready"))
		return err == nil && json.Unmarshal(data, &prime) == nil
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, prime.PID, prime.Group, "direct invocation must isolate prime from the caller")
	cancel()
	require.Error(t, cmd.Wait())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "release"), nil, 0o600))
	assert.Never(t, func() bool {
		_, err := os.Stat(filepath.Join(dir, "late-write"))
		return err == nil
	}, 500*time.Millisecond, 10*time.Millisecond)
}

func TestCursorPrimeLifetimeHelper(t *testing.T) {
	role, dir := os.Getenv("OX_CURSOR_PROCESS_TEST_ROLE"), os.Getenv("OX_CURSOR_PROCESS_TEST_DIR")
	switch role {
	case "hook":
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCursorPrimeLifetimeHelper$")
		cmd.Env = append(os.Environ(), "OX_CURSOR_PROCESS_TEST_ROLE=prime") // safe: isolated helper never invokes ox commands
		configureCursorPrimeProcess(cmd)
		_ = cmd.Run()
	case "prime":
		data, err := json.Marshal(cursorPrimeProcessIdentity{PID: os.Getpid(), Group: syscall.Getpgrp()})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "ready"), data, 0o600))
		for {
			if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "late-write"), nil, 0o600))
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}
