package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: startup can publish .recording.json and then report a
// directory-sync error. Its old native boundary must not survive as unconsumed
// after the daemon finishes and removes that recording.
func TestCursorStartupPublishedErrorDoesNotReplayCompletedTurns(t *testing.T) {
	if testing.Short() {
		t.Skip("short: exercises publication failure and daemon completion recovery")
	}
	for _, tc := range []struct {
		name            string
		failAssociation bool
		completed       bool
	}{
		{name: "association repaired"},
		{name: "association failure guarded", failAssociation: true},
		{name: "daemon completed before error recovery", completed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cursorAutoRecordingFixture(t)
			old, first, second := cursorReviewTurn("OLD"), cursorReviewTurn("FIRST"), cursorReviewTurn("SECOND")
			f.write(t, old)
			initial := prepareCursorStartupPrompt(t, f, "g1")
			reserveCursorStartupIdentity(t, f, "Oxqueued")
			path := markerPath(cursorConversationID)
			original, err := os.ReadFile(path)
			require.NoError(t, err)
			var published *session.RecordingState
			status := startCursorSessionRecording(f.root, "Oxqueued", "cursor", "", "", cursorConversationID,
				func(root string, state *session.RecordingState) error {
					if err := writeRawHeader(root, state); err != nil {
						return err
					}
					// Publish a valid marker, then make the following real save
					// fail encoding. This recreates a post-header save error with
					// already-visible state without a global persistence hook.
					if err := session.SaveRecordingState(root, state); err != nil {
						return err
					}
					copy := *state
					published = &copy
					if tc.completed {
						f.write(t, old+first)
						if err := finalizeCursorHostRecording(t, f, published); err != nil {
							return err
						}
						if err := session.ClearRecordingStateAt(published.SessionPath, published.SessionID); err != nil {
							return err
						}
					}
					if tc.failAssociation {
						if err := os.Remove(path); err != nil {
							return err
						}
						if err := os.Mkdir(path, 0o700); err != nil {
							return err
						}
					}
					state.StartedAt = time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
					return nil
				}, initial.CursorSourceBoundary)
			require.Nil(t, status, "publication error must remain visible as failed startup")
			state, err := session.LoadRecordingStateForAgent(f.root, "Oxqueued")
			require.NoError(t, err)
			require.NotNil(t, published)
			if tc.completed {
				require.Nil(t, state, "the daemon cleared publication before error recovery inspected it")
				assert.True(t, session.HasExplicitStop(f.root, "Oxqueued"), "missing state cannot prove publication never happened")
			} else {
				require.NotNil(t, state, "the failed startup has already published its recording")
			}
			if tc.failAssociation {
				assert.True(t, session.HasExplicitStop(f.root, "Oxqueued"), "failed repair must block automatic restart")
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.WriteFile(path, original, 0o600))
			} else if !tc.completed {
				marker, err := ReadSessionMarker(cursorConversationID)
				require.NoError(t, err)
				assert.Equal(t, state.SessionID, marker.CursorSourceBoundary.RecordingSessionID)
			}
			// No intervening startup or prompt gets an opportunity to repair the
			// association before normal capture finishes the first recording.
			if !tc.completed {
				f.write(t, old+first)
				require.NoError(t, finalizeCursorHostRecording(t, f, state))
				require.NoError(t, session.ClearRecordingStateAt(state.SessionPath, state.SessionID))
			}
			marker := prepareCursorStartupPrompt(t, f, "g2")
			assert.Equal(t, int64(len(old+first)), marker.CursorSourceBoundary.Offset, "next prompt must select a fresh boundary")
			assert.Equal(t, []string{"g2"}, marker.CursorSourceBoundary.PromptGenerations)
			status = startSessionRecording(f.root, "Oxqueued", "cursor", "", "", cursorConversationID, marker.CursorSourceBoundary)
			next, err := session.LoadRecordingStateForAgent(f.root, "Oxqueued")
			require.NoError(t, err)
			if tc.failAssociation || tc.completed {
				assert.Nil(t, status)
				assert.Nil(t, next)
				return
			}
			require.NotNil(t, status)
			require.True(t, status.Recording)
			require.NotNil(t, next)
			f.write(t, old+first+second)
			require.NoError(t, finalizeCursorHostRecording(t, f, next))
			stored, err := session.ReadSessionFromPath(filepath.Join(next.SessionPath, "raw.jsonl"))
			require.NoError(t, err)
			require.Len(t, stored.Entries, 2, "the completed first turn must not be recorded again")
			for _, entry := range stored.Entries {
				assert.NotContains(t, entry["content"], "FIRST")
				assert.NotContains(t, entry["content"], "OLD")
			}
		})
	}
}

