// Package effects defines the interface for dashboard side effects (daemon IPC,
// session store queries) and provides the production implementation.
//
// Keeping effects behind an interface allows unit tests to inject fakes without
// a running daemon or real ledger on disk.
//
// The full production implementation lives in cli_client.go. This file retains
// only the Client interface definition so it acts as the package's single import
// point for callers that only need the type.
package effects

import (
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/session"
)

// Client is the contract the dashboard uses to fetch live data.
// All methods are synchronous; callers must run them inside tea.Cmd goroutines
// so the TUI event loop never blocks.
type Client interface {
	// GetDaemonStatus returns the current daemon status snapshot.
	// Never returns an error for "daemon not running" — it returns a StatusData
	// with Running=false instead, so callers can distinguish gracefully.
	GetDaemonStatus() (*daemon.StatusData, error)

	// ListSessions returns recent sessions from the project ledger.
	// Returns nil, nil when no ledger path is available.
	ListSessions() ([]session.SessionInfo, error)

	// ListMurmurs returns murmur entries from team context workspace paths.
	// Returns nil, nil on any filesystem error.
	ListMurmurs() ([]domain.MurmurEntry, error)

	// ListTeamDiscussions returns team discussion preview entries from memory/
	// directories in team context paths.
	// Returns nil, nil on any filesystem error.
	ListTeamDiscussions() ([]domain.TeamDiscussion, error)
}
