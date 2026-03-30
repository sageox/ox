// Package state defines the application state for the dashboard TUI and the
// read-only interface panes use to access that state without importing app.
package state

import (
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/dashboard/domain"
)

// ReadOnlyStore is the pane-facing view of application state.
// It intentionally exposes no mutation methods so panes cannot modify shared
// state directly — all mutations flow through tea.Cmd messages back to the app.
type ReadOnlyStore interface {
	// Nav returns the current navigation tree nodes, in display order.
	Nav() []domain.NavNode

	// NavCursor returns the index of the currently focused nav node.
	NavCursor() int

	// Timeline returns the current timeline entries, ordered newest first.
	Timeline() []domain.TimelineEntry

	// TimelineCursor returns the index of the currently selected timeline entry.
	TimelineCursor() int

	// Inspector returns the entity currently shown in the inspector pane.
	// Returns an InspectorTarget with Kind == TargetNone when nothing is selected.
	Inspector() domain.InspectorTarget

	// Health returns the current overall system health level.
	Health() domain.HealthLevel

	// StatusMessage returns a short human-readable status line for the status bar.
	// Empty string means no override; the status bar falls back to health level text.
	StatusMessage() string

	// Loading reports whether the dashboard is still waiting for the initial
	// data fetch to complete. Panes may render a placeholder while true.
	Loading() bool

	// GetDaemonStatus returns the raw daemon status, or nil when unavailable.
	// Panes that need to distinguish "daemon offline" from "no data yet" use this.
	GetDaemonStatus() *daemon.StatusData

	// ActiveMurmurCoworkers returns the count of unique AgentIDs with murmurs in the last 30 min.
	ActiveMurmurCoworkers() int

	// ActiveMurmurTeams returns the count of unique team slugs from murmurs in the last 30 min.
	ActiveMurmurTeams() int
}

// Ensure *Store satisfies the pane-facing interface at compile time.
var _ ReadOnlyStore = (*Store)(nil)

// Nav builds the flat navigation node list from the current store state.
// Sections are always present; child rows are only included when the section
// has data and is considered expanded by the root model.
func (s *Store) Nav() []domain.NavNode {
	return BuildNav(s)
}

// NavCursor returns the current nav cursor position.
func (s *Store) NavCursor() int { return s.NavCursorPos }

// Timeline returns timeline entries derived from the current store state.
func (s *Store) Timeline() []domain.TimelineEntry {
	return ActivityEntries(s)
}

// TimelineCursor returns the current timeline cursor position.
func (s *Store) TimelineCursor() int { return s.TimelineCursorPos }

// Inspector returns the currently selected entity, or a zero value with
// Kind == TargetNone when nothing is selected.
func (s *Store) Inspector() domain.InspectorTarget {
	if s.Selected == nil {
		return domain.InspectorTarget{Kind: domain.TargetNone}
	}
	return *s.Selected
}

// Health computes the overall health level from daemon state.
func (s *Store) Health() domain.HealthLevel {
	return DaemonHealthLevel(s)
}

// StatusMessage returns the current status bar override text.
func (s *Store) StatusMessage() string { return s.StatusMsg }

// Loading reports whether the initial data load is still in flight.
func (s *Store) Loading() bool { return s.IsLoading }
