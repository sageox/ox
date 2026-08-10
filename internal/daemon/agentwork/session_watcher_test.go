package agentwork

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testAdapter is a minimal adapter implementation for unit tests.
// It implements Adapter, IncrementalReader, and Watch using TailWatcher,
// treating each JSONL line as a generic RawEntry. This allows the session
// watcher manager tests to exercise catch-up reads and live tailing without
// requiring real external adapter binaries.
type testAdapter struct {
	name string
}

func (a *testAdapter) Name() string { return a.name }
func (a *testAdapter) Detect() bool { return false }
func (a *testAdapter) FindSessionFile(_ adapters.SessionLookup) (string, error) {
	return "", adapters.ErrSessionNotFound
}
func (a *testAdapter) Read(_ string) ([]adapters.RawEntry, error) { return nil, nil }
func (a *testAdapter) ReadMetadata(_ string) (*adapters.SessionMetadata, error) {
	return nil, nil
}

func (a *testAdapter) Watch(ctx context.Context, path string) (<-chan adapters.RawEntry, error) {
	tw := adapters.NewTailWatcher(path, 0, testParseLine)
	return tw.Watch(ctx)
}

func (a *testAdapter) ReadFromOffset(path string, offset int64) ([]adapters.RawEntry, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()

	if _, err := f.Seek(offset, 0); err != nil {
		return nil, offset, err
	}

	var entries []adapters.RawEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		parsed, _ := testParseLine(line)
		entries = append(entries, parsed...)
	}

	fi, _ := f.Stat()
	return entries, fi.Size(), scanner.Err()
}

// testParseLine treats each JSONL line as a generic assistant entry.
func testParseLine(line []byte) ([]adapters.RawEntry, error) {
	return []adapters.RawEntry{
		{
			Role:      "assistant",
			Content:   string(line),
			Timestamp: time.Now(),
		},
	}, nil
}

func TestMain(m *testing.M) {
	// register a test adapter so resolveAdapter("codex") works without
	// the real ox-adapter-codex binary on PATH
	adapters.Register(&testAdapter{name: "codex"})
	os.Exit(m.Run())
}

// newTestWatcherManager returns a manager whose session-file allow-list is
// rooted at a temp home, so these tests run through the real path validation in
// startWatchAt rather than around it. Disabling that check for tests would
// defeat its purpose: it is the guard that stops the daemon tailing a file the
// adapter does not own.
func newTestWatcherManager(t *testing.T) *SessionWatcherManager {
	t.Helper()
	m := NewSessionWatcherManager(slog.Default())
	m.SetHomeDirForTest(t.TempDir())
	return m
}

// codexSessionPath returns a path inside codex's real session root under this
// manager's test home, creating the directory. Session files must live where
// the adapter actually stores them or the allow-list rejects them.
func (m *SessionWatcherManager) codexSessionPath(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(m.homeDir(), ".codex", "sessions")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return filepath.Join(dir, name)
}

// --- A. Lifecycle ---

// TestSessionWatcherManager_StartWatch_Idempotent verifies that starting a
// watcher twice for the same session is a no-op (not an error or double-start).
// Failure prevented: duplicate goroutines tailing the same file.
func TestSessionWatcherManager_StartWatch_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test")
	}

	mgr := newTestWatcherManager(t)
	defer mgr.StopAll()

	dir := t.TempDir()
	sessionFile := mgr.codexSessionPath(t, "session.jsonl")
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

	mgr := newTestWatcherManager(t)
	defer mgr.StopAll()

	dir := t.TempDir()
	sessionFile := mgr.codexSessionPath(t, "session.jsonl")
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
	mgr := newTestWatcherManager(t)
	mgr.StopWatch("nonexistent") // must not panic
}

