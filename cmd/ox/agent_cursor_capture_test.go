package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/agenttask"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/session/cursorpaths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cursorHostFixture struct {
	root, home, source string
	reader             *cursorHostReader
}

func newCursorHostFixture(t *testing.T) *cursorHostFixture {
	t.Helper()
	oldCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = oldCfg })
	isolateAgentDetection(t)
	isolateSessionMarkerDir(t)
	root := setupSessionTestProject(t)
	root, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	t.Chdir(root)
	t.Setenv(config.EnvProjectRoot, root)
	t.Setenv("AGENT_ENV", "cursor")
	t.Setenv("SAGEOX_AGENT_ID", "")
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(name, t.TempDir())
	}
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	require.NoError(t, config.SaveProjectConfig(root, &config.ProjectConfig{
		RepoID: "cursor-host-test", SessionRecording: "manual", SessionPublishing: "manual",
	}))
	source, err := cursorpaths.SessionPath(home, root, cursorConversationID)
	require.NoError(t, err)
	reader := &cursorHostReader{}
	previous, _ := adapters.GetAdapter("cursor")
	adapters.Unregister("cursor")
	adapters.Register(reader)
	t.Cleanup(func() {
		adapters.Unregister("cursor")
		if previous != nil {
			adapters.Register(previous)
		}
	})
	return &cursorHostFixture{root: root, home: home, source: source, reader: reader}
}

func (f *cursorHostFixture) input(t *testing.T, event, generation string) *agentx.HookInput {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"conversation_id": cursorConversationID, "session_id": cursorConversationID,
		"generation_id": generation, "hook_event_name": event,
		"workspace_roots": []string{f.root}, "transcript_path": nil,
		"opaque": map[string]any{"must_survive": true},
	})
	require.NoError(t, err)
	return &agentx.HookInput{SessionID: cursorConversationID, HookEventName: event, RawBytes: raw}
}

func (f *cursorHostFixture) write(t *testing.T, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(f.source), 0o700))
	require.NoError(t, os.WriteFile(f.source, []byte(body), 0o600))
}

func (f *cursorHostFixture) record(t *testing.T) *session.RecordingState {
	t.Helper()
	state, err := session.StartRecording(f.root, session.StartRecordingOptions{
		AgentID: "Oxcur1", AgentSessionID: cursorConversationID, AdapterName: "cursor",
		AgentType: "cursor", WorkspacePath: f.root, WatchMode: "tail",
		StartOffsetKnown: true, SourcePrefixSHA256: cursorEmptyPrefixSHA256,
	})
	require.NoError(t, err)
	require.NoError(t, writeRawHeader(f.root, state))
	require.NoError(t, WriteSessionMarker(&SessionMarker{
		AgentSessionID: cursorConversationID, AgentID: state.AgentID, PrimedAt: time.Now(),
	}))
	return state
}

const cursorHostUser = `{"role":"user","message":{"content":[{"type":"text","text":"first prompt"}]}}` + "\n"
const cursorHostAnswer = `{"role":"assistant","message":{"content":[{"type":"text","text":"final answer"}]}}` + "\n"
const cursorHostTerminal = `{"type":"turn_ended","status":"success"}` + "\n"

func TestCursorHostPromptBoundaryPrecedesPrimeAndSurvivesDelayedExport(t *testing.T) {
	f := newCursorHostFixture(t)
	var primes int
	primeHook := func(_ string, hook *HookContext) error {
		primes++
		marker, err := ReadSessionMarker(cursorConversationID)
		require.NoError(t, err)
		require.NotNil(t, marker.CursorSourceBoundary)
		assert.Zero(t, marker.CursorSourceBoundary.Offset)
		assert.False(t, marker.IsPrimed())
		assert.Contains(t, string(hook.Input.RawBytes), `"must_survive":true`)
		return WriteSessionMarker(&SessionMarker{AgentSessionID: cursorConversationID, AgentID: "Oxcur1", PrimedAt: time.Now()})
	}
	require.NoError(t, runCursorAgentHookWithPrime("beforeSubmitPrompt", f.input(t, "beforeSubmitPrompt", "g1"), f.root, primeHook))
	assert.Zero(t, primes)
	f.write(t, cursorHostUser+`{"role":"assistant"`)
	// Retrying the same generation must not resample its now-incomplete EOF.
	require.NoError(t, runCursorAgentHookWithPrime("beforeSubmitPrompt", f.input(t, "beforeSubmitPrompt", "g1"), f.root, primeHook))
	require.NoError(t, runCursorAgentHookWithPrime("sessionStart", f.input(t, "sessionStart", ""), f.root, primeHook))
	assert.Equal(t, 1, primes)
	marker, err := ReadSessionMarker(cursorConversationID)
	require.NoError(t, err)
	assert.True(t, marker.IsPrimed())
	assert.Zero(t, marker.CursorSourceBoundary.Offset)
	assert.Equal(t, "g1", marker.CursorSourceBoundary.GenerationID)
}

