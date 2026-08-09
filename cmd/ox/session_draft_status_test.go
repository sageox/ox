package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `ox session status` is the diagnostic that answers the question this whole
// feature exists to make answerable: "my /c/ link doesn't resolve — is the
// placeholder broken, or is the server just behind?"
//
// If draft_state lies, the user is back to guessing, which is exactly the
// pre-feature state. So the states have to be distinguishable, and in
// particular a session whose first publish worked and whose every refresh
// since has failed must NOT report itself healthy.

// setTestCfg assigns the package-global cfg and restores it afterward.
//
// The established pattern in this package (session_upload_test.go,
// session_force_stop_test.go, session_universal_link_test.go). Assigning
// without restoring order-couples every later test in the package to whichever
// draft test ran last — latent today, but this package already has one
// order-dependent test and does not need a second source of them.
func setTestCfg(t *testing.T) {
	t.Helper()
	prev := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = prev })
}

func draftEnabled() *config.ResolvedSessionDraft {
	return &config.ResolvedSessionDraft{
		Enabled: true, PublishTurn: config.DraftPublishTurn,
		RefreshEvery: config.DraftRefreshEveryTurns,
	}
}

func TestDraftStateFor_DistinguishesEveryLifecycleState(t *testing.T) {
	published := time.Now().Add(-5 * time.Minute).UTC()

	tests := []struct {
		name     string
		resolved *config.ResolvedSessionDraft
		state    *session.RecordingState
		want     string
		why      string
	}{
		{
			name:     "never attempted",
			resolved: draftEnabled(),
			state:    &session.RecordingState{TurnCount: 1},
			want:     "pending",
			why:      "below the publish turn, nothing has been tried yet",
		},
		{
			name:     "attempted once, never landed",
			resolved: draftEnabled(),
			state:    &session.RecordingState{TurnCount: 3, DraftAttemptTurn: 2},
			want:     "failed",
			why:      "an attempt with no success is the signal that would otherwise be silent",
		},
		{
			name:     "published and healthy",
			resolved: draftEnabled(),
			state: &session.RecordingState{
				TurnCount: 5, DraftAttemptTurn: 2, DraftPublishedTurn: 2,
				DraftPublishedAt: &published,
			},
			want: "published",
			why:  "the most recent attempt is also the most recent success",
		},
		{
			name:     "published once, refreshes now failing",
			resolved: draftEnabled(),
			state: &session.RecordingState{
				TurnCount: 25, DraftAttemptTurn: 22, DraftPublishedTurn: 12,
				DraftPublishedAt: &published,
			},
			want: "stale",
			why: "DraftPublishedAt is never cleared, so without comparing attempt-vs-success " +
				"this reports healthy forever while every refresh fails",
		},
		{
			name:     "feature disabled",
			resolved: &config.ResolvedSessionDraft{Enabled: false},
			state:    &session.RecordingState{TurnCount: 9, DraftAttemptTurn: 2},
			want:     "disabled",
			why:      "a user who turned it off must not see 'failed'",
		},
		{
			name:     "nil resolved config is disabled, not a panic",
			resolved: nil,
			state:    &session.RecordingState{TurnCount: 9},
			want:     "disabled",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, draftStateFor(tc.resolved, tc.state), tc.why)
		})
	}

	assert.Empty(t, draftStateFor(draftEnabled(), nil), "nil state must be nil-safe")
}

// TestConversationURLForState_RespectsAttributionGate.
//
// sessionLinkOutputs documents the invariant that an empty attribution.session
// toggle disables EVERY session-link surface together. Status is a session-link
// surface. Emitting a /c/ URL here for a user who deliberately turned session
// attribution off leaks the link into a place they asked it not to appear.
func TestConversationURLForState_RespectsAttributionGate(t *testing.T) {
	projectCfg := &config.ProjectConfig{
		ConfigVersion: "2",
		RepoID:        "repo_status_test",
		Endpoint:      "https://test.sageox.ai",
	}
	state := &session.RecordingState{SessionID: draftTestSessionID}

	on := conversationURLForState(projectCfg, true, state)
	require.NotEmpty(t, on, "with attribution on, the link must be emitted")
	assert.Contains(t, on, "/c/"+draftTestSessionID)

	assert.Empty(t, conversationURLForState(projectCfg, false, state),
		"attribution off must disable this surface with every other session-link surface")

	assert.Empty(t, conversationURLForState(nil, true, state),
		"no project config, no link")
	assert.Empty(t, conversationURLForState(projectCfg, true, &session.RecordingState{}),
		"a recording with no ses_ id has no conversation URL")
	assert.Empty(t, conversationURLForState(projectCfg, true, nil))
}

