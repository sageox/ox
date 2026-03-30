// Package state holds all dashboard domain data as a plain value type and
// implements the ReadOnlyStore interface the pane layer depends on.
//
// The root BubbleTea model owns the single Store instance and replaces it
// atomically on every update via the pure functions in mutations.go — there
// is no shared mutable state.
package state

import (
	"time"

	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/session"
)

// Store holds the current dashboard snapshot. It is a value type; callers
// receive a new copy from each mutation function rather than mutating in place.
type Store struct {
	DaemonStatus     *daemon.StatusData
	DaemonErr        error
	DaemonLoadedAt   time.Time

	Sessions         []session.SessionInfo
	SessionsErr      error
	SessionsLoadedAt time.Time

	Murmurs    []domain.MurmurEntry
	MurmursErr error

	Discussions    []domain.TeamDiscussion
	DiscussionsErr error

	Selected *domain.InspectorTarget

	// nav and timeline cursor positions — owned by the root model, not panes.
	NavCursorPos      int
	TimelineCursorPos int

	// StatusMsg overrides the status bar text when non-empty.
	StatusMsg string

	// Loading is true until the first successful daemon status response arrives.
	IsLoading bool

	// Generation is incremented on every data refresh cycle so stale async
	// responses can be detected and dropped.
	Generation int
}

// Accessor helpers used by selectors and tests. These keep direct field access
// out of selector code so renaming fields is a one-place change.

func (s *Store) GetDaemonStatus() *daemon.StatusData     { return s.DaemonStatus }
func (s *Store) GetSessions() []session.SessionInfo      { return s.Sessions }
func (s *Store) GetMurmurs() []domain.MurmurEntry        { return s.Murmurs }
func (s *Store) GetDiscussions() []domain.TeamDiscussion { return s.Discussions }
