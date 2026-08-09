// serve.go — serve mode handler for codex adapter.
package main

import (
	"context"
	"log"
	"os"
	"sync"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

type codexSessionState struct {
	sessionFile string
	offset      int64
}

// pendingCallStore carries mergeToolEntries' pending-call state across
// incremental reads, keyed by session file. A tool call read in one window
// (fsnotify batch or an OnReadFromOffset poll) commonly has its result land
// in a later window — the ReadFunc callback FileWatcher invokes only knows
// the file path, not an agent ID, so file path is the shared key both the
// watcher and OnReadFromOffset use below. Locking wraps the whole merge so
// two reads of the same file can't interleave their pending-map mutations.
type pendingCallStore struct {
	mu      sync.Mutex
	pending map[string]map[string]adapterprotocol.RawEntry // sessionFile -> call_id -> call entry
}

func newPendingCallStore() *pendingCallStore {
	return &pendingCallStore{pending: make(map[string]map[string]adapterprotocol.RawEntry)}
}

func (s *pendingCallStore) merge(sessionFile string, entries []adapterprotocol.RawEntry) []adapterprotocol.RawEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[sessionFile]
	if !ok {
		p = make(map[string]adapterprotocol.RawEntry)
		s.pending[sessionFile] = p
	}
	return mergeToolEntries(entries, p)
}

// forget drops pending-call state for a session file once it's no longer
// being watched, so a long-running daemon doesn't accumulate state for
// sessions that have ended.
func (s *pendingCallStore) forget(sessionFile string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, sessionFile)
}

func handleServe(srv *adapterruntime.Server) {
	store := adapterruntime.NewSessionStore[*codexSessionState]()
	pendingCalls := newPendingCallStore()

	fw, err := adapterruntime.NewFileWatcher(srv.Writer(), func(file string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
		entries, newOffset, err := readCodexFromOffset(file, offset)
		if err != nil {
			return nil, offset, err
		}
		return pendingCalls.merge(file, entries), newOffset, nil
	})
	if err != nil {
		log.Printf("file watcher unavailable: %v", err)
	}
	if fw != nil {
		defer fw.Close()
	}

	srv.OnFindSession(func(ctx context.Context, p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
		sessionFile, err := findCodexSession(p.RepoRoot, p.AgentID, p.Since, p.AgentSessionID)
		if err != nil {
			return nil, err
		}

		var offset int64
		if info, err := os.Stat(sessionFile); err == nil {
			offset = info.Size()
		}

		store.Set(p.AgentID, &codexSessionState{sessionFile: sessionFile, offset: offset})

		if fw != nil {
			if werr := fw.Watch(p.AgentID, sessionFile, offset); werr != nil {
				log.Printf("file watcher: failed to watch %s: %v", sessionFile, werr)
			}
		}

		return &adapterprotocol.FindSessionResult{SessionFile: sessionFile, Offset: offset}, nil
	})

	srv.OnReadFromOffset(func(ctx context.Context, p adapterprotocol.ReadFromOffsetParams) (*adapterprotocol.ReadFromOffsetResult, error) {
		state, ok := store.Get(p.AgentID)
		if !ok {
			return nil, adapterruntime.ErrSessionNotFound
		}

		entries, newOffset, err := readCodexFromOffset(state.sessionFile, p.Offset)
		if err != nil {
			return nil, err
		}

		merged := pendingCalls.merge(state.sessionFile, entries)
		state.offset = newOffset

		return &adapterprotocol.ReadFromOffsetResult{Entries: merged, NewOffset: newOffset}, nil
	})

	srv.OnEndSession(func(ctx context.Context, p adapterprotocol.EndSessionParams) error {
		if fw != nil {
			fw.Unwatch(p.AgentID)
		}
		if state, ok := store.Get(p.AgentID); ok {
			pendingCalls.forget(state.sessionFile)
		}
		store.Delete(p.AgentID)
		return nil
	})

	srv.Serve()
}
