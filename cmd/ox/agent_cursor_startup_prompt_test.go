package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A metadata read happens in BeforePublish, after startup has chosen its
// native boundary and before .recording.json becomes visible to prompt hooks.
type gatedCursorStartupReader struct {
	*cursorHostReader
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type callbackCursorStartupReader struct {
	*cursorHostReader
	beforePublish func() error
}

func (r *callbackCursorStartupReader) ReadMetadata(string) (*adapters.SessionMetadata, error) {
	return nil, r.beforePublish()
}

func (r *gatedCursorStartupReader) ReadMetadata(string) (*adapters.SessionMetadata, error) {
	r.once.Do(func() { close(r.entered); <-r.release })
	return nil, nil
}

func prepareCursorStartupPrompt(t *testing.T, f *cursorHostFixture, generation string) *SessionMarker {
	t.Helper()
	input, err := normalizeCursorHookInput(f.input(t, "beforeSubmitPrompt", generation).RawBytes, f.root, f.home)
	require.NoError(t, err)
	marker, err := prepareCursorHookBoundary(input, "beforeSubmitPrompt")
	require.NoError(t, err)
	return marker
}

func reserveCursorStartupIdentity(t *testing.T, f *cursorHostFixture, agentID string) {
	t.Helper()
	_, err := UpdateSessionMarker(cursorConversationID, func(marker *SessionMarker) error { marker.AgentID = agentID; return nil })
	require.NoError(t, err)
}

func TestCursorQueuedPromptsBeforeStartupKeepEveryGenerationAndFirstBoundary(t *testing.T) {
	for _, failedFirstStart := range []bool{false, true} {
		name := "queued before startup lock"
		if failedFirstStart {
			name = "retry after failed startup"
		}
		t.Run(name, func(t *testing.T) {
			f := cursorAutoRecordingFixture(t)
			old := cursorReviewTurn("OLD_BEFORE_RECORDING")
			first, second, third := cursorReviewTurn("FIRST"), cursorReviewTurn("SECOND"), cursorReviewTurn("THIRD")
			f.write(t, old)
			initial := prepareCursorStartupPrompt(t, f, "g1")
			stale := *initial.CursorSourceBoundary
			reserveCursorStartupIdentity(t, f, "Oxqueued")
			if failedFirstStart {
				path, err := ledger.DefaultPath()
				require.NoError(t, err)
				require.NoError(t, os.Remove(filepath.Join(path, ".git")))
				status := startSessionRecording(f.root, "Oxqueued", "cursor", "", "", cursorConversationID, &stale)
				require.NotNil(t, status)
				require.False(t, status.Recording)
				require.NoError(t, os.MkdirAll(filepath.Join(path, ".git"), 0o700))
			}
			// A copied prime boundary must not lose submissions that win the startup
			// lock first. Cursor can already have exported the first complete turn.
			f.write(t, old+first)
			for _, generation := range []string{"g2", "g3", "g2"} {
				prepareCursorStartupPrompt(t, f, generation)
			}
			status := startSessionRecording(f.root, "Oxqueued", "cursor", "", "", cursorConversationID, &stale)
			require.NotNil(t, status)
			require.True(t, status.Recording)
			state, err := session.LoadRecordingStateForAgent(f.root, "Oxqueued")
			require.NoError(t, err)
			require.NotNil(t, state)
			assert.Equal(t, stale.Offset, state.StartOffset)
			assert.Equal(t, stale.SourcePrefixSHA256, state.SourcePrefixSHA256)
			assert.Equal(t, []string{"g1", "g2", "g3"}, state.CursorPromptGenerations)
			assert.Equal(t, 4, state.CursorExpectedTurns, "one prior turn plus three distinct submissions")
			require.ErrorIs(t, finalizeCursorHostRecording(t, f, state), session.ErrCursorFinalDrainPending)
			// Repeated startup and prompt delivery cannot count the same turn twice.
			again := startSessionRecording(f.root, "Oxqueued", "cursor", "", "", cursorConversationID, &stale)
			require.NotNil(t, again)
			prepareCursorStartupPrompt(t, f, "g3")
			f.write(t, old+first+second)
			require.ErrorIs(t, finalizeCursorHostRecording(t, f, state), session.ErrCursorFinalDrainPending)
			f.write(t, old+first+second+third)
			require.NoError(t, finalizeCursorHostRecording(t, f, state))
			stored, err := session.ReadSessionFromPath(filepath.Join(state.SessionPath, "raw.jsonl"))
			require.NoError(t, err)
			require.Len(t, stored.Entries, 6)
			for _, entry := range stored.Entries {
				assert.NotContains(t, entry["content"], "OLD_BEFORE_RECORDING")
			}
		})
	}
}

func TestCursorPromptDuringPublicationWaitsThenJoinsRecording(t *testing.T) {
	if testing.Short() {
		t.Skip("short: coordinates a gated recording publication")
	}
	f := cursorAutoRecordingFixture(t)
	old, first, second := cursorReviewTurn("OLD"), cursorReviewTurn("FIRST"), cursorReviewTurn("SECOND")
	f.write(t, old)
	initial := prepareCursorStartupPrompt(t, f, "g1")
	stale := *initial.CursorSourceBoundary
	reserveCursorStartupIdentity(t, f, "Oxqueued")
	gate := &gatedCursorStartupReader{cursorHostReader: f.reader, entered: make(chan struct{}), release: make(chan struct{})}
	adapters.Unregister("cursor")
	adapters.Register(gate)
	release := sync.OnceFunc(func() { close(gate.release) })
	started := make(chan struct{})
	var status *sessionStatus
	go func() {
		defer close(started)
		status = startSessionRecording(f.root, "Oxqueued", "cursor", "", "", cursorConversationID, &stale)
	}()
	t.Cleanup(func() { release(); <-started })
	select {
	case <-gate.entered:
	case <-started:
		t.Fatal("startup did not reach the pre-publication gate")
	case <-time.After(5 * time.Second):
		t.Fatal("startup did not reach the pre-publication gate")
	}
	f.write(t, old+first)
	input, err := normalizeCursorHookInput(f.input(t, "beforeSubmitPrompt", "g2").RawBytes, f.root, f.home)
	require.NoError(t, err)
	admitted := make(chan error, 1)
	go func() { _, err := prepareCursorHookBoundary(input, "beforeSubmitPrompt"); admitted <- err }()
	select {
	case err := <-admitted:
		require.NoError(t, err)
		t.Error("prompt admission ran before startup published its recording")
		// Keep the channel readable for the final join on the pre-fix path.
		admitted <- nil
	case <-time.After(100 * time.Millisecond):
	}
	release()
	<-started
	require.NoError(t, <-admitted)
	require.NotNil(t, status)
	require.True(t, status.Recording)
	state, err := session.LoadRecordingStateForAgent(f.root, "Oxqueued")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, stale.Offset, state.StartOffset)
	assert.Equal(t, []string{"g1", "g2"}, state.CursorPromptGenerations)
	require.ErrorIs(t, finalizeCursorHostRecording(t, f, state), session.ErrCursorFinalDrainPending)
	f.write(t, old+first+second)
	require.NoError(t, finalizeCursorHostRecording(t, f, state))
	stored, err := session.ReadSessionFromPath(filepath.Join(state.SessionPath, "raw.jsonl"))
	require.NoError(t, err)
	require.Len(t, stored.Entries, 4)
}

