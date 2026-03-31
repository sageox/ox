// Package app contains the BubbleTea application layer for the dashboard:
// messages, key bindings, layout geometry, and focus management.
//
// The app package sits between the domain types and the TUI model/view code.
// It imports domain but not effects, so data-fetching stays out of the event loop.
package app

import "github.com/sageox/ox/internal/dashboard/domain"

// ── Murmur filter messages ────────────────────────────────────────────────────

// MurmurFilterMsg sets the active murmur topic filter.
type MurmurFilterMsg struct {
	Filter domain.MurmurTopicFilter
}

// MurmurSearchOpenMsg opens the inline murmur search input.
type MurmurSearchOpenMsg struct{}

// MurmurSearchCloseMsg closes the inline murmur search input and clears the query.
type MurmurSearchCloseMsg struct{}

// MurmurSearchQueryMsg updates the inline murmur search query.
type MurmurSearchQueryMsg struct {
	Query string
}

// ── Intent messages (user-initiated actions) ─────────────────────────────────
// These are dispatched by key handlers and translated into state changes by the
// root model's Update function.

// FocusNextMsg cycles pane focus forward (Tab).
type FocusNextMsg struct{}

// FocusPrevMsg cycles pane focus backward (Shift+Tab).
type FocusPrevMsg struct{}

// FocusPaneMsg jumps directly to a specific pane.
type FocusPaneMsg struct{ Target FocusTarget }

// NavCursorUpMsg moves the nav tree cursor up one row.
type NavCursorUpMsg struct{}

// NavCursorDownMsg moves the nav tree cursor down one row.
type NavCursorDownMsg struct{}

// NavSelectMsg confirms the currently highlighted nav node.
type NavSelectMsg struct{}

// NavExpandMsg toggles the expansion state of the highlighted nav node.
type NavExpandMsg struct{}

// TimelineCursorUpMsg moves the timeline cursor up one row.
type TimelineCursorUpMsg struct{}

// TimelineCursorDownMsg moves the timeline cursor down one row.
type TimelineCursorDownMsg struct{}

// TimelineSelectMsg selects a specific timeline entry by ID (e.g., on mouse click).
type TimelineSelectMsg struct{ EntryID string }

// RefreshMsg triggers a full data reload from the daemon and ledger.
type RefreshMsg struct{}

// QuitMsg requests a clean shutdown of the TUI.
type QuitMsg struct{}

// ShowHelpMsg opens the help overlay.
type ShowHelpMsg struct{}

// HideHelpMsg closes the help overlay.
type HideHelpMsg struct{}

// ── State-change messages (pane → root model) ────────────────────────────────

// SelectionChangedMsg is emitted by nav and timeline panes when the highlighted
// row changes, so the inspector pane can update its content.
type SelectionChangedMsg struct {
	Target *domain.InspectorTarget
}

// OpenBrowserMsg requests that the given URL be opened in the system browser.
// Handled by the root model as a fire-and-forget side effect.
type OpenBrowserMsg struct {
	URL string
}