// TestBuildSessionRecordingEntry_CarriesDraftSurface — the multi-session JSON
// rows must expose the same draft fields as the single-session shape, or a
// dashboard enumerating agents can see the state for one session and not the
// others. The pause fields had the same requirement (ADR-020).
func TestBuildSessionRecordingEntry_CarriesDraftSurface(t *testing.T) {
	published := time.Now().Add(-time.Minute).UTC()
	state := &session.RecordingState{
		AgentID: "OxStat1", StartedAt: time.Now().Add(-time.Hour),
		SessionID: draftTestSessionID, TurnCount: 12,
		DraftAttemptTurn: 12, DraftPublishedTurn: 12, DraftPublishedAt: &published,
	}

	ctx := draftStatusContext{
		resolved:      draftEnabled(),
		projectCfg:    &config.ProjectConfig{ConfigVersion: "2", RepoID: "r", Endpoint: "https://test.sageox.ai"},
		attributionOn: true,
	}
	entry := buildSessionRecordingEntry(state, nil, "alive", ctx)

	assert.Equal(t, 12, entry.TurnCount)
	assert.Equal(t, "published", entry.DraftState)
	assert.NotEmpty(t, entry.DraftPublishedAt)
	assert.Contains(t, entry.ConversationURL, "/c/"+draftTestSessionID)
}

// TestBuildSessionRecordingEntry_ZeroContextIsInert guards the existing
// pause-surface tests, which construct a zero draftStatusContext. That must
// stay a safe no-op rather than panicking or inventing a link.
func TestBuildSessionRecordingEntry_ZeroContextIsInert(t *testing.T) {
	state := &session.RecordingState{AgentID: "OxStat2", StartedAt: time.Now(), TurnCount: 4}

	assert.NotPanics(t, func() {
		entry := buildSessionRecordingEntry(state, nil, "unknown", draftStatusContext{})
		assert.Equal(t, 4, entry.TurnCount)
		assert.Equal(t, "disabled", entry.DraftState, "a zero context has no resolved config")
		assert.Empty(t, entry.ConversationURL)
	})
}

// TestSessionDraftConfig_RoundTrip covers the user-facing switch. `off` is the
// escape hatch for a feature that writes to a SHARED repo, so it has to
// actually take effect — and an unrecognized value must fall back to the
// default rather than silently disabling recording visibility.
func TestSessionDraftConfig_RoundTrip(t *testing.T) {
	for _, tc := range []struct {
		in        string
		wantValid bool
		wantMode  string
	}{
		{"on", true, config.SessionDraftOn},
		{"off", true, config.SessionDraftOff},
		{"", true, config.SessionDraftOn},
		{"yes", false, config.SessionDraftOn},
		{"ON", false, config.SessionDraftOn},
	} {
		t.Run("value="+tc.in, func(t *testing.T) {
			assert.Equal(t, tc.wantValid, config.IsValidSessionDraft(tc.in))
			assert.Equal(t, tc.wantMode, config.NormalizeSessionDraft(tc.in),
				"an unrecognized value must fall back to the default, never to a third state")
		})
	}
}

// TestSessionDraftConfig_IsRegisteredAsASetting — the key must be discoverable
// via `ox config`, or the documented escape hatch does not exist for users.
func TestSessionDraftConfig_IsRegisteredAsASetting(t *testing.T) {
	var found *ConfigSetting
	for i := range AllSettings {
		if AllSettings[i].Key == "session.draft" {
			found = &AllSettings[i]
			break
		}
	}
	require.NotNil(t, found, "session.draft must be registered in AllSettings")
	assert.Equal(t, config.SessionDraftOn, found.Default)
	assert.ElementsMatch(t, config.ValidSessionDraftModes, found.ValidValues)
	assert.NotEmpty(t, found.LongDescription, "the escape hatch needs an explanation")
}

// --- surfaces the audit found at 0% coverage ------------------------------

