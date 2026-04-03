package agentwork

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestWatcherManager() *SessionWatcherManager {
	return NewSessionWatcherManager(slog.Default())
}

// --- A. Lifecycle ---

// TestSessionWatcherManager_StartWatch_Idempotent verifies that starting a
// watcher twice for the same session is a no-op (not an error or double-start).
// Failure prevented: duplicate goroutines tailing the same file.
func TestSessionWatcherManager_StartWatch_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test")
	}

	mgr := newTestWatcherManager()
	defer mgr.StopAll()

	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "session.jsonl")
	require.NoError(t, os.WriteFile(sessionFile, []byte{}, 0644))

	err := mgr.StartWatch("test-session", sessionFile, "codex", "/ledger", dir)
	require.NoError(t, err)

	// second start is idempotent
	err = mgr.StartWatch("test-session", sessionFile, "codex", "/ledger", dir)
	require.NoError(t, err)

	assert.Len(t, mgr.ActiveSessions(), 1)
}

// TestSessionWatcherManager_StopWatch_RemovesWatcher verifies stopping removes
// the session from the active set.
// Failure prevented: stopped watchers linger, preventing re-start.
func TestSessionWatcherManager_StopWatch_RemovesWatcher(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test")
	}

	mgr := newTestWatcherManager()
	defer mgr.StopAll()

	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "session.jsonl")
	require.NoError(t, os.WriteFile(sessionFile, []byte{}, 0644))

	require.NoError(t, mgr.StartWatch("s1", sessionFile, "codex", "/ledger", dir))
	assert.Len(t, mgr.ActiveSessions(), 1)

	mgr.StopWatch("s1")
	assert.Empty(t, mgr.ActiveSessions())
}

// TestSessionWatcherManager_StopWatch_NoOp verifies stopping a non-existent
// session doesn't panic.
// Failure prevented: daemon crashes when IPC stop arrives for unknown session.
func TestSessionWatcherManager_StopWatch_NoOp(t *testing.T) {
	mgr := newTestWatcherManager()
	mgr.StopWatch("nonexistent") // must not panic
}

// TestSessionWatcherManager_StopAll_CleansUp verifies StopAll cancels all watchers.
// Failure prevented: goroutine leak during daemon shutdown.
func TestSessionWatcherManager_StopAll_CleansUp(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test")
	}

	mgr := newTestWatcherManager()

	dir := t.TempDir()
	for _, name := range []string{"s1", "s2", "s3"} {
		f := filepath.Join(dir, name+".jsonl")
		require.NoError(t, os.WriteFile(f, []byte{}, 0644))
		require.NoError(t, mgr.StartWatch(name, f, "codex", "/ledger", dir))
	}
	assert.Len(t, mgr.ActiveSessions(), 3)

	mgr.StopAll()
	assert.Empty(t, mgr.ActiveSessions())

	// StartWatch must fail after StopAll
	err := mgr.StartWatch("s4", filepath.Join(dir, "s4.jsonl"), "codex", "/ledger", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopped")
}

// --- B. Adapter resolution ---

// TestResolveAdapter_KnownAdapters verifies all supported adapters resolve.
// Failure prevented: new adapter added but not registered in resolveAdapter.
func TestResolveAdapter_KnownAdapters(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"codex", "claude-code", "gemini"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			adapter, err := resolveAdapter(name)
			require.NoError(t, err)
			assert.Equal(t, name, adapter.Name())
		})
	}
}

// TestResolveAdapter_UnknownReturnsError verifies unknown adapter names fail.
// Failure prevented: silent no-op when daemon receives unknown adapter name.
func TestResolveAdapter_UnknownReturnsError(t *testing.T) {
	t.Parallel()
	_, err := resolveAdapter("unknown-agent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown adapter")
}

// TestSessionWatcherManager_StartWatch_UnknownAdapter verifies unknown adapter
// prevents watcher start.
// Failure prevented: watcher goroutine started with nil adapter.
func TestSessionWatcherManager_StartWatch_UnknownAdapter(t *testing.T) {
	mgr := newTestWatcherManager()
	err := mgr.StartWatch("s1", "/file", "nonexistent", "/ledger", "/cache")
	require.Error(t, err)
	assert.Empty(t, mgr.ActiveSessions())
}

