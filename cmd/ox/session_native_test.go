package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Customer promise under test (tests/acceptance lens): a recording is
// joinable and self-describing — it names every native coding-agent session
// id it observed, every tool call and its result share the agent's own call
// id, and it says when it stopped. Those three fields are what lets an
// artifact labeled with the agent's session id (a native transcript, a
// trace) be matched to the recording after the fact.
//
// Red-first (each verified while authoring):
//   - drop `NativeSessions(state.NativeSessions)` from the stop builder →
//     TestSessionStop_NativeSessionsAndCallIDsReachBareRemote fails on the
//     remote meta.json assertion;
//   - drop `CallID: raw.CallID` from ConvertRawEntries → the same test fails
//     on the uploaded raw.jsonl call_id pair;
//   - drop stampRecordingHeaderAtStop from handleEnd → the SessionEnd case of
//     TestSessionEndAndClear_HandHeaderToDaemonFinalize fails.

// nativeHookFixture is a project with auto session recording, a real ledger
// clone of a real bare remote, and the Claude Code test adapter registered —
// the environment the SessionStart hook runs in.
type nativeHookFixture struct {
	*draftLedgerFixture
	agentID string
}

func newNativeHookFixture(t *testing.T) *nativeHookFixture {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("SAGEOX_DAEMON", "false")
	f := newDraftLedgerFixture(t)
	t.Chdir(f.projectRoot)
	defaultLedger, err := ledger.DefaultPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(defaultLedger), 0o755))
	runGit(t, f.projectRoot, "clone", "--quiet", f.barePath, defaultLedger)
	oldCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = oldCfg })
	oxConfigSetRepo(t, "session_recording", "auto")
	adapters.Register(&testClaudeCodeAdapter{})
	t.Cleanup(func() { adapters.Unregister("claude-code") })
	return &nativeHookFixture{draftLedgerFixture: f, agentID: "OxNative"}
}

// sessionStart fires the SessionStart hook the way Claude Code does: the
// agent's session id and the start reason arrive on stdin.
func (f *nativeHookFixture) sessionStart(t *testing.T, nativeID, source string) *session.RecordingState {
	t.Helper()
	startSessionRecordingIfConfigured(&HookContext{
		Phase:       phaseStart,
		AgentType:   "claude-code",
		ProjectRoot: f.projectRoot,
		Input:       &agentx.HookInput{SessionID: nativeID, Source: source},
		Marker:      &SessionMarker{AgentID: f.agentID},
	})
	state, err := session.LoadRecordingStateForAgent(f.projectRoot, f.agentID)
	require.NoError(t, err)
	require.NotNil(t, state, "auto recording must have started")
	return state
}

// TestHookStart_ListsEveryNativeSessionIdTheRecordingSpans: Devon starts
// Claude Code, runs /clear, and keeps working in the same recording; the
// live recording lists both of Claude Code's session ids with why each
// appeared. A /compact re-reports the current id and must not duplicate it.
func TestHookStart_ListsEveryNativeSessionIdTheRecordingSpans(t *testing.T) {
	f := newNativeHookFixture(t)
	const first, second = "0f1a2b3c-0000-4000-8000-000000000001", "0f1a2b3c-0000-4000-8000-000000000002"

	state := f.sessionStart(t, first, "startup")
	require.Len(t, state.NativeSessions, 1)
	assert.Equal(t, first, state.NativeSessions[0].ID)
	assert.Equal(t, "startup", state.NativeSessions[0].Source)
	assert.False(t, state.NativeSessions[0].FirstSeen.IsZero())
	assert.Equal(t, first, state.AgentSessionID)
	sessionPath := state.SessionPath

	// /clear: Claude Code starts a new native session; the recording carries on
	state = f.sessionStart(t, second, "clear")
	assert.Equal(t, sessionPath, state.SessionPath, "the same recording must span the /clear")
	require.Len(t, state.NativeSessions, 2, "both native ids must be listed")
	assert.Equal(t, first, state.NativeSessions[0].ID)
	assert.Equal(t, second, state.NativeSessions[1].ID)
	assert.Equal(t, "clear", state.NativeSessions[1].Source)
	assert.Equal(t, second, state.AgentSessionID, "adapter lookups must follow the agent to its current session")
	lastSeen := state.NativeSessions[1].LastSeen

	// /compact re-reports the current id
	state = f.sessionStart(t, second, "compact")
	require.Len(t, state.NativeSessions, 2, "a repeat sighting must not add an entry")
	assert.Equal(t, "clear", state.NativeSessions[1].Source, "the first reason for an id is kept")
	assert.False(t, state.NativeSessions[1].LastSeen.Before(lastSeen), "a repeat sighting advances last_seen")
}

