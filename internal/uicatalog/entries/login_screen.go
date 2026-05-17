package entries

import (
	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/theme"
	"github.com/sageox/ox/internal/ui"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "login-screen",
		Family:  uicatalog.FamilyScreen,
		Package: "cmd/ox/login.go",
		Exports: []string{"loginSpinnerModel"},
		Since:   "0.3.0",
		WhenToUse: "The smallest possible full-screen bubbletea program in ox: one " +
			"spinner, one message line, one Ctrl+C handler. Use this as the " +
			"reference for any \"wait for external thing\" screen (OAuth callback, daemon handshake).",
		WhenNotTo: "Foreground operations whose duration is bounded and predictable — " +
			"use the inline Spinner component instead. The full-screen treatment " +
			"is reserved for genuinely open-ended waits where Ctrl+C is the user's only out.",
		Render: func() string {
			dim := cli.StyleDim.Render
			ok := cli.StyleSuccess.Render

			// Pre-auth state — what the user sees most of the time.
			preBox := ui.RenderBox("",
				"Opening "+cli.StyleAccent.Render("sageox.ai/cli/auth")+" in your browser...\n"+
					"Local listener on "+cli.StyleAccent.Render("127.0.0.1:7392"),
				ui.BoxInfo)

			spinnerLine := cli.StyleBrand.Render("⠋") + " " +
				cli.StyleBrand.Render("Waiting for authorization...") + "\n" +
				dim("  press Ctrl+C to cancel")

			// Post-auth state — what the user sees after success.
			postBox := lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(theme.ColorSuccess).
				Padding(0, 2).
				Render(ok("✓") + " Authenticated as " + cli.StyleAccent.Render("ryan+ox@sageox.ai") + "\n" +
					"  Endpoint: " + cli.StyleAccent.Render("sageox.ai") + "\n" +
					"  " + dim("Tip: run `ox init` in a repo to get started."))

			return preBox + "\n\n" + spinnerLine + "\n\n" +
				dim("─── after browser callback ───") + "\n\n" +
				postBox
		},
	})
}
