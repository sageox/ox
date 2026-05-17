package entries

import (
	"strings"

	"github.com/sageox/ox/internal/dashboard/theme"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "filter-tabs",
		Family:  uicatalog.FamilyInput,
		Package: "internal/dashboard/panes/timeline",
		Exports: []string{"renderFilterBar (private)"},
		Since:   "0.7.0",
		WhenToUse: "A narrow numbered tab row at the top of a content pane — quick " +
			"single-key switching between views of the same data (timeline filters, " +
			"section selectors, log-level filters). Number keys 1–9 jump tabs; `/` opens an inline search line below.",
		WhenNotTo: "More than ~6 tabs (the number-key shortcut breaks down). " +
			"Selecting between entire screens — use the dashboard's section tabs instead.",
		Render: func() string {
			dim := theme.NavDimStyle.Render
			sel := theme.NavSelectedStyle.Render
			accent := theme.NavSectionStyle.Render

			labels := []string{"All", "WIP", "Blocked", "Decisions", "Reviews"}
			activeIdx := 1 // "WIP" is highlighted in the demo

			var tabs []string
			for i, lbl := range labels {
				numKey := "[" + string(rune('1'+i)) + "]"
				if i == activeIdx {
					tabs = append(tabs, sel(numKey+lbl))
				} else {
					tabs = append(tabs, dim(numKey)+lbl)
				}
			}
			tabRow := strings.Join(tabs, "  ")
			searchLine := accent("/") + " sync█"
			return tabRow + "\n" + searchLine
		},
	})
}