func TestCursorCompletedRecordingRefreshesConsumedBoundary(t *testing.T) {
	for _, event := range []string{"beforeSubmitPrompt", "sessionStart"} {
		t.Run(event, func(t *testing.T) {
			f := cursorAutoRecordingFixture(t)
			old, first, next := cursorReviewTurn("OLD"), cursorReviewTurn("FIRST"), cursorReviewTurn("NEXT")
			f.write(t, old)
			initial := prepareCursorStartupPrompt(t, f, "g1")
			status := startSessionRecording(f.root, "Oxqueued", "cursor", "", "", cursorConversationID, initial.CursorSourceBoundary)
			require.NotNil(t, status)
			require.True(t, status.Recording)
			state, err := session.LoadRecordingStateForAgent(f.root, "Oxqueued")
			require.NoError(t, err)
			f.write(t, old+first)
			require.NoError(t, finalizeCursorHostRecording(t, f, state))
			require.NoError(t, session.ClearRecordingStateAt(state.SessionPath, state.SessionID))
			input, err := normalizeCursorHookInput(f.input(t, event, "g2").RawBytes, f.root, f.home)
			require.NoError(t, err)
			marker, err := prepareCursorHookBoundary(input, event)
			require.NoError(t, err)
			require.Equal(t, int64(len(old+first)), marker.CursorSourceBoundary.Offset)
			require.Empty(t, marker.CursorSourceBoundary.RecordingSessionID)
			require.NotContains(t, marker.CursorSourceBoundary.PromptGenerations, "g1")
			status = startSessionRecording(f.root, "Oxqueued", "cursor", "", "", cursorConversationID, marker.CursorSourceBoundary)
			require.NotNil(t, status)
			require.True(t, status.Recording)
			resumed, err := session.LoadRecordingStateForAgent(f.root, "Oxqueued")
			require.NoError(t, err)
			require.NotEqual(t, state.SessionID, resumed.SessionID)
			require.Equal(t, []string{"g2"}, resumed.CursorPromptGenerations)
			f.write(t, old+first+next)
			require.NoError(t, finalizeCursorHostRecording(t, f, resumed))
			stored, err := session.ReadSessionFromPath(filepath.Join(resumed.SessionPath, "raw.jsonl"))
			require.NoError(t, err)
			require.Len(t, stored.Entries, 2)
		})
	}
}

