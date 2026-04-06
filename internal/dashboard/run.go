package dashboard

import (
	tea "charm.land/bubbletea/v2"
	"github.com/sageox/ox/internal/dashboard/app"
	"github.com/sageox/ox/internal/dashboard/overlays"
	"github.com/sageox/ox/internal/dashboard/overlays/help"
	inspectorpane "github.com/sageox/ox/internal/dashboard/panes/inspector"
)

// Run starts the dashboard TUI and blocks until the user quits.
func Run(deps Deps) error {
	inspector := inspectorpane.New()

	gk := app.DefaultGlobalKeys()
	pk := app.DefaultPaneKeys()
	helpFactory := func() overlays.Overlay {
		return help.New(
			help.GlobalKeyMap{
				FocusNext:   gk.NextSection,
				FocusPrev:   gk.PrevSection,
				Refresh:     gk.Refresh,
				Help:        gk.Help,
				Quit:        gk.Quit,
				Palette:     gk.Palette,
				OpenBrowser: gk.OpenBrowser,
			},
			help.PaneKeyMap{
				Up:     pk.Up,
				Down:   pk.Down,
				Select: pk.Select,
				Expand: pk.Expand,
			},
		)
	}

	m := app.NewModel(deps.Client, inspector, helpFactory)
	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}
