package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/require"
)

func TestImportResumeAfterLocalCommitBeforePush(t *testing.T) {
	// The preview sees local committed coverage, although the journal says its
	// first remote publication has not been verified yet.
	pending := &importJournal{Version: 1, UploadVerified: false}
	require.NoError(t, checkUnverifiedImportResume("already_uploaded", true, pending))
	require.Error(t, checkUnverifiedImportResume("already_uploaded", false, pending))
	require.Error(t, checkUnverifiedImportResume("already_uploaded", true, &importJournal{UploadVerified: true}))
}

func TestCodexResumeKeepsPauseWhenNativeWatcherIsBehind(t *testing.T) {
	root := pauseResumeProject(t)
	id := "Oxlagged"
	_, err := session.StartRecording(root, session.StartRecordingOptions{AgentID: id, AdapterName: "claude-code", Username: "testuser"})
	require.NoError(t, err)
	native := filepath.Join(t.TempDir(), "native.jsonl")
	require.NoError(t, os.WriteFile(native, []byte("paused native bytes\n"), 0600))
	now := time.Now().UTC()
	require.NoError(t, session.UpdateRecordingStateForAgent(root, id, func(s *session.RecordingState) {
		s.AdapterName = "codex"
		s.SessionFile = native
		s.SuspendedAt = &now
		s.SourceOffset = 0
		s.Lifecycle = []session.LifecycleEvent{{Action: session.LifecycleActionPause, At: now, Offset: 0, SourceOffsetKnown: true}}
	}))
	require.ErrorContains(t, runAgentSessionResume(&agentinstance.Instance{AgentID: id}, nil), "recording remains paused")
	after, err := session.LoadRecordingStateForAgent(root, id)
	require.NoError(t, err)
	require.NotNil(t, after.SuspendedAt)
	require.Len(t, after.Lifecycle, 1)
	// Once the ordinary paused watcher has consumed the bytes, the same guard
	// allows resume and its sequence mask can safely exclude those entries.
	after.SourceOffset = int64(len("paused native bytes\n"))
	require.NoError(t, checkCodexResumeBoundary(after))
}