// --- C. DetectAndRestart (anti-entropy) ---

// TestSessionWatcherManager_DetectAndRestart_FindsTailRecordings verifies
// detection of .recording.json with WatchMode:"tail".
// Failure prevented: daemon restart loses all active watchers.
func TestSessionWatcherManager_DetectAndRestart_FindsTailRecordings(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test")
	}

	mgr := newTestWatcherManager()
	defer mgr.StopAll()

	ledgerDir := t.TempDir()
	sessionsDir := filepath.Join(ledgerDir, "sessions")

	// create a tail-mode recording
	sessionDir := filepath.Join(sessionsDir, "2026-03-31T10-00-test")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	sessionFile := filepath.Join(t.TempDir(), "agent-session.jsonl")
	require.NoError(t, os.WriteFile(sessionFile, []byte{}, 0644))

	state := session.RecordingState{
		WatchMode:   "tail",
		AdapterName: "codex",
		SessionFile: sessionFile,
	}
	data, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, ".recording.json"), data, 0644))

	started := mgr.DetectAndRestart(ledgerDir)
	assert.Equal(t, 1, started)
	assert.Len(t, mgr.ActiveSessions(), 1)
}

// TestSessionWatcherManager_DetectAndRestart_SkipsHookMode verifies hook-mode
// recordings are not picked up by detection.
// Failure prevented: daemon starts duplicate watcher for hook-driven sessions.
func TestSessionWatcherManager_DetectAndRestart_SkipsHookMode(t *testing.T) {
	mgr := newTestWatcherManager()
	ledgerDir := t.TempDir()
	sessionsDir := filepath.Join(ledgerDir, "sessions")

	sessionDir := filepath.Join(sessionsDir, "hook-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	state := session.RecordingState{WatchMode: "hook", AdapterName: "claude-code"}
	data, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, ".recording.json"), data, 0644))

	started := mgr.DetectAndRestart(ledgerDir)
	assert.Equal(t, 0, started)
}

// TestSessionWatcherManager_DetectAndRestart_SkipsStopped verifies stopped
// tail recordings are not restarted (left for session_finalize).
// Failure prevented: daemon restarts watcher for session that's being finalized.
func TestSessionWatcherManager_DetectAndRestart_SkipsStopped(t *testing.T) {
	mgr := newTestWatcherManager()
	ledgerDir := t.TempDir()
	sessionsDir := filepath.Join(ledgerDir, "sessions")

	sessionDir := filepath.Join(sessionsDir, "stopped-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	now := time.Now()
	state := session.RecordingState{
		WatchMode:   "tail",
		AdapterName: "codex",
		SessionFile: "/some/file.jsonl",
		StoppedAt:   &now,
	}
	data, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, ".recording.json"), data, 0644))

	started := mgr.DetectAndRestart(ledgerDir)
	assert.Equal(t, 0, started)
}

// TestSessionWatcherManager_DetectAndRestart_SkipsDeadPID verifies that
// recordings with a dead parent PID are not restarted.
// Failure prevented: daemon restarts watcher for agent that already exited,
// causing a goroutine to tail a file that will never grow.
func TestSessionWatcherManager_DetectAndRestart_SkipsDeadPID(t *testing.T) {
	mgr := newTestWatcherManager()
	ledgerDir := t.TempDir()
	sessionsDir := filepath.Join(ledgerDir, "sessions")

	sessionDir := filepath.Join(sessionsDir, "dead-agent")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	state := session.RecordingState{
		WatchMode:   "tail",
		AdapterName: "codex",
		SessionFile: "/some/file.jsonl",
		ParentPID:   999999999, // PID that definitely doesn't exist
	}
	data, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, ".recording.json"), data, 0644))

	started := mgr.DetectAndRestart(ledgerDir)
	assert.Equal(t, 0, started)
}

