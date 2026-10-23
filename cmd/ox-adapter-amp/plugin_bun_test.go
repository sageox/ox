package main

import (
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOxBridgePlugin_BunTests runs the ox-bridge plugin's own tests
// (plugin/ox-bridge.test.ts) under Bun, the runtime Amp loads plugins with.
// They cover the orb session end (sageox/ox#1314). Skips when no working
// bun is on PATH.
func TestOxBridgePlugin_BunTests(t *testing.T) {
	if testing.Short() {
		t.Skip("short: runs bun")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun not on PATH")
	}
	if err := exec.Command(bun, "--version").Run(); err != nil {
		t.Skipf("bun on PATH does not run: %v", err)
	}

	cmd := exec.Command(bun, "test")
	cmd.Dir = "plugin"
	// the plugin writes sidecars under HOME, and the tests decide AMP_ORB themselves
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "AMP_ORB=") // safe: runs bun, not ox; HOME is overridden

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "bun test failed:\n%s", out)
}
