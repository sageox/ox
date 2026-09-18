//go:build integration

package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// statusIsolatedNoAuthEnv points every auth/config lookup at empty temp
// directories, with no auth.json written -- so the subprocess is
// deterministically unauthenticated, regardless of the developer machine's
// real login state.
func statusIsolatedNoAuthEnv(t *testing.T) []string {
	t.Helper()
	base := t.TempDir()
	return []string{
		"OX_XDG_ENABLE=1",
		fmt.Sprintf("XDG_CONFIG_HOME=%s", filepath.Join(base, "config")),
		fmt.Sprintf("XDG_DATA_HOME=%s", filepath.Join(base, "data")),
		fmt.Sprintf("XDG_STATE_HOME=%s", filepath.Join(base, "state")),
		fmt.Sprintf("XDG_CACHE_HOME=%s", filepath.Join(base, "cache")),
		fmt.Sprintf("XDG_RUNTIME_DIR=%s", filepath.Join(base, "run")),
	}
}

// TestStatusE2E_UnauthenticatedOutsideGitRepo_ExitsNonZero is the compiled-
// binary proof for https://github.com/sageox/ox/issues/866: `cd /tmp/empty-dir
// && ox status` and a healthy repo both exited 0, so a script could not tell
// the two states apart without parsing --json. Run from a fresh, non-git temp
// dir with no credentials -- the exact repro in the issue.
func TestStatusE2E_UnauthenticatedOutsideGitRepo_ExitsNonZero(t *testing.T) {
	oxBin := buildOxBinary(t)
	dir := t.TempDir() // not a git repo
	env := statusIsolatedNoAuthEnv(t)

	out, exitCode, _ := testguard.RunOx(t, oxBin, dir, env, "status")

	assert.NotEqual(t, 0, exitCode, "unauthenticated + uninitialized must not exit 0, got output:\n%s", out)
	assert.Contains(t, out, "not authenticated", "the failure reason must be named")
}

// TestStatusE2E_JSONModeStaysExitZero mirrors `ox doctor`'s convention: a
// --json consumer branches on the auth/project fields in the payload, not
// the process exit code, so JSON output must stay exit-0 even when
// unauthenticated/uninitialized.
func TestStatusE2E_JSONModeStaysExitZero(t *testing.T) {
	oxBin := buildOxBinary(t)
	dir := t.TempDir()
	env := statusIsolatedNoAuthEnv(t)

	out, exitCode, _ := testguard.RunOx(t, oxBin, dir, env, "status", "--json")

	assert.Equal(t, 0, exitCode, "--json must stay exit-0 regardless of auth/init state, got output:\n%s", out)
}

// TestStatusE2E_Quiet_SuppressesInformationalBody_KeepsExitCode is the
// red-first proof for the second half of #866: `ox status --help` advertised
// `-q, --quiet` but `ox status | wc -l` and `ox status --quiet | wc -l`
// produced byte-identical output. --quiet must silence the informational
// body while still communicating failure via the exit code.
func TestStatusE2E_Quiet_SuppressesInformationalBody_KeepsExitCode(t *testing.T) {
	oxBin := buildOxBinary(t)
	dir := t.TempDir()
	env := statusIsolatedNoAuthEnv(t)

	plainOut, plainExit, _ := testguard.RunOx(t, oxBin, dir, env, "status")
	quietOut, quietExit, _ := testguard.RunOx(t, oxBin, dir, env, "status", "--quiet")

	require.NotEqual(t, 0, plainExit, "sanity check: plain invocation must fail in this unauthenticated/uninitialized fixture")
	assert.Contains(t, plainOut, "Authentication Status", "plain invocation renders the informational body")

	assert.Equal(t, plainExit, quietExit, "--quiet must not change the exit-code verdict")
	assert.NotContains(t, quietOut, "Authentication Status", "--quiet must suppress the informational body")

	// stdout only (not the combined stderr "Error: ..." line) is the part
	// --quiet promises to silence -- assert on the literal stdout stream too,
	// not just the combined-output heuristic above.
	stdoutOnly := strings.TrimSpace(quietOut)
	if idx := strings.Index(stdoutOnly, "Error:"); idx >= 0 {
		stdoutOnly = strings.TrimSpace(stdoutOnly[:idx])
	}
	assert.Empty(t, stdoutOnly, "--quiet must produce no non-error stdout, got:\n%s", quietOut)
}
