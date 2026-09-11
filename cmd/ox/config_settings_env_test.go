package main

import (
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveConfigValue_SessionPublishing_EnvWinsOverUser is the red-first
// proof for the CodeRabbit thread on cmd/ox/config_settings.go:601:
// ResolveSessionPublishing (the resolver actually consulted at runtime)
// gives OX_SESSION_PUBLISHING top priority, but ResolveConfigValue (what
// 'ox config get session_publishing' displays) never consulted the env var
// at all — so a coworker auditing their privacy posture with 'ox config get
// session_publishing' would see "auto" (their stored user value) while the
// CLI was actually running in "manual" because of the env var. That is
// exactly the "setting lists but doesn't reflect what's in effect" defect
// class this PR closes everywhere else.
//
// Run red-first against the pre-fix ResolveConfigValue (no EnvVal / no env
// read in the "session_publishing" case): this fails because cv.Value comes
// back "auto" (from UserVal) and cv.Source comes back "user", not "env".
func TestResolveConfigValue_SessionPublishing_EnvWinsOverUser(t *testing.T) {
	setupIsolatedUserConfig(t)
	require.NoError(t, SetConfigValue("session_publishing", "auto", ConfigLevelUser, ""))
	t.Setenv(config.EnvSessionPublishing, "manual")

	cv, err := ResolveConfigValue("session_publishing", "")
	require.NoError(t, err)

	assert.Equal(t, "manual", cv.Value, "displayed effective value must match what ResolveSessionPublishing actually uses")
	assert.Equal(t, ConfigLevelEnv, cv.Source, "source must be attributed to the env var, not the stored user value")
	assert.Equal(t, "manual", cv.EnvVal)
	assert.Equal(t, "auto", cv.UserVal, "the stored user value is still shown in the override chain, just not treated as effective")
}

// TestResolveConfigValue_SessionRecording_EnvWinsOverUser mirrors the above
// for session_recording, the sibling setting named in contract C2.
func TestResolveConfigValue_SessionRecording_EnvWinsOverUser(t *testing.T) {
	setupIsolatedUserConfig(t)
	require.NoError(t, SetConfigValue("session_recording", "auto", ConfigLevelUser, ""))
	t.Setenv(config.EnvSessionRecording, "disabled")

	cv, err := ResolveConfigValue("session_recording", "")
	require.NoError(t, err)

	assert.Equal(t, "disabled", cv.Value)
	assert.Equal(t, ConfigLevelEnv, cv.Source)
	assert.Equal(t, "disabled", cv.EnvVal)
	assert.Equal(t, "auto", cv.UserVal)
}

// TestResolveConfigValue_SessionPublishing_NoEnv_UnaffectedByFix guards the
// precedence chain the fix must NOT disturb: with no env var set, the
// pre-existing user > repo > team > default resolution is untouched.
func TestResolveConfigValue_SessionPublishing_NoEnv_UnaffectedByFix(t *testing.T) {
	setupIsolatedUserConfig(t)
	t.Setenv(config.EnvSessionPublishing, "") // explicit: no ambient leak from the developer's shell
	require.NoError(t, SetConfigValue("session_publishing", "manual", ConfigLevelUser, ""))

	cv, err := ResolveConfigValue("session_publishing", "")
	require.NoError(t, err)

	assert.Equal(t, "manual", cv.Value)
	assert.Equal(t, ConfigLevelUser, cv.Source)
	assert.Empty(t, cv.EnvVal)
}

// TestResolveConfigValue_OtherSettings_NeverGetEnvVal confirms the fix is
// scoped to session_publishing/session_recording only (contract C2 — no
// re-architecture of ResolveConfigValue for every setting). A setting with
// no env layer must never populate EnvVal or report ConfigLevelEnv.
func TestResolveConfigValue_OtherSettings_NeverGetEnvVal(t *testing.T) {
	setupIsolatedUserConfig(t)
	require.NoError(t, SetConfigValue("telemetry", "off", ConfigLevelUser, ""))

	cv, err := ResolveConfigValue("telemetry", "")
	require.NoError(t, err)

	assert.Equal(t, "off", cv.Value)
	assert.Equal(t, ConfigLevelUser, cv.Source)
	assert.Empty(t, cv.EnvVal)
}