func TestCursorStartupAssociationSaveFailureBlocksAutomaticRestart(t *testing.T) {
	for _, retry := range []string{"no repair before completion", "startup", "active prompt"} {
		t.Run(retry, func(t *testing.T) {
			f := cursorAutoRecordingFixture(t)
			old, first := cursorReviewTurn("OLD"), cursorReviewTurn("FIRST")
			f.write(t, old)
			initial := prepareCursorStartupPrompt(t, f, "g1")
			reserveCursorStartupIdentity(t, f, "Oxqueued")
			path := markerPath(cursorConversationID)
			original, err := os.ReadFile(path)
			require.NoError(t, err)
			adapters.Unregister("cursor")
			adapters.Register(&callbackCursorStartupReader{cursorHostReader: f.reader, beforePublish: func() error {
				if err := os.Remove(path); err != nil {
					return err
				}
				return os.Mkdir(path, 0o700)
			}})
			status := startSessionRecording(f.root, "Oxqueued", "cursor", "", "", cursorConversationID, initial.CursorSourceBoundary)
			require.Nil(t, status, "association failure must not report successful startup")
			state, err := session.LoadRecordingStateForAgent(f.root, "Oxqueued")
			require.NoError(t, err)
			require.NotNil(t, state, "recording publication precedes the failed association save")
			require.True(t, session.HasExplicitStop(f.root, "Oxqueued"), "uncertain association must prevent replay after this recording is cleared")
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.WriteFile(path, original, 0o600))
			adapters.Unregister("cursor")
			adapters.Register(f.reader)
			switch retry {
			case "startup":
				status = startSessionRecording(f.root, "Oxqueued", "cursor", "", "", cursorConversationID, initial.CursorSourceBoundary)
				require.Nil(t, status, "automatic retry must preserve the recovery guard")
			case "active prompt":
				prepareCursorStartupPrompt(t, f, "g1")
			}
			current, err := session.LoadRecordingStateForAgent(f.root, "Oxqueued")
			require.NoError(t, err)
			require.Equal(t, state.SessionID, current.SessionID)
			require.Equal(t, []string{"g1"}, current.CursorPromptGenerations)
			require.Equal(t, 2, current.CursorExpectedTurns)
			marker, err := ReadSessionMarker(cursorConversationID)
			require.NoError(t, err)
			if retry == "active prompt" {
				require.Equal(t, state.SessionID, marker.CursorSourceBoundary.RecordingSessionID)
			}
			require.True(t, session.HasExplicitStop(f.root, "Oxqueued"))
			f.write(t, old+first)
			require.NoError(t, finalizeCursorHostRecording(t, f, current))
			require.NoError(t, session.ClearRecordingStateAt(current.SessionPath, current.SessionID))
			marker = prepareCursorStartupPrompt(t, f, "g2")
			require.Equal(t, int64(len(old+first)), marker.CursorSourceBoundary.Offset)
			require.Equal(t, []string{"g2"}, marker.CursorSourceBoundary.PromptGenerations)
			require.Nil(t, startSessionRecording(f.root, "Oxqueued", "cursor", "", "", cursorConversationID, marker.CursorSourceBoundary))
			after, err := session.LoadRecordingStateForAgent(f.root, "Oxqueued")
			require.NoError(t, err)
			require.Nil(t, after, "next prompt must not create a recording that replays the completed turn")
			require.True(t, session.HasExplicitStop(f.root, "Oxqueued"))
		})
	}
}

