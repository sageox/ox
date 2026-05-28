package adapterruntime

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// ReadFunc reads new entries from offset and returns them with the new offset.
type ReadFunc func(sessionFile string, offset int64) ([]adapterprotocol.RawEntry, int64, error)

// FileWatcher watches session files for changes and pushes entry events.
// Thread-safe: multiple sessions can be watched concurrently.
type FileWatcher struct {
	writer  *Writer
	readFn  ReadFunc
	watcher *fsnotify.Watcher

	mu       sync.Mutex
	sessions map[string]*watchedSession // agentID -> session

	// debounce rapid writes (e.g., multiple JSONL lines in one flush)
	debounce time.Duration

	// detector is the optional terminal-error pattern matcher. When
	// non-nil and Enabled, every entries batch the watcher pushes is
	// also fed to the detector; a heartbeat goroutine fires confirmed
	// terminal_error events when their silence window elapses.
	detector      *TerminalDetector
	heartbeatStop chan struct{}
	heartbeatOnce sync.Once
}

type watchedSession struct {
	agentID     string
	sessionFile string
	offset      int64
	timer       *time.Timer
	// seq is the monotonically increasing batch number for this session.
	// Bumped before each entries event so terminal_error events can be
	// tagged with the batch they originated from.
	seq int64
}

// NewFileWatcher creates a watcher that pushes entry events via the writer.
// readFn is called to read new entries from the session file at a given offset.
func NewFileWatcher(writer *Writer, readFn ReadFunc) (*FileWatcher, error) {
	return NewFileWatcherWithDetector(writer, readFn, nil)
}

// NewFileWatcherWithDetector creates a watcher with an optional
// terminal-error detector. Passing a nil or empty detector is
// equivalent to NewFileWatcher.
func NewFileWatcherWithDetector(writer *Writer, readFn ReadFunc, detector *TerminalDetector) (*FileWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	fw := &FileWatcher{
		writer:   writer,
		readFn:   readFn,
		watcher:  w,
		sessions: make(map[string]*watchedSession),
		debounce: 100 * time.Millisecond,
		detector: detector,
	}
	go fw.loop()
	if detector.Enabled() {
		fw.heartbeatStop = make(chan struct{})
		go fw.detectorHeartbeat()
	}
	return fw, nil
}

// Watch starts watching a session file for the given agent.
func (fw *FileWatcher) Watch(agentID, sessionFile string, offset int64) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	// stop existing watch for this agent if any
	if existing, ok := fw.sessions[agentID]; ok {
		if existing.timer != nil {
			existing.timer.Stop()
		}
		// only remove from fsnotify if no other session uses the
		// existing file. The previous arg here was sessionFile (the NEW
		// file), which was wrong: when re-Watching agent A from file X
		// to file Y while another agent B watches Y, fileInUse(Y, A)
		// returns true and we'd skip Remove(X), orphaning fsnotify's
		// entry for X forever. Fixed to consult existing.sessionFile.
		if !fw.fileInUse(existing.sessionFile, agentID) {
			_ = fw.watcher.Remove(existing.sessionFile)
		}
	}

	fw.sessions[agentID] = &watchedSession{
		agentID:     agentID,
		sessionFile: sessionFile,
		offset:      offset,
	}

	return fw.watcher.Add(sessionFile)
}

// Unwatch stops watching for the given agent.
func (fw *FileWatcher) Unwatch(agentID string) {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	ws, ok := fw.sessions[agentID]
	if !ok {
		return
	}
	if ws.timer != nil {
		ws.timer.Stop()
	}
	delete(fw.sessions, agentID)

	// only remove from fsnotify if no other session uses this file
	if !fw.fileInUse(ws.sessionFile, "") {
		_ = fw.watcher.Remove(ws.sessionFile)
	}

	// drop any pending detector state so a re-Watch under the same
	// agentID does not inherit a half-confirmed match.
	if fw.detector != nil {
		fw.detector.Forget(agentID)
	}
}

