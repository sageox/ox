package main

import (
	"context"
	"fmt"
	"log"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

// sessionState is stored by value (not pointer) in SessionStore to prevent
// races — sync.Map returns a copy, so mutations require an explicit Set() call.
type sessionState struct {
	sessionFile string
	offset      int64
}

func handleServe(srv *adapterruntime.Server) {
	store := adapterruntime.NewSessionStore[sessionState]()

	detector := registerTerminalDetector()
	fw, err := adapterruntime.NewFileWatcherWithDetector(srv.Writer(), func(file string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
		return readFromOffset(file, offset)
	}, detector)
	if err != nil {
		log.Printf("file watcher unavailable: %v", err)
	}
	if fw != nil {
		defer fw.Close()
	}

	srv.OnFindSession(func(ctx context.Context, p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
		sessionFile, offset, err := findSessionFile(p.RepoRoot, p.AgentID, p.Since, p.AgentSessionID)
		if err != nil {
			return nil, fmt.Errorf("session not found: %w", err)
		}

		store.Set(p.AgentID, sessionState{
			sessionFile: sessionFile,
			offset:      offset,
		})

		if fw != nil {
			if werr := fw.Watch(p.AgentID, sessionFile, offset); werr != nil {
				log.Printf("file watcher: failed to watch %s: %v", sessionFile, werr)
			}
		}

		return &adapterprotocol.FindSessionResult{
			SessionFile: sessionFile,
			Offset:      offset,
		}, nil
	})

	srv.OnReadFromOffset(func(ctx context.Context, p adapterprotocol.ReadFromOffsetParams) (*adapterprotocol.ReadFromOffsetResult, error) {
		state, ok := store.Get(p.AgentID)
		if !ok {
			return nil, adapterruntime.ErrSessionNotFound
		}

		entries, newOffset, err := readFromOffset(state.sessionFile, p.Offset)
		if err != nil {
			return nil, err
		}

		store.Set(p.AgentID, sessionState{
			sessionFile: state.sessionFile,
			offset:      newOffset,
		})

		return &adapterprotocol.ReadFromOffsetResult{
			Entries:   entries,
			NewOffset: newOffset,
		}, nil
	})

	srv.OnEndSession(func(ctx context.Context, p adapterprotocol.EndSessionParams) error {
		if fw != nil {
			fw.Unwatch(p.AgentID)
		}
		store.Delete(p.AgentID)
		return nil
	})

	srv.Serve()
}
