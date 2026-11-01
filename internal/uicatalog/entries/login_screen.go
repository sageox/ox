package entries

import (
	"github.com/charmbracelet/colorprofile"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/pet"
	"github.com/sageox/ox/internal/theme"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "login-screen",
		Family:  uicatalog.FamilyScreen,
		Package: "cmd/ox/login.go",
		Exports: []string{"loginSpinnerModel", "cli.DeviceCodeLines", "cli.LoginArrival"},
		Since:   "0.3.0",
		WhenToUse: "The smallest possible full-screen bubbletea program in ox: one " +
			"spinner, one message line, one Ctrl+C handler. Use this as the " +
			"reference for any \"wait for external thing\" screen (OAuth device code, daemon handshake).",
		WhenNotTo: "Foreground operations whose duration is bounded and predictable — " +
			"use the inline Spinner component instead. The full-screen treatment " +
			"is reserved for genuinely open-ended waits where Ctrl+C is the user's only out.",
		Render: func() string {
			dim := cli.StyleDim.Render
			caption := func(s string) string { return dim("─── "+s+" ───") + "\n\n" }

			// Every block below is the text `ox login` prints, drawn by the same
			// functions: this entry cannot drift from the command.
			code := cli.DeviceCodeLines("https://sageox.ai/device?user_code=WXYZ-1234", "WXYZ-1234", true)
			spinnerLine := cli.StyleBrand.Render("⠋") + " " + cli.StyleBrand.Render("Waiting for authorization...")

			returning := cli.NewLoginArrival("Person A", "person.a@company.test", "sageox.ai", true)
			firstTime := cli.NewLoginArrival("Person A", "person.a@company.test", "sageox.ai", false)

			// Oxy joins only a person at a color terminal; the catalog shows the
			// same rule. Without color his picture would be solid blocks.
			interactive := returning.Render(nil)
			if theme.Profile >= colorprofile.ANSI256 {
				if art, err := pet.TerminalFrame("confetti", 0.35, 6); err == nil {
					interactive = returning.Render(art.Lines)
				}
			}

			denied := cli.StyleError.Render("Error:") + " authentication failed: authorization was denied\n" +
				"  Nothing was shared. Run `ox login` to try again\n"

			return caption("waiting for approval: the code always prints") + code + "\n" + spinnerLine + "\n\n" +
				caption("approved, at a color terminal") + interactive + "\n" +
				caption("approved, piped, in CI, or with --quiet or --json: text only") + firstTime.Render(nil) + "\n" +
				caption("denied or expired") + denied
		},
	})
}
