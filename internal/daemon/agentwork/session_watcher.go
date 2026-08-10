package agentwork

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
)

// ErrSessionFileShape is returned when a session file is neither an absolute
// path nor a well-formed opaque handle.
//
// A sentinel, not a message: tests that match on error TEXT cannot tell a
// rejection-for-the-right-reason from an unrelated failure, so they stay green
// when the check they guard is deleted. errors.Is against this is decidable.
var ErrSessionFileShape = errors.New("session file is neither an absolute path nor an opaque handle")

// SessionWatcherManager manages TailWatchers for active tail-mode recordings.
// It runs watchers that tail agent session files and write entries to raw.jsonl.
//
// Two entry points:
//  1. IPC (fast): CLI sends session_watch_start → StartWatch called directly
//  2. Doctor (slow): DetectAndRestart scans for .recording.json with WatchMode:"tail"
//     that don't have an active watcher → restarts them (daemon restarted)
//
// Offset persistence: the watcher persists SourceOffset to .recording.json after
// each batch. On daemon restart, DetectAndRestart reads the persisted offset and
// does a catch-up read to recover entries written while the daemon was down.
type SessionWatcherManager struct {
	logger   *slog.Logger
	mu       sync.Mutex
	wg       sync.WaitGroup
	watchers map[string]*activeWatcher // keyed by session name
	stopped  bool                      // set by StopAll to prevent new watchers and defer re-insertion
	home     string                    // root for the session-file allow-list; see homeDir
}

// activeWatcher tracks a running TailWatcher goroutine.
type activeWatcher struct {
	cancel      context.CancelFunc
	sessionName string
	adapterName string
	sessionFile string
	cachePath   string
	ledgerPath  string
	startOffset int64 // byte offset in source file to resume from (0 for fresh start)
	startedAt   time.Time
}

// NewSessionWatcherManager creates a manager for tail-mode session watchers.
func NewSessionWatcherManager(logger *slog.Logger) *SessionWatcherManager {
	home, err := os.UserHomeDir()
	if err != nil {
		// an empty home makes every root empty, so SafeSessionFilePath
		// rejects everything — fail closed rather than watch unchecked paths
		logger.Warn("cannot determine home directory; session watching will reject all paths", "error", err)
	}
	return &SessionWatcherManager{
		logger:   logger,
		watchers: make(map[string]*activeWatcher),
		home:     home,
	}
}

// homeDir is the root the session-file allow-list is resolved against.
func (m *SessionWatcherManager) homeDir() string { return m.home }

// SetHomeDirForTest points the allow-list at a controlled home so a test can
// plant files under an adapter's real root layout without touching the
// developer's actual home directory.
func (m *SessionWatcherManager) SetHomeDirForTest(home string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.home = home
}

// StartWatch begins tailing a session file and writing entries to raw.jsonl.
// Called directly from IPC handler (bypasses work queue for fast startup).
// Idempotent: if already watching this session, returns nil.
func (m *SessionWatcherManager) StartWatch(
	sessionName, sessionFile, adapterName, ledgerPath, cachePath string,
) error {
	return m.startWatchAt(sessionName, sessionFile, adapterName, ledgerPath, cachePath, 0)
}

// startWatchAt is the internal start method that accepts a starting offset.
// offset=0 means start from current EOF (fresh start from IPC).
// offset>0 means catch up from that position first (daemon restart recovery).
func (m *SessionWatcherManager) startWatchAt(
	sessionName, sessionFile, adapterName, ledgerPath, cachePath string, offset int64,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stopped {
		return fmt.Errorf("session watcher manager is stopped")
	}

	if _, ok := m.watchers[sessionName]; ok {
		m.logger.Debug("session watcher already active", "session", sessionName)
		return nil
	}

	// basic validation: a session file is either an absolute path to tail, or
	// an opaque "<adapter>:<id>" handle for the adapters that read from a
	// database instead of a file (opencode, goose). Requiring a path here
	// rejected both of those outright, so recording never started for them
	// however correct their readers were.
	if !filepath.IsAbs(sessionFile) && !adapters.IsOpaqueSessionHandle(adapterName, sessionFile) {
		return fmt.Errorf("%w: %s gave %q, which is neither an absolute path nor an opaque handle",
			ErrSessionFileShape, adapterName, sessionFile)
	}

	// Both entry points funnel through here — IPC via StartWatch and doctor
	// via DetectAndRestart — so this is the one place that has to hold. The
	// daemon's IPC handler already ran the lexical allow-list check, but that
	// check cannot see a symlink inside an allowed root pointing somewhere it
	// should not; the tail loop below opens this path and uploads what it
	// reads. Resolve before trusting.
	// keep the requested path for the error message — SafeSessionFilePath
	// returns "" on refusal, and "refusing to watch \"\"" tells nobody anything
	safePath, err := adapters.SafeSessionFilePath(adapterName, sessionFile, m.homeDir())
	if err != nil {
		return fmt.Errorf("refusing to watch %q for %s: %w", sessionFile, adapterName, err)
	}
	sessionFile = safePath

	adapter, err := resolveAdapter(adapterName)
	if err != nil {
		return err
	}

	rawPath := filepath.Join(cachePath, "raw.jsonl")

	ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // G118: cancel is stored in activeWatcher.cancel, called by StopWatch/StopAll
	aw := &activeWatcher{
		cancel:      cancel,
		sessionName: sessionName,
		adapterName: adapterName,
		sessionFile: sessionFile,
		cachePath:   cachePath,
		ledgerPath:  ledgerPath,
		startOffset: offset,
		startedAt:   time.Now(),
	}
	m.watchers[sessionName] = aw

	m.wg.Add(1)
	go m.runWatcher(ctx, aw, adapter, rawPath)
	m.logger.Info("session watcher started",
		"session", sessionName,
		"adapter", adapterName,
		"file", sessionFile,
		"offset", offset,
	)
	return nil
}