// TestHookStart_AgentWithoutNativeIdRecordsEmptyList: an agent that reports no
// session id still records; the list is simply empty.
func TestHookStart_AgentWithoutNativeIdRecordsEmptyList(t *testing.T) {
	f := newNativeHookFixture(t)
	state := f.sessionStart(t, "", "")
	assert.Empty(t, state.NativeSessions)
	assert.Empty(t, state.AgentSessionID)
}

// TestSessionStop_NativeSessionsAndCallIDsReachBareRemote is the end-to-end
// proof for PR 1: Devon starts Claude Code, runs /clear, makes one tool call,
// and stops. What lands on the shared ledger — not the worktree — lists both
// native session ids with their reasons, says when the recording stopped,
// and carries the same call id on the tool call and on its result.
func TestSessionStop_NativeSessionsAndCallIDsReachBareRemote(t *testing.T) {
	f := newNativeHookFixture(t)
	// manual publishing keeps the stop from uploading on its own; the upload
	// below drives the real commit + push path against the bare remote with
	// only the LFS transport stubbed, the seam every ledger upload test uses.
	require.NoError(t, os.WriteFile(filepath.Join(f.projectRoot, ".sageox", "config.json"),
		[]byte(`{"config_version":"2","repo_id":"repo_draft_test","session_publishing":"manual"}`), 0o644))
	const first, second = "0f1a2b3c-0000-4000-8000-00000000000a", "0f1a2b3c-0000-4000-8000-00000000000b"
	const callID = "toolu_01E2EpairXYZ"

	// Given: a recording that spans a /clear ...
	f.sessionStart(t, first, "startup")
	f.sessionStart(t, second, "clear")

	// ... with one tool call and its result captured from Claude Code's transcript.
	sourceFile := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(sourceFile, nil, 0o644))
	require.NoError(t, session.UpdateRecordingStateForAgent(f.projectRoot, f.agentID, func(s *session.RecordingState) {
		s.SessionFile = sourceFile
	}))
	at := time.Now().Add(time.Second)
	appendLines(t, sourceFile,
		`{"type":"user","timestamp":"`+at.Format(time.RFC3339Nano)+`","message":{"role":"user","content":"Run the tests and tell me if they pass"}}`,
		`{"type":"assistant","timestamp":"`+at.Add(time.Second).Format(time.RFC3339Nano)+`","message":{"role":"assistant","content":[{"type":"text","text":"Running them now."},{"type":"tool_use","id":"`+callID+`","name":"Bash","input":{"command":"go test ./..."}}]}}`,
		`{"type":"user","timestamp":"`+at.Add(2*time.Second).Format(time.RFC3339Nano)+`","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"`+callID+`","content":"ok"}]}}`,
	)
	require.NoError(t, handleAfterTool(&HookContext{
		Phase: phaseAfterTool, AgentType: "claude-code", ProjectRoot: f.projectRoot,
		Marker: &SessionMarker{AgentID: f.agentID},
	}))

	// When: Devon stops the recording and it is uploaded.
	state, err := session.LoadRecordingStateForAgent(f.projectRoot, f.agentID)
	require.NoError(t, err)
	require.NotNil(t, state)
	stopRequested := time.Now().UTC()
	state.StoppedAt = &stopRequested // as runAgentSessionStop does before processing
	result, err := processAgentSession(f.projectRoot, state)
	require.NoError(t, err)
	require.Equal(t, 4, result.EntryCount, "prompt, reply, the tool call and its result")

	sessionName := session.GetSessionName(state.SessionPath)
	var calls []string
	var uploadedRaw []byte
	effects := scriptedSessionUploadEffects(&calls, nil, "")
	effects.uploadLFS = func(_, sessionDir string) (map[string]lfs.FileRef, error) {
		var readErr error
		uploadedRaw, readErr = os.ReadFile(filepath.Join(sessionDir, ledgerFileRaw))
		require.NoError(t, readErr)
		return map[string]lfs.FileRef{ledgerFileRaw: lfs.NewFileRef(uploadedRaw)}, nil
	}
	effects.commitInitial = commitAndPushLedger
	effects.commitRetry = commitAndPushLedgerWithExtras
	require.NoError(t, uploadSessionToLedgerWithEffects(f.projectRoot, result, state, f.ledgerPath, sessionName, effects))

	// Then: the meta.json on the bare remote lists both ids and the stop time.
	remoteDir := "sessions/" + sessionName
	require.Contains(t, remoteTree(t, f.barePath), remoteDir+"/meta.json")
	var meta lfs.SessionMeta
	require.NoError(t, json.Unmarshal([]byte(runGit(t, f.barePath, "show", "HEAD:"+remoteDir+"/meta.json")), &meta))
	require.Len(t, meta.NativeSessions, 2, "meta.json must list every native session id the recording spanned")
	assert.Equal(t, first, meta.NativeSessions[0].ID)
	assert.Equal(t, "startup", meta.NativeSessions[0].Source)
	assert.Equal(t, second, meta.NativeSessions[1].ID)
	assert.Equal(t, "clear", meta.NativeSessions[1].Source)
	require.NotNil(t, meta.StoppedAt, "meta.json must say when the recording stopped")
	assert.True(t, meta.StoppedAt.After(meta.CreatedAt), "stopped_at=%s must be later than created_at=%s", meta.StoppedAt, meta.CreatedAt)
	assert.True(t, meta.StoppedAt.Equal(stopRequested), "the requested stop time wins: got %s want %s", meta.StoppedAt, stopRequested)

	// And: the uploaded raw.jsonl is self-describing on its own.
	lines := parseJSONLBytes(t, uploadedRaw)
	require.NotEmpty(t, lines)
	header, ok := lines[0]["metadata"].(map[string]any)
	require.True(t, ok, "first line must be the header: %v", lines[0])
	headerNative, _ := header["native_sessions"].([]any)
	assert.Len(t, headerNative, 2, "the raw.jsonl header carries the native ids too")
	assert.NotEmpty(t, header["stopped_at"], "the raw.jsonl header carries the stop time too")

	var paired []map[string]any
	for _, line := range lines {
		if line["call_id"] == callID {
			paired = append(paired, line)
		}
	}
	require.Len(t, paired, 2, "the tool call and its result must both carry the agent's call id; lines=%v", lines)
	assert.Equal(t, "Bash", paired[0]["tool_name"], "the call entry names the tool")
	assert.Equal(t, "ok", paired[1]["tool_output"], "the result entry carries the output")
}

