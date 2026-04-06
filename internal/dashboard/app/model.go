package app

import (
	"github.com/sageox/ox/internal/dashboard/effects"
	"github.com/sageox/ox/internal/dashboard/overlays"
	"github.com/sageox/ox/internal/dashboard/state"
)

// Model is the single root BubbleTea model for the dashboard TUI.
type Model struct {
	// Terminal dimensions
	width, height int
	ready         bool // true once first WindowSizeMsg received

	// Layout geometry (recomputed on resize or inspector toggle)
	layout Layout

	// Application state (value type, replaced on every mutation)
	store state.Store

	// Active section tab (Overview, Sync, Code, Sessions, Feed)
	section Section

	// Whether the inspector detail pane is open
	inspectorOpen bool

	// Per-section cursor position (preserved when switching sections)
	cursors [int(sectionCount)]int

	// Per-section scroll offset
	scrolls [int(sectionCount)]int

	// Per-section list length from last render (for cursor clamping)
	listLens *[int(sectionCount)]int

	// Inspector scroll position
	inspectorScroll int

	// Inspector pane (renders detail for selected item)
	inspectorPane InspectorRenderer

	// Overlay stack (help, palette, confirm)
	overlays overlays.Stack

	// Generation counter for stale async response detection
	effectGen int

	// Effects client (daemon IPC + session store)
	client effects.Client

	// Global key bindings
	keys GlobalKeyMap

	// helpFactory creates a new help overlay on demand.
	helpFactory func() overlays.Overlay
}

// InspectorRenderer is implemented by the inspector pane.
type InspectorRenderer interface {
	View(store state.ReadOnlyStore, width, height, scroll int) string
}

// NewModel creates an initialized Model.
func NewModel(client effects.Client, inspector InspectorRenderer, helpFactory func() overlays.Overlay) Model {
	store := state.Store{}
	store = state.SetLoading(store)

	return Model{
		width:         80,
		height:        24,
		layout:        ComputeLayout(80, 24, false),
		store:         store,
		section:       SectionOverview,
		client:        client,
		keys:          DefaultGlobalKeys(),
		helpFactory:   helpFactory,
		inspectorPane: inspector,
		listLens:      &[int(sectionCount)]int{},
	}
}