// Close shuts down the file watcher.
func (fw *FileWatcher) Close() {
	fw.mu.Lock()
	for _, ws := range fw.sessions {
		if ws.timer != nil {
			ws.timer.Stop()
		}
	}
	fw.sessions = make(map[string]*watchedSession)
	fw.mu.Unlock()
	fw.heartbeatOnce.Do(func() {
		if fw.heartbeatStop != nil {
			close(fw.heartbeatStop)
		}
	})
	_ = fw.watcher.Close()
}

// fileInUse returns true if any session (other than excludeAgent) watches the file.
// caller must hold fw.mu.
func (fw *FileWatcher) fileInUse(file, excludeAgent string) bool {
	for id, ws := range fw.sessions {
		if id != excludeAgent && ws.sessionFile == file {
			return true
		}
	}
	return false
}

func (fw *FileWatcher) loop() {
	for {
		select {
		case event, ok := <-fw.watcher.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Write) {
				fw.scheduleRead(event.Name)
			}
		case err, ok := <-fw.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("file watcher error: %v", err)
		}
	}
}

// scheduleRead debounces reads for all sessions watching the given file.
func (fw *FileWatcher) scheduleRead(file string) {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	for _, ws := range fw.sessions {
		if ws.sessionFile != file {
			continue
		}
		ws := ws // capture for closure
		if ws.timer != nil {
			ws.timer.Reset(fw.debounce)
		} else {
			ws.timer = time.AfterFunc(fw.debounce, func() {
				fw.readAndPush(ws.agentID)
			})
		}
	}
}

func (fw *FileWatcher) readAndPush(agentID string) {
	fw.mu.Lock()
	ws, ok := fw.sessions[agentID]
	if !ok {
		fw.mu.Unlock()
		return
	}
	sessionFile := ws.sessionFile
	offset := ws.offset
	fw.mu.Unlock()

	entries, newOffset, err := fw.readFn(sessionFile, offset)
	if err != nil {
		log.Printf("file watcher read error for %s: %v", agentID, err)
		return
	}

	if len(entries) == 0 {
		return
	}

	// Bump per-session sequence under the lock so the entries event
	// and the detector's pending-match record agree on the batch
	// identifier the daemon will later use to gate finalize.
	fw.mu.Lock()
	var seq int64
	if ws, ok := fw.sessions[agentID]; ok {
		ws.offset = newOffset
		ws.timer = nil
		ws.seq++
		seq = ws.seq
	}
	fw.mu.Unlock()

	data, err := json.Marshal(adapterprotocol.EntriesEventData{
		Entries:   entries,
		NewOffset: newOffset,
		Seq:       seq,
	})
	if err != nil {
		log.Printf("file watcher marshal error: %v", err)
		return
	}

	// Ordering invariant: entries event MUST be pushed before the
	// detector observes the batch. The daemon's terminal-error
	// handler gates finalize on "entries with Seq <= EntrySeq have
	// been persisted", which is only true if the entries event hits
	// the wire first.
	fw.writer.PushEvent(adapterprotocol.Event{
		Event:   adapterprotocol.EventEntries,
		AgentID: agentID,
		Data:    data,
	})

	if fw.detector.Enabled() {
		// rawLines parallel-to-entries is not currently surfaced by the
		// ReadFunc contract; pass nil so structured-pass falls back to
		// inspecting RawEntry fields. Adapters wanting full JSON-path
		// access should extend the ReadFunc signature in a follow-up.
		fw.detector.OnBatch(agentID, entries, seq, nil)
	}
}

// detectorHeartbeat polls the detector every 2s for confirmed matches
// and pushes them to the daemon as terminal_error events. Runs until
// the watcher is closed.
func (fw *FileWatcher) detectorHeartbeat() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-fw.heartbeatStop:
			return
		case now := <-ticker.C:
			confirmed := fw.detector.Tick(now)
			for _, c := range confirmed {
				payload := c.ToTerminalErrorData()
				data, err := json.Marshal(payload)
				if err != nil {
					log.Printf("terminal_error marshal error: %v", err)
					continue
				}
				fw.writer.PushEvent(adapterprotocol.Event{
					Event:   adapterprotocol.EventTerminalError,
					AgentID: c.SessionID,
					Data:    data,
				})
			}
		}
	}
}
