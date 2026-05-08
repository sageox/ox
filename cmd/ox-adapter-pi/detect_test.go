package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolateDetectEnv pins HOME away from any real ~/.pi and clears PATH so
// exec.LookPath("pi") deterministically fails. Without this, handleDetect's
// later fallbacks (~/.pi present, pi binary in PATH) can mask the env-var
// detection logic these tests are trying to verify.
func isolateDetectEnv(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("PATH", filepath.Join(tmp, "no-such-bin"))
	t.Setenv("AGENT_ENV", "")
	t.Setenv("PI_CODING_AGENT", "")
}

// TestDetect_PICodingAgentTrue confirms handleDetect recognizes pi when
// PI_CODING_AGENT=true is propagated by pi's bash subprocess.
//
// Failure prevented: ox CLI invoked from inside pi (which sets
// process.env.PI_CODING_AGENT="true" at cli.ts:13) reports
// "must be run from within a coding agent" because detection misses
// the only env signal pi actually exports.
func TestDetect_PICodingAgentTrue(t *testing.T) {
	isolateDetectEnv(t)
	t.Setenv("PI_CODING_AGENT", "true")

	resp, err := handleDetect()
	require.NoError(t, err)
	assert.True(t, resp.Detected)
	assert.Equal(t, "PI_CODING_AGENT=true", resp.Reason)
}

// TestDetect_PICodingAgentOne accepts "1" alongside "true" so callers that
// follow the more common boolean-env convention still match.
func TestDetect_PICodingAgentOne(t *testing.T) {
	isolateDetectEnv(t)
	t.Setenv("PI_CODING_AGENT", "1")

	resp, err := handleDetect()
	require.NoError(t, err)
	assert.True(t, resp.Detected)
	assert.Equal(t, "PI_CODING_AGENT=1", resp.Reason)
}

// TestDetect_PICodingAgentFalse must NOT trigger detection — explicitly
// disabling the flag should behave like "unset", otherwise we false-positive
// for any process that happens to forward PI_CODING_AGENT=false from elsewhere.
func TestDetect_PICodingAgentFalse(t *testing.T) {
	isolateDetectEnv(t)
	t.Setenv("PI_CODING_AGENT", "false")

	resp, err := handleDetect()
	require.NoError(t, err)
	assert.False(t, resp.Detected, "PI_CODING_AGENT=false must not be treated as detection signal")
}

// TestDetect_AGENTEnvWinsOverPICodingAgent — when both signals are present,
// the existing AGENT_ENV=pi short-circuit must still fire first so the Reason
// stays stable for callers that key off it.
func TestDetect_AGENTEnvWinsOverPICodingAgent(t *testing.T) {
	isolateDetectEnv(t)
	t.Setenv("AGENT_ENV", "pi")
	t.Setenv("PI_CODING_AGENT", "true")

	resp, err := handleDetect()
	require.NoError(t, err)
	assert.True(t, resp.Detected)
	assert.Equal(t, "AGENT_ENV=pi", resp.Reason)
}

// TestDetect_NoSignals — clean machine, nothing claiming pi: must report
// not-detected with the original "~/.pi/ not found and pi not in PATH" reason.
// Failure prevented: silently flipping default detection to true would force
// pi adapter onto Claude-Code/Cursor sessions on machines that incidentally
// have neither pi nor PI_CODING_AGENT set.
func TestDetect_NoSignals(t *testing.T) {
	isolateDetectEnv(t)

	resp, err := handleDetect()
	require.NoError(t, err)
	assert.False(t, resp.Detected)
	assert.Equal(t, "~/.pi/ not found and pi not in PATH", resp.Reason)
}