func TestCursorHostNondeliverableEventsPreserveContextAndConversation(t *testing.T) {
	f := newCursorHostFixture(t)
	state := f.record(t)
	f.write(t, cursorHostUser+cursorHostAnswer+cursorHostTerminal)
	_, err := agenttask.Enqueue(f.root, &agenttask.Task{Title: "CURSOR_CONTEXT_SENTINEL", TargetAgent: "cursor"})
	require.NoError(t, err)
	primeHook := func(string, *HookContext) error { t.Fatal("already primed hook invoked prime"); return nil }
	for _, event := range []string{"beforeSubmitPrompt", "afterAgentResponse", "preCompact", "stop"} {
		output := captureStdoutForPlanCLI(t, func() {
			require.NoError(t, runCursorAgentHookWithPrime(event, f.input(t, event, "g1"), f.root, primeHook))
		})
		assert.Empty(t, output, event)
		assert.Empty(t, readTaskCursor(f.root, state.AgentID).Signature, event)
	}
	current, err := session.LoadRecordingStateForAgent(f.root, state.AgentID)
	require.NoError(t, err)
	require.NotNil(t, current)
	assert.Nil(t, current.StoppedAt, "native stop and compaction are not conversation end")
	assert.Equal(t, 1, current.TurnCount, "afterAgentResponse must not count as a second turn")
	assert.Equal(t, 2, current.EntryCount)
	output := captureStdoutForPlanCLI(t, func() {
		require.NoError(t, runCursorAgentHookWithPrime("postToolUseFailure", f.input(t, "postToolUseFailure", "g1"), f.root, primeHook))
	})
	assert.Contains(t, output, "CURSOR_CONTEXT_SENTINEL")
	assert.NotEmpty(t, readTaskCursor(f.root, state.AgentID).Signature)
	stored, err := session.ReadSessionFromPath(filepath.Join(state.SessionPath, "raw.jsonl"))
	require.NoError(t, err)
	require.Len(t, stored.Entries, 2)
	for _, entry := range stored.Entries {
		assert.NotEqual(t, true, entry["is_error"], "failure hooks cannot enrich unrelated native rows")
	}
}

func TestCursorHostMissingStartupRecoversOnceOnConcurrentDeliverableHooks(t *testing.T) {
	f := newCursorHostFixture(t)
	unused := func(string, *HookContext) error { t.Fatal("prompt cannot prime"); return nil }
	require.NoError(t, runCursorAgentHookWithPrime("beforeSubmitPrompt", f.input(t, "beforeSubmitPrompt", "g1"), f.root, unused))
	var primes atomic.Int32
	primeHook := func(string, *HookContext) error {
		primes.Add(1)
		return WriteSessionMarker(&SessionMarker{AgentSessionID: cursorConversationID, AgentID: "Oxcur1", PrimedAt: time.Now()})
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, event := range []string{"postToolUse", "postToolUseFailure"} {
		input := f.input(t, event, "g1")
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- runCursorAgentHookWithPrime(input.HookEventName, input, f.root, primeHook)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), primes.Load())
}