// TestSessionEndAndClear_HandHeaderToDaemonFinalize: when Claude Code exits
// (SessionEnd) or Devon runs /clear with the hook installed, the recording is
// finalized later by the daemon, after the recording-state file is gone. The
// hook must leave the native ids and the stop time in the raw.jsonl header,
// which is the only carrier the daemon can still read.
func TestSessionEndAndClear_HandHeaderToDaemonFinalize(t *testing.T) {
	for _, door := range []struct {
		name string
		run  func(t *testing.T, ctx *HookContext, agentID string)
	}{
		{name: "SessionEnd", run: func(t *testing.T, ctx *HookContext, _ string) { require.NoError(t, handleEnd(ctx)) }},
		{name: "clear", run: func(_ *testing.T, ctx *HookContext, agentID string) { stopSessionForClear(ctx, agentID) }},
	} {
		t.Run(door.name, func(t *testing.T) {
			projectRoot, _ := setupTestProject(t)
			const agentID = "OxHandoff"
			state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
				AgentID: agentID, AdapterName: "claude-code", Username: "testuser",
				AgentSessionID: "cc-handoff-1", AgentSessionSource: "startup",
			})
			require.NoError(t, err)
			require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
				s.RecordNativeSession("cc-handoff-2", "clear", time.Now().UTC())
			}))
			require.NoError(t, writeRawHeader(projectRoot, state))
			rawPath := filepath.Join(state.SessionPath, "raw.jsonl")

			before := time.Now().Add(-time.Second)
			door.run(t, &HookContext{
				Phase: phaseEnd, AgentType: "claude-code", ProjectRoot: projectRoot,
				Marker: &SessionMarker{AgentID: agentID},
			}, agentID)

			cleared, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
			require.NoError(t, err)
			require.Nil(t, cleared, "precondition: the door clears the state file, so the header is all the daemon has")

			stored, err := session.ReadSessionFromPath(rawPath)
			require.NoError(t, err)
			require.NotNil(t, stored.Meta)
			require.Len(t, stored.Meta.NativeSessions, 2, "header must carry every native id")
			assert.Equal(t, "cc-handoff-1", stored.Meta.NativeSessions[0].ID)
			assert.Equal(t, "cc-handoff-2", stored.Meta.NativeSessions[1].ID)
			assert.Equal(t, "clear", stored.Meta.NativeSessions[1].Source)
			require.NotNil(t, stored.Meta.StoppedAt, "header must carry the stop time")
			assert.True(t, stored.Meta.StoppedAt.After(before))
			assert.True(t, stored.Meta.StoppedAt.After(stored.Meta.CreatedAt))
		})
	}
}

