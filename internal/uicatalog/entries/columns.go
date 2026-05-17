package entries

import (
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "columns",
		Family:  uicatalog.FamilyLayout,
		Package: "internal/cli",
		Exports: []string{"ColumnWidths", "FormatRow"},
		Since:   "0.4.0",
		WhenToUse: "Aligned tabular output where columns share semantic meaning across rows " +
			"(session lists, team rosters, sync state). Widths auto-compute from content with min/max clamps.",
		WhenNotTo: "Single-row key=value displays (use log-formatter) or content that " +
			"can't survive truncation on narrow terminals — design column widths around 80 columns.",
		Render: func() string {
			header := []string{"REPO", "TEAM", "LEDGER", "AGE"}
			rows := [][]string{
				header,
				{"ox", "sageox-core", "synced", "2m"},
				{"sageox-design", "sageox-design", "synced", "5m"},
				{"ox-test-harness", "sageox-core", "stale", "2h"},
			}
			widths := cli.ColumnWidths(rows, []int{10, 12, 8, 4}, []int{40, 40, 12, 8})
			out := ""
			for i, r := range rows {
				line := cli.FormatRow(r, widths)
				if i == 0 {
					line = cli.StyleBrand.Render(line)
				}
				out += line + "\n"
			}
			return out
		},
	})
}
