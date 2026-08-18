package entries

import (
	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/theme"
	"github.com/sageox/ox/internal/ui"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "timeline",
		Family:  uicatalog.FamilyDataDisplay,
		Package: "internal/ui",
		Exports: []string{"RenderTimeline", "TimelineNode", "TimelineItem"},
		Since:   "0.5.0",
		WhenToUse: "Multi-step output where order matters — doctor results, " +
			"sync stages, post-install steps. Each node anchors a phase, items inside report findings.",
		WhenNotTo: "Single-line status (use `StyleSuccess` directly) or tabular history " +
			"(use Columns). Timelines are vertical; horizontal scans want tables.",
		Render: func() string {
			pass := lipgloss.NewStyle().Foreground(theme.Adapt(theme.ColorSuccess))
			warn := lipgloss.NewStyle().Foreground(theme.Adapt(theme.ColorWarning))
			nodes := []ui.TimelineNode{
				{
					Title:   "Auth",
					Style:   pass,
					Summary: "2 passed",
					Items: []ui.TimelineItem{
						{Icon: ui.IconPass, Style: pass, Text: "API token loaded", Detail: "from ~/.sageox/config"},
						{Icon: ui.IconPass, Style: pass, Text: "Endpoint reachable", Detail: "api.sageox.ai"},
					},
				},
				{
					Title:   "Workspace",
					Style:   warn,
					Summary: "1 warning",
					Items: []ui.TimelineItem{
						{Icon: ui.IconPass, Style: pass, Text: "Git repo detected"},
						{Icon: ui.IconWarn, Style: warn, Text: "Ledger out of date", Detail: "run `ox sync` to refresh", Badge: "[--fix]"},
					},
				},
			}
			return ui.RenderTimeline(nodes, "Done")
		},
	})
}
