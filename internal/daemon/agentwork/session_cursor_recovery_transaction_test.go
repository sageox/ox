package agentwork

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cursorRecoveryFirstTurn = `{"role":"user","content":"acknowledged prompt"}` + "\n" +
	`{"type":"turn_ended","status":"success"}` + "\n"
const cursorRecoverySecondTurn = `{"role":"assistant","content":"saved second response"}` + "\n" +
	`{"type":"turn_ended","status":"success"}` + "\n"

// Leave exactly the journal produced when capture fsyncs the batch but exits
// before its new checkpoint is durable. Source and raw content use real parsers.
func cursorRecoveryPendingBatch(t *testing.T) *cursorWatcherFixture {
	t.Helper()
	f := newCursorWatcherFixture(t, cursorRecoveryFirstTurn+cursorRecoverySecondTurn, false, true)
	t.Setenv("HOME", f.homeDir)
	f.state.SourceOffset = int64(len(cursorRecoveryFirstTurn))
	f.state.EntryCount = 1
	f.state.SourcePrefixSHA256 = watcherSHA256([]byte(cursorRecoveryFirstTurn))
	stopped := time.Now().UTC()
	f.state.StoppedAt = &stopped
	writeRecordingState(t, filepath.Join(f.cachePath, recordingMarker), f.state)
	rawPath := filepath.Join(f.cachePath, artifactRaw)
	writer, err := session.NewRawWriter(rawPath, f.projectRoot)
	require.NoError(t, err)
	t.Cleanup(func() { _ = writer.Close() })
	require.NoError(t, writer.WriteEntry(&session.Entry{Type: session.EntryTypeUser, Content: "acknowledged prompt"}))
	require.NoError(t, writer.BeginAppend(f.state.SourceOffset, int64(len(cursorRecoveryFirstTurn+cursorRecoverySecondTurn))))
	require.NoError(t, writer.WriteEntry(&session.Entry{Type: session.EntryTypeAssistant, Content: "saved second response"}))
	require.NoError(t, writer.SealAppend())
	require.NoError(t, writer.CloseAndSync())
	return f
}

func TestCursorRecoveryPreservesPendingBatchWhenSourceCannotReplay(t *testing.T) {
	installCursorWatcherAdapter(t)
	for _, missing := range []bool{true, false} {
		name := "rewritten source"
		if missing {
			name = "missing source"
		}
		t.Run(name, func(t *testing.T) {
			f := cursorRecoveryPendingBatch(t)
			rawPath := filepath.Join(f.cachePath, artifactRaw)
			recPath := filepath.Join(f.cachePath, recordingMarker)
			journalPath := rawPath + ".append.json"
			beforeRaw, err := os.ReadFile(rawPath)
			require.NoError(t, err)
			beforeJournal, err := os.ReadFile(journalPath)
			require.NoError(t, err)
			beforeState, err := os.ReadFile(recPath)
			require.NoError(t, err)
			if missing {
				require.NoError(t, os.Remove(f.sourcePath))
			} else {
				require.NoError(t, os.WriteFile(f.sourcePath, []byte(`{"role":"user","content":"foreign replacement"}`+"\n"), 0o600))
			}
			var recovered bool
			err = fileutil.WithFileLock(context.Background(), rawPath, func() error {
				var err error
				recovered, err = recoverRawFromSessionFile(slog.New(slog.DiscardHandler), recPath, f.cachePath, rawPath)
				return err
			})
			require.Error(t, err)
			assert.False(t, recovered)
			afterRaw, err := os.ReadFile(rawPath)
			require.NoError(t, err)
			assert.Equal(t, beforeRaw, afterRaw, "the unavailable source cannot replace saved messages")
			afterJournal, err := os.ReadFile(journalPath)
			require.NoError(t, err, "leave rollback evidence intact until native replay is proved")
			assert.Equal(t, beforeJournal, afterJournal)
			afterState, err := os.ReadFile(recPath)
			require.NoError(t, err)
			assert.Equal(t, beforeState, afterState)
		})
	}
}

