package entries

import (
	"strings"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/theme"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "config-screen",
		Family:  uicatalog.FamilyScreen,
		Package: "cmd/ox/config_tui.go",
		Exports: []string{"configModel"},
		Since:   "0.6.0",
		WhenToUse: "The full `ox config` TUI surface: a rounded-bordered frame with " +
			"an overlaid title, categorized settings list, cursor-driven selection, " +
			"value column with source-precedence arrows, and a help footer. This is the canonical example of an interactive ox screen.",
		WhenNotTo: "Inline commands — the frame and per-row source arrows assume " +
			"a controlled full-screen surface. Don't fake the chrome in scrollable output.",
		Render: func() string {
			frame := lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(theme.Adapt(theme.ColorDim)).
				Padding(0, 1).
				Width(76)

			title := cli.StyleBrand.Bold(true).Render(" ox config ")
			cat := cli.StyleSecondary.Bold(true).Render
			sel := lipgloss.NewStyle().Bold(true).Foreground(theme.Adapt(theme.ColorAccent)).Render
			dim := cli.StyleDim.Render
			val := cli.StyleAccent.Render
			def := cli.StyleDim.Render

			arrowUser := dim("← user")
			arrowLocal := cli.StyleSecondary.Render("← local")
			arrowDefault := dim("← default")

			row := func(cursor, key, value, arrow string, selected bool) string {
				k := dim(key)
				if selected {
					k = sel(key)
				}
				return cursor + padRight(k, 30) + value + "  " + arrow
			}

			lines := []string{
				cat("GENERAL"),
				row("  ", "default.endpoint", val("api.sageox.ai"), arrowUser, false),
				row("> ", "auto-sync.interval", val("5m"), arrowLocal, true),
				row("  ", "pager", def("less -R"), arrowDefault, false),
				"",
				cat("PRIVACY"),
				row("  ", "murmur.visibility", val("team"), arrowUser, false),
				row("  ", "redaction.profile", val("aggressive"), arrowUser, false),
				"",
				cat("AGENTS"),
				row("  ", "claude-code.hooks", val("enabled"), arrowLocal, false),
				row("  ", "codex-cli.hooks", def("disabled"), arrowDefault, false),
				"",
				dim(strings.Repeat("─", 70)),
				"",
				cli.StyleAccent.Bold(true).Render("auto-sync.interval"),
				dim("How often the daemon pulls upstream changes."),
				dim("Scopes:  ") + dim("default 1m  ") + cli.StyleSecondary.Render("→ local 5m") + dim("  user —  global —"),
				"",
				dim("↑/↓ navigate  ·  Enter edit  ·  Tab switch scope  ·  s save  ·  esc quit"),
			}

			// Prepend the title line — matches the real config_tui.go
			// overlay visually without splicing inside lipgloss ANSI runs.
			body := title + "  " + dim("scope: ") + cli.StyleSecondary.Render("local") + "  " +
				dim("(Tab to switch)") + "\n" +
				dim(strings.Repeat("─", 70)) + "\n" +
				strings.Join(lines, "\n")
			return frame.Render(body)
		},
	})
}

// padRight pads s with spaces so its visible width is at least w.
// Uses lipgloss.Width for ANSI-aware measurement (handles styled text).
func padRight(s string, w int) string {
	vis := lipgloss.Width(s)
	if vis >= w {
		return s
	}
	return s + strings.Repeat(" ", w-vis)
}
