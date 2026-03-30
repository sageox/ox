package app

import (
	"github.com/sageox/ox/internal/dashboard/effects"
	"github.com/sageox/ox/internal/dashboard/overlays"
	"github.com/sageox/ox/internal/dashboard/panes"
	"github.com/sageox/ox/internal/dashboard/state"
)

// Model is the single root BubbleTea model for the dashboard TUI.
// All state lives here; panes are components, not independent models.
type Model struct {
	// Terminal dimensions
	width, height int
	ready         bool // true once first WindowSizeMsg received

	// Layout geometry (recomputed on resize)
	layout Layout

	// Application state (value type, replaced on every mutation)
	store state.Store

	// Panes indexed by FocusTarget (0=nav, 1=timeline, 2=inspector, 3=statusbar).
	// Constructed and injected by the parent dashboard package to avoid import cycles
	// (pane packages import app for key bindings, so app cannot import them back).
	panes []panes.Pane

	// Currently focused pane
	focus FocusTarget

	// Overlay stack (help, confirm, etc)
	overlays overlays.Stack

	// Generation counter for stale async response detection
	effectGen int

	// Effects client (daemon IPC + session store)
	client effects.Client

	// Global key bindings
	keys GlobalKeyMap

	// helpFactory creates a new help overlay on demand. Injected by the parent
	// dashboard package to avoid the import cycle that would arise from app
	// importing overlays/help (which already imports app for key-binding types).
	helpFactory func() overlays.Overlay
}

// NewModel creates an initialized Model.
// panelist must be ordered by FocusTarget iota: [nav(0), timeline(1), inspector(2), statusbar(3)].
// helpFactory creates the help overlay on demand; pass nil to disable the help overlay.
// The parent dashboard package constructs concrete panes and the help factory to avoid
// the circular import that would arise from app importing pane sub-packages or overlays/help.
func NewModel(client effects.Client, panelist []panes.Pane, helpFactory func() overlays.Overlay) Model {
	store := state.Store{}
	store = state.SetLoading(store)

	return Model{
		width:  80,
		height: 24,
		layout: ComputeLayout(80, 24),
		store:  store,
		panes:        panelist,
		focus:        FocusNav,
		client:       client,
		keys:         DefaultGlobalKeys(),
		helpFactory:  helpFactory,
	}
}

// paneCtx builds the Context for the pane at the given focus target.
func (m Model) paneCtx(f FocusTarget) panes.Context {
	return panes.Context{
		Store:   &m.store,
		Focused: m.focus == f,
		Width:   m.paneRect(f).Width,
		Height:  m.paneRect(f).Height,
	}
}

// paneRect returns the Rect for the given focus target.
func (m Model) paneRect(f FocusTarget) Rect {
	switch f {
	case FocusNav:
		return m.layout.Nav
	case FocusTimeline:
		return m.layout.Timeline
	case FocusInspector:
		return m.layout.Inspector
	default:
		return m.layout.StatusBar
	}
}

// statusBarCtx builds the Context for the always-passive status bar pane.
func (m Model) statusBarCtx() panes.Context {
	return panes.Context{
		Store:   &m.store,
		Focused: false,
		Width:   m.layout.StatusBar.Width,
		Height:  m.layout.StatusBar.Height,
	}
}

