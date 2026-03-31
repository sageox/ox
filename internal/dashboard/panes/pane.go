// Package panes defines the Pane interface and shared types used by every
// panel in the dashboard TUI.
//
// Panes are NOT independent tea.Model instances. They are components rendered
// and updated by the top-level app model, which passes a Context on every
// Update/View call so panes never need to hold their own copy of application
// state.
package panes

import tea "charm.land/bubbletea/v2"

// Rect defines the position and dimensions allocated to a pane by the layout
// engine. It is intentionally duplicated here (independent of any app.Rect)
// to break the potential import cycle where panes → app would be required.
type Rect struct {
	X, Y, Width, Height int
}

// PaneID uniquely identifies each panel in the dashboard layout.
type PaneID int

const (
	PaneNav       PaneID = iota // left-hand navigation tree
	PaneTimeline                // center timeline feed
	PaneInspector               // right-hand detail inspector
	PaneStatusBar               // bottom status bar
)

// Pane is the interface all dashboard panels must implement.
//
// The lifecycle is:
//  1. App creates panes and calls SetSize once the terminal dimensions are known.
//  2. On every tea.Msg the app calls Update on the focused pane (and potentially
//     all panes for global messages such as WindowSizeMsg).
//  3. The app calls View to obtain each pane's rendered string for compositing.
type Pane interface {
	// ID returns the stable identifier for this pane.
	ID() PaneID

	// SetSize informs the pane of its allocated screen real-estate.
	// Called by the layout engine whenever the terminal is resized.
	SetSize(r Rect)

	// Update handles a bubbletea message and returns the (possibly new) pane
	// value plus any commands to run. The supplied Context provides read-only
	// access to the current application state.
	Update(msg tea.Msg, ctx Context) (Pane, tea.Cmd)

	// View renders the pane to a string. The supplied Context reflects the
	// state at the moment the frame is being drawn.
	View(ctx Context) string
}
