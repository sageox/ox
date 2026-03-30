package dashboard

import (
	tea "charm.land/bubbletea/v2"
	"github.com/sageox/ox/internal/dashboard/app"
	"github.com/sageox/ox/internal/dashboard/overlays"
	"github.com/sageox/ox/internal/dashboard/overlays/help"
	"github.com/sageox/ox/internal/dashboard/panes"
	inspectorpane "github.com/sageox/ox/internal/dashboard/panes/inspector"
	navpane "github.com/sageox/ox/internal/dashboard/panes/nav"
	statusbarpane "github.com/sageox/ox/internal/dashboard/panes/statusbar"
	timelinepane "github.com/sageox/ox/internal/dashboard/panes/timeline"
)

// Run starts the dashboard TUI and blocks until the user quits.
func Run(deps Deps) error {
	// Pane order matches FocusTarget iota: nav(0), timeline(1), inspector(2), statusbar(3).
	// Constructed here (in the parent package) to avoid the import cycle that would arise
	// from app importing pane sub-packages that already import app for key-binding types.
	panelist := []panes.Pane{
		navpane.New(),
		timelinepane.New(),
		inspectorpane.New(),
		statusbarpane.New(),
	}

	// helpFactory is also injected here for the same cycle reason: overlays/help imports app.
	// Key maps are translated to help's mirror types here so help need not import app.
	gk := app.DefaultGlobalKeys()
	pk := app.DefaultPaneKeys()
	helpFactory := func() overlays.Overlay {
		return help.New(
			help.GlobalKeyMap{
				FocusNext: gk.FocusNext,
				FocusPrev: gk.FocusPrev,
				Refresh:   gk.Refresh,
				Help:      gk.Help,
				Quit:      gk.Quit,
			},
			help.PaneKeyMap{
				Up:     pk.Up,
				Down:   pk.Down,
				Select: pk.Select,
				Expand: pk.Expand,
			},
		)
	}

	m := app.NewModel(deps.Client, panelist, helpFactory)
	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}