// TestSessionWatcherManager_StopAll_CleansUp verifies StopAll cancels all watchers.
// Failure prevented: goroutine leak during daemon shutdown.
func TestSessionWatcherManager_StopAll_CleansUp(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test")
	}

	mgr := newTestWatcherManager(t)

	dir := t.TempDir()
	for _, name := range []string{"s1", "s2", "s3"} {
		f := mgr.codexSessionPath(t, name+".jsonl")
		require.NoError(t, os.WriteFile(f, []byte{}, 0644))
		require.NoError(t, mgr.StartWatch(name, f, "codex", "/ledger", dir))
	}
	assert.Len(t, mgr.ActiveSessions(), 3)

	mgr.StopAll()
	assert.Empty(t, mgr.ActiveSessions())

	// StartWatch must fail after StopAll
	err := mgr.StartWatch("s4", mgr.codexSessionPath(t, "s4.jsonl"), "codex", "/ledger", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopped")
}

// --- B. Adapter resolution ---

// TestResolveAdapter_KnownAdapters verifies registered adapters resolve via
// the adapter registry. Uses mock adapters since built-in adapters were removed
// in favor of external adapter binaries.
// Failure prevented: resolveAdapter fails to delegate to adapter registry.
func TestResolveAdapter_KnownAdapters(t *testing.T) {
	// "codex" is registered in TestMain; only register the others
	for _, name := range []string{"claude-code", "gemini"} {
		adapters.Register(&testAdapter{name: name})
		t.Cleanup(func() { adapters.Unregister(name) })
	}

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
	mgr := newTestWatcherManager(t)
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

	mgr := newTestWatcherManager(t)
	defer mgr.StopAll()

	ledgerDir := t.TempDir()
	sessionsDir := filepath.Join(ledgerDir, "sessions")

	// create a tail-mode recording
	sessionDir := filepath.Join(sessionsDir, "2026-03-31T10-00-test")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	sessionFile := mgr.codexSessionPath(t, "agent-session.jsonl")
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
	mgr := newTestWatcherManager(t)
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
	mgr := newTestWatcherManager(t)
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
	mgr := newTestWatcherManager(t)
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

	mgr := newTestWatcherManager(t)

	ledgerDir := t.TempDir()
	sessionsDir := filepath.Join(ledgerDir, "sessions")
	sessionDir := filepath.Join(sessionsDir, "active-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	sessionFile := mgr.codexSessionPath(t, "session.jsonl")
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
	mgr := newTestWatcherManager(t)
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

	mgr := newTestWatcherManager(t)
	defer mgr.StopAll()

	ledgerDir := t.TempDir()
	sessionsDir := filepath.Join(ledgerDir, "sessions")
	sessionDir := filepath.Join(sessionsDir, "catchup-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	// create a Codex session file with two entries:
	// entry1 (already processed before daemon crash) + entry2 (written while daemon was down)
	sessionFile := mgr.codexSessionPath(t, "codex-session.jsonl")
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

	// verify SourceOffset was persisted to .recording.json. raw.jsonl is
	// written inside the same loop that calls persistOffset, but persist
	// runs *after* the encode. On slower runners (CI) the test would see
	// raw.jsonl > 0 before persistOffset finished, racing the read of
	// .recording.json. Poll until the persisted offset advances.
	var updatedState session.RecordingState
	require.Eventually(t, func() bool {
		recData, err := os.ReadFile(filepath.Join(sessionDir, ".recording.json"))
		if err != nil {
			return false
		}
		if err := json.Unmarshal(recData, &updatedState); err != nil {
			return false
		}
		return updatedState.SourceOffset > state.SourceOffset
	}, 2*time.Second, 50*time.Millisecond, "persisted offset should advance past entry2")
}

// --- D'. Write-path redaction (ox-hhc3) ---

// TestSessionWatcherManager_WritePath_RedactsCredentials verifies that the
// session watcher applies the secret redactor BEFORE writing to raw.jsonl.
// Failure prevented: AWS keys / GitLab PATs / Bearer headers reach .sageox/cache/
// in plaintext, then ride backup tools (Time Machine, iCloud, Dropbox) off the
// user's machine. This was the active leak path in the 2026-05-10 forensic scan.
func TestSessionWatcherManager_WritePath_RedactsCredentials(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test")
	}

	mgr := newTestWatcherManager(t)
	defer mgr.StopAll()

	ledgerDir := t.TempDir()
	sessionsDir := filepath.Join(ledgerDir, "sessions")
	sessionDir := filepath.Join(sessionsDir, "canary-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	// Plant credentials of three classes across two entries. Using the test
	// adapter, each JSONL line becomes a single Content field on a session
	// entry. The redactor must scrub all three before they hit raw.jsonl.
	awsCanary := "AKIAIOSFODNN7EXAMPLE"
	gitlabCanary := "glpat-AbCdEfGhIjKlMnOpQrSt"
	bearerLine := `Authorization: Bearer ya29.thisIsATokenValue1234567890abc`

	sessionFile := mgr.codexSessionPath(t, "session.jsonl")
	entry1 := `aws_access_key_id=` + awsCanary + "\n"
	entry2 := gitlabCanary + " " + bearerLine + "\n"
	require.NoError(t, os.WriteFile(sessionFile, []byte(entry1+entry2), 0644))

	state := session.RecordingState{
		WatchMode:    "tail",
		AdapterName:  "codex",
		SessionFile:  sessionFile,
		SourceOffset: 0, // catch-up from start of file
	}
	// SourceOffset of 0 means runWatcher's catch-up block is skipped — but we
	// want the canary entries to flow through the SAME redactor path. To force
	// catch-up, set offset to a positive number smaller than the entries length
	// so the IncrementalReader's ReadFromOffset returns all entries from offset.
	// Easier: just set offset=1 so catch-up reads from byte 1 to EOF, which
	// captures both entries minus their first byte. That's not a clean test.
	//
	// Instead: pre-populate offset such that catch-up reads from 0 by setting
	// offset to 1 (>0 so catch-up branch runs) and rely on the fact that the
	// testAdapter's ReadFromOffset seeks to that byte and reads forward. Use a
	// dummy first character to absorb the offset.
	preamble := "X"
	require.NoError(t, os.WriteFile(sessionFile, []byte(preamble+entry1+entry2), 0644))
	state.SourceOffset = int64(len(preamble))
	data, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, ".recording.json"), data, 0644))

	started := mgr.DetectAndRestart(ledgerDir)
	require.Equal(t, 1, started)

	// Wait until ALL three redaction slugs land in raw.jsonl. Polling
	// on file size alone races against the watcher: the AWS entry can
	// flush before the GitLab/Bearer entry, and the assertion fires on
	// partial output. The slugs are the load-bearing signal that both
	// entries have been processed, so wait for them directly.
	rawPath := filepath.Join(sessionDir, "raw.jsonl")
	var rawStr string
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(rawPath)
		if err != nil {
			return false
		}
		rawStr = string(data)
		return strings.Contains(rawStr, "[REDACTED_AWS_KEY]") &&
			strings.Contains(rawStr, "[REDACTED_GITLAB_TOKEN]") &&
			strings.Contains(rawStr, "[REDACTED_BEARER_TOKEN]")
	}, 5*time.Second, 50*time.Millisecond, "raw.jsonl should contain all three redaction slugs after catch-up read")

	// The actual canary bytes must NEVER appear in raw.jsonl. This is the
	// load-bearing assertion of the entire ox-hhc3 fix.
	assert.NotContains(t, rawStr, awsCanary, "AWS key leaked to raw.jsonl: %s", rawStr)
	assert.NotContains(t, rawStr, gitlabCanary, "GitLab PAT leaked to raw.jsonl: %s", rawStr)
	assert.NotContains(t, rawStr, "ya29.thisIsATokenValue1234567890abc", "Bearer token leaked: %s", rawStr)

	// And the redaction slugs MUST appear (proves the redactor ran, not that
	// the entries silently dropped).
	assert.Contains(t, rawStr, "[REDACTED_AWS_KEY]")
	assert.Contains(t, rawStr, "[REDACTED_GITLAB_TOKEN]")
	assert.Contains(t, rawStr, "[REDACTED_BEARER_TOKEN]")
}

