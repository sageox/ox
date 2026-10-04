package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type appendDuringReadClaudeAdapter struct {
	testClaudeCodeAdapter
	appendTurn func()
}

func (a *appendDuringReadClaudeAdapter) Read(path string) ([]adapters.RawEntry, error) {
	a.appendTurn()
	return a.testClaudeCodeAdapter.Read(path)
}

func TestRecoverViaNormalStopQuarantinesNewForeignTurn(t *testing.T) {
	projectRoot, agentID, sourceFile := setupHandleAfterToolTest(t)
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil {
		t.Fatal(err)
	}
	foreign := fmt.Sprintf("{\"type\":\"assistant\",\"sessionId\":\"session\",\"cwd\":%q}\n", filepath.Dir(projectRoot))
	f, err := os.OpenFile(sourceFile, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(foreign); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := recoverViaNormalStop(&agentinstance.Instance{AgentID: agentID}, projectRoot, state); err == nil {
		t.Fatal("normal recovery must not publish newly foreign turns")
	}
	persisted, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil || persisted == nil || !persisted.SourceRejected {
		t.Fatalf("normal recovery must preserve quarantine, state=%v error=%v", persisted, err)
	}
}

// Quarantine must have a way out that deletes nothing. Releasing re-runs the
// ownership check; it cannot publish a source that still crosses repositories.
func TestReleaseSourceQuarantine_RechecksOwnershipBeforeAnythingPublishes(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		name := "source that now passes"
		if foreign {
			name = "source that still crosses repositories"
		}
		t.Run(name, func(t *testing.T) {
			projectRoot, agentID, sourceFile := setupHandleAfterToolTest(t)
			t.Chdir(projectRoot)
			inst := &agentinstance.Instance{AgentID: agentID}
			if foreign {
				f, err := os.OpenFile(sourceFile, os.O_APPEND|os.O_WRONLY, 0)
				require.NoError(t, err)
				_, err = fmt.Fprintf(f, "{\"type\":\"assistant\",\"sessionId\":\"session\",\"cwd\":%q}\n", filepath.Dir(projectRoot))
				require.NoError(t, err)
				require.NoError(t, f.Close())
			}
			require.NoError(t, session.MarkSourceRejected(projectRoot, agentID))

			err := runAgentSessionRecover(inst)
			require.Error(t, err, "a quarantined recording must not recover without an explicit release")
			assert.Contains(t, err.Error(), "--release-quarantine", "the refusal must say how to get out")

			require.NoError(t, releaseSourceQuarantine(inst))
			err = runAgentSessionRecover(inst)
			current, loadErr := session.LoadRecordingStateForAgent(projectRoot, agentID)
			require.NoError(t, loadErr)
			if foreign {
				require.Error(t, err, "release must not publish a source that still crosses repositories")
				require.NotNil(t, current, "the recording must survive")
				assert.True(t, current.SourceRejected, "a still-foreign source is quarantined again")
				return
			}
			require.NoError(t, err)
			assert.Nil(t, current, "a source that passes the recheck recovers normally")
		})
	}
}

func TestRecoverPreservesUndiscoveredClaudeHookSource(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	t.Chdir(projectRoot)
	if err := session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
		s.SessionFile = ""
		s.WatchMode = "hook"
	}); err != nil {
		t.Fatal(err)
	}
	if err := runAgentSessionRecover(&agentinstance.Instance{AgentID: agentID}); err == nil {
		t.Fatal("recover must defer when the native source was never verified")
	}
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil || state == nil {
		t.Fatalf("uncertain source must retain recording marker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state.SessionPath, "raw.jsonl")); err != nil {
		t.Fatalf("uncertain source must retain cached header: %v", err)
	}
}

