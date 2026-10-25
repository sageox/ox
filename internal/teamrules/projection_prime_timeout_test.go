package teamrules

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sageox/ox/internal/teamdocs"
	"github.com/stretchr/testify/require"
)

// stageHangingGit puts a `git` on PATH that never returns, and PROVES it is the
// one ForPrime will find. Asserting the isolation is the point: a fake binary
// that silently fails to shadow the real one turns this test into a measurement
// of real git's speed, which passes for the wrong reason.
func stageHangingGit(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		// The shim below is a POSIX shell script. Writing a .cmd twin that a
		// Windows runner would actually exec is a port nobody here can exercise,
		// and an unexercised port is how a fail-open gets authored.
		t.Skip("windows: PATH shim is a POSIX shell script")
	}

	dir := t.TempDir()
	shim := filepath.Join(dir, "git")
	require.NoError(t, os.WriteFile(shim, []byte("#!/bin/sh\nsleep 120\n"), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	found, err := exec.LookPath("git")
	require.NoError(t, err)
	require.Equal(t, shim, found, "PATH shim did not shadow the real git; the test would measure real git instead")
}

// TestForPrime_HungGitDoesNotWedgeSessionStart is the reason primeProbeTimeout
// exists. ForPrime runs `git check-ignore` on the ox agent prime critical path;
// with an unbounded context a wedged git — an index lock, a stalled network
// mount — is a coding session that never starts.
//
// Failure prevented: prime hanging forever instead of degrading to duplicated
// delivery.
func TestForPrime_HungGitDoesNotWedgeSessionStart(t *testing.T) {
	stageHangingGit(t)

	projectRoot := t.TempDir()
	p, ok := policyFor("claude")
	require.True(t, ok)

	// A rule with an ox-owned native file present is the ONLY path that probes
	// protection; without it ForPrime never shells out and the test proves nothing.
	rule := teamdocs.TeamRule{Name: "example", Body: "body", Visibility: teamdocs.VisibilityAlways}
	native, ok := NativePath(projectRoot, "claude", rule)
	require.True(t, ok)
	require.NoError(t, os.MkdirAll(filepath.Dir(native), 0o755))
	require.NoError(t, os.WriteFile(native, stampProjection([]byte("# example\n\nbody\n")), 0o644))
	require.True(t, nativePresent(projectRoot, "claude", rule), "fixture must stage an ox-owned native file, or the probe is never reached")
	require.NotEmpty(t, p.Root)

	// Restore the production bound: this is package state, and a test that
	// leaves a 200ms timeout behind would silently weaken every later caller.
	t.Cleanup(func(orig time.Duration) func() { return func() { primeProbeTimeout = orig } }(primeProbeTimeout))
	primeProbeTimeout = 200 * time.Millisecond

	done := make(chan []teamdocs.TeamRule, 1)
	start := time.Now()
	go func() { done <- ForPrime(projectRoot, "claude", []teamdocs.TeamRule{rule}) }()

	select {
	case out := <-done:
		require.Less(t, time.Since(start), 30*time.Second, "ForPrime must not wait on a hung git")
		// Timing out means "not protected", so the rule is DELIVERED through
		// prime rather than suppressed — duplicated beats silently missing.
		require.Len(t, out, 1, "a timed-out probe must fall to delivering the rule, not dropping it")
		require.Equal(t, "example", out[0].Name)
	case <-time.After(30 * time.Second):
		t.Fatal("ForPrime never returned: the probe is unbounded and prime would hang forever")
	}
}