// TestSessionWatcherManager_WritePath_WholeOutputRedactsAwsSso verifies the
// command-allowlist (ox-mmkf) fires on the write path. AWS SSO output is
// multi-line and includes credentials in shapes that no single regex catches
// reliably; whole-output redaction is the primary boundary.
// Failure prevented: aws sso login captures ASIA + secret + session token into
// raw.jsonl because individual fields look benign to per-regex detectors.
func TestSessionWatcherManager_WritePath_WholeOutputRedactsAwsSso(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test")
	}

	mgr := newTestWatcherManager(t)
	defer mgr.StopAll()

	ledgerDir := t.TempDir()
	sessionsDir := filepath.Join(ledgerDir, "sessions")
	sessionDir := filepath.Join(sessionsDir, "aws-sso-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	// Source line carries a synthetic tool-result-shaped JSON. The test adapter
	// surfaces each line as a generic entry; the convert path that produces
	// ToolInput/ToolOutput isn't exercised by the generic adapter, so this
	// test asserts the regex backstop on raw Content fields (which the
	// generic adapter populates with the full JSON line).
	canarySecret := "ASIATESTTESTTESTTEST"
	preamble := "X"
	awsLine := `aws_credentials="ASIATESTTESTTESTTEST + wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"` + "\n"
	sessionFile := mgr.codexSessionPath(t, "session.jsonl")
	require.NoError(t, os.WriteFile(sessionFile, []byte(preamble+awsLine), 0644))

	state := session.RecordingState{
		WatchMode:    "tail",
		AdapterName:  "codex",
		SessionFile:  sessionFile,
		SourceOffset: int64(len(preamble)),
	}
	data, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, ".recording.json"), data, 0644))

	require.Equal(t, 1, mgr.DetectAndRestart(ledgerDir))

	rawPath := filepath.Join(sessionDir, "raw.jsonl")
	require.Eventually(t, func() bool {
		info, err := os.Stat(rawPath)
		return err == nil && info.Size() > 0
	}, 2*time.Second, 50*time.Millisecond)

	rawData, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	rawStr := string(rawData)

	// Either path (cmd-redactor on a proper tool entry, or regex backstop on
	// raw content) must produce a clean output. Assert the canary bytes are
	// absent regardless of which mechanism caught them.
	assert.NotContains(t, rawStr, canarySecret, "AWS canary reached raw.jsonl: %s", rawStr)
}

