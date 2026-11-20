package main

import (
	"context"
	"encoding/json"
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
	"github.com/sageox/ox/internal/sessionid"
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
	// the start path: writes raw.jsonl's header, which is where stream state is prepared
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
	b, err := os.ReadFile(filepath.Join(f.dir(), "spool.jsonl"))
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

// stopResult is stop, returning what the stop command would report.
func (f *liveFixture) stopResult() *agentSessionResult {
	f.t.Helper()
	state, err := session.LoadRecordingStateForAgent(f.projectRoot, f.agentID)
	require.NoError(f.t, err)
	adapter, err := adapters.GetAdapter(state.AdapterName)
	require.NoError(f.t, err)
	result, err := finalizeIncrementalSession(f.projectRoot, state, filepath.Join(state.SessionPath, "raw.jsonl"), adapter, &agentSessionResult{})
	require.NoError(f.t, err)
	return result
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
	assert.NoFileExists(t, filepath.Join(f.dir(), "spool.jsonl"))
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

// A session deleted on SageOx while it streams is rejected end to end: the
// stream answers 410, later turns stay local and quiet, and stop keeps raw.jsonl
// on this machine but never hands the session to the Ledger. Failure prevented:
// the v1 push re-publishing a session someone deleted on SageOx.
func TestLiveSessionDeletedOnServerIsNeverPublishedAtStop(t *testing.T) {
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("before the delete")
	st := f.openAndDrain()
	f.srv.DeleteSession(st.ConversationID)

	f.turn("after the delete")
	// the daemon's next drain hears the 410
	_, err := livesession.Drain(context.Background(), liveConfig(f.projectRoot), f.dir(), 5*time.Second)
	require.NoError(t, err)
	calls := len(f.srv.Calls())
	f.turn("one more after the delete")
	assert.Len(t, f.srv.Calls(), calls, "a turn after the delete must not reach the server")
	assert.NoFileExists(t, filepath.Join(f.dir(), "spool.jsonl"), "a turn after the delete must not start a new spool")

	result := f.stopResult()

	assert.True(t, result.Held, "stop must keep the deleted session on this machine")
	assert.True(t, result.DeletedOnServer)
	assert.Equal(t, deletedOnServerWarning, result.UploadWarning, "stop must say once why it was not shared")
	assert.Empty(t, result.LedgerSessionDir, "nothing may go to the Ledger")
	assert.Empty(t, result.SummaryPrompt, "a summary prompt leads to push-summary, which publishes")
	assert.NoFileExists(t, filepath.Join(f.dir(), ".needs-summary"), "the daemon publishes a session marked for summary")
	hold, err := session.ReadHoldMarker(f.dir())
	require.NoError(t, err, "every automatic publisher (daemon, doctor, hooks) checks the hold")
	assert.Equal(t, session.HoldDeletedOnServer, hold.Reason)
	assert.Contains(t, string(f.rawBytes()), "one more after the delete", "raw.jsonl is the person's own copy and stays")
}

// The /clear and SessionEnd hooks hand a stopped recording to the daemon, which
// summarizes and uploads it. A session deleted on SageOx must not be handed off.
func TestLiveHookStopNeverHandsADeletedSessionToTheDaemon(t *testing.T) {
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("before the delete")
	st := f.openAndDrain()
	f.srv.DeleteSession(st.ConversationID)
	f.turn("after the delete")
	_, err := livesession.Drain(context.Background(), liveConfig(f.projectRoot), f.dir(), 5*time.Second)
	require.NoError(t, err)
	var sent int
	orig := sendHookFinalizeIPC
	sendHookFinalizeIPC = func(daemon.SessionFinalizeIPCPayload) error { sent++; return nil }
	t.Cleanup(func() { sendHookFinalizeIPC = orig })
	require.NotEmpty(t, deriveLedgerPath(f.dir()), "precondition: a live session would be handed off")

	require.NoError(t, finalizeOrHoldOnHookStop(&HookContext{ProjectRoot: f.projectRoot}, f.state, "end"))

	assert.Zero(t, sent, "a deleted session must not reach the daemon's finalize")
	hold, err := session.ReadHoldMarker(f.dir())
	require.NoError(t, err)
	assert.Equal(t, session.HoldDeletedOnServer, hold.Reason)
}

// A local abort also ends in status deleted, but it must not read as a
// server-side delete: only the server's answer carries that meaning.
func TestLiveAbortIsNotADeleteOnServer(t *testing.T) {
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("to be discarded")
	f.openAndDrain()
	_, err := livesession.Abort(context.Background(), liveConfig(f.projectRoot), f.dir(), livesession.AbortBudget)
	require.NoError(t, err)
	require.Equal(t, livesession.StatusDeleted, f.liveState().Status)
	assert.False(t, liveDeletedOnServer(f.dir()))
	assert.False(t, session.IsHeld(f.dir()))
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

// markStreamWithheld records what the drain records when it replaces a refused
// batch with its gap line.
func markStreamWithheld(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "stream-state.json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var st map[string]any
	require.NoError(t, json.Unmarshal(raw, &st))
	st["secret_withheld"] = true
	raw, err = json.Marshal(st)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0o600))
}

// printStop writes the stop's output for result the way the stop command
// does (JSON by default) and returns it.
func (f *liveFixture) printStop(result *agentSessionResult) string {
	f.t.Helper()
	var err error
	out := captureStdoutForPlanCLI(f.t, func() {
		err = emitSessionStop(f.projectRoot, &agentinstance.Instance{AgentID: f.agentID}, f.state, "1s", result, nil, map[string]int64{})
	})
	require.NoError(f.t, err)
	return out
}

// owedLater is what the next recording's start or stop in this project would
// say about earlier streams.
func (f *liveFixture) owedLater() []string {
	return earlierLiveSecretWithheld(f.dir(), "ses_next")
}

// A batch the publication secret scan withheld from the live stream is told to
// the person once, at stop, without saying what matched (F1, F10). It is no
// upload failure: upload_warning stays for the Ledger push.
func TestLiveStopTellsThePersonOnceThatTheStreamWithheldASecret(t *testing.T) {
	if testing.Short() {
		t.Skip("short: a full hook-recorded stop against the fake server")
	}
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("before the secret")
	f.openAndDrain()
	markStreamWithheld(t, f.dir())

	result := f.stopResult()

	require.True(t, result.LiveSecretWithheld, "stop must learn the stream withheld a batch")
	assert.False(t, result.EarlierLiveSecretWithheld)
	assert.NotContains(t, result.UploadWarning, "credential", "a withheld live batch is not an upload failure")
	output := sessionStopOutput{Guidance: sessionStopGuidance}
	addLiveSecretWithheldNotice(&output, result)
	assert.Equal(t, []string{liveSecretWithheldWarning}, output.DataWarnings, "said once")
	assert.Contains(t, output.Guidance, liveSecretWithheldGuidance)
	assert.NotEmpty(t, f.owedLater(), "owed until the stop's output is written")
	assert.Equal(t, 1, strings.Count(f.printStop(result), liveSecretWithheldWarning))
	assert.Empty(t, f.owedLater(), "said at this stop, so owed to no later one")
}

// A recording whose raw.jsonl keeps no entry returns from stop early. The
// stream may still have withheld a batch. Failure prevented: the early return
// skipped the read, and neither stop output said so.
func TestLiveStopOfARecordingWithNoEntriesStillTellsTheWithheldNotice(t *testing.T) {
	if testing.Short() {
		t.Skip("short: a full hook-recorded stop against the fake server")
	}
	f := newLiveFixture(t, liveOpts{streaming: true})
	markStreamWithheld(t, f.dir())

	result := f.stopResult()

	require.Zero(t, result.EntryCount, "precondition: the early return for a recording with no entries")
	assert.True(t, result.LiveSecretWithheld, "the persisted flag must be read before the early return")
}

// A stop whose close is left to the daemon cannot know whether the stream will
// withhold anything: the scan runs when the daemon drains it. Failure
// prevented: the stop sampled the flag before the daemon withheld a batch, and
// nothing told the person afterwards. The notice stays owed until a later
// start or stop prints it. Recovery and a stop that fails run the same
// processing but print nothing, so processing alone must not use it up.
func TestLiveStopWhoseCloseIsDeferredTellsTheWithheldNoticeLater(t *testing.T) {
	if testing.Short() {
		t.Skip("short: a stop whose close fails against a down fake server, then the daemon close")
	}
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("before the outage")
	f.openAndDrain()
	f.srv.SetDown(true)
	f.turn("deploy future_synthetic")

	result := f.stopResult()

	require.Equal(t, livesession.StatusClosing, f.liveState().Status, "precondition: the close was left to the daemon")
	assert.False(t, result.LiveSecretWithheld, "nothing was withheld by the time stop returned")
	assert.Empty(t, f.owedLater(), "not withheld yet: nothing to say")

	// the daemon refreshes a detector for that credential, then finishes the close
	catalog := []byte("[[rules]]\nid='future-format'\nregex='future_[a-z]+'\n")
	require.NoError(t, os.WriteFile(livesession.SecretRulesPath(paths.DataDir()), catalog, 0o600))
	f.srv.SetDown(false)
	res, err := livesession.Close(context.Background(), liveConfig(f.projectRoot), f.dir(), "", 5*time.Second)
	require.NoError(t, err)
	require.Equal(t, livesession.StatusClosed, res.Status)
	require.True(t, f.liveState().SecretWithheld, "precondition: the daemon's close withheld the batch")

	next := &session.RecordingState{SessionID: "ses_next", SessionPath: filepath.Join(filepath.Dir(f.dir()), "next-recording")}
	for range 2 { // a recovery, then a stop that fails: processing with no output
		var processed agentSessionResult
		collectLiveSecretWithheldNotices(&processed, next)
		require.True(t, processed.EarlierLiveSecretWithheld, "the next start or stop in this project must say it")
	}
	var said agentSessionResult
	collectLiveSecretWithheldNotices(&said, next)
	assert.False(t, said.LiveSecretWithheld)
	output := sessionStopOutput{Guidance: sessionStopGuidance}
	addLiveSecretWithheldNotice(&output, &said)
	assert.Equal(t, []string{liveSecretWithheldEarlierWarning}, output.DataWarnings)
	assert.Contains(t, output.Guidance, liveSecretWithheldEarlierGuidance)
	assert.Equal(t, 1, strings.Count(f.printStop(&said), liveSecretWithheldEarlierWarning))
	assert.Empty(t, f.owedLater(), "said once")
}

// A retried stop of a recording whose stream already owes the notice says it
// once, as this recording's own. Failure prevented: the scan for earlier
// recordings counted the current one too, and stop printed both warnings.
func TestLiveStopNeverCountsItsOwnStreamAsAnEarlierOne(t *testing.T) {
	if testing.Short() {
		t.Skip("short: a full hook-recorded stop against the fake server")
	}
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("before the secret")
	f.openAndDrain()
	markStreamWithheld(t, f.dir())
	require.NoError(t, livesession.OweSecretWithheldNotice(f.dir())) // a first stop that failed after marking it

	result := f.stopResult()

	assert.True(t, result.LiveSecretWithheld)
	assert.False(t, result.EarlierLiveSecretWithheld, "this recording is not an earlier one")
	out := f.printStop(result)
	assert.Equal(t, 1, strings.Count(out, liveSecretWithheldWarning))
	assert.NotContains(t, out, liveSecretWithheldEarlierWarning)
}

// A /clear or SessionEnd hook stops a recording with no one to tell, so a
// stream that withheld a batch owes its notice to the next start or stop.
func TestLiveHookStopOwesTheWithheldNoticeToTheNextStartOrStop(t *testing.T) {
	if testing.Short() {
		t.Skip("short: a full hook-recorded stop against the fake server")
	}
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("before the secret")
	f.openAndDrain()
	markStreamWithheld(t, f.dir())
	orig := sendHookFinalizeIPC
	sendHookFinalizeIPC = func(daemon.SessionFinalizeIPCPayload) error { return nil }
	t.Cleanup(func() { sendHookFinalizeIPC = orig })

	require.NoError(t, finalizeOrHoldOnHookStop(&HookContext{ProjectRoot: f.projectRoot}, f.state, "end"))

	assert.Equal(t, []string{f.dir()}, f.owedLater(), "the next session start or stop must say it")
}

// A recording that starts in the folder an ended stream left (an automatic
// start after /clear lands in the same minute) retires that stream. Failure
// prevented: retiring removed an ended stream's files, its owed withheld
// notice with them, before any start or stop had said it.
func TestLiveRetiringAnEndedStreamKeepsItsOwedNotice(t *testing.T) {
	if testing.Short() {
		t.Skip("short: a full hook-recorded stop against the fake server")
	}
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("before the secret")
	f.openAndDrain()
	markStreamWithheld(t, f.dir())
	orig := sendHookFinalizeIPC
	sendHookFinalizeIPC = func(daemon.SessionFinalizeIPCPayload) error { return nil }
	t.Cleanup(func() { sendHookFinalizeIPC = orig })
	require.NoError(t, finalizeOrHoldOnHookStop(&HookContext{ProjectRoot: f.projectRoot}, f.state, "clear"))
	require.Equal(t, livesession.StatusClosed, f.liveState().Status, "precondition: the hook's close ended the stream")

	require.NoError(t, retirePreviousLiveStream(f.projectRoot, f.dir(), f.state.SessionID))

	assert.NoFileExists(t, filepath.Join(f.dir(), "stream-state.json"), "the folder is free for the new recording")
	owed := f.owedLater()
	require.Len(t, owed, 1, "the owed notice must survive the retire")
	assert.Equal(t, ".live-"+f.state.SessionID, filepath.Base(owed[0]))
}

func TestLiveSecretWithheldNoticeOnlyForAStreamThatWithheld(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result agentSessionResult
	}{
		{"nothing withheld", agentSessionResult{}},
		{"deleted on SageOx says not shared at all", agentSessionResult{LiveSecretWithheld: true, DeletedOnServer: true, Held: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := sessionStopOutput{Guidance: sessionStopGuidance}
			addLiveSecretWithheldNotice(&output, &tc.result)
			assert.Empty(t, output.DataWarnings)
			assert.Equal(t, sessionStopGuidance, output.Guidance)
		})
	}
}

