package entries

import (
	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/theme"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "modal",
		Family:  uicatalog.FamilyLayout,
		Package: "internal/dashboard/overlays",
		Exports: []string{"Overlay", "OverlayConfirm"},
		Since:   "0.7.0",
		WhenToUse: "Floating dialogs inside a full-screen TUI: help (?), command " +
			"palette (Ctrl+K), confirmation gates. Consumes all keys until dismissed. " +
			"Backdrop dims the underlying surface so focus is obvious.",
		WhenNotTo: "One-shot commands — no underlying TUI to overlay. Use a Box " +
			"or ConfirmDangerousOperation prompt instead.",
		Render: func() string {
			border := lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(theme.Adapt(theme.ColorAccent)).
				Padding(1, 3)

			title := cli.StyleBrand.Render("⌘  Command palette")
			input := cli.StyleAccent.Render("> ") + "sync"
			hi := cli.StyleSecondary.Render
			dim := cli.StyleDim.Render

			items := []string{
				hi("▸ ") + "Sync ledger             " + dim("ox sync"),
				"  Sync all repos          " + dim("ox sync --all"),
				"  Open sync logs          " + dim("ox logs sync"),
				"  Pause auto-sync         " + dim("ox sync --pause"),
			}

			body := title + "\n" +
				dim("─────────────────────────────────────") + "\n" +
				input + "\n\n" +
				items[0] + "\n" + items[1] + "\n" + items[2] + "\n" + items[3] + "\n\n" +
				dim("↑/↓ navigate · enter execute · esc close")

			return border.Render(body)
		},
	})
}