// TestSessionWatcherManager_DetectAndRestart_SkipsAlreadyWatched verifies
// sessions already being watched are not duplicated.
// Failure prevented: doctor interval creates duplicate watchers.
func TestSessionWatcherManager_DetectAndRestart_SkipsAlreadyWatched(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test")
	}

	mgr := newTestWatcherManager()

	ledgerDir := t.TempDir()
	sessionsDir := filepath.Join(ledgerDir, "sessions")
	sessionDir := filepath.Join(sessionsDir, "active-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	sessionFile := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(sessionFile, []byte{}, 0644))

	state := session.RecordingState{
		WatchMode:   "tail",
		AdapterName: "codex",
		SessionFile: sessionFile,
	}
	data, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, ".recording.json"), data, 0644))

	// start manually first
	require.NoError(t, mgr.StartWatch("active-session", sessionFile, "codex", ledgerDir, sessionDir))

	// detect should find 0 new ones
	started := mgr.DetectAndRestart(ledgerDir)
	assert.Equal(t, 0, started)
	assert.Len(t, mgr.ActiveSessions(), 1)

	// stop and wait for goroutines to release file handles before TempDir cleanup
	mgr.StopAll()
	require.Eventually(t, func() bool {
		return len(mgr.ActiveSessions()) == 0
	}, 2*time.Second, 10*time.Millisecond)
}

// TestSessionWatcherManager_DetectAndRestart_NoSessionsDir verifies missing
// sessions directory doesn't error.
// Failure prevented: daemon panics on fresh ledger with no sessions.
func TestSessionWatcherManager_DetectAndRestart_NoSessionsDir(t *testing.T) {
	mgr := newTestWatcherManager()
	started := mgr.DetectAndRestart(t.TempDir())
	assert.Equal(t, 0, started)
}

// --- D. Offset persistence and catch-up recovery ---

// TestSessionWatcherManager_DetectAndRestart_CatchUpFromPersistedOffset verifies
// that on daemon restart, the watcher reads from the persisted SourceOffset and
// recovers entries written while the daemon was down.
// Failure prevented: entries lost during daemon restart.
func TestSessionWatcherManager_DetectAndRestart_CatchUpFromPersistedOffset(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test")
	}

	mgr := newTestWatcherManager()
	defer mgr.StopAll()

	ledgerDir := t.TempDir()
	sessionsDir := filepath.Join(ledgerDir, "sessions")
	sessionDir := filepath.Join(sessionsDir, "catchup-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	// create a Codex session file with two entries:
	// entry1 (already processed before daemon crash) + entry2 (written while daemon was down)
	sessionFile := filepath.Join(t.TempDir(), "codex-session.jsonl")
	entry1 := `{"timestamp":"2026-03-31T10:00:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"first message"}]}}` + "\n"
	entry2 := `{"timestamp":"2026-03-31T10:01:00Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"second message"}]}}` + "\n"
	require.NoError(t, os.WriteFile(sessionFile, []byte(entry1+entry2), 0644))

	// set SourceOffset to after entry1 (simulating daemon crashed after processing entry1)
	state := session.RecordingState{
		WatchMode:    "tail",
		AdapterName:  "codex",
		SessionFile:  sessionFile,
		SourceOffset: int64(len(entry1)), // entry2 is the unprocessed part
	}
	data, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, ".recording.json"), data, 0644))

	// DetectAndRestart should start with the persisted offset
	started := mgr.DetectAndRestart(ledgerDir)
	require.Equal(t, 1, started)

	// wait for the catch-up read to write to raw.jsonl
	rawPath := filepath.Join(sessionDir, "raw.jsonl")
	require.Eventually(t, func() bool {
		info, err := os.Stat(rawPath)
		return err == nil && info.Size() > 0
	}, 2*time.Second, 50*time.Millisecond, "raw.jsonl should be written by catch-up read")

	// verify raw.jsonl contains the recovered entry (entry2, not entry1)
	rawData, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	rawStr := string(rawData)
	assert.Contains(t, rawStr, "second message", "catch-up should recover entry2")
	assert.NotContains(t, rawStr, "first message", "catch-up should NOT re-read entry1")

	// verify SourceOffset was persisted to .recording.json
	recData, err := os.ReadFile(filepath.Join(sessionDir, ".recording.json"))
	require.NoError(t, err)
	var updatedState session.RecordingState
	require.NoError(t, json.Unmarshal(recData, &updatedState))
	assert.Greater(t, updatedState.SourceOffset, state.SourceOffset,
		"persisted offset should advance past entry2")
}