func TestLiveCloseKeepsTheStreamResumableAcrossCachePruning(t *testing.T) {
	if testing.Short() {
		t.Skip("short: a full hook-recorded stop against the fake server")
	}
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("first task")
	f.stop()
	require.Equal(t, livesession.StatusClosed, f.liveState().Status, "precondition: the stop closed the stream")
	sessions := filepath.Dir(f.dir())
	// what PruneSessionCache does to a closed stream once push-summary publishes
	require.NoError(t, os.RemoveAll(f.dir()))

	src, err := livesession.FindReopenSource(t.Context(), liveConfig(f.projectRoot), sessions, f.state.SessionID)
	require.NoError(t, err)
	require.NotNil(t, src, "the next prompt must still resume the stopped conversation")
	assert.Contains(t, string(src.Spool), "first task")
}

func TestLiveSameFolderResumeFollowsAMovedStream(t *testing.T) {
	if testing.Short() {
		t.Skip("short: a full hook-recorded stop against the fake server")
	}
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("before the secret")
	f.openAndDrain()
	markStreamWithheld(t, f.dir())
	orig := sendHookFinalizeIPC
	sendHookFinalizeIPC = func(daemon.SessionFinalizeIPCPayload) error { return nil }
	t.Cleanup(func() { sendHookFinalizeIPC = orig })
	require.NoError(t, finalizeOrHoldOnHookStop(&HookContext{ProjectRoot: f.projectRoot}, f.state, "clear"))
	require.Equal(t, livesession.StatusClosed, f.liveState().Status, "precondition: the hook's close ended the stream")

	// a restart within the minute: same folder, new recording id
	next := &session.RecordingState{SessionID: sessionid.GenerateSessionID(), SessionPath: f.dir(), ContinuedFromSessionID: f.state.SessionID}
	src := liveReopenSource(f.projectRoot, next)
	require.NotNil(t, src)
	require.Equal(t, f.dir(), src.Dir)
	require.NoError(t, retirePreviousLiveStream(f.projectRoot, f.dir(), f.state.SessionID))
	require.Len(t, f.owedLater(), 1, "precondition: retiring moved the stream to keep its owed notice")

	src = reopenSourceAfterRetire(f.projectRoot, next, src)
	_, err := livesession.PrepareReopen(t.Context(), liveConfig(f.projectRoot), f.dir(), livesession.OpenParams{SessionID: next.SessionID}, src)
	require.NoError(t, err, "the moved stream's conversation must rebind to the new recording")
}
