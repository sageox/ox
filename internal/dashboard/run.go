package dashboard

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/sageox/ox/internal/dashboard/app"
	"github.com/sageox/ox/internal/dashboard/overlays"
	"github.com/sageox/ox/internal/dashboard/overlays/help"
	inspectorpane "github.com/sageox/ox/internal/dashboard/panes/inspector"
)

// Run starts the dashboard TUI and blocks until the user quits.
func Run(deps Deps) error {
	inspector := inspectorpane.New()

	m := app.NewModel(deps.Client, inspector, helpFactory)
	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}

// helpFactory opens the `?` view with the keys the dashboard dispatches.
func helpFactory() overlays.Overlay { return newHelpOverlay(app.DefaultGlobalKeys()) }

// newHelpOverlay builds the `?` view from the dashboard's own bindings;
// help_conformance_test.go fails when a GlobalKeyMap field has no row.
// ToggleInspector needs no row of its own: Select owns enter and carries the
// same help text. Two groups, not three, keep the modal inside an 80x24
// terminal (help_conformance_test.go checks the height). Pane keys are not
// listed: the nav pane that binds them is not mounted in the dashboard, and a
// help row for a key that does nothing is worse than none.
func newHelpOverlay(gk app.GlobalKeyMap) overlays.Overlay {
	return help.New([]help.Section{
		{Title: "Global", Bindings: []key.Binding{gk.NextSection, gk.PrevSection, gk.Refresh, gk.Palette, gk.OpenBrowser, gk.Help, gk.Quit}},
		{Title: "Navigation", Bindings: []key.Binding{gk.Up, gk.Down, gk.Select, gk.CloseInspector, gk.Section1, gk.Section2, gk.Section3, gk.Section4, gk.Section5}},
	})
}