// TestSessionWatcherManager_PersistOffset_UpdatesRecordingState verifies that
// persistOffset writes SourceOffset to .recording.json.
// Failure prevented: daemon crash loses offset, causing full re-read on restart.
func TestSessionWatcherManager_PersistOffset_UpdatesRecordingState(t *testing.T) {
	mgr := newTestWatcherManager()

	dir := t.TempDir()
	aw := &activeWatcher{cachePath: dir}

	// write initial recording state
	state := session.RecordingState{WatchMode: "tail", EntryCount: 5}
	data, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), data, 0644))

	mgr.persistOffset(aw, 12345, 3)

	// read back and verify
	recData, err := os.ReadFile(filepath.Join(dir, ".recording.json"))
	require.NoError(t, err)
	var updated session.RecordingState
	require.NoError(t, json.Unmarshal(recData, &updated))
	assert.Equal(t, int64(12345), updated.SourceOffset)
	assert.Equal(t, 8, updated.EntryCount) // 5 + 3
}

// TestSessionWatcherManager_LiveTail_EntryCountLinear verifies that EntryCount
// grows linearly (not quadratically) when multiple entries are processed.
// Failure prevented: cumulative counter passed as delta to persistOffset causes
// EntryCount to inflate as N*(N+1)/2 instead of N.
func TestSessionWatcherManager_LiveTail_EntryCountLinear(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test with file I/O timing")
	}

	mgr := newTestWatcherManager()
	defer mgr.StopAll()

	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "session.jsonl")
	require.NoError(t, os.WriteFile(sessionFile, []byte{}, 0644))

	// write initial .recording.json with EntryCount=0
	state := session.RecordingState{WatchMode: "tail", EntryCount: 0}
	recData, _ := json.Marshal(state)
	recPath := filepath.Join(dir, ".recording.json")
	require.NoError(t, os.WriteFile(recPath, recData, 0644))

	require.NoError(t, mgr.StartWatch("count-test", sessionFile, "codex", "/ledger", dir))

	// let fsnotify watcher register before writing entries;
	// ActiveSessions confirms the session is tracked but fsnotify needs
	// additional time to register the OS-level file watch
	require.Eventually(t, func() bool {
		return len(mgr.ActiveSessions()) > 0
	}, 2*time.Second, 10*time.Millisecond)
	time.Sleep(200 * time.Millisecond) // fsnotify OS registration

	// write 3 entries sequentially, waiting for each to be processed
	entries := []string{
		`{"timestamp":"2026-03-31T10:00:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"msg 1"}]}}`,
		`{"timestamp":"2026-03-31T10:01:00Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"reply 1"}]}}`,
		`{"timestamp":"2026-03-31T10:02:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"msg 2"}]}}`,
	}

	for i, entry := range entries {
		f, err := os.OpenFile(sessionFile, os.O_WRONLY|os.O_APPEND, 0644)
		require.NoError(t, err)
		_, err = f.WriteString(entry + "\n")
		require.NoError(t, err)
		require.NoError(t, f.Close())

		// wait for this entry to be reflected in EntryCount
		expectedCount := i + 1
		require.Eventually(t, func() bool {
			data, err := os.ReadFile(recPath)
			if err != nil {
				return false
			}
			var s session.RecordingState
			if err := json.Unmarshal(data, &s); err != nil {
				return false
			}
			return s.EntryCount >= expectedCount
		}, 5*time.Second, 100*time.Millisecond,
			"EntryCount should reach %d after entry %d", expectedCount, i+1)
	}

	// read final state — EntryCount must be exactly 3, not 6 (quadratic)
	finalData, err := os.ReadFile(recPath)
	require.NoError(t, err)
	var finalState session.RecordingState
	require.NoError(t, json.Unmarshal(finalData, &finalState))

	assert.Equal(t, 3, finalState.EntryCount,
		"EntryCount must grow linearly (3), not quadratically (6)")
}