// StopWatch stops tailing a session.
func (m *SessionWatcherManager) StopWatch(sessionName string) {
	m.mu.Lock()
	aw, ok := m.watchers[sessionName]
	if ok {
		delete(m.watchers, sessionName)
	}
	m.mu.Unlock()

	if ok {
		aw.cancel()
		m.logger.Info("session watcher stopped", "session", sessionName)
	}
}

// StopAll stops all active watchers and waits for goroutines to finish.
// Called during daemon shutdown.
func (m *SessionWatcherManager) StopAll() {
	m.mu.Lock()
	m.stopped = true
	watchers := make(map[string]*activeWatcher, len(m.watchers))
	for k, v := range m.watchers {
		watchers[k] = v
	}
	m.watchers = make(map[string]*activeWatcher)
	m.mu.Unlock()

	for _, aw := range watchers {
		aw.cancel()
	}
	m.wg.Wait()
}

// ActiveSessions returns the names of sessions currently being watched.
func (m *SessionWatcherManager) ActiveSessions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.watchers))
	for name := range m.watchers {
		names = append(names, name)
	}
	return names
}

// DetectAndRestart scans for .recording.json files with WatchMode:"tail" that
// aren't currently being watched, and restarts their watchers. Called by the
// daemon's doctor interval to handle daemon restarts.
//
// On restart, reads the persisted SourceOffset from .recording.json so the
// catch-up read recovers entries written while the daemon was down.
func (m *SessionWatcherManager) DetectAndRestart(ledgerPath string) int {
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return 0
	}

	m.mu.Lock()
	activeSet := make(map[string]bool, len(m.watchers))
	for name := range m.watchers {
		activeSet[name] = true
	}
	m.mu.Unlock()

	started := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionName := entry.Name()
		if activeSet[sessionName] {
			continue
		}

		recPath := filepath.Join(sessionsDir, sessionName, recordingMarker)
		data, err := os.ReadFile(recPath)
		if err != nil {
			continue
		}

		var state session.RecordingState
		if err := json.Unmarshal(data, &state); err != nil {
			continue
		}
		if state.WatchMode != "tail" || state.StoppedAt != nil {
			continue
		}
		if state.SessionFile == "" || state.AdapterName == "" {
			continue
		}
		// don't restart watchers for dead agents — let session_finalize handle them
		if !state.IsAgentAlive() {
			continue
		}

		cachePath := filepath.Join(sessionsDir, sessionName)
		// use persisted offset to catch up from where we left off
		if err := m.startWatchAt(sessionName, state.SessionFile, state.AdapterName, ledgerPath, cachePath, state.SourceOffset); err != nil {
			m.logger.Warn("failed to restart session watcher",
				"session", sessionName, "error", err)
			continue
		}
		started++
	}
	return started
}

// Cleanup stops watchers for sessions that have been stopped, whose
// .recording.json has been removed, or whose agent PID has died.
func (m *SessionWatcherManager) Cleanup() {
	m.mu.Lock()
	var toStop []string
	for name, aw := range m.watchers {
		recPath := filepath.Join(aw.cachePath, recordingMarker)
		data, err := os.ReadFile(recPath)
		if err != nil {
			toStop = append(toStop, name)
			continue
		}
		var state session.RecordingState
		if err := json.Unmarshal(data, &state); err != nil {
			continue
		}
		if state.StoppedAt != nil || !state.IsAgentAlive() {
			toStop = append(toStop, name)
		}
	}
	m.mu.Unlock()

	for _, name := range toStop {
		m.StopWatch(name)
	}
}

