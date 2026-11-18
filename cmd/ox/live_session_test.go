package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/flags"
	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/livesession/livesessiontest"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/sessionstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// liveFixture is a hook-recorded Claude-format session pointed at the fake live
// server. SAGEOX_DAEMON=false, so nothing drains until stop/abort or a test
// drives the daemon explicitly: exactly the no-daemon world.
type liveFixture struct {
	t           *testing.T
	srv         *livesessiontest.Server
	projectRoot string
	agentID     string
	sourceFile  string
	state       *session.RecordingState
	turns       int
}

type liveOpts struct {
	publishing string // "" (auto) or "manual"
	streaming  bool   // server rollout flag cached as on
}

func newLiveFixture(t *testing.T, o liveOpts) *liveFixture {
	t.Helper()
	srv := livesessiontest.New(t)

	adapters.Register(&testClaudeCodeAdapter{})
	t.Cleanup(func() { adapters.Unregister("claude-code") })

	home := t.TempDir()
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("SAGEOX_ENDPOINT", srv.URL)
	t.Setenv("SAGEOX_DAEMON", "false")

	projectRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, ".sageox"), 0o755))
	publishing := ""
	if o.publishing != "" {
		publishing = fmt.Sprintf(`,"session_publishing":%q`, o.publishing)
	}
	conf := fmt.Sprintf(`{"config_version":"2","repo_id":"repo_live_test","endpoint":%q%s}`, srv.URL, publishing)
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".sageox", "config.json"), []byte(conf), 0o644))
	require.NoError(t, auth.SaveTokenForEndpoint(srv.URL, &auth.StoredToken{
		AccessToken: livesessiontest.Token, TokenType: "Bearer", ExpiresAt: time.Now().Add(2 * time.Hour),
		UserInfo: auth.UserInfo{UserID: "person-a", Email: "person-a@test.sageox.ai"},
	}))
	if o.streaming {
		on := true
		require.NoError(t, flags.SaveCachedSettings(srv.URL, &flags.CLISettingsResponse{
			Features: flags.CLIFeatures{SessionStreaming: &on}, FetchedAt: time.Now(),
		}))
	}

	f := &liveFixture{t: t, srv: srv, projectRoot: projectRoot, agentID: "OxLive1"}
	state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{AgentID: f.agentID, AdapterName: "claude-code", Username: "person-a"})
	require.NoError(t, err)
	f.sourceFile = filepath.Join(t.TempDir(), "session.jsonl")
	header := fmt.Sprintf("{\"type\":\"session\",\"sessionId\":\"session\",\"cwd\":%q}\n", projectRoot)
	require.NoError(t, os.WriteFile(f.sourceFile, []byte(header), 0o644))
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, f.agentID, func(s *session.RecordingState) { s.SessionFile = f.sourceFile }))
	// the start path: writes raw.jsonl's header, which is where live state is prepared
	require.NoError(t, writeRawHeader(projectRoot, state))
	f.state = state

	cfg = &config.Config{}
	t.Chdir(projectRoot)
	return f
}

func (f *liveFixture) dir() string { return f.state.SessionPath }

func (f *liveFixture) rawBytes() []byte {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir(), "raw.jsonl"))
	require.NoError(f.t, err)
	return b
}

func (f *liveFixture) spoolBytes() []byte {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir(), "live.ndjson"))
	require.NoError(f.t, err)
	return b
}

// turn adds one user+assistant exchange to the transcript and runs the
// PostToolUse hook over it, returning how long the hook took.
func (f *liveFixture) turn(text string) time.Duration {
	f.t.Helper()
	f.turns++
	at := f.state.StartedAt.Add(time.Duration(f.turns) * time.Second)
	appendClaudeEntries(f.t, f.sourceFile, at,
		`{"type":"user","timestamp":"`+at.Format(time.RFC3339Nano)+`","message":{"role":"user","content":"`+text+`"}}`,
		`{"type":"assistant","timestamp":"`+at.Add(500*time.Millisecond).Format(time.RFC3339Nano)+`","message":{"role":"assistant","content":[{"type":"text","text":"reply to `+text+`"}]}}`,
	)
	start := time.Now()
	require.NoError(f.t, handleAfterTool(&HookContext{Phase: phaseAfterTool, ProjectRoot: f.projectRoot, Marker: &SessionMarker{AgentID: f.agentID}}))
	return time.Since(start)
}