// Failure prevented: a startup failure before publication consumes the queued
// prompt or unnecessarily disables an otherwise safe retry.
func TestCursorStartupUnpublishedErrorPreservesPendingBoundary(t *testing.T) {
	f := cursorAutoRecordingFixture(t)
	old := cursorReviewTurn("OLD")
	f.write(t, old)
	initial := prepareCursorStartupPrompt(t, f, "g1")
	status := startCursorSessionRecording(f.root, "Oxqueued", "cursor", "", "", cursorConversationID,
		func(string, *session.RecordingState) error {
			return errors.New("header write failed before publication")
		}, initial.CursorSourceBoundary)
	require.Nil(t, status)
	state, err := session.LoadRecordingStateForAgent(f.root, "Oxqueued")
	require.NoError(t, err)
	require.Nil(t, state)
	marker, err := ReadSessionMarker(cursorConversationID)
	require.NoError(t, err)
	assert.Empty(t, marker.CursorSourceBoundary.RecordingSessionID)
	assert.Equal(t, initial.CursorSourceBoundary.Offset, marker.CursorSourceBoundary.Offset)
	assert.False(t, session.HasExplicitStop(f.root, "Oxqueued"))
	status = startSessionRecording(f.root, "Oxqueued", "cursor", "", "", cursorConversationID, marker.CursorSourceBoundary)
	require.NotNil(t, status)
	require.True(t, status.Recording)
	state, err = session.LoadRecordingStateForAgent(f.root, "Oxqueued")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, []string{"g1"}, state.CursorPromptGenerations)
	assert.Equal(t, int64(len(old)), state.StartOffset)
}

// Failure prevented: a successful publication is cleared by the daemon before
// startup saves its native association, making an unconsumed boundary replay.
func TestCursorStartupCompletedPublicationKeepsKnownIdentity(t *testing.T) {
	f := cursorAutoRecordingFixture(t)
	old, first := cursorReviewTurn("OLD"), cursorReviewTurn("FIRST")
	f.write(t, old)
	initial := prepareCursorStartupPrompt(t, f, "g1")
	reserveCursorStartupIdentity(t, f, "Oxqueued")
	prepared, err := session.PrepareRecordingIdentity(f.root)
	require.NoError(t, err)
	startup := &cursorRecordingStartup{
		username: "test", ledgerExists: true, preparedIdentity: prepared, writeHeader: writeRawHeader,
	}
	// Split the production transaction at the association handoff to run the
	// daemon's independent lifecycle deterministically between its two writes.
	status := startSessionRecordingUnlocked(f.root, "Oxqueued", "cursor", "", "", cursorConversationID, startup, initial.CursorSourceBoundary)
	require.NotNil(t, status)
	require.True(t, status.Recording)
	require.NotNil(t, startup.startedState)
	f.write(t, old+first)
	require.NoError(t, finalizeCursorHostRecording(t, f, startup.startedState))
	require.NoError(t, session.ClearRecordingStateAt(startup.startedState.SessionPath, startup.startedState.SessionID))
	_, err = UpdateSessionMarker(cursorConversationID, func(marker *SessionMarker) error {
		return associateCursorStartupRecording(f.root, "Oxqueued", cursorConversationID, startup, marker)
	})
	require.NoError(t, err)
	marker, err := ReadSessionMarker(cursorConversationID)
	require.NoError(t, err)
	assert.Equal(t, startup.startedState.SessionID, marker.CursorSourceBoundary.RecordingSessionID)
	assert.False(t, session.HasExplicitStop(f.root, "Oxqueued"), "known successful publication needs no recovery guard")
	next := prepareCursorStartupPrompt(t, f, "g2")
	assert.Equal(t, int64(len(old+first)), next.CursorSourceBoundary.Offset)
	assert.Equal(t, []string{"g2"}, next.CursorSourceBoundary.PromptGenerations)

	// An existing-recording handoff has no newly published identity to rely
	// on. Its disappearance must report an error for the outer restart guard.
	err = associateCursorStartupRecording(f.root, "Oxqueued", cursorConversationID, &cursorRecordingStartup{}, marker)
	require.ErrorContains(t, err, "unavailable for source association")
}