func TestRecoverAndProcessPreserveQuarantinedClaudeSession(t *testing.T) {
	projectRoot, agentID, sourceFile := setupHandleAfterToolTest(t)
	t.Chdir(projectRoot)
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(state.SessionPath, "raw.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := session.MarkSourceRejected(projectRoot, agentID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sourceFile); err != nil {
		t.Fatal(err)
	}
	if err := runAgentSessionRecover(&agentinstance.Instance{AgentID: agentID}); err == nil {
		t.Fatal("recover must not publish cached data after native file disappears")
	}
	state, err = session.LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil || state == nil || !state.SourceRejected {
		t.Fatalf("quarantined marker must survive recovery: state=%v error=%v", state, err)
	}
	if _, err := processAgentSession(projectRoot, state); err == nil {
		t.Fatal("internal stop must not process quarantined data")
	}
	after, err := os.ReadFile(filepath.Join(state.SessionPath, "raw.jsonl"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("recovery must leave cached prefix untouched: %v", err)
	}
}

func TestProcessAgentSession_RejectsSourceThatMovedToAnotherRepo(t *testing.T) {
	adapters.Register(&testClaudeCodeAdapter{})
	t.Cleanup(func() { adapters.Unregister("claude-code") })
	repo := t.TempDir()
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	const id = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	path := filepath.Join(t.TempDir(), id+".jsonl")
	data := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, repo)
	data += fmt.Sprintf("{\"type\":\"assistant\",\"sessionId\":%q,\"cwd\":%q}\n", id, filepath.Dir(repo))
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	state := &session.RecordingState{AdapterName: "claude-code", AgentSessionID: id, WorkspacePath: repo, SessionFile: path, SessionPath: t.TempDir()}
	if _, err := processAgentSession(repo, state); err == nil || !strings.Contains(err.Error(), "no longer belongs") {
		t.Fatalf("final drain accepted a Claude session after it left the repo: %v", err)
	}
}

func TestProcessAgentSession_RejectsTurnAppendedDuringRead(t *testing.T) {
	repo := t.TempDir()
	const id = "77b16b24-5b7d-4598-aacf-4c9afeb4b5ca"
	path := filepath.Join(t.TempDir(), id+".jsonl")
	first := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, repo)
	if err := os.WriteFile(path, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &appendDuringReadClaudeAdapter{appendTurn: func() {
		foreign := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, filepath.Dir(repo))
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(foreign); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}}
	adapters.Register(adapter)
	t.Cleanup(func() { adapters.Unregister("claude-code") })
	state := &session.RecordingState{
		AdapterName: "claude-code", AgentSessionID: id, WorkspacePath: repo,
		SessionFile: path, SessionPath: t.TempDir(),
	}
	if _, err := processAgentSession(repo, state); err == nil || !strings.Contains(err.Error(), "no longer belongs") {
		t.Fatalf("foreign turn appended during Read must block capture, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(state.SessionPath, "raw.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("read must not produce raw data before validating, stat error: %v", err)
	}
}

func TestHandleAfterTool_MissingClaudeSourceKeepsRecordingRetryable(t *testing.T) {
	repo, agentID, source := setupHandleAfterToolTest(t)
	state, err := session.LoadRecordingStateForAgent(repo, agentID)
	if err != nil || state == nil {
		t.Fatalf("load recording state: %v", err)
	}
	raw := filepath.Join(state.SessionPath, "raw.jsonl")
	before, err := os.ReadFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := handleAfterTool(&HookContext{Phase: phaseAfterTool, AgentType: "claude-code", ProjectRoot: repo, Marker: &SessionMarker{AgentID: agentID}}); err != nil {
		t.Fatal(err)
	}
	after, err := session.LoadRecordingStateForAgent(repo, agentID)
	if err != nil || after == nil || after.SourceRejected || after.SourceOffset != state.SourceOffset || after.SessionFile != source {
		t.Fatalf("missing native source must retain a retryable marker, state=%+v, error=%v", after, err)
	}
	contents, err := os.ReadFile(raw)
	if err != nil || string(contents) != string(before) {
		t.Fatalf("missing source must not change cached capture: %v", err)
	}
}

