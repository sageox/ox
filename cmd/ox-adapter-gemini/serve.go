// serve.go handles serve mode for incremental session reading.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

type geminiSessionState struct {
	sessionFile string
	entryCount  int64
}

func handleServe(srv *adapterruntime.Server) {
	store := adapterruntime.NewSessionStore[*geminiSessionState]()

	srv.OnFindSession(func(ctx context.Context, p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
		sessionFile, err := findGeminiSession(p.RepoRoot, p.AgentID, p.Since, p.AgentSessionID)
		if err != nil {
			return nil, err
		}

		// determine initial entry count
		var offset int64
		if data, err := os.ReadFile(sessionFile); err == nil {
			if entries, _, err := parseGeminiSession(data); err == nil {
				offset = int64(len(entries))
			}
		}

		store.Set(p.AgentID, &geminiSessionState{sessionFile: sessionFile, entryCount: offset})

		return &adapterprotocol.FindSessionResult{SessionFile: sessionFile, Offset: offset}, nil
	})

	srv.OnReadFromOffset(func(ctx context.Context, p adapterprotocol.ReadFromOffsetParams) (*adapterprotocol.ReadFromOffsetResult, error) {
		state, ok := store.Get(p.AgentID)
		if !ok {
			return nil, adapterruntime.ErrSessionNotFound
		}

		data, err := os.ReadFile(state.sessionFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read session file: %w", err)
		}

		allEntries, _, err := parseGeminiSession(data)
		if err != nil {
			return nil, err
		}

		total := int64(len(allEntries))
		if p.Offset >= total {
			return &adapterprotocol.ReadFromOffsetResult{Entries: nil, NewOffset: total}, nil
		}

		newEntries := allEntries[p.Offset:]
		state.entryCount = total

		return &adapterprotocol.ReadFromOffsetResult{Entries: newEntries, NewOffset: total}, nil
	})

	srv.OnEndSession(func(ctx context.Context, p adapterprotocol.EndSessionParams) error {
		store.Delete(p.AgentID)
		return nil
	})

	srv.Serve()
}
