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
		Name:    "session-list-screen",
		Family:  uicatalog.FamilyScreen,
		Package: "cmd/ox/session_list.go",
		Exports: []string{"runSessionList"},
		Since:   "0.5.0",
		WhenToUse: "List view with session-specific badges — hydration state, " +
			"duration coloring, type indicator. Reference for any per-row list where " +
			"each row has a status that the user might filter or sort by.",
		WhenNotTo: "Generic tabular data — use Columns. Multi-line per-row content — " +
			"use Timeline with one Node per row.",
		Render: func() string {
			brand := cli.StyleBrand.Render
			dim := cli.StyleDim.Render
			ok := cli.StyleSuccess.Render
			warn := cli.StyleWarning.Render
			val := lipgloss.NewStyle().Foreground(theme.ColorAccent).Render
			sec := cli.StyleSecondary.Render

			pad := func(s string, w int) string {
				v := lipgloss.Width(s)
				if v >= w {
					return s
				}
				return s + strings.Repeat(" ", w-v)
			}

			heading := brand("ox session list") + "  " + dim("--workspace=ox") + "\n\n"

			headerRow := dim(pad("SESSION", 22)) +
				dim(pad("STARTED", 12)) +
				dim(pad("DURATION", 11)) +
				dim(pad("TURNS", 7)) +
				dim(pad("STATE", 12)) +
				dim("AGENT") + "\n" +
				dim(strings.Repeat("─", 78)) + "\n"

			rows := strings.Join([]string{
				val(pad("oxsid_01JE...A7QX", 22)) + pad("4m ago", 12) + pad(ok("0:18"), 11) + pad("12", 7) + pad(ok("● live"), 12) + sec("claude-code"),
				val(pad("oxsid_01JE...8MNP", 22)) + pad("28m ago", 12) + pad(val("1:04"), 11) + pad("47", 7) + pad(ok("✓ hydrated"), 12) + sec("claude-code"),
				val(pad("oxsid_01JE...3KZF", 22)) + pad("2h ago", 12) + pad(val("0:42"), 11) + pad("23", 7) + pad(warn("⟳ stub"), 12) + sec("codex"),
				val(pad("oxsid_01JE...7VRC", 22)) + pad("yesterday", 12) + pad(val("2:18"), 11) + pad("93", 7) + pad(ok("✓ hydrated"), 12) + sec("claude-code"),
				val(pad("oxsid_01JE...2WMD", 22)) + pad("yesterday", 12) + pad(warn("4:31"), 11) + pad("181", 7) + pad(ok("✓ hydrated"), 12) + sec("cursor"),
			}, "\n")

			footer := "\n\n" + dim("5 sessions · 1 live · 3 hydrated · 1 stub · run `ox session view <id>`")

			return heading + headerRow + rows + footer
		},
	})
}