func TestCursorHostFallbackUsesRawOwnershipWhenIPCUnavailable(t *testing.T) {
	f := newCursorHostFixture(t)
	state := f.record(t)
	f.write(t, cursorHostUser)
	locked, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- fileutil.WithFileLock(context.Background(), filepath.Join(state.SessionPath, "raw.jsonl"), func() error {
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	start := time.Now()
	err := requestCursorCapture(f.root, state.AgentID, f.home)
	var timeout *fileutil.ErrLockTimeout
	assert.ErrorAs(t, err, &timeout)
	assert.Less(t, time.Since(start), time.Second)
	assert.Zero(t, f.reader.calls.Load())
	close(release)
	require.NoError(t, <-done)
	require.NoError(t, requestCursorCapture(f.root, state.AgentID, f.home))
	require.NoError(t, requestCursorCapture(f.root, state.AgentID, f.home))
	current, err := session.LoadRecordingStateForAgent(f.root, state.AgentID)
	require.NoError(t, err)
	assert.Equal(t, 1, current.EntryCount)
	assert.Equal(t, int64(len(cursorHostUser)), current.SourceOffset)
}

func TestCursorHostFinalDrainKeepsHeaderOnlyBoundaryAndLateResponse(t *testing.T) {
	f := newCursorHostFixture(t)
	state := f.record(t)
	f.write(t, cursorHostUser)
	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
	var result *agentSessionResult
	finalize := func() error {
		return fileutil.WithFileLock(context.Background(), rawPath, func() error {
			var err error
			result, err = processAgentSession(f.root, state)
			return err
		})
	}
	require.ErrorIs(t, finalize(), session.ErrCursorFinalDrainPending)
	f.write(t, cursorHostUser+cursorHostAnswer+cursorHostTerminal)
	require.NoError(t, finalize())
	require.NotNil(t, result)
	assert.Equal(t, 2, result.EntryCount)
	assert.Contains(t, result.UploadWarning, "manual")
	stored, err := session.ReadSessionFromPath(rawPath)
	require.NoError(t, err)
	require.Len(t, stored.Entries, 2)
	assert.Equal(t, "first prompt", stored.Entries[0]["content"])
	assert.Equal(t, "final answer", stored.Entries[1]["content"])
}

func TestCursorHostImplicitControlRejectsInheritedCoworkerID(t *testing.T) {
	newCursorHostFixture(t)
	t.Setenv("SAGEOX_AGENT_ID", "Oxbad1")
	assert.Empty(t, resolveImplicitAgentID())
}

func TestCursorHostStatusDoesNotInferConversationLivenessFromPID(t *testing.T) {
	for _, pid := range []int{0, os.Getpid(), 999999999} {
		state := &session.RecordingState{AgentID: "Oxcur1", AdapterName: "cursor", ParentPID: pid}
		assert.Nil(t, agentAlivePtr(state))
		alive, status := agentLivenessFor(resolveAgentLiveness(t.TempDir(), []*session.RecordingState{state}), state.AgentID)
		assert.Nil(t, alive)
		assert.Equal(t, "unknown", status)
	}
}

func TestCursorHostCurrentStopRejectsInheritedCoworkerID(t *testing.T) {
	f := newCursorHostFixture(t)
	state := f.record(t)
	t.Setenv("SAGEOX_AGENT_ID", state.AgentID)
	resetSessionForceStopFlags(t)
	t.Cleanup(func() { resetSessionForceStopFlags(t) })
	cmd := sessionForceStopCmd
	require.NoError(t, cmd.Flags().Set("current", "true"))
	require.ErrorContains(t, runSessionForceStop(cmd, nil), "explicit AI coworker ID")
	saved, err := session.LoadRecordingStateForAgent(f.root, state.AgentID)
	require.NoError(t, err)
	require.NotNil(t, saved)
	assert.Nil(t, saved.StoppedAt)
	assert.False(t, session.HasExplicitStop(f.root, state.AgentID))
}

func TestCursorPrimeBufferCapsIoCopy(t *testing.T) {
	var output cursorPrimeBuffer
	_, err := io.Copy(&output, bytes.NewReader(make([]byte, (1<<20)+1)))
	require.Error(t, err)
	assert.LessOrEqual(t, output.data.Len(), 1<<20)
}

type cursorHostReader struct{ calls atomic.Int32 }

func (*cursorHostReader) Name() string { return "cursor" }
func (*cursorHostReader) Detect() bool { return true }
func (*cursorHostReader) FindSessionFile(adapters.SessionLookup) (string, error) {
	return "", adapters.ErrSessionNotFound
}
func (*cursorHostReader) ReadMetadata(string) (*adapters.SessionMetadata, error) { return nil, nil }
func (*cursorHostReader) Watch(context.Context, string) (<-chan adapters.RawEntry, error) {
	return nil, adapters.ErrWatchNotSupported
}
func (r *cursorHostReader) Read(path string) ([]adapters.RawEntry, error) {
	entries, _, err := r.ReadFromOffset(path, 0)
	return entries, err
}
func (r *cursorHostReader) ReadFromOffset(path string, offset int64) ([]adapters.RawEntry, int64, error) {
	r.calls.Add(1)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, offset, err
	}
	end := int64(bytes.LastIndexByte(data, '\n') + 1)
	if offset < 0 || offset > end {
		return nil, offset, errors.New("invalid test offset")
	}
	var entries []adapters.RawEntry
	for _, line := range bytes.Split(data[offset:end], []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var row struct {
			Role    string `json:"role"`
			Message struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &row); err != nil {
			return nil, offset, err
		}
		for _, block := range row.Message.Content {
			entries = append(entries, adapters.RawEntry{Role: row.Role, Content: block.Text})
		}
	}
	return entries, end, nil
}