// runWatcher tails the session file and appends entries to raw.jsonl.
//
// On startup, if startOffset > 0, does a catch-up read to recover entries
// written between the persisted offset and current EOF (daemon restart case).
// Then starts live tailing for new entries via adapter.Watch().
func (m *SessionWatcherManager) runWatcher(
	ctx context.Context, aw *activeWatcher,
	adapter adapters.Adapter, rawPath string,
) {
	defer m.wg.Done()
	defer func() {
		m.mu.Lock()
		// skip cleanup if StopAll already ran (prevents re-inserting into cleared map)
		if !m.stopped {
			if current, ok := m.watchers[aw.sessionName]; ok && current == aw {
				delete(m.watchers, aw.sessionName)
			}
		}
		m.mu.Unlock()
	}()

	// Per ox-h20u: ALL raw.jsonl writes go through session.RawWriter. The
	// writer constructs and applies the three-layer redaction stack
	// (CommandRedactor → built-in Redactor → gitleaks extras). There is no
	// way to call WriteEntry without redaction running first. Adapters can
	// only emit RawEntry JSON on stdout — they have no write access to
	// raw.jsonl.
	rw, err := session.NewRawWriter(rawPath, "")
	if err != nil {
		m.logger.Error("failed to open raw.jsonl for writing",
			"session", aw.sessionName, "path", rawPath, "error", err)
		return
	}
	defer rw.Close()

	// cursor is where recording resumes from. It starts at the persisted
	// offset and must be carried through catch-up into the poll loop —
	// re-deriving it from aw.startOffset later would re-read and duplicate
	// everything catch-up just recovered.
	cursor := aw.startOffset

	// catch-up: read entries between the persisted offset and now
	if reader, ok := adapter.(adapters.IncrementalReader); ok && cursor > 0 {
		entries, newOffset, readErr := reader.ReadFromOffset(aw.sessionFile, cursor)
		switch {
		case readErr != nil:
			m.logger.Warn("catch-up read failed; continuing from the persisted offset",
				"session", aw.sessionName, "offset", cursor, "error", readErr)
		case len(entries) > 0:
			converted := session.ConvertRawEntries(entries)
			if writeErr := writeEntries(rw, converted); writeErr != nil {
				// the cursor must NOT advance past entries that never reached
				// the ledger: doing so marks them consumed and they are gone
				// for good. Leaving it where it is costs a re-read.
				m.logger.Error("catch-up write failed; leaving the cursor in place so the entries can be recovered",
					"session", aw.sessionName, "offset", cursor, "error", writeErr)
				return
			}
			cursor = newOffset
			m.persistOffset(aw, cursor, len(converted))
			m.logger.Info("catch-up read recovered entries",
				"session", aw.sessionName,
				"entries", len(entries),
				"from_offset", aw.startOffset,
				"to_offset", cursor,
			)
		}
	}

	// Record through the adapter's own incremental reader whenever it has one.
	//
	// The alternative, adapter.Watch, delivers entries with NO cursor, so any
	// resume offset has to be inferred from the file afterwards — and both
	// ways of being wrong lose data. Infer high (file size, or a boundary
	// scanned after the write) and a record the agent appended in between is
	// marked consumed but never written. Infer low and a restart re-reads and
	// duplicates. Database-backed adapters cannot even infer: their offset is
	// a row count nothing outside the adapter can compute.
	//
	// ReadFromOffset returns the cursor it actually consumed to, so there is
	// nothing to infer.
	if reader, ok := adapter.(adapters.IncrementalReader); ok {
		m.pollSession(ctx, aw, reader, rw, cursor)
		return
	}

	// Fallback for an adapter with no incremental reader: tail for entries and
	// accept that the session cannot be resumed. Every shipped adapter now
	// implements ReadFromOffset (enforced by tests/adapters), so this path is
	// for third-party adapters only.
	ch, err := adapter.Watch(ctx, aw.sessionFile)
	if err != nil {
		m.logger.Error("failed to start tail watcher",
			"session", aw.sessionName, "error", err)
		return
	}
	m.logger.Warn("adapter has no incremental reader; this session cannot resume after a daemon restart",
		"session", aw.sessionName, "adapter", aw.adapterName)

	for entry := range ch {
		converted := session.ConvertRawEntries([]adapters.RawEntry{entry})
		for i := range converted {
			if encErr := rw.WriteEntry(&converted[i]); encErr != nil {
				m.logger.Warn("failed to write entry to raw.jsonl",
					"session", aw.sessionName, "error", encErr)
			}
		}
		// no offset is persisted: Watch delivers entries with no cursor, and
		// every way of guessing one from the file loses data in one direction
		// or the other — see pollSession
	}
}

