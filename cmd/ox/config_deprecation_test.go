package main

import (
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- FIX K (D5, superseded 2026-09-10 to "deprecate with redirect"): ---
//
// context_git.auto_push / context_git.auto_commit were never wired to any
// behavior (bead ox-6p5y.11). Ryan's ruling: keep the key resolvable and
// settable — 'ox config set context_git.auto_push off' must still succeed
// — but tell the user plainly that it never took effect, and point at the
// control that actually works (session_publishing). A hard removal would
// have turned this into an unknown-setting error or silent data loss;
// neither tells anyone who set it for privacy reasons that they were
// unprotected.

func TestSetConfigValue_DeprecatedContextGitStillAccepted(t *testing.T) {
	setupIsolatedUserConfig(t)

	// Must be ACCEPTED, not "unknown setting" and not silently dropped.
	require.NoError(t, SetConfigValue("context_git.auto_push", "off", ConfigLevelUser, ""))
	require.NoError(t, SetConfigValue("context_git.auto_commit", "off", ConfigLevelUser, ""))

	// The value round-trips (so 'ox config get' can tell the user what
	// they set), even though it never controlled anything.
	cv, err := ResolveConfigValue("context_git.auto_push", "")
	require.NoError(t, err)
	assert.Equal(t, "off", cv.Value)
	assert.Equal(t, ConfigLevelUser, cv.Source)

	cfg, err := config.LoadUserConfig()
	require.NoError(t, err)
	require.NotNil(t, cfg.ContextGit)
	require.NotNil(t, cfg.ContextGit.AutoPush)
	assert.False(t, *cfg.ContextGit.AutoPush)
}

func TestGetSetting_ContextGitMarkedDeprecatedWithHonestNotice(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"context_git.auto_push", "context_git.auto_commit"} {
		setting := GetSetting(key)
		require.NotNil(t, setting, "key=%s", key)
		assert.True(t, setting.Deprecated, "key=%s must be marked Deprecated", key)
		require.NotEmpty(t, setting.DeprecationNotice, "key=%s", key)
		// The two required beats (per Ryan's ruling): it never worked, and
		// here is what to use instead. "is no longer used" would soften the
		// first beat and must not appear in place of "never had any effect".
		assert.Contains(t, setting.DeprecationNotice, "never had any effect", "key=%s", key)
		assert.Contains(t, setting.DeprecationNotice, "session_publishing manual", "key=%s", key)
		assert.NotContains(t, setting.DeprecationNotice, "no longer used", "key=%s", key)
	}

	// session_recording, the sibling non-deprecated setting, must not be
	// caught by the same marking.
	assert.False(t, GetSetting("session_recording").Deprecated)
}

func TestRunConfigSet_PrintsDeprecationNoticeForContextGit(t *testing.T) {
	setupIsolatedUserConfig(t)

	out := string(captureRealStdout(t, func() {
		require.NoError(t, configSetCmd.RunE(configSetCmd, []string{"context_git.auto_push", "off"}))
	}))

	assert.Contains(t, out, "Set context_git.auto_push = off", "the normal success line must still print")
	assert.Contains(t, out, "never had any effect", "the deprecation notice must print on set")
	assert.Contains(t, out, "session_publishing manual", "the notice must point at the working control")
}

func TestRunConfigSet_NoDeprecationNoiseForNonDeprecatedSetting(t *testing.T) {
	setupIsolatedUserConfig(t)

	out := string(captureRealStdout(t, func() {
		require.NoError(t, configSetCmd.RunE(configSetCmd, []string{"telemetry", "off"}))
	}))

	assert.NotContains(t, out, "never had any effect", "a working setting must not print a deprecation notice")
}

func TestRunConfigGet_PrintsDeprecationNoticeForContextGit(t *testing.T) {
	setupIsolatedUserConfig(t)

	out := string(captureRealStdout(t, func() {
		require.NoError(t, configGetCmd.RunE(configGetCmd, []string{"context_git.auto_push"}))
	}))

	// Must print unconditionally on 'get' — not only after a fresh 'set' —
	// because the coworker most at risk set this long ago and is now
	// auditing, not re-setting it.
	assert.Contains(t, out, "never had any effect")
	assert.Contains(t, out, "session_publishing manual")
}

func TestRunConfigList_ShowsDeprecatedSuffixForContextGit(t *testing.T) {
	setupIsolatedUserConfig(t)

	out := string(captureRealStdout(t, func() {
		require.NoError(t, configListCmd.RunE(configListCmd, []string{}))
	}))

	assert.Contains(t, out, "context_git.auto_push:")
	assert.Contains(t, out, "(deprecated)")
}
