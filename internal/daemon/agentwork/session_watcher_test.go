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
	for _, name := range []string{"codex", "claude-code"} {
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