// raw.jsonl was ownership-checked batch by batch as the hooks appended to it, so
// a stop or recover that finds the native transcript gone (pruned by Claude,
// workspace archived) must finalize that capture instead of failing forever.
func TestFinalizeIncrementalSession_FinalizesCaptureWhenNativeSourceIsGone(t *testing.T) {
	repo, agentID, source := setupHandleAfterToolTest(t)
	state, err := session.LoadRecordingStateForAgent(repo, agentID)
	require.NoError(t, err)
	first := state.StartedAt.Add(time.Second)
	appendClaudeEntries(t, source, first,
		`{"type":"user","timestamp":"`+first.Format(time.RFC3339Nano)+`","message":{"role":"user","content":"captured before the transcript vanished"}}`,
	)
	require.NoError(t, handleAfterTool(&HookContext{Phase: phaseAfterTool, AgentType: "claude-code", ProjectRoot: repo, Marker: &SessionMarker{AgentID: agentID}}))
	require.NoError(t, os.Remove(source))

	state, err = session.LoadRecordingStateForAgent(repo, agentID)
	require.NoError(t, err)
	raw := filepath.Join(state.SessionPath, "raw.jsonl")
	adapter, err := adapters.GetAdapter(state.AdapterName)
	require.NoError(t, err)
	result, err := finalizeIncrementalSession(repo, state, raw, adapter, &agentSessionResult{})
	require.NoError(t, err, "a missing native source must not strand a validated capture")
	assert.Equal(t, 1, result.EntryCount)
	contents, err := os.ReadFile(raw)
	require.NoError(t, err)
	assert.Contains(t, string(contents), "captured before the transcript vanished")
}

// The recover prompt can sit for minutes with the capture lock released. A
// watcher or hook that quarantines the source in that window must still stop
// the publish, which reads the recording again under the lock.
func TestRecoverFromCache_HonorsQuarantineSetAfterTheInitialCheck(t *testing.T) {
	repo, agentID, source := setupHandleAfterToolTest(t)
	loaded, err := session.LoadRecordingStateForAgent(repo, agentID)
	require.NoError(t, err)
	first := loaded.StartedAt.Add(time.Second)
	appendClaudeEntries(t, source, first,
		`{"type":"user","timestamp":"`+first.Format(time.RFC3339Nano)+`","message":{"role":"user","content":"captured prefix"}}`,
	)
	require.NoError(t, handleAfterTool(&HookContext{Phase: phaseAfterTool, AgentType: "claude-code", ProjectRoot: repo, Marker: &SessionMarker{AgentID: agentID}}))

	stale, err := session.LoadRecordingStateForAgent(repo, agentID)
	require.NoError(t, err)
	require.False(t, stale.SourceRejected)
	raw := filepath.Join(stale.SessionPath, "raw.jsonl")
	before, err := os.ReadFile(raw)
	require.NoError(t, err)

	require.NoError(t, session.MarkSourceRejected(repo, agentID))

	err = recoverFromCache(&agentinstance.Instance{AgentID: agentID}, repo, stale, raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "untrusted repository ownership")

	after, err := os.ReadFile(raw)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "a quarantined cache must be left exactly as it was")
	current, err := session.LoadRecordingStateForAgent(repo, agentID)
	require.NoError(t, err)
	require.NotNil(t, current, "the quarantined recording marker must survive")
	assert.True(t, current.SourceRejected)
}

func TestRecoverFromCache_RefusesQuarantinedSource(t *testing.T) {
	raw := filepath.Join(t.TempDir(), "raw.jsonl")
	original := []byte("unpublished raw capture")
	if err := os.WriteFile(raw, original, 0o600); err != nil {
		t.Fatal(err)
	}
	state := &session.RecordingState{AgentID: "OxQuarantined", SourceRejected: true}
	if err := recoverFromCache(&agentinstance.Instance{AgentID: state.AgentID}, t.TempDir(), state, raw); err == nil || !strings.Contains(err.Error(), "untrusted repository ownership") {
		t.Fatalf("a quarantined native source must never upload its cached prefix: %v", err)
	}
	contents, err := os.ReadFile(raw)
	if err != nil || string(contents) != string(original) {
		t.Fatalf("quarantined cache must remain intact: %v", err)
	}
}