// pollInterval is how often a session is re-read to advance its resume cursor.
const pollInterval = 2 * time.Second

// pollSession records a session by repeatedly asking the adapter for
// everything after the cursor it last returned.
//
// The cursor comes from the adapter on every pass, so it always reflects what
// was actually consumed. That is the whole point: the previous code persisted
// the offset the watcher STARTED with, so a daemon restart re-read every entry
// written during the live phase and appended each one a second time.
func (m *SessionWatcherManager) pollSession(
	ctx context.Context, aw *activeWatcher,
	reader adapters.IncrementalReader, rw *session.RawWriter, startCursor int64,
) {
	offset := startCursor
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		entries, newOffset, err := reader.ReadFromOffset(aw.sessionFile, offset)
		if err != nil {
			m.logger.Warn("handle-based session read failed",
				"session", aw.sessionName, "adapter", aw.adapterName, "offset", offset, "error", err)
			continue
		}
		if len(entries) == 0 {
			// nothing new; do NOT persist, so a cursor that went backwards or
			// stalled cannot be written over a good one
			continue
		}

		// Check the cursor BEFORE writing. An adapter that returns rows without
		// advancing would otherwise have those rows appended to raw.jsonl on
		// every single poll — the duplication is in the ledger, not just in
		// the persisted offset, and refusing to persist afterwards does not
		// undo it. Nothing this loop can do makes such an adapter usable, so
		// stop rather than accumulate garbage.
		if newOffset <= offset {
			m.logger.Error("adapter returned entries without advancing its cursor; stopping this recording to avoid duplicating them",
				"session", aw.sessionName, "adapter", aw.adapterName,
				"offset", offset, "returned", newOffset, "entries", len(entries))
			return
		}

		converted := session.ConvertRawEntries(entries)
		if writeErr := writeEntries(rw, converted); writeErr != nil {
			// Advancing here would mark entries consumed that never reached
			// the ledger, and the adapter would resume past them — they are
			// unrecoverable. Stop with the cursor where it is so a restart
			// re-reads them; a duplicated batch is visible and fixable, a
			// silently dropped one is not.
			m.logger.Error("write to raw.jsonl failed; stopping this recording with the cursor unadvanced so the entries can be recovered",
				"session", aw.sessionName, "adapter", aw.adapterName, "offset", offset, "error", writeErr)
			return
		}

		offset = newOffset
		m.persistOffset(aw, offset, len(converted))
	}
}

// writeEntries writes every entry, returning the first failure. A partial batch is
// reported as a failure so the caller can decline to advance its cursor past
// entries the ledger never received.
func writeEntries(rw *session.RawWriter, entries []session.Entry) error {
	for i := range entries {
		if err := rw.WriteEntry(&entries[i]); err != nil {
			return err
		}
	}
	return nil
}

// persistOffset updates SourceOffset and EntryCount in .recording.json.
// Uses atomic write (temp file + rename) to avoid races with CLI writes.
// Best-effort: errors are logged but don't stop the watcher.
func (m *SessionWatcherManager) persistOffset(aw *activeWatcher, offset int64, entryDelta int) {
	recPath := filepath.Join(aw.cachePath, recordingMarker)
	data, err := os.ReadFile(recPath)
	if err != nil {
		return
	}
	var state session.RecordingState
	if err := json.Unmarshal(data, &state); err != nil {
		return
	}
	state.SourceOffset = offset
	state.EntryCount += entryDelta
	updated, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	// atomic write: write to temp file then rename to avoid partial writes
	// and reduce the race window with CLI writes to the same file
	tmpPath := recPath + ".tmp"
	if err := os.WriteFile(tmpPath, updated, 0600); err != nil {
		m.logger.Debug("failed to write temp offset file", "session", aw.sessionName, "error", err)
		return
	}
	if err := os.Rename(tmpPath, recPath); err != nil {
		m.logger.Debug("failed to rename temp offset file", "session", aw.sessionName, "error", err)
		_ = os.Remove(tmpPath)
	}
}

// resolveAdapter returns the adapter for the given name.
// Uses the adapter registry which discovers external adapter binaries.
func resolveAdapter(name string) (adapters.Adapter, error) {
	adapter, err := adapters.GetAdapter(name)
	if err != nil {
		return nil, fmt.Errorf("unknown adapter: %q", name)
	}
	return adapter, nil
}