// stop runs the real stop-time finalization (final drain, summary, live close, v1 upload attempt).
func (f *liveFixture) stop() {
	f.t.Helper()
	state, err := session.LoadRecordingStateForAgent(f.projectRoot, f.agentID)
	require.NoError(f.t, err)
	adapter, err := adapters.GetAdapter(state.AdapterName)
	require.NoError(f.t, err)
	_, err = finalizeIncrementalSession(f.projectRoot, state, filepath.Join(state.SessionPath, "raw.jsonl"), adapter, &agentSessionResult{})
	require.NoError(f.t, err)
}

func (f *liveFixture) liveState() livesession.State {
	f.t.Helper()
	st, err := livesession.ReadState(f.dir())
	require.NoError(f.t, err)
	return st
}

// openAndDrain does what the daemon does on its first drain of a pending session.
func (f *liveFixture) openAndDrain() livesession.State {
	f.t.Helper()
	cfg := liveConfig(f.projectRoot)
	st := f.liveState()
	_, err := livesession.Open(context.Background(), cfg, f.dir(), livesession.OpenParams{SessionID: st.SessionID})
	require.NoError(f.t, err)
	_, err = livesession.Drain(context.Background(), cfg, f.dir(), 5*time.Second)
	require.NoError(f.t, err)
	return f.liveState()
}

func (f *liveFixture) abort() {
	f.t.Helper()
	setForceFlag(f.t, true)
	require.NoError(f.t, runAgentSessionAbort(&agentinstance.Instance{AgentID: f.agentID}, agentCmd, nil))
}

func TestLiveHookSessionLeavesServerHoldingTheLocalSpoolAndAClosedLayer(t *testing.T) {
	f := newLiveFixture(t, liveOpts{streaming: true})

	f.turn("first question")
	f.turn("second question")

	// hooks only spool: the spool IS raw.jsonl so far, and the network was never touched
	assert.Equal(t, string(f.rawBytes()), string(f.spoolBytes()), "the spool must be exactly what the capture writer put in raw.jsonl")
	assert.Equal(t, livesession.StatusPending, f.liveState().Status)
	assert.Empty(t, f.srv.Calls(), "a hook must never wait on, or even call, the server")

	f.stop() // no daemon: stop does open -> drain -> close

	st := f.liveState()
	spool := f.spoolBytes()
	require.Equal(t, livesession.StatusClosed, st.Status)
	assert.Equal(t, string(spool), string(f.srv.Stored(t, st.ConversationID, st.LayerID)), "server must hold the local spool byte for byte")
	closed, ok := f.srv.ClosedWith(st.ConversationID)
	require.True(t, ok, "the server never saw a close")
	assert.Equal(t, sessionstream.BytesSHA256(spool), closed.StreamSHA256)
	assert.Equal(t, int64(len(spool)), closed.StoredThrough)
	assert.Contains(t, string(spool), "second question")
}

// The transcript can grow after the last hook (a turn that ended without a
// PostToolUse). Stop's final drain captures it; it must reach the stream too, or
// the server's copy silently ends early.
func TestLiveStopsFinalDrainReachesTheServer(t *testing.T) {
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("hooked question")
	at := f.state.StartedAt.Add(30 * time.Second)
	appendClaudeEntries(t, f.sourceFile, at,
		`{"type":"user","timestamp":"`+at.Format(time.RFC3339Nano)+`","message":{"role":"user","content":"unhooked last question"}}`)

	f.stop()

	st := f.liveState()
	stored := string(f.srv.Stored(t, st.ConversationID, st.LayerID))
	assert.Contains(t, stored, "unhooked last question")
	assert.Equal(t, string(f.spoolBytes()), stored)
	closed, ok := f.srv.ClosedWith(st.ConversationID)
	require.True(t, ok)
	assert.Equal(t, sessionstream.BytesSHA256(f.spoolBytes()), closed.StreamSHA256)
}

func TestLiveFlagOffMakesNoLiveCallsAndWritesNoLiveState(t *testing.T) {
	f := newLiveFixture(t, liveOpts{}) // no cached settings; the one-off fetch finds none either
	f.turn("a question")
	f.stop()
	assert.Empty(t, f.srv.Calls())
	_, err := livesession.ReadState(f.dir())
	assert.ErrorIs(t, err, livesession.ErrNotStreaming)
	assert.NoFileExists(t, filepath.Join(f.dir(), "live.ndjson"))
}

func TestLiveManualPublishingMakesNoLiveCalls(t *testing.T) {
	f := newLiveFixture(t, liveOpts{streaming: true, publishing: "manual"})
	f.turn("a private question")
	f.stop()
	assert.Empty(t, f.srv.Calls())
	_, err := livesession.ReadState(f.dir())
	assert.ErrorIs(t, err, livesession.ErrNotStreaming, "manual publishing must not even prepare a spool")
}