// TestSessionWatcherManager_PersistOffset_AtomicWrite verifies that persistOffset
// uses atomic write (temp + rename) so concurrent readers never see partial JSON.
// Failure prevented: CLI reads truncated .recording.json during daemon write.
func TestSessionWatcherManager_PersistOffset_AtomicWrite(t *testing.T) {
	mgr := newTestWatcherManager()

	dir := t.TempDir()
	aw := &activeWatcher{cachePath: dir, sessionName: "atomic-test"}
	recPath := filepath.Join(dir, ".recording.json")

	state := session.RecordingState{WatchMode: "tail", EntryCount: 0}
	data, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(recPath, data, 0644))

	// run many concurrent persistOffset calls — every read of the file
	// should yield valid JSON (never a partial write)
	const iterations = 100
	done := make(chan struct{})

	// writer goroutine
	go func() {
		defer close(done)
		for i := 0; i < iterations; i++ {
			mgr.persistOffset(aw, int64(i*100), 1)
		}
	}()

	// reader goroutine — continuously reads the file and checks valid JSON
	invalidReads := 0
	for {
		select {
		case <-done:
			assert.Equal(t, 0, invalidReads,
				"all reads during concurrent writes must yield valid JSON")
			return
		default:
			raw, err := os.ReadFile(recPath)
			if err != nil {
				continue // file might be mid-rename
			}
			var s session.RecordingState
			if err := json.Unmarshal(raw, &s); err != nil {
				invalidReads++
			}
		}
	}
}

// TestSessionWatcherManager_PersistOffset_PreservesCLIFields verifies that
// persistOffset only updates SourceOffset and EntryCount, preserving all
// other fields written by the CLI (e.g., StoppedAt, SessionFile).
// Failure prevented: daemon overwrites CLI-set StoppedAt, causing watcher to never stop.
func TestSessionWatcherManager_PersistOffset_PreservesCLIFields(t *testing.T) {
	mgr := newTestWatcherManager()

	dir := t.TempDir()
	aw := &activeWatcher{cachePath: dir}
	recPath := filepath.Join(dir, ".recording.json")

	// CLI writes .recording.json with StoppedAt and other fields
	now := time.Now().UTC().Truncate(time.Second)
	state := session.RecordingState{
		WatchMode:   "tail",
		EntryCount:  10,
		AgentID:     "test-agent",
		AdapterName: "codex",
		SessionFile: "/some/session.jsonl",
		StoppedAt:   &now,
	}
	data, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(recPath, data, 0644))

	// daemon persists offset
	mgr.persistOffset(aw, 5000, 2)

	// read back — StoppedAt and other CLI fields must survive
	raw, err := os.ReadFile(recPath)
	require.NoError(t, err)
	var updated session.RecordingState
	require.NoError(t, json.Unmarshal(raw, &updated))

	assert.Equal(t, int64(5000), updated.SourceOffset)
	assert.Equal(t, 12, updated.EntryCount) // 10 + 2
	assert.NotNil(t, updated.StoppedAt, "StoppedAt must survive persistOffset")
	assert.Equal(t, now, *updated.StoppedAt, "StoppedAt value must be preserved")
	assert.Equal(t, "test-agent", updated.AgentID, "AgentID must be preserved")
	assert.Equal(t, "codex", updated.AdapterName, "AdapterName must be preserved")
	assert.Equal(t, "/some/session.jsonl", updated.SessionFile, "SessionFile must be preserved")
}

// --- E. Cleanup ---

// TestSessionWatcherManager_Cleanup_StopsStopped verifies cleanup stops watchers
// whose .recording.json has StoppedAt set.
// Failure prevented: watcher keeps tailing after ox session stop.
func TestSessionWatcherManager_Cleanup_StopsStopped(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test")
	}

	mgr := newTestWatcherManager()
	defer mgr.StopAll()

	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "session.jsonl")
	require.NoError(t, os.WriteFile(sessionFile, []byte{}, 0644))
	require.NoError(t, mgr.StartWatch("s1", sessionFile, "codex", "/ledger", dir))

	// write a stopped recording marker
	now := time.Now()
	state := session.RecordingState{StoppedAt: &now}
	data, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), data, 0644))

	mgr.Cleanup()

	require.Eventually(t, func() bool {
		return len(mgr.ActiveSessions()) == 0
	}, 2*time.Second, 10*time.Millisecond, "watcher should be removed after cleanup")
}

