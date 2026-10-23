package agentwork

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorWatcherInactivityReleasesOwnershipForFinalization(t *testing.T) {
	installCursorWatcherAdapter(t)
	source := `{"role":"user","content":"idle chat prompt"}` + "\n" +
		`{"role":"assistant","content":"idle chat answer"}` + "\n" +
		`{"type":"turn_ended","status":"success"}` + "\n"
	f := newCursorWatcherFixture(t, source, false, true)
	t.Setenv("HOME", f.homeDir)
	recPath := filepath.Join(f.cachePath, recordingMarker)
	f.state.ParentPID = 0 // Cursor has no unique process whose exit can end capture.
	f.state.StartedAt = time.Now().Add(-2 * staleRecordingThreshold)
	now := time.Now().UTC()
	f.state.LastHookAt = &now
	writeRecordingState(t, recPath, f.state)
	mgr := newCursorWatcherManager(t, f.homeDir)
	startCursorWatch(t, mgr, f)
	require.Eventually(t, func() bool {
		return loadCursorWatcherState(t, f).EntryCount == 2
	}, 5*time.Second, 10*time.Millisecond)
	mgr.Cleanup()
	require.Equal(t, []string{f.sessionName}, mgr.ActiveSessions(), "a fresh hook keeps an old conversation active")

	rawPath := filepath.Join(f.cachePath, artifactRaw)
	err := fileutil.WithFileLockTimeout(context.Background(), rawPath, 20*time.Millisecond, func() error { return nil })
	require.Error(t, err, "the live watcher owns raw.jsonl")
	require.NoError(t, session.MutateRecordingStateFile(recPath, func(state *session.RecordingState) error {
		inactiveAt := time.Now().Add(-staleRecordingThreshold - time.Minute)
		state.LastHookAt = &inactiveAt
		return nil
	}))

	mgr.Cleanup()
	require.Empty(t, mgr.ActiveSessions())
	assert.Zero(t, mgr.DetectAndRestart(f.ledgerPath), "stale recordings must not reacquire raw ownership")
	require.ErrorContains(t, mgr.StartWatch(f.sessionName, f.sourcePath, "cursor", f.ledgerPath, f.cachePath), "inactive")
	err = fileutil.WithFileLockTimeout(context.Background(), rawPath, time.Second, func() error { return nil })
	require.NoError(t, err, "the finalizer can acquire ownership after Cleanup joins the watcher")
	handler := NewSessionFinalizeHandlerForTest(mgr.logger)
	items, _, err := handler.detectInDir(filepath.Dir(f.cachePath), f.ledgerPath)
	require.NoError(t, err)
	require.Len(t, items, 1, "inactivity must queue finalization without a sessionEnd hook")
	assert.NoFileExists(t, recPath)
	raw, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"cursor_finalized":true`)
	stored, err := session.ReadSessionFromPath(rawPath)
	require.NoError(t, err)
	assert.Len(t, stored.Entries, 2)
}

func TestCursorWatcherRecoversUncheckpointedBatchAfterRedactionChange(t *testing.T) {
	installCursorWatcherAdapter(t)
	f := newCursorWatcherFixture(t, `{"role":"user","content":"internal.example.test"}`+"\n"+
		`{"type":"turn_ended","status":"success"}`+"\n", false, true)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Join(f.projectRoot, ".sageox"), 0o700))
	policyPath := filepath.Join(f.projectRoot, ".sageox", "REDACT.md")
	writePolicy := func(replacement string) {
		t.Helper()
		require.NoError(t, os.WriteFile(policyPath, []byte("```redact\nliteral \"internal.example.test\" -> "+replacement+"\n```\n"), 0o600))
	}
	writePolicy("[REDACTED_OLD]")
	rawPath := filepath.Join(f.cachePath, artifactRaw)
	source, err := os.ReadFile(f.sourcePath)
	require.NoError(t, err)
	// Reproduce a crash after the fsynced append but before the marker commit.
	// Use the real journal API; no test-only capture seam is needed.
	require.NoError(t, fileutil.WithFileLock(context.Background(), rawPath, func() error {
		writer, err := session.NewRawWriter(rawPath, f.projectRoot)
		if err != nil {
			return err
		}
		defer writer.Close()
		if err := writer.BeginAppend(0, int64(len(source))); err != nil {
			return err
		}
		if err := writer.WriteEntry(&session.Entry{Type: session.EntryTypeUser, Content: "internal.example.test"}); err != nil {
			return err
		}
		return writer.SealAppend()
	}))
	before, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	require.Contains(t, string(before), "[REDACTED_OLD]")
	writePolicy("[REDACTED_NEW]")

	mgr := newCursorWatcherManager(t, f.homeDir)
	startCursorWatch(t, mgr, f)
	require.Eventually(t, func() bool {
		return loadCursorWatcherState(t, f).SourceOffset == int64(len(source))
	}, 5*time.Second, 10*time.Millisecond)
	mgr.StopWatch(f.sessionName)
	state := loadCursorWatcherState(t, f)
	assert.Equal(t, 1, state.EntryCount)
	after, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	assert.Contains(t, string(after), "[REDACTED_NEW]")
	assert.NotContains(t, string(after), "[REDACTED_OLD]")
	assert.NotContains(t, string(after), "internal.example.test")
	assert.Equal(t, 1, countRawJSONLEntries(t, rawPath))
}
