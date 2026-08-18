package entries

import (
	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/theme"
	"github.com/sageox/ox/internal/ui"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "doctor-screen",
		Family:  uicatalog.FamilyScreen,
		Package: "cmd/ox/doctor.go",
		Exports: []string{"renderDoctorHeader", "ui.RenderTimeline", "ui.RenderSummaryBox"},
		Since:   "0.5.0",
		WhenToUse: "The canonical \"destination command\" composition: branded " +
			"wordmark + categorized check timeline + closing summary box. " +
			"Reference this shape for any command the user runs to *resolve* something, not just describe it.",
		WhenNotTo: "Pipeline commands — the wordmark earns its vertical space only " +
			"when the command is a destination. Don't print it from automation paths.",
		Render: func() string {
			pass := lipgloss.NewStyle().Foreground(theme.Adapt(theme.ColorSuccess))
			warn := lipgloss.NewStyle().Foreground(theme.Adapt(theme.ColorWarning))

			header := ui.RenderWordmark("0.9.0", false)

			nodes := []ui.TimelineNode{
				{
					Title:   "Auth",
					Style:   pass,
					Summary: "2 passed",
					Items: []ui.TimelineItem{
						{Icon: ui.IconPass, Style: pass, Text: "API token loaded", Detail: "from ~/.sageox/config"},
						{Icon: ui.IconPass, Style: pass, Text: "Endpoint reachable", Detail: "sageox.ai"},
					},
				},
				{
					Title:   "Workspace",
					Style:   warn,
					Summary: "1 warning",
					Items: []ui.TimelineItem{
						{Icon: ui.IconPass, Style: pass, Text: "Git repo detected"},
						{Icon: ui.IconWarn, Style: warn, Text: "Ledger stale", Detail: "2h32m since last sync", Badge: "[--fix]"},
					},
				},
				{
					Title:   "Hooks",
					Style:   pass,
					Summary: "3 passed",
					Items: []ui.TimelineItem{
						{Icon: ui.IconPass, Style: pass, Text: "claude-code SessionStart"},
						{Icon: ui.IconPass, Style: pass, Text: "claude-code Stop"},
						{Icon: ui.IconPass, Style: pass, Text: "claude-code PostCompact"},
					},
				},
			}

			timeline := ui.RenderTimeline(nodes, "Done")
			summary := ui.RenderSummaryBox(7, 1, 0, 0,
				"Run `ox doctor --fix` to clear the warning.")

			return header + timeline + "\n" + summary
		},
	})
}
