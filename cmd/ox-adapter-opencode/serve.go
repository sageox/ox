// serve.go handles the long-running serve mode for incremental session reading.
package main

import (
	"context"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

type ocSessionState struct {
	sessionID string
	offset    int64
}

func handleServe(srv *adapterruntime.Server) {
	store := adapterruntime.NewSessionStore[ocSessionState]()

	srv.OnFindSession(func(ctx context.Context, p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
		result, err := handleFindSession(p)
		if err != nil {
			return nil, err
		}

		sessionID := extractSessionID(result.SessionFile)
		store.Set(p.AgentID, ocSessionState{sessionID: sessionID, offset: result.Offset})

		return result, nil
	})

	srv.OnReadFromOffset(func(ctx context.Context, p adapterprotocol.ReadFromOffsetParams) (*adapterprotocol.ReadFromOffsetResult, error) {
		state, ok := store.Get(p.AgentID)
		if !ok {
			return nil, adapterruntime.ErrSessionNotFound
		}

		sessionFile := "opencode:" + state.sessionID
		p.SessionFile = sessionFile

		result, err := handleReadFromOffset(p)
		if err != nil {
			return nil, err
		}

		store.Set(p.AgentID, ocSessionState{sessionID: state.sessionID, offset: result.NewOffset})
		return result, nil
	})

	srv.OnEndSession(func(ctx context.Context, p adapterprotocol.EndSessionParams) error {
		store.Delete(p.AgentID)
		return nil
	})

	srv.Serve()
}
