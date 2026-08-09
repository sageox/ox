package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `session.draft` is the user-facing off switch for a feature that COMMITS TO A
// SHARED GIT REPO on the user's behalf. If `ox config set session.draft off`
// does not actually reach the publisher, a user who opted out keeps writing
// placeholders into their team's ledger — the worst possible failure for an
// opt-out.
//
// The pure string helpers being correct proves nothing about that: the
// publisher branches on ResolvedSessionDraft.Enabled, and only
// ResolveSessionDraft produces it.

// withUserConfig points user-config resolution at a temp HOME and writes the
// given config.yaml body (empty string = no file at all).
func withUserConfig(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	if body == "" {
		return
	}
	dir := filepath.Join(home, ".config", "sageox")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644))
}

// TestResolveSessionDraft_OffSwitchActuallyDisables is the one that matters.
//
// Red-first check: make ResolveSessionDraft ignore userCfg.SessionDraft and
// this fails — while every string-helper test stays green.
func TestResolveSessionDraft_OffSwitchActuallyDisables(t *testing.T) {
	withUserConfig(t, "session_draft: \"off\"\n")

	resolved := ResolveSessionDraft()
	require.NotNil(t, resolved)
	assert.False(t, resolved.Enabled,
		"session_draft: off must reach the publisher, or opting out does nothing")
	assert.Equal(t, SessionDraftSourceUserConfig, resolved.Source)
}

func TestResolveSessionDraft_DefaultsToOn(t *testing.T) {
	withUserConfig(t, "")

	resolved := ResolveSessionDraft()
	require.NotNil(t, resolved)
	assert.True(t, resolved.Enabled, "unset means on")
	assert.Equal(t, SessionDraftSourceDefault, resolved.Source)
	assert.Equal(t, DraftPublishTurn, resolved.PublishTurn)
	assert.Equal(t, DraftRefreshEveryTurns, resolved.RefreshEvery)
}

func TestResolveSessionDraft_ExplicitOnIsUserSourced(t *testing.T) {
	withUserConfig(t, "session_draft: \"on\"\n")

	resolved := ResolveSessionDraft()
	require.NotNil(t, resolved)
	assert.True(t, resolved.Enabled)
	assert.Equal(t, SessionDraftSourceUserConfig, resolved.Source,
		"provenance matters: `ox config` shows the user where a value came from")
}

// TestResolveSessionDraft_GarbageValueFallsBackToOn.
//
// A hand-edited config with a typo must not land in a third state. Falling back
// to ON is the deliberate direction: the failure mode of wrongly-on is ledger
// noise, while wrongly-off is a silently broken feature the user thinks is
// working — which is the exact confusion drafts exist to remove.
func TestResolveSessionDraft_GarbageValueFallsBackToOn(t *testing.T) {
	for _, body := range []string{
		"session_draft: \"yes\"\n",
		"session_draft: \"OFF\"\n", // case-sensitive by design
		"session_draft: \"0\"\n",
	} {
		t.Run(body, func(t *testing.T) {
			withUserConfig(t, body)
			resolved := ResolveSessionDraft()
			require.NotNil(t, resolved)
			assert.True(t, resolved.Enabled, "an unrecognized value must not silently disable")
		})
	}
}

// TestResolveSessionDraft_AlwaysCarriesUsableCadence — the publisher divides by
// RefreshEvery and compares against PublishTurn. A resolution path that left
// either at zero would either disable refreshes silently or publish on turn 0.
func TestResolveSessionDraft_AlwaysCarriesUsableCadence(t *testing.T) {
	for _, body := range []string{"", "session_draft: \"on\"\n", "session_draft: \"off\"\n"} {
		withUserConfig(t, body)
		resolved := ResolveSessionDraft()
		require.NotNil(t, resolved)
		assert.Positive(t, resolved.PublishTurn, "body=%q", body)
		assert.Positive(t, resolved.RefreshEvery, "body=%q", body)
	}
}

// TestSessionDraft_UserConfigRoundTrip proves the value survives the actual
// save/load cycle `ox config set` uses — not just an in-memory struct.
func TestSessionDraft_UserConfigRoundTrip(t *testing.T) {
	withUserConfig(t, "")

	cfg, err := LoadUserConfig()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Empty(t, cfg.SessionDraft, "fresh config carries no value")

	cfg.SessionDraft = SessionDraftOff
	require.NoError(t, SaveUserConfig(cfg))

	reloaded, err := LoadUserConfig()
	require.NoError(t, err)
	assert.Equal(t, SessionDraftOff, reloaded.SessionDraft)
	assert.False(t, ResolveSessionDraft().Enabled,
		"the persisted off switch must be honored on the next process")

	// And unsetting restores the default rather than sticking at off.
	reloaded.SessionDraft = ""
	require.NoError(t, SaveUserConfig(reloaded))
	assert.True(t, ResolveSessionDraft().Enabled, "unset returns to the default")
}

func TestIsValidSessionDraft(t *testing.T) {
	for _, v := range []string{SessionDraftOn, SessionDraftOff, ""} {
		assert.True(t, IsValidSessionDraft(v), "%q must be accepted", v)
	}
	for _, v := range []string{"yes", "ON", "true", "1", "cloud"} {
		assert.False(t, IsValidSessionDraft(v), "%q must be rejected by `ox config set`", v)
	}
}

func TestNormalizeSessionDraft(t *testing.T) {
	assert.Equal(t, SessionDraftOff, NormalizeSessionDraft(SessionDraftOff))
	for _, v := range []string{SessionDraftOn, "", "garbage", "OFF"} {
		assert.Equal(t, SessionDraftOn, NormalizeSessionDraft(v),
			"%q must normalize to the default, never to a third state", v)
	}
}
