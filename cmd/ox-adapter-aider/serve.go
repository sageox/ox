// serve.go handles the long-running serve mode for incremental session reading.
package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

type aiderSessionState struct {
	sessionFile string
	offset      int64
	sessionTS   time.Time
}

func handleServe(srv *adapterruntime.Server) {
	store := adapterruntime.NewSessionStore[aiderSessionState]()

	fw, err := adapterruntime.NewFileWatcher(srv.Writer(), func(file string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
		return readAiderFromOffset(file, offset)
	})
	if err != nil {
		log.Printf("file watcher unavailable: %v", err)
	}
	if fw != nil {
		defer fw.Close()
	}

	srv.OnFindSession(func(ctx context.Context, p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
		sessionFile, err := findAiderSession(p.RepoRoot, p.AgentID, p.Since, p.AgentSessionID)
		if err != nil {
			return nil, err
		}

		var offset int64
		if info, err := os.Stat(sessionFile); err == nil {
			offset = info.Size()
		}

		// resolve the last session timestamp so incremental reads inherit it
		sessionTS := resolveLatestSessionTS(sessionFile, offset)

		store.Set(p.AgentID, aiderSessionState{sessionFile: sessionFile, offset: offset, sessionTS: sessionTS})

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

		entries, newOffset, err := readAiderFromOffsetWithTS(state.sessionFile, p.Offset, state.sessionTS)
		if err != nil {
			return nil, err
		}

		store.Set(p.AgentID, aiderSessionState{sessionFile: state.sessionFile, offset: newOffset, sessionTS: state.sessionTS})

		return &adapterprotocol.ReadFromOffsetResult{Entries: entries, NewOffset: newOffset}, nil
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