func TestCursorManualBoundaryDoesNotBecomeQueuedAfterAutoEnable(t *testing.T) {
	f := cursorAutoRecordingFixture(t)
	f.write(t, cursorReviewTurn("OLD"))
	t.Setenv("OX_SESSION_RECORDING", "manual")
	prepareCursorStartupPrompt(t, f, "g1")
	completed := cursorReviewTurn("OLD") + cursorReviewTurn("MANUAL")
	f.write(t, completed)
	t.Setenv("OX_SESSION_RECORDING", "auto")
	marker := prepareCursorStartupPrompt(t, f, "g2")
	require.Equal(t, int64(len(completed)), marker.CursorSourceBoundary.Offset)
	require.Equal(t, []string{"g2"}, marker.CursorSourceBoundary.PromptGenerations)
}

func TestCursorSessionStartGenerationKeepsQueuedPromptBoundary(t *testing.T) {
	f := cursorAutoRecordingFixture(t)
	old, first, second := cursorReviewTurn("OLD"), cursorReviewTurn("FIRST"), cursorReviewTurn("SECOND")
	f.write(t, old)
	input, err := normalizeCursorHookInput(f.input(t, "sessionStart", "g1").RawBytes, f.root, f.home)
	require.NoError(t, err)
	marker, err := prepareCursorHookBoundary(input, "sessionStart")
	require.NoError(t, err)
	stale := *marker.CursorSourceBoundary
	f.write(t, old+first)
	prepareCursorStartupPrompt(t, f, "g2")
	status := startSessionRecording(f.root, "Oxqueued", "cursor", "", "", cursorConversationID, &stale)
	require.NotNil(t, status)
	require.True(t, status.Recording)
	state, err := session.LoadRecordingStateForAgent(f.root, "Oxqueued")
	require.NoError(t, err)
	require.Equal(t, int64(len(old)), state.StartOffset)
	require.Equal(t, []string{"g1", "g2"}, state.CursorPromptGenerations)
	require.ErrorIs(t, finalizeCursorHostRecording(t, f, state), session.ErrCursorFinalDrainPending)
	f.write(t, old+first+second)
	require.NoError(t, finalizeCursorHostRecording(t, f, state))
	stored, err := session.ReadSessionFromPath(filepath.Join(state.SessionPath, "raw.jsonl"))
	require.NoError(t, err)
	require.Len(t, stored.Entries, 4)
}
