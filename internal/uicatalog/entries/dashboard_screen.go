package entries

import (
	"strings"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/dashboard/theme"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "dashboard-screen",
		Family:  uicatalog.FamilyScreen,
		Package: "internal/dashboard/app",
		Exports: []string{"Model"},
		Since:   "0.7.0",
		WhenToUse: "Reference for any new full-screen bubbletea program in ox. Shows " +
			"how Nav · Timeline · Inspector · StatusBar compose; how focused vs " +
			"unfocused panes signal attention; how a Tab/section navigator anchors orientation.",
		WhenNotTo: "Inline commands. The four-pane layout assumes 80×24 minimum and " +
			"a controlled alternate screen — don't fake the chrome inline.",
		Render: func() string {
			dim := cli.StyleDim.Render
			navSect := theme.NavSectionStyle.Render
			navItem := theme.NavItemStyle.Render
			navSel := theme.NavSelectedStyle.Render
			tlNow := theme.TimelineNowLabel.Render
			tlRec := theme.TimelineRecentLabel.Render
			murmur := theme.TimelineMurmurStyle.Render

			navPane := theme.PaneBorderUnfocused.Width(20).Height(14).Padding(0, 1).Render(
				navSect("WORKSPACES") + "\n" +
					"  " + navItem("ox") + "\n" +
					"  " + navSel("sageox-design") + "\n" +
					"  " + navItem("ox-test-harness") + "\n" +
					"\n" +
					navSect("TEAMS") + "\n" +
					"  " + navItem("sageox-core") + "\n" +
					"  " + navItem("sageox-design") + "\n" +
					"\n" +
					navSect("RECENT") + "\n" +
					"  " + dim("4m ago") + "\n" +
					"  " + dim("18m ago"),
			)

			tabRow := navSel("[1]WIP") + "  " + dim("[2]") + "Blocked  " + dim("[3]") + "Reviews"
			timelinePane := theme.PaneBorderFocused.Width(34).Height(14).Padding(0, 1).Render(
				tabRow + "\n" +
					dim(strings.Repeat("─", 30)) + "\n" +
					tlNow("now ") + "  " + murmur("ryan: refactoring uicatalog\n") +
					"      " + dim("docs/design/* registry.go") + "\n" +
					"\n" +
					tlRec("5m  ") + "  ryan: started session\n" +
					"      " + dim("ox dev catalog --json") + "\n" +
					"\n" +
					dim("18m ") + "  ajit: opened PR #612\n" +
					"      " + dim("security review pipeline"),
			)

			inspectorPane := theme.PaneBorderUnfocused.Width(20).Height(14).Padding(0, 1).Render(
				cli.StyleAccent.Bold(true).Render("✦ Inspector") + "\n" +
					dim(strings.Repeat("─", 16)) + "\n" +
					"Murmur · ryan\n" +
					dim("4m ago") + "\n\n" +
					"refactoring\nuicatalog\n\n" +
					dim("files:") + "\n" +
					dim("docs/design/")+"\n" +
					dim("registry.go"),
			)

			body := lipgloss.JoinHorizontal(lipgloss.Top, navPane, timelinePane, inspectorPane)

			statusOK := cli.StyleSuccess.Render("●")
			statusBar := statusOK + " daemon · " + cli.StyleWarning.Render("2 stale") + " · " +
				dim("ox 0.9.0") +
				strings.Repeat(" ", 30) +
				dim("Tab pane · 1-3 filter · ? help · q quit")

			return body + "\n" + statusBar
		},
	})
}
