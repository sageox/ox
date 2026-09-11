package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/require"
)

// session_publishing: manual must suppress notifySessionStartedAsync exactly
// as it already suppresses the transcript upload (agent_session.go:1256,
// agent_session_incremental.go:264). Before this fix, a grep for
// "SessionPublishing" in this file returned zero hits: the cloud
// session-started POST — session_id, repo_id, session_name, agent id/type,
// and the BRANCH NAME — went out regardless of the setting.
//
// These tests drive the real notifySessionStartedAsync -> runSessionSignal ->
// api.RepoClient path against a real httptest server rather than stubbing
// runSessionSignal, so a regression that bypasses the gate at any layer (not
// just the one this patch touches) shows up as an unexpected inbound request.

// newNotifyManualFixture wires a counting HTTP endpoint, a saved auth token
// for it, and an initialized project with the given publishing mode. Returns
// the project root and the live request counter.
func newNotifyManualFixture(t *testing.T, publishing string) (projectRoot string, requestCount *int32) {
	t.Helper()
	requestCount = new(int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(requestCount, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(server.Close)

	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	projectRoot = createInitializedProjectWithConfig(t, &config.ProjectConfig{
		RepoID:            "repo_notify_manual_test",
		Endpoint:          server.URL,
		SessionPublishing: publishing,
	})

	require.NoError(t, auth.SaveToken(&auth.StoredToken{
		AccessToken: "test-access-token",
		TokenType:   "Bearer",
		ExpiresAt:   time.Now().Add(2 * time.Hour),
		UserInfo:    auth.UserInfo{UserID: "test-user-id", Email: "test@example.com"},
	}))

	return projectRoot, requestCount
}

// newRegisteredTurnState builds a RecordingState whose raw.jsonl already has a
// user turn, so notifySessionStartedAsync passes the HasUserTurn gate and
// reaches the code under test rather than deferring for an unrelated reason.
func newRegisteredTurnState(t *testing.T, agentID string) *session.RecordingState {
	t.Helper()
	sessionPath := t.TempDir()
	rawPath := filepath.Join(sessionPath, "raw.jsonl")
	require.NoError(t, os.WriteFile(rawPath,
		[]byte(`{"type":"user","content":"hello"}`+"\n"), 0644))

	return &session.RecordingState{
		AgentID:                    agentID,
		SessionID:                  "ses_01950000-0000-7000-8000-0000000000bb",
		SessionPath:                sessionPath,
		StartedAt:                  time.Now().Add(-time.Minute),
		LifecycleRegistrationState: "deferred",
	}
}

// TestNotifySessionStartedAsync_ManualMakesNoRequest is the red-first target:
// remove the session_publishing check in notifySessionStartedAsync and this
// test fails with requestCount == 1 instead of 0.
func TestNotifySessionStartedAsync_ManualMakesNoRequest(t *testing.T) {
	projectRoot, requestCount := newNotifyManualFixture(t, config.SessionPublishingManual)
	state := newRegisteredTurnState(t, "OxNotifyManual")

	notifySessionStartedAsync(projectRoot, state)

	require.Zero(t, atomic.LoadInt32(requestCount), "manual publishing must not contact the server")
	require.Equal(t, "deferred", state.LifecycleRegistrationState,
		"must stay deferred (not pending/confirmed) so a later switch to auto, or the prime/doctor retry, tries again")
}

// TestNotifySessionStartedAsync_AutoStillRegisters is the regression that
// matters most: the overwhelming majority of sessions are auto-published and
// must be completely unaffected by the manual-mode guard.
func TestNotifySessionStartedAsync_AutoStillRegisters(t *testing.T) {
	projectRoot, requestCount := newNotifyManualFixture(t, config.SessionPublishingAuto)
	state := newRegisteredTurnState(t, "OxNotifyAuto")

	notifySessionStartedAsync(projectRoot, state)

	require.Equal(t, int32(1), atomic.LoadInt32(requestCount), "auto publishing must still register the session")
	require.Equal(t, "confirmed", state.LifecycleRegistrationState)
}

// TestNotifySessionStartedAsync_DefaultPublishingStillRegisters guards the
// unset case (no session_publishing key at all), which resolves to "auto" via
// config.NormalizeSessionPublishing. Most repos never set this key, so this
// is the actual common path in production, not just a config edge case.
func TestNotifySessionStartedAsync_DefaultPublishingStillRegisters(t *testing.T) {
	projectRoot, requestCount := newNotifyManualFixture(t, "")
	state := newRegisteredTurnState(t, "OxNotifyDefault")

	notifySessionStartedAsync(projectRoot, state)

	require.Equal(t, int32(1), atomic.LoadInt32(requestCount), "unset session_publishing must default to auto")
}

// TestNotifySessionStartedAsync_MidSessionFlipToAutoResumes proves the state
// machine is not wedged: a session that starts in manual mode and later has
// its config flipped to auto must register on the very next attempt, using
// the exact retry mechanism production relies on (LifecycleRegistrationState
// == "deferred" triggers a re-attempt from maybePublishSessionDraft / prime /
// doctor).
func TestNotifySessionStartedAsync_MidSessionFlipToAutoResumes(t *testing.T) {
	projectRoot, requestCount := newNotifyManualFixture(t, config.SessionPublishingManual)
	state := newRegisteredTurnState(t, "OxNotifyFlip")

	notifySessionStartedAsync(projectRoot, state)
	require.Zero(t, atomic.LoadInt32(requestCount), "still suppressed under manual")
	require.Equal(t, "deferred", state.LifecycleRegistrationState)

	// Flip the repo to auto mid-session, exactly as `ox config set
	// session_publishing auto` would.
	projCfg, err := config.LoadProjectConfig(projectRoot)
	require.NoError(t, err)
	projCfg.SessionPublishing = config.SessionPublishingAuto
	require.NoError(t, config.SaveProjectConfig(projectRoot, projCfg))

	notifySessionStartedAsync(projectRoot, state)
	require.Equal(t, int32(1), atomic.LoadInt32(requestCount), "switching to auto must register on the next attempt")
	require.Equal(t, "confirmed", state.LifecycleRegistrationState)
}

// TestNotifySessionStartedAsync_MidSessionFlipToManualStopsFurtherRequests is
// the mirror case: a session registered under auto that is later flipped to
// manual must not make any further outbound requests. (The already-registered
// /c/ page stays live server-side; ox does not attempt to retract it — that
// is a separate, already-accepted cost documented for abort/discard flows.)
func TestNotifySessionStartedAsync_MidSessionFlipToManualStopsFurtherRequests(t *testing.T) {
	projectRoot, requestCount := newNotifyManualFixture(t, config.SessionPublishingAuto)
	state := newRegisteredTurnState(t, "OxNotifyFlipBack")

	notifySessionStartedAsync(projectRoot, state)
	require.Equal(t, int32(1), atomic.LoadInt32(requestCount))
	require.Equal(t, "confirmed", state.LifecycleRegistrationState)

	projCfg, err := config.LoadProjectConfig(projectRoot)
	require.NoError(t, err)
	projCfg.SessionPublishing = config.SessionPublishingManual
	require.NoError(t, config.SaveProjectConfig(projectRoot, projCfg))

	// A later call (e.g. a stray retry) must not add a second request. The
	// deferred-vs-confirmed re-fire guards in session_draft_publish.go already
	// prevent re-calling for a confirmed state in production; this test calls
	// unconditionally to prove the manual gate itself is a hard stop even if
	// some future caller lost that guard.
	notifySessionStartedAsync(projectRoot, state)
	require.Equal(t, int32(1), atomic.LoadInt32(requestCount), "manual must not add a second request even mid-session")
}

// TestNotifySessionStartedAsync_UnreadableProjectConfigMakesNoRequest answers
// the fail-open-vs-fail-closed question for a project whose config cannot be
// read at all (corrupt .sageox/config.json).
//
// Decision: this is fail-CLOSED today, but not by anything this patch adds.
// runSessionSignal's pre-existing guard (`config.LoadProjectConfig` err or
// empty RepoID -> "project configuration unavailable") already refuses to
// dial out before my session_publishing check ever runs. I deliberately did
// not add a second, possibly-divergent unreadable-config path in my own gate:
// config.GetSessionPublishing resolves an unreadable/absent config to "auto"
// (matching the two existing, already-shipped transcript-upload gates in
// agent_session.go and agent_session_incremental.go, which call the exact
// same function) — introducing a bespoke fail-closed special case here would
// make the three "manual" enforcement points disagree about what an
// unreadable config means for the identical setting, which is a worse
// inconsistency than the rare unreadable-config case itself. The test proves
// the practical outcome (no request) is unaffected by which layer supplies
// the safety net.
func TestNotifySessionStartedAsync_UnreadableProjectConfigMakesNoRequest(t *testing.T) {
	requestCount := new(int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	projectRoot := t.TempDir()
	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	// Corrupt, not merely absent: proves the "config unavailable" branch, not
	// just "no file yet".
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte("{not json"), 0644))

	state := newRegisteredTurnState(t, "OxNotifyCorrupt")

	notifySessionStartedAsync(projectRoot, state)

	require.Zero(t, atomic.LoadInt32(requestCount), "an unreadable project config must not be treated as license to publish")
}
