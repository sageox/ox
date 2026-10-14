//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorBridgeCleansDescendantsWhenHookReturns(t *testing.T) {
	if testing.Short() {
		t.Skip("short: exercises hook descendant lifetime")
	}
	dir := t.TempDir()
	child := filepath.Join(dir, "ox")
	release, late := filepath.Join(dir, "release"), filepath.Join(dir, "late-write")
	script := "#!/bin/sh\n(while [ ! -f " + shellQuoteCursorHookArg(release) + " ]; do sleep 0.01; done; printf late > " + shellQuoteCursorHookArg(late) + ") </dev/null >/dev/null 2>&1 &\nprintf '%s' \"$$\"\n"
	require.NoError(t, os.WriteFile(child, []byte(script), 0o700))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := runCursorOx(ctx, child, dir, "sessionStart", nil)
	require.NoError(t, err)
	group, err := strconv.Atoi(strings.TrimSpace(string(output)))
	require.NoError(t, err)
	require.Greater(t, group, 1)
	t.Cleanup(func() { _ = syscall.Kill(-group, syscall.SIGKILL) })
	require.NoError(t, os.WriteFile(release, nil, 0o600))
	assert.Never(t, func() bool {
		_, err := os.Stat(late)
		return err == nil
	}, 500*time.Millisecond, 10*time.Millisecond, "a descendant survived the completed hook")
}
