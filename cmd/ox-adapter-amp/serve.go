// serve.go handles the long-running serve mode for incremental session reading.
package main

import (
	"context"
	"log"
	"os"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

type ampSessionState struct {
	sessionFile string
	offset      int64
}

func handleServe(srv *adapterruntime.Server) {
	store := adapterruntime.NewSessionStore[ampSessionState]()

	fw, err := adapterruntime.NewFileWatcher(srv.Writer(), func(file string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
		return readAmpFromOffset(file, offset)
	})
	if err != nil {
		log.Printf("file watcher unavailable: %v", err)
	}
	if fw != nil {
		defer fw.Close()
	}

	srv.OnFindSession(func(ctx context.Context, p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
		sessionFile, err := findAmpSession(p.RepoRoot, p.AgentID, p.Since, p.AgentSessionID)
		if err != nil {
			return nil, err
		}

		var offset int64
		if info, err := os.Stat(sessionFile); err == nil {
			offset = info.Size()
		}

		store.Set(p.AgentID, ampSessionState{sessionFile: sessionFile, offset: offset})

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

		entries, newOffset, err := readAmpFromOffset(state.sessionFile, p.Offset)
		if err != nil {
			return nil, err
		}

		store.Set(p.AgentID, ampSessionState{sessionFile: state.sessionFile, offset: newOffset})

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