// TestSessionWatcherManager_LiveTail_RedactsCredentials verifies redaction also
// fires in the live-tail loop (separate from catch-up). Different code path,
// same invariant.
func TestSessionWatcherManager_LiveTail_RedactsCredentials(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watcher test")
	}

	mgr := newTestWatcherManager(t)
	defer mgr.StopAll()

	cacheDir := t.TempDir()
	sessionFile := mgr.codexSessionPath(t, "session.jsonl")
	require.NoError(t, os.WriteFile(sessionFile, []byte{}, 0644))

	state := session.RecordingState{WatchMode: "tail", EntryCount: 0}
	recData, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, ".recording.json"), recData, 0644))

	require.NoError(t, mgr.StartWatch("live-canary", sessionFile, "codex", "/ledger", cacheDir))

	require.Eventually(t, func() bool {
		return len(mgr.ActiveSessions()) > 0
	}, 2*time.Second, 10*time.Millisecond)
	time.Sleep(200 * time.Millisecond) // fsnotify OS registration

	// append a single line carrying an AWS key
	canary := "AKIAIOSFODNN7EXAMPLE"
	f, err := os.OpenFile(sessionFile, os.O_WRONLY|os.O_APPEND, 0644)
	require.NoError(t, err)
	_, err = f.WriteString(`{"text":"export AWS_ACCESS_KEY_ID=` + canary + `"}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	rawPath := filepath.Join(cacheDir, "raw.jsonl")
	require.Eventually(t, func() bool {
		info, err := os.Stat(rawPath)
		return err == nil && info.Size() > 0
	}, 5*time.Second, 100*time.Millisecond, "raw.jsonl should be written via live tail")

	rawData, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	rawStr := string(rawData)
	assert.NotContains(t, rawStr, canary, "AWS key reached raw.jsonl via live tail: %s", rawStr)
	assert.Contains(t, rawStr, "[REDACTED_AWS_KEY]", "expected redaction slug in raw.jsonl: %s", rawStr)
}

// TestSessionWatcherManager_PersistOffset_UpdatesRecordingState verifies that
// persistOffset writes SourceOffset to .recording.json.
// Failure prevented: daemon crash loses offset, causing full re-read on restart.
func TestSessionWatcherManager_PersistOffset_UpdatesRecordingState(t *testing.T) {
	mgr := newTestWatcherManager(t)

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

	mgr := newTestWatcherManager(t)
	defer mgr.StopAll()

	dir := t.TempDir()
	sessionFile := mgr.codexSessionPath(t, "session.jsonl")
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
	mgr := newTestWatcherManager(t)

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
	mgr := newTestWatcherManager(t)

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

	mgr := newTestWatcherManager(t)
	defer mgr.StopAll()

	dir := t.TempDir()
	sessionFile := mgr.codexSessionPath(t, "session.jsonl")
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

	mgr := newTestWatcherManager(t)
	defer mgr.StopAll()

	dir := t.TempDir()
	sessionFile := mgr.codexSessionPath(t, "session.jsonl")
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

	mgr := newTestWatcherManager(t)

	dir := t.TempDir()
	sessionFile := mgr.codexSessionPath(t, "session.jsonl")
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
	mgr := newTestWatcherManager(t)

	err := mgr.StartWatch("s1", "relative/path.jsonl", "codex", "/ledger", "/cache")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute path")
	assert.Empty(t, mgr.ActiveSessions())
}

// TestSessionWatcherManager_DetectAndRestart_NonExistentSessionsDir verifies
// DetectAndRestart returns 0 when the ledger has no sessions directory at all.
// Failure prevented: daemon panics scanning a ledger that has never recorded a session.
func TestSessionWatcherManager_DetectAndRestart_NonExistentSessionsDir(t *testing.T) {
	mgr := newTestWatcherManager(t)
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

	mgr := newTestWatcherManager(t)

	ledgerDir := t.TempDir()
	sessionsDir := filepath.Join(ledgerDir, "sessions")
	sessionDir := filepath.Join(sessionsDir, "truncated-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	// small session file — only a few bytes
	sessionFile := mgr.codexSessionPath(t, "small-session.jsonl")
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

	mgr := newTestWatcherManager(t)

	ledgerDir := t.TempDir()
	sessionsDir := filepath.Join(ledgerDir, "sessions")
	sessionDir := filepath.Join(sessionsDir, "catchup-fail-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	// session file exists (required for Watch to succeed) but is empty, while
	// SourceOffset > 0 means catch-up read will try to read from a position
	// that has no data — the ReadFromOffset call will either return an error
	// or return zero entries, but the watcher should still start
	sessionFile := mgr.codexSessionPath(t, "session.jsonl")
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

	mgr := newTestWatcherManager(t)

	dir := t.TempDir()
	sessionFile := mgr.codexSessionPath(t, "session.jsonl")
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

	mgr := newTestWatcherManager(t)

	dir := t.TempDir()
	sessionFile := mgr.codexSessionPath(t, "session.jsonl")
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
