// serve.go handles the long-running serve mode for incremental session reading.
package main

import (
	"context"
	"fmt"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

type gooseSessionState struct {
	sessionID string
	offset    int64
}

func handleServe(srv *adapterruntime.Server) {
	store := adapterruntime.NewSessionStore[gooseSessionState]()

	srv.OnFindSession(func(ctx context.Context, p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
		result, err := handleFindSession(p)
		if err != nil {
			return nil, fmt.Errorf("find Goose session: %w", err)
		}

		store.Set(p.AgentID, gooseSessionState{
			sessionID: extractSessionID(result.SessionFile),
			offset:    result.Offset,
		})

		return result, nil
	})

	srv.OnReadFromOffset(func(ctx context.Context, p adapterprotocol.ReadFromOffsetParams) (*adapterprotocol.ReadFromOffsetResult, error) {
		state, ok := store.Get(p.AgentID)
		if !ok {
			return nil, adapterruntime.ErrSessionNotFound
		}

		p.SessionFile = sessionFilePrefix + state.sessionID

		result, err := handleReadFromOffset(p)
		if err != nil {
			return nil, fmt.Errorf("read Goose session from offset: %w", err)
		}

		store.Set(p.AgentID, gooseSessionState{sessionID: state.sessionID, offset: result.NewOffset})
		return result, nil
	})

	srv.OnEndSession(func(ctx context.Context, p adapterprotocol.EndSessionParams) error {
		store.Delete(p.AgentID)
		return nil
	})

	srv.Serve()
}