// TestUploadSessionToLedger_ExplicitStopDoorWritesNativeFields covers the
// explicit `ox agent <id> session stop` door in isolation: what is in the
// recording state reaches meta.json, and stopped_at is set even when no
// stop time was requested.
func TestUploadSessionToLedger_ExplicitStopDoorWritesNativeFields(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requested *time.Time
	}{
		{name: "requested stop time wins", requested: func() *time.Time {
			at := time.Date(2026, 9, 1, 4, 0, 0, 0, time.UTC)
			return &at
		}()},
		{name: "no requested stop time still yields one", requested: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSessionUploadFixture(t)
			fixture.state.StoppedAt = tc.requested
			fixture.state.NativeSessions = []session.NativeSession{{
				ID: "cc-explicit-1", Source: "startup",
				FirstSeen: fixture.state.StartedAt, LastSeen: fixture.state.StartedAt,
			}}
			var calls []string
			require.NoError(t, uploadSessionToLedgerWithEffects(
				fixture.projectRoot, fixture.result, fixture.state,
				fixture.ledgerPath, fixture.sessionName, scriptedSessionUploadEffects(&calls, fixture.refs, ""),
			))

			meta, err := lfs.ReadSessionMeta(filepath.Join(fixture.ledgerPath, "sessions", fixture.sessionName))
			require.NoError(t, err)
			require.Len(t, meta.NativeSessions, 1)
			assert.Equal(t, "cc-explicit-1", meta.NativeSessions[0].ID)
			assert.Equal(t, "startup", meta.NativeSessions[0].Source)
			require.NotNil(t, meta.StoppedAt)
			assert.False(t, meta.StoppedAt.Before(meta.CreatedAt), "stopped_at=%s must not precede created_at=%s", meta.StoppedAt, meta.CreatedAt)
			if tc.requested != nil {
				assert.True(t, meta.StoppedAt.Equal(*tc.requested))
			}
		})
	}
}