// TestSessionWatcherManager_Cleanup_StopsOrphaned verifies cleanup stops
// watchers whose .recording.json has been deleted.
// Failure prevented: watcher goroutine leak after external cleanup.
func TestSessionWatcherManager_Cleanup_StopsOrphaned(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test")
	}

	mgr := newTestWatcherManager()
	defer mgr.StopAll()

	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "session.jsonl")
	require.NoError(t, os.WriteFile(sessionFile, []byte{}, 0644))
	require.NoError(t, mgr.StartWatch("s1", sessionFile, "codex", "/ledger", dir))

	// no .recording.json in dir → orphaned
	mgr.Cleanup()

	require.Eventually(t, func() bool {
		return len(mgr.ActiveSessions()) == 0
	}, 2*time.Second, 10*time.Millisecond, "orphaned watcher should be removed after cleanup")
}

// TestSessionWatcherManager_Cleanup_SkipsCorruptRecording verifies that Cleanup
// skips (does not stop) watchers whose .recording.json contains invalid JSON.
// Failure prevented: corrupt JSON causes Cleanup to crash or stop valid watchers.
func TestSessionWatcherManager_Cleanup_SkipsCorruptRecording(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test")
	}

	mgr := newTestWatcherManager()

	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "session.jsonl")
	require.NoError(t, os.WriteFile(sessionFile, []byte{}, 0644))
	require.NoError(t, mgr.StartWatch("s1", sessionFile, "codex", "/ledger", dir))

	// overwrite .recording.json with garbage
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), []byte("{{not json"), 0644))

	mgr.Cleanup()

	// corrupt JSON means unmarshal fails → skip, don't stop
	assert.Len(t, mgr.ActiveSessions(), 1, "watcher should survive corrupt .recording.json during Cleanup")

	mgr.StopAll()
}

// --- F. Failure modes and edge cases ---

// TestSessionWatcherManager_StartWatch_RejectsRelativePath verifies that
// StartWatch rejects a relative session file path.
// Failure prevented: watcher started with relative path breaks path logic downstream.
func TestSessionWatcherManager_StartWatch_RejectsRelativePath(t *testing.T) {
	mgr := newTestWatcherManager()

	err := mgr.StartWatch("s1", "relative/path.jsonl", "codex", "/ledger", "/cache")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute path")
	assert.Empty(t, mgr.ActiveSessions())
}

// TestSessionWatcherManager_DetectAndRestart_NonExistentSessionsDir verifies
// DetectAndRestart returns 0 when the ledger has no sessions directory at all.
// Failure prevented: daemon panics scanning a ledger that has never recorded a session.
func TestSessionWatcherManager_DetectAndRestart_NonExistentSessionsDir(t *testing.T) {
	mgr := newTestWatcherManager()
	ledgerDir := t.TempDir()
	// no "sessions" subdirectory created

	started := mgr.DetectAndRestart(ledgerDir)
	assert.Equal(t, 0, started)
}

// TestSessionWatcherManager_DetectAndRestart_OffsetBeyondEOF verifies that
// when the persisted SourceOffset exceeds the actual file size (file was
// truncated), the watcher starts without crashing.
// Failure prevented: daemon crash on restart when agent rewrote a shorter session file.
func TestSessionWatcherManager_DetectAndRestart_OffsetBeyondEOF(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test")
	}

	mgr := newTestWatcherManager()

	ledgerDir := t.TempDir()
	sessionsDir := filepath.Join(ledgerDir, "sessions")
	sessionDir := filepath.Join(sessionsDir, "truncated-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	// small session file — only a few bytes
	sessionFile := filepath.Join(t.TempDir(), "small-session.jsonl")
	smallContent := `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}` + "\n"
	require.NoError(t, os.WriteFile(sessionFile, []byte(smallContent), 0644))

	// persisted offset way beyond actual file size
	state := session.RecordingState{
		WatchMode:    "tail",
		AdapterName:  "codex",
		SessionFile:  sessionFile,
		SourceOffset: 99999,
	}
	data, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, ".recording.json"), data, 0644))

	started := mgr.DetectAndRestart(ledgerDir)
	require.Equal(t, 1, started)

	// watcher should be in the active set despite the offset mismatch
	require.Eventually(t, func() bool {
		return len(mgr.ActiveSessions()) == 1
	}, 2*time.Second, 10*time.Millisecond, "watcher should start despite offset beyond EOF")

	mgr.StopAll()
}

