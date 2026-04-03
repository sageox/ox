package main

import (
	"context"
	"fmt"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

type sessionState struct {
	sessionFile string
	offset      int64
}

func handleServe(srv *adapterruntime.Server) {
	store := adapterruntime.NewSessionStore[*sessionState]()

	srv.OnFindSession(func(ctx context.Context, p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
		sessionFile, offset, err := findSessionFile(p.RepoRoot, p.AgentID, p.Since, p.AgentSessionID)
		if err != nil {
			return nil, fmt.Errorf("session not found: %w", err)
		}

		store.Set(p.AgentID, &sessionState{
			sessionFile: sessionFile,
			offset:      offset,
		})

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

		state.offset = newOffset

		return &adapterprotocol.ReadFromOffsetResult{
			Entries:   entries,
			NewOffset: newOffset,
		}, nil
	})

	srv.OnEndSession(func(ctx context.Context, p adapterprotocol.EndSessionParams) error {
		store.Delete(p.AgentID)
		return nil
	})

	srv.Serve()
}