// TestRecoverFromCache_WritesNativeFieldsToBareRemote covers the recover door
// end to end: Devon's agent died mid-recording, `ox agent <id> session
// recover` uploads the orphaned cache to the ledger, and the meta.json that
// reaches the remote lists the native id and a stop time — the last thing the
// recording captured, since nobody asked for the stop.
func TestRecoverFromCache_WritesNativeFieldsToBareRemote(t *testing.T) {
	f := newDraftLedgerFixture(t)
	t.Chdir(f.projectRoot)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
	t.Setenv("SAGEOX_DAEMON", "false")
	priorDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	t.Cleanup(func() { gitserver.TestSetConfigDirOverride(priorDir) })
	priorStorage := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() { gitserver.TestSetForceFileStorage(priorStorage) })
	oldCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = oldCfg })
	cli.SetNoInteractive(true)
	t.Cleanup(func() { cli.SetNoInteractive(false) })

	// a fake LFS store: batch hands out upload + verify actions on itself
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/info/lfs/objects/batch"):
			var request struct {
				Objects []lfs.BatchObject `json:"objects"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			response := lfs.BatchResponse{Transfer: "basic"}
			for _, obj := range request.Objects {
				response.Objects = append(response.Objects, lfs.BatchResponseObject{
					OID: obj.OID, Size: obj.Size,
					Actions: &lfs.Actions{
						Upload: &lfs.Action{Href: serverURL + "/upload/" + obj.OID},
						Verify: &lfs.Action{Href: serverURL + "/verify"},
					},
				})
			}
			w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
			_ = json.NewEncoder(w).Encode(response)
		case r.Method == http.MethodPut, r.URL.Path == "/verify":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	serverURL = server.URL
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(server.URL, gitserver.GitCredentials{
		Username: "testuser", Token: "test-token", ServerURL: server.URL, ExpiresAt: time.Now().Add(time.Hour),
	}))
	// LFS batch URL derives from the fetch remote; the push still lands on the bare repo
	runGit(t, f.ledgerPath, "remote", "set-url", "origin", server.URL+"/ledger.git")
	runGit(t, f.ledgerPath, "remote", "set-url", "--push", "origin", f.barePath)

	// Given: an orphaned recording whose owner never asked for a stop.
	const agentID = "OxRecov"
	state, err := session.StartRecording(f.projectRoot, session.StartRecordingOptions{
		AgentID: agentID, AdapterName: "claude-code", Username: "testuser",
		AgentSessionID: "cc-recover-1", AgentSessionSource: "startup",
	})
	require.NoError(t, err)
	require.NoError(t, writeRawHeader(f.projectRoot, state))
	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
	lastEntryAt := state.StartedAt.Add(2 * time.Minute).UTC().Truncate(time.Second)
	rawFile, err := os.OpenFile(rawPath, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	for _, line := range []string{
		`{"type":"user","content":"where were we","timestamp":"` + lastEntryAt.Add(-time.Minute).Format(time.RFC3339Nano) + `"}`,
		`{"type":"assistant","content":"finishing the refactor","timestamp":"` + lastEntryAt.Format(time.RFC3339Nano) + `"}`,
	} {
		_, err := rawFile.WriteString(line + "\n")
		require.NoError(t, err)
	}
	require.NoError(t, rawFile.Close())

	// When: the orphan is recovered.
	inst := &agentinstance.Instance{AgentID: agentID, AgentType: "claude-code"}
	require.NoError(t, recoverFromCache(inst, f.projectRoot, state, rawPath))

	// Then: the recovered meta.json on the remote carries both fields.
	var metaPath string
	for _, p := range remoteTree(t, f.barePath) {
		if strings.HasPrefix(p, "sessions/") && strings.HasSuffix(p, "-"+agentID+"/meta.json") {
			metaPath = p
		}
	}
	require.NotEmpty(t, metaPath, "the recovered session must reach the remote; tree=%v", remoteTree(t, f.barePath))
	var meta lfs.SessionMeta
	require.NoError(t, json.Unmarshal([]byte(runGit(t, f.barePath, "show", "HEAD:"+metaPath)), &meta))
	assert.Equal(t, session.StopReasonRecovered, meta.StopReason)
	require.Len(t, meta.NativeSessions, 1)
	assert.Equal(t, "cc-recover-1", meta.NativeSessions[0].ID)
	assert.Equal(t, "startup", meta.NativeSessions[0].Source)
	require.NotNil(t, meta.StoppedAt)
	assert.True(t, meta.StoppedAt.After(meta.CreatedAt), "stopped_at=%s must be later than created_at=%s", meta.StoppedAt, meta.CreatedAt)
	assert.True(t, meta.StoppedAt.Equal(lastEntryAt), "with no requested stop, the last captured entry is the stop time: got %s want %s", meta.StoppedAt, lastEntryAt)
}

// TestDoctor_SessionNativeIds: the report-only check fires on a finalized
// recording written without native ids by an agent that exposes one, and
// stays silent for recordings that carry them, for recordings older than the
// field, and for agents that expose no id.
func TestDoctor_SessionNativeIds(t *testing.T) {
	projectRoot, ledgerPath := draftReaperFixture(t)
	t.Chdir(projectRoot)
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	stopped := time.Now().UTC()
	write := func(name, agentType string, stoppedAt *time.Time, native []lfs.NativeSession) {
		t.Helper()
		dir := filepath.Join(sessionsDir, name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
			Version: "1.0", SessionName: name, SessionID: sessionScopedID(name),
			AgentID: "Ox1234", AgentType: agentType, CreatedAt: stopped.Add(-time.Hour),
			StoppedAt: stoppedAt, NativeSessions: native,
		}))
	}
	sighting := []lfs.NativeSession{{ID: "cc-1", Source: "startup", FirstSeen: stopped.Add(-time.Hour), LastSeen: stopped.Add(-time.Hour)}}

	t.Run("nothing to check", func(t *testing.T) {
		require.NoError(t, os.MkdirAll(sessionsDir, 0o755))
		got := checkSessionNativeSessions()
		assert.True(t, got.skipped, "%+v", got)
	})

	write("2026-09-01T00-00-user-legacy", "claude-code", nil, nil) // predates the field
	write("2026-09-02T00-00-user-noid", "gemini", &stopped, nil)   // agent exposes no id
	write("2026-09-03T00-00-user-good", "claude-code", &stopped, sighting)

	t.Run("recordings that carry their ids pass", func(t *testing.T) {
		checked, missing, err := scanSessionNativeSessions(sessionsDir)
		require.NoError(t, err)
		assert.Equal(t, 1, checked, "only the finalized claude-code recording is expected to carry ids")
		assert.Empty(t, missing)
		got := checkSessionNativeSessions()
		assert.True(t, got.passed, "%+v", got)
	})

	write("2026-09-04T00-00-user-broken", "claude-code", &stopped, nil) // the broken hook path
	write("2026-09-05T00-00-user-codex", "codex", &stopped, nil)        // also expected to carry one

	t.Run("a recording without ids is reported", func(t *testing.T) {
		checked, missing, err := scanSessionNativeSessions(sessionsDir)
		require.NoError(t, err)
		assert.Equal(t, 3, checked)
		assert.Equal(t, []string{"2026-09-04T00-00-user-broken", "2026-09-05T00-00-user-codex"}, missing)
		got := checkSessionNativeSessions()
		assert.True(t, got.warning, "%+v", got)
		assert.Contains(t, got.message, "2/3")
		assert.Contains(t, got.message, "2026-09-04T00-00-user-broken")
		assert.NotContains(t, got.message, "legacy")
		assert.NotContains(t, got.message, "noid")
		assert.Contains(t, got.detail, "ox hooks list", "the fix must point at the SessionStart path")
		assert.Equal(t, FixLevelCheckOnly, doctorCheckFixLevel(t, CheckSlugSessionNativeSessions), "the ids cannot be recovered after the fact, so nothing is auto-fixed")
	})
}

// doctorCheckFixLevel returns the registered FixLevel for a doctor check slug.
func doctorCheckFixLevel(t *testing.T, slug string) FixLevel {
	t.Helper()
	check := GetDoctorCheck(slug)
	require.NotNil(t, check, "doctor check %q is not registered", slug)
	return check.FixLevel
}

// appendLines appends each line to path, newline-terminated. Local rather
// than the !short-tagged hook helper so this file builds in the fast tier.
func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	require.NoError(t, err)
	defer f.Close()
	for _, line := range lines {
		_, err := f.WriteString(line + "\n")
		require.NoError(t, err)
	}
}

// parseJSONLBytes decodes every non-empty line of data as a JSON object.
func parseJSONLBytes(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m), "invalid JSON line: %s", line)
		lines = append(lines, m)
	}
	return lines
}
