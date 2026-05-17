package entries

import (
	"github.com/sageox/ox/internal/dashboard/theme"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "pane",
		Family:  uicatalog.FamilyLayout,
		Package: "internal/dashboard/theme",
		Exports: []string{"PaneBorderFocused", "PaneBorderUnfocused"},
		Since:   "0.7.0",
		WhenToUse: "Full-screen TUIs that split work across regions (dashboard, " +
			"config-tui). The focused pane gets a double border in brand primary; " +
			"unfocused panes use a rounded border in dim. Tab swaps focus.",
		WhenNotTo: "One-shot command output — a bordered region just steals " +
			"vertical space. Use Box for static callouts instead.",
		Render: func() string {
			body := "Nav · Timeline\nFocus moves on Tab"
			focused := theme.PaneBorderFocused.Padding(0, 2).Width(28).Height(3).
				Render(theme.HeaderStyle.Render("focused") + "\n" + body)
			unfocused := theme.PaneBorderUnfocused.Padding(0, 2).Width(28).Height(3).
				Render(theme.HeaderDimStyle.Render("unfocused") + "\n" + body)
			return focused + "    " + unfocused
		},
	})
}