// TestSessionWatcherManager_CatchUpReadFailure_LiveTailStillStarts verifies
// that when the catch-up read fails (e.g., file was deleted after offset was
// persisted), the watcher still starts live tailing.
// Failure prevented: catch-up failure kills the entire watcher, losing live events.
func TestSessionWatcherManager_CatchUpReadFailure_LiveTailStillStarts(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test")
	}

	mgr := newTestWatcherManager()

	ledgerDir := t.TempDir()
	sessionsDir := filepath.Join(ledgerDir, "sessions")
	sessionDir := filepath.Join(sessionsDir, "catchup-fail-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	// session file exists (required for Watch to succeed) but is empty, while
	// SourceOffset > 0 means catch-up read will try to read from a position
	// that has no data — the ReadFromOffset call will either return an error
	// or return zero entries, but the watcher should still start
	sessionFile := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(sessionFile, []byte{}, 0644))

	state := session.RecordingState{
		WatchMode:    "tail",
		AdapterName:  "codex",
		SessionFile:  sessionFile,
		SourceOffset: 5000, // points beyond EOF of empty file
	}
	data, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, ".recording.json"), data, 0644))

	started := mgr.DetectAndRestart(ledgerDir)
	require.Equal(t, 1, started)

	// the watcher should be active (live tail started despite catch-up failure)
	require.Eventually(t, func() bool {
		return len(mgr.ActiveSessions()) == 1
	}, 2*time.Second, 10*time.Millisecond, "watcher should be active after catch-up failure")

	mgr.StopAll()
}

// TestSessionWatcherManager_PersistOffset_MissingRecordingJSON verifies that
// persistOffset silently returns when .recording.json has been deleted.
// Failure prevented: watcher crashes mid-tail when .recording.json is removed externally.
func TestSessionWatcherManager_PersistOffset_MissingRecordingJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test")
	}

	mgr := newTestWatcherManager()

	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "session.jsonl")
	require.NoError(t, os.WriteFile(sessionFile, []byte{}, 0644))

	// write a valid .recording.json so StartWatch succeeds
	state := session.RecordingState{WatchMode: "tail", AdapterName: "codex", SessionFile: sessionFile}
	recData, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), recData, 0644))

	require.NoError(t, mgr.StartWatch("s1", sessionFile, "codex", "/ledger", dir))

	// delete .recording.json while watcher is active
	require.NoError(t, os.Remove(filepath.Join(dir, ".recording.json")))

	// write to session file to trigger persistOffset internally
	f, err := os.OpenFile(sessionFile, os.O_WRONLY|os.O_APPEND, 0644)
	require.NoError(t, err)
	_, err = f.WriteString(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"test"}]}}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	// watcher should still be running (persistOffset didn't crash)
	require.Eventually(t, func() bool {
		return len(mgr.ActiveSessions()) == 1
	}, 2*time.Second, 10*time.Millisecond, "watcher must survive missing .recording.json during persistOffset")

	mgr.StopAll()
}

// TestSessionWatcherManager_PersistOffset_CorruptRecordingJSON verifies that
// persistOffset silently returns when .recording.json contains invalid JSON.
// Failure prevented: watcher crashes when .recording.json is corrupted by
// concurrent write or disk issue.
func TestSessionWatcherManager_PersistOffset_CorruptRecordingJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test")
	}

	mgr := newTestWatcherManager()

	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "session.jsonl")
	require.NoError(t, os.WriteFile(sessionFile, []byte{}, 0644))

	// write a valid .recording.json so StartWatch succeeds
	state := session.RecordingState{WatchMode: "tail", AdapterName: "codex", SessionFile: sessionFile}
	recData, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), recData, 0644))

	require.NoError(t, mgr.StartWatch("s1", sessionFile, "codex", "/ledger", dir))

	// corrupt .recording.json while watcher is active
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), []byte("%%%CORRUPT%%%"), 0644))

	// write to session file to trigger persistOffset internally
	f, err := os.OpenFile(sessionFile, os.O_WRONLY|os.O_APPEND, 0644)
	require.NoError(t, err)
	_, err = f.WriteString(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"test"}]}}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	// watcher should still be running (persistOffset didn't crash)
	require.Eventually(t, func() bool {
		return len(mgr.ActiveSessions()) == 1
	}, 2*time.Second, 10*time.Millisecond, "watcher must survive corrupt .recording.json during persistOffset")

	mgr.StopAll()
}