func TestLiveLinesCapturedWhilePausedNeverReachTheServer(t *testing.T) {
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("before the pause")
	now := time.Now()
	require.NoError(t, session.UpdateRecordingStateForAgent(f.projectRoot, f.agentID, func(s *session.RecordingState) { s.SuspendedAt = &now }))
	f.turn("PAUSED-SECRET")
	require.NoError(t, session.UpdateRecordingStateForAgent(f.projectRoot, f.agentID, func(s *session.RecordingState) { s.SuspendedAt = nil }))
	f.turn("after the resume")

	require.Contains(t, string(f.rawBytes()), "PAUSED-SECRET", "precondition: raw.jsonl keeps paused content locally until stop masks it")
	f.stop()

	st := f.liveState()
	stored := string(f.srv.Stored(t, st.ConversationID, st.LayerID))
	assert.NotContains(t, stored, "PAUSED-SECRET")
	assert.NotContains(t, string(f.spoolBytes()), "PAUSED-SECRET")
	assert.Contains(t, stored, "before the pause")
	assert.Contains(t, stored, "after the resume")
}

func TestLiveAbortOnlineDeletesTheServerCopy(t *testing.T) {
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("to be discarded")
	st := f.openAndDrain()
	require.NotEmpty(t, f.srv.Stored(t, st.ConversationID, st.LayerID))

	f.abort()

	assert.True(t, f.srv.IsDeleted(st.ConversationID), "abort must delete the server copy")
	assert.NoDirExists(t, f.dir())
}

func TestLiveAbortOfflineQueuesTheDeleteAndTheDaemonTickRetiresIt(t *testing.T) {
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("to be discarded")
	st := f.openAndDrain()
	f.srv.SetDown(true)

	f.abort() // must not fail or hang while the server is down

	assert.False(t, f.srv.IsDeleted(st.ConversationID))
	assert.NoDirExists(t, f.dir(), "abort still removes local state")
	queue, err := os.ReadFile(filepath.Join(paths.DataDir(), "live-pending-deletes.json"))
	require.NoError(t, err)
	assert.Contains(t, string(queue), st.ConversationID)

	f.srv.SetDown(false)
	live := daemon.NewLiveSessions(slog.New(slog.NewTextHandler(testWriter{t}, nil)), t.TempDir(), f.srv.URL)
	live.Tick(context.Background())
	assert.True(t, f.srv.IsDeleted(st.ConversationID), "the daemon tick must retry the queued delete")
}

func TestLiveHookLatencyStaysTinyWhenTheServerHangs(t *testing.T) {
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.srv.Hang(true)
	var worst time.Duration
	for i := range 3 {
		if d := f.turn(fmt.Sprintf("question %d", i)); d > worst {
			worst = d
		}
	}
	t.Logf("worst hook latency with a hung server: %s", worst)
	assert.Less(t, worst, time.Second, "a hung server must not slow a hook")
	assert.Equal(t, f.rawBytes(), f.spoolBytes())
}

func TestLiveStartFetchesSettingsOnceWhenTheCacheIsMissing(t *testing.T) {
	var hits int
	settings := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"features":{"session_streaming":true},"killswitches":{}}`))
	}))
	t.Cleanup(settings.Close)
	f := newLiveFixture(t, liveOpts{})
	t.Setenv("SAGEOX_ENDPOINT", settings.URL)
	require.NoError(t, auth.SaveTokenForEndpoint(settings.URL, &auth.StoredToken{AccessToken: "t", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour)}))

	assert.True(t, liveStreamingEnabled(f.projectRoot), "a missing cache must be fetched, not treated as off forever")
	assert.True(t, liveStreamingEnabled(f.projectRoot))
	assert.Equal(t, 1, hits, "the second start must read the cache")
}

func TestLiveStartNeverWaitsLongerThanTheBoundOnASlowSettingsServer(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	t.Cleanup(func() { close(release); slow.Close() })
	f := newLiveFixture(t, liveOpts{})
	t.Setenv("SAGEOX_ENDPOINT", slow.URL)
	require.NoError(t, auth.SaveTokenForEndpoint(slow.URL, &auth.StoredToken{AccessToken: "t", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour)}))

	start := time.Now()
	assert.False(t, liveStreamingEnabled(f.projectRoot))
	assert.Less(t, time.Since(start), liveSettingsFetchBound+500*time.Millisecond)
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
