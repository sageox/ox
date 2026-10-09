package main

import (
	"context"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

// handleServe is intentionally stateless. The host passes the validated
// source path and byte offset on every read, so concurrent Cursor Sessions do
// not share a mutable cursor and restart behavior matches one-shot dispatch.
func handleServe(srv *adapterruntime.Server) {
	srv.OnFindSession(func(_ context.Context, p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
		return handleFindSession(p)
	})
	srv.OnReadFromOffset(func(_ context.Context, p adapterprotocol.ReadFromOffsetParams) (*adapterprotocol.ReadFromOffsetResult, error) {
		return handleReadFromOffset(p)
	})
	srv.OnEndSession(func(_ context.Context, _ adapterprotocol.EndSessionParams) error {
		return nil
	})
	srv.Serve()
}
