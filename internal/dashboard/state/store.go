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
	DaemonStatus   *daemon.StatusData
	DaemonErr      error
	DaemonLoadedAt time.Time

	Sessions         []session.SessionInfo
	SessionsErr      error
	SessionsLoadedAt time.Time

	Murmurs    []domain.MurmurEntry
	MurmursErr error

	Discussions    []domain.TeamDiscussion
	DiscussionsErr error

	Instances    []daemon.InstanceInfo
	InstancesErr error

	StoredErrors    []daemon.StoredError
	StoredErrorsErr error

	TeamContexts    []domain.TeamContextEntry
	TeamContextsErr error

	CodeIndexStats    *daemon.CodeDBStats
	CodeIndexStatsErr error

	WhisperHistory    []domain.WhisperHistoryEntry
	WhisperHistoryErr error

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

	// MurmurTopic restricts the timeline/nav to a specific murmur topic.
	// Empty string means all topics are shown.
	MurmurTopic domain.MurmurTopicFilter

	// MurmurQuery holds the current inline search query for murmur content.
	// Empty string means no active search.
	MurmurQuery string

	// MurmurQueryOpen tracks whether the inline search input is currently open.
	MurmurQueryOpen bool
}

// Accessor helpers used by selectors and tests. These keep direct field access
// out of selector code so renaming fields is a one-place change.

func (s *Store) GetDaemonStatus() *daemon.StatusData     { return s.DaemonStatus }
func (s *Store) GetSessions() []session.SessionInfo      { return s.Sessions }
func (s *Store) GetMurmurs() []domain.MurmurEntry        { return s.Murmurs }
func (s *Store) GetDiscussions() []domain.TeamDiscussion { return s.Discussions }

// GetInstances returns the current AI coworker instance list.
func (s *Store) GetInstances() []daemon.InstanceInfo { return s.Instances }

// GetStoredErrors returns the current unviewed stored error list.
func (s *Store) GetStoredErrors() []daemon.StoredError { return s.StoredErrors }

// GetTeamContexts returns the current team context metadata list.
func (s *Store) GetTeamContexts() []domain.TeamContextEntry { return s.TeamContexts }

// GetCodeIndexStats returns the current code index statistics, or nil if unavailable.
func (s *Store) GetCodeIndexStats() *daemon.CodeDBStats { return s.CodeIndexStats }

// GetWhisperHistory returns the current whisper history entries.
func (s *Store) GetWhisperHistory() []domain.WhisperHistoryEntry { return s.WhisperHistory }

// ActiveMurmurCoworkers implements ReadOnlyStore.
func (s *Store) ActiveMurmurCoworkers() int { return ActiveMurmurCoworkers(s) }

// ActiveMurmurTeams implements ReadOnlyStore.
func (s *Store) ActiveMurmurTeams() int { return ActiveMurmurTeams(s) }

// MurmurFilter implements ReadOnlyStore.
func (s *Store) MurmurFilter() domain.MurmurTopicFilter { return s.MurmurTopic }

// MurmurSearch implements ReadOnlyStore.
func (s *Store) MurmurSearch() string { return s.MurmurQuery }

// MurmurSearchActive implements ReadOnlyStore.
func (s *Store) MurmurSearchActive() bool { return s.MurmurQueryOpen }