// lockCursorRecoveryGate holds a real shared lock until release. Tests use the
// gate to place a completed hook between the detector's first read and capture.
func lockCursorRecoveryGate(t *testing.T, path string) func() {
	t.Helper()
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	unlock := sync.OnceFunc(func() { close(release) })
	go func() {
		done <- fileutil.WithFileLock(context.Background(), path, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	select {
	case <-held:
	case err := <-done:
		require.NoError(t, err)
		t.Fatal("recovery gate did not acquire lock")
	}
	t.Cleanup(func() { unlock(); require.NoError(t, <-done) })
	return unlock
}

func TestCursorRecoveryReloadsCheckpointAfterAppendLockWait(t *testing.T) {
	if testing.Short() {
		t.Skip("short: coordinates a held capture transaction")
	}
	installCursorWatcherAdapter(t)
	f := cursorRecoveryPendingBatch(t)
	rawPath := filepath.Join(f.cachePath, artifactRaw)
	recPath := filepath.Join(f.cachePath, recordingMarker)
	canonicalRaw, err := filepath.EvalSymlinks(rawPath)
	require.NoError(t, err)
	release := lockCursorRecoveryGate(t, canonicalRaw+".append")
	done := make(chan error, 1)
	go func() {
		done <- fileutil.WithFileLock(context.Background(), rawPath, func() error {
			_, err := recoverRawFromSessionFile(slog.New(slog.DiscardHandler), recPath, f.cachePath, rawPath)
			return err
		})
	}()
	t.Cleanup(func() { release() })
	select {
	case err := <-done:
		t.Fatalf("recovery escaped the held append lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	// A control committed its marker while holding append + state locks, then
	// crashed before FinishAppend. Recovery's earlier copy must not roll it back.
	require.NoError(t, session.MutateRecordingStateFile(recPath, func(state *session.RecordingState) error {
		state.SourceOffset = int64(len(cursorRecoveryFirstTurn + cursorRecoverySecondTurn))
		state.SourcePrefixSHA256 = watcherSHA256([]byte(cursorRecoveryFirstTurn + cursorRecoverySecondTurn))
		state.EntryCount = 2
		return nil
	}))
	release()
	require.NoError(t, <-done)
	assert.NoFileExists(t, rawPath+".append.json")
	stored, err := session.ReadSessionFromPath(rawPath)
	require.NoError(t, err)
	assert.Len(t, stored.Entries, 2, "both acknowledged rows must survive journal settlement")
	after, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	assert.Contains(t, string(after), "saved second response")
	assert.Contains(t, string(after), `"cursor_finalized":true`)
}

func TestCursorInactivityRecoveryRechecksActivityInsideCaptureLocks(t *testing.T) {
	if testing.Short() {
		t.Skip("short: coordinates a held capture transaction")
	}
	installCursorWatcherAdapter(t)
	for _, gate := range []string{"owner", "append"} {
		t.Run(gate, func(t *testing.T) {
			f := newCursorWatcherFixture(t, cursorRecoveryFirstTurn, false, true)
			t.Setenv("HOME", f.homeDir)
			recPath := filepath.Join(f.cachePath, recordingMarker)
			rawPath := filepath.Join(f.cachePath, artifactRaw)
			inactive := time.Now().Add(-2 * staleRecordingThreshold)
			f.state.StartedAt = inactive
			f.state.LastHookAt = &inactive
			writeRecordingState(t, recPath, f.state)
			require.NoError(t, fileutil.WithFileLock(context.Background(), rawPath, func() error {
				_, err := session.DrainCursorSource(context.Background(), f.projectRoot, f.cachePath, f.homeDir, &cursorWatcherAdapter{}, false)
				return err
			}))
			lockPath := rawPath
			if gate == "append" {
				var err error
				lockPath, err = filepath.EvalSymlinks(rawPath)
				require.NoError(t, err)
				lockPath += ".append"
			}
			release := lockCursorRecoveryGate(t, lockPath)
			handler := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))
			handler.captureLockWait = 30 * time.Second
			done := make(chan struct{})
			var items []*WorkItem
			var detectErr error
			go func() {
				defer close(done)
				items, _, detectErr = handler.detectInDir(filepath.Dir(f.cachePath), f.ledgerPath)
			}()
			t.Cleanup(func() { release(); <-done })
			select {
			case <-done:
				t.Fatal("detector escaped the held capture lock")
			case <-time.After(100 * time.Millisecond):
			}
			require.NoError(t, session.MutateRecordingStateFile(recPath, func(state *session.RecordingState) error {
				now := time.Now().UTC()
				state.LastHookAt = &now
				return nil
			}))
			before, err := os.ReadFile(rawPath)
			require.NoError(t, err)
			release()
			<-done
			require.NoError(t, detectErr)
			assert.Empty(t, items, "a fresh hook invalidates the earlier inactivity observation")
			assert.FileExists(t, recPath)
			after, err := os.ReadFile(rawPath)
			require.NoError(t, err)
			assert.Equal(t, before, after, "active recording must stay open for subsequent turns")
		})
	}
}
