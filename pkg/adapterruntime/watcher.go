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
}

type watchedSession struct {
	agentID     string
	sessionFile string
	offset      int64
	timer       *time.Timer
}

// NewFileWatcher creates a watcher that pushes entry events via the writer.
// readFn is called to read new entries from the session file at a given offset.
func NewFileWatcher(writer *Writer, readFn ReadFunc) (*FileWatcher, error) {
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
	}
	go fw.loop()
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
		// only remove from fsnotify if no other session uses this file
		if !fw.fileInUse(sessionFile, agentID) {
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

	fw.mu.Lock()
	if ws, ok := fw.sessions[agentID]; ok {
		ws.offset = newOffset
		ws.timer = nil
	}
	fw.mu.Unlock()

	data, err := json.Marshal(adapterprotocol.EntriesEventData{
		Entries:   entries,
		NewOffset: newOffset,
	})
	if err != nil {
		log.Printf("file watcher marshal error: %v", err)
		return
	}

	fw.writer.PushEvent(adapterprotocol.Event{
		Event:   "entries",
		AgentID: agentID,
		Data:    data,
	})
}
