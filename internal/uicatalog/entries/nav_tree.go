package entries

import (
	"github.com/sageox/ox/internal/dashboard/theme"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "nav-tree",
		Family:  uicatalog.FamilyLayout,
		Package: "internal/dashboard/panes/nav",
		Exports: []string{"Pane", "View"},
		Since:   "0.7.0",
		WhenToUse: "Left-rail navigation in full-screen TUIs — workspaces, teams, " +
			"sessions, recent activity. Hierarchical with collapse/expand. Selected " +
			"row gets a bold inverted highlight; section headers in secondary color.",
		WhenNotTo: "Flat lists fit better in Columns. Trees deeper than 3 levels " +
			"become a navigation problem of their own — paginate or filter first.",
		Render: func() string {
			sect := theme.NavSectionStyle.Render
			item := theme.NavItemStyle.Render
			dim := theme.NavDimStyle.Render
			sel := theme.NavSelectedStyle.Render

			lines := []string{
				sect("WORKSPACES"),
				"  " + item("ox"),
				"  " + sel("sageox-design") + dim("  ← here"),
				"  " + item("ox-test-harness"),
				"",
				sect("TEAMS"),
				"  " + item("sageox-core"),
				"  " + item("sageox-design"),
				"",
				sect("RECENT"),
				"  " + dim("session 4m ago"),
				"  " + dim("session 18m ago"),
			}
			out := ""
			for _, l := range lines {
				out += l + "\n"
			}
			return out
		},
	})
}
