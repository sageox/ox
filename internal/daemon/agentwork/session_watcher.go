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
	"github.com/sageox/ox/pkg/adapterruntime"
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

	// catch-up: read entries between persisted offset and current EOF
	if reader, ok := adapter.(adapters.IncrementalReader); ok && aw.startOffset > 0 {
		entries, newOffset, readErr := reader.ReadFromOffset(aw.sessionFile, aw.startOffset)
		if readErr != nil {
			m.logger.Warn("catch-up read failed, starting from EOF",
				"session", aw.sessionName, "offset", aw.startOffset, "error", readErr)
		} else if len(entries) > 0 {
			converted := session.ConvertRawEntries(entries)
			for i := range converted {
				if encErr := rw.WriteEntry(&converted[i]); encErr != nil {
					m.logger.Warn("failed to write catch-up entry to raw.jsonl",
						"session", aw.sessionName, "error", encErr)
				}
			}
			m.persistOffset(aw, newOffset, len(converted))
			m.logger.Info("catch-up read recovered entries",
				"session", aw.sessionName,
				"entries", len(entries),
				"from_offset", aw.startOffset,
				"to_offset", newOffset,
			)
		}
	}

	// Handle-based adapters (opencode, goose) have no file to watch — they read
	// from a database, and their offset is a row count only the adapter can
	// compute. Polling their own incremental reader is the only way this loop
	// can hold an offset that actually advances; adapter.Watch emits entries
	// with no cursor, so persisting anything alongside it was guesswork, and
	// persisting the START offset meant a restart re-read every live entry and
	// appended it a second time.
	if adapters.IsOpaqueSessionHandle(aw.adapterName, aw.sessionFile) {
		reader, ok := adapter.(adapters.IncrementalReader)
		if !ok {
			m.logger.Error("handle-based adapter has no incremental reader; cannot record",
				"session", aw.sessionName, "adapter", aw.adapterName)
			return
		}
		m.pollHandleSession(ctx, aw, reader, rw)
		return
	}

	// live tail: watch for new entries from current EOF onward
	ch, err := adapter.Watch(ctx, aw.sessionFile)
	if err != nil {
		m.logger.Error("failed to start tail watcher",
			"session", aw.sessionName, "error", err)
		return
	}

	for entry := range ch {
		converted := session.ConvertRawEntries([]adapters.RawEntry{entry})
		for i := range converted {
			if encErr := rw.WriteEntry(&converted[i]); encErr != nil {
				m.logger.Warn("failed to write entry to raw.jsonl",
					"session", aw.sessionName, "error", encErr)
			}
		}

		// persist offset after each batch so daemon restart can resume.
		//
		// It must be the end of the last COMPLETE record, not the file size.
		// The agent appends while we read, so EOF is frequently the middle of
		// a record it is still writing; persisting that lands the next resume
		// inside a record, TailJSONL refuses the non-boundary offset and
		// restarts from zero, and the whole transcript is appended a second
		// time. Rounding down to the last newline costs at most a re-read of
		// the trailing partial record, which yields nothing until it is
		// complete.
		//
		if boundary, boundaryErr := adapterruntime.LastRecordBoundary(aw.sessionFile); boundaryErr == nil {
			m.persistOffset(aw, boundary, len(converted))
		}
	}
}

// handlePollInterval is how often a database-backed session is re-read. These
// adapters have no filesystem event to wake on, so this is a straight poll.
const handlePollInterval = 2 * time.Second

// pollHandleSession records a database-backed session (opencode, goose) by
// repeatedly asking the adapter for everything after the cursor it last
// returned.
//
// The cursor comes from the adapter on every pass, so it always reflects what
// was actually consumed. That is the whole point: the previous code persisted
// the offset the watcher STARTED with, so a daemon restart re-read every entry
// written during the live phase and appended each one a second time.
func (m *SessionWatcherManager) pollHandleSession(
	ctx context.Context, aw *activeWatcher,
	reader adapters.IncrementalReader, rw *session.RawWriter,
) {
	offset := aw.startOffset
	ticker := time.NewTicker(handlePollInterval)
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

		converted := session.ConvertRawEntries(entries)
		for i := range converted {
			if writeErr := rw.WriteEntry(&converted[i]); writeErr != nil {
				m.logger.Warn("failed to write entry to raw.jsonl",
					"session", aw.sessionName, "error", writeErr)
			}
		}

		// only advance on forward progress — an adapter that reports a cursor
		// at or behind where we asked would otherwise make the next poll
		// re-emit the same rows forever
		if newOffset > offset {
			offset = newOffset
			m.persistOffset(aw, offset, len(converted))
		} else {
			m.logger.Warn("handle-based adapter returned entries without advancing its cursor",
				"session", aw.sessionName, "adapter", aw.adapterName,
				"offset", offset, "returned", newOffset, "entries", len(entries))
		}
	}
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
