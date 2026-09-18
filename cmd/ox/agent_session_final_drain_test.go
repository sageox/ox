//go:build !short

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/require"
)

// A hook may persist its cursor while stop waits on raw.jsonl. Recovery must
// use that committed cursor, even when the hook died before deleting its journal.
func TestFinalDrainReloadsCommittedHookBatch(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	stale, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	// The adapter file did not exist at recording start, so none is persisted.
	stale.WatchMode = "hook"
	stale.SessionFile = ""
	require.NoError(t, session.SaveRecordingState(projectRoot, stale))
	rawPath := filepath.Join(stale.SessionPath, "raw.jsonl")
	nextOffset := stale.SourceOffset + 100
	require.NoError(t, fileutil.WithFileLock(context.Background(), rawPath, func() error {
		writer, err := session.NewRawWriter(rawPath, projectRoot)
		if err != nil {
			return err
		}
		defer writer.Close()
		if err = writer.BeginAppend(stale.SourceOffset, nextOffset); err != nil {
			return err
		}
		if err = writer.WriteEntry(&session.Entry{Type: session.EntryTypeUser, Content: "committed hook entry"}); err != nil {
			return err
		}
		if err = writer.SealAppend(); err != nil {
			return err
		}
		return session.MutateRecordingStateFile(filepath.Join(stale.SessionPath, ".recording.json"), func(current *session.RecordingState) error {
			current.SourceOffset = nextOffset
			current.EntryCount++
			return nil
		})
	}))
	committed, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	// File discovery can happen during stop; preserve it while refreshing cursor.
	stale.SessionFile = filepath.Join(t.TempDir(), "newly-discovered.jsonl")
	require.NoError(t, fileutil.WithFileLock(context.Background(), rawPath, func() error {
		latest, err := reloadRecordingForFinalDrain(projectRoot, stale)
		if err != nil {
			return err
		}
		require.Equal(t, nextOffset, latest.SourceOffset)
		require.Equal(t, stale.EntryCount+1, latest.EntryCount)
		require.Equal(t, stale.SessionFile, latest.SessionFile)
		return session.RecoverRawAppend(rawPath, latest.SourceOffset)
	}))
	after, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	require.Equal(t, string(committed), string(after), "final drain must not roll back a committed hook batch")
}

// TestFinalDrainKeepsTheSourceFileItsCursorBelongsTo verifies a source file a
// hook rediscovered and committed during the wait is not swapped back out.
// Failure prevented: the final drain reading the wrong transcript from another
// file's byte offset, silently skipping or tearing entries.
func TestFinalDrainKeepsTheSourceFileItsCursorBelongsTo(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	stale, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotEmpty(t, stale.SessionFile)

	rediscovered := filepath.Join(t.TempDir(), "rediscovered.jsonl")
	require.NoError(t, session.UpdateRecordingStateAt(stale.SessionPath, func(current *session.RecordingState) {
		current.SessionFile = rediscovered
		current.SourceOffset = 4096
	}))

	require.NoError(t, fileutil.WithFileLock(context.Background(), filepath.Join(stale.SessionPath, "raw.jsonl"), func() error {
		latest, err := reloadRecordingForFinalDrain(projectRoot, stale)
		if err != nil {
			return err
		}
		require.Equal(t, rediscovered, latest.SessionFile)
		require.Equal(t, int64(4096), latest.SourceOffset)
		return nil
	}))
}

func TestFinalDrainRejectsRestartedRecordingAtSamePath(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	stale, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	restarted := *stale
	restarted.SessionID += "-restarted"
	require.NoError(t, session.SaveRecordingState(projectRoot, &restarted))
	require.NoError(t, fileutil.WithFileLock(context.Background(), filepath.Join(stale.SessionPath, "raw.jsonl"), func() error {
		_, err := reloadRecordingForFinalDrain(projectRoot, stale)
		require.ErrorContains(t, err, "recording changed")
		return nil
	}))
}
