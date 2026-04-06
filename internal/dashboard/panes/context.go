package panes

import (
	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/dashboard/state"
)

// Context is the read-only snapshot passed to every pane on each Update and
// View call. It gives panes access to the current application state without
// coupling them to the app package (which would create a circular import).
//
// Width and Height mirror the values from the most recent SetSize call so panes
// don't have to cache them independently.
type Context struct {
	// Store provides read-only access to the dashboard application state.
	Store state.ReadOnlyStore

	// Focused is true when this pane currently holds keyboard focus.
	// Panes use this to adjust border styles and cursor visibility.
	Focused bool

	// Width is the current allocated pane width (columns), matching the last SetSize call.
	Width int

	// Height is the current allocated pane height (rows), matching the last SetSize call.
	Height int

	// NavNodes is the pre-computed nav tree for this render frame.
	// Computed once per frame by the app model to avoid repeated derivation.
	NavNodes []domain.NavNode

	// TimelineEntries is the pre-computed timeline for this render frame.
	// Computed once per frame by the app model to avoid repeated derivation.
	TimelineEntries []domain.TimelineEntry
}