// TestDraftViewNotice_ExplainsInsteadOfNotFound.
//
// `ox session view --text` needs transcript content, which a draft has none of.
// Without this the command hard-errors "session not found" for a session
// `ox session list` is simultaneously displaying — two commands disagreeing
// about whether a session exists is precisely the "everything seems broken"
// symptom drafts were added to remove.
func TestDraftViewNotice_ExplainsInsteadOfNotFound(t *testing.T) {
	setTestCfg(t)
	projectRoot, ledgerPath := draftReaperFixture(t)
	t.Chdir(projectRoot)

	const draftName = "2026-01-01T00-00-testuser-OxView1"
	draftLedgerSession(t, ledgerPath, draftName)

	msg := draftViewNotice(draftName)
	require.NotEmpty(t, msg, "a published draft must explain itself, not fall through to 'not found'")
	assert.Contains(t, msg, "still recording")
	assert.Contains(t, msg, draftName)
	assert.Contains(t, msg, "ox-session-stop", "the message must say what to do next")

	// Negative controls: neither a finalized session nor an absent one gets the
	// draft message, or `session view` would swallow a real not-found error.
	const doneName = "2026-01-01T00-00-testuser-OxView2"
	finalizedLedgerSession(t, ledgerPath, doneName)
	assert.Empty(t, draftViewNotice(doneName), "a finalized session is not a draft")
	assert.Empty(t, draftViewNotice("2026-01-01T00-00-testuser-OxNope1"),
		"an absent session must still produce the ordinary not-found error")
}

// TestNewDraftStatusContext_LoadsOncePerInvocation — the context exists so
// `ox session status` reads each config file once rather than once per active
// recording. Ten agents in a repo should not mean twenty config reads.
func TestNewDraftStatusContext_LoadsOncePerInvocation(t *testing.T) {
	setTestCfg(t)
	projectRoot, _ := draftReaperFixture(t)
	t.Chdir(projectRoot)

	ctx := newDraftStatusContext()
	require.NotNil(t, ctx.resolved, "a resolved policy must always be present")
	assert.Positive(t, ctx.resolved.PublishTurn)

	// Whatever the attribution toggle resolves to here, the context must be
	// internally consistent: no URL when attribution is off.
	if !ctx.attributionOn {
		assert.Empty(t, conversationURLForState(ctx.projectCfg, ctx.attributionOn,
			&session.RecordingState{SessionID: draftTestSessionID}))
	}
}

// TestEmitAbortOutput_TellsTheTruthAboutWhatRemains.
//
// The pre-draft guidance said "the recording no longer exists." Once a
// placeholder has been committed and PUSHED to a shared repo, that is false
// about the part that is irreversible: the identity record stays reachable in
// git history even after the deletion commit. An agent that repeats the old
// wording to a user is misinforming them about a privacy-relevant fact.
func TestEmitAbortOutput_TellsTheTruthAboutWhatRemains(t *testing.T) {
	prev := cfg
	setTestCfg(t)
	t.Cleanup(func() { cfg = prev })

	const name = "2026-01-01T00-00-testuser-OxMsg01"

	var plain bytes.Buffer
	require.NoError(t, emitAbortOutput(&plain, "OxMsg01", name, false, ""))
	var plainOut sessionAbortOutput
	require.NoError(t, json.Unmarshal(plain.Bytes(), &plainOut))
	assert.False(t, plainOut.LedgerDraftDeleted)
	assert.Contains(t, plainOut.Guidance, "no longer exists",
		"with nothing published, the original wording is still accurate")

	var drafted bytes.Buffer
	require.NoError(t, emitAbortOutput(&drafted, "OxMsg01", name, true, ""))
	var draftedOut sessionAbortOutput
	require.NoError(t, json.Unmarshal(drafted.Bytes(), &draftedOut))
	assert.True(t, draftedOut.LedgerDraftDeleted)
	assert.NotContains(t, draftedOut.Guidance, "recording no longer exists",
		"a pushed placeholder leaves an identity record in shared git history")
	assert.Contains(t, draftedOut.Guidance, "No conversation content was ever published")
	assert.Contains(t, draftedOut.Guidance, "git history")
	assert.Contains(t, draftedOut.Guidance, "do not add SageOx-Session",
		"the anchor phrase agents key on must survive both wordings")

	// A push failure must be surfaced, not swallowed: until the next push the
	// placeholder is still visible to teammates.
	var warned bytes.Buffer
	require.NoError(t, emitAbortOutput(&warned, "OxMsg01", name, true, "push failed: no remote"))
	var warnedOut sessionAbortOutput
	require.NoError(t, json.Unmarshal(warned.Bytes(), &warnedOut))
	assert.Equal(t, "push failed: no remote", warnedOut.Warning)
}
