package entries

import (
	"strings"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "status-bar",
		Family:  uicatalog.FamilyFeedback,
		Package: "internal/dashboard/panes/statusbar",
		Exports: []string{"Pane", "View"},
		Since:   "0.7.0",
		WhenToUse: "Bottom row of full-screen TUIs. Left side carries persistent " +
			"state (daemon health, open issues); right side carries the active " +
			"hint (\"? for help\", \"esc to cancel\"). Transient toasts (\"copied!\") replace the right side for ~2s.",
		WhenNotTo: "Inline commands — the bar wants a fixed-position last row, " +
			"which only full-screen bubbletea programs offer. Don't fake it.",
		Render: func() string {
			width := 78
			ok := cli.StyleSuccess.Render
			dim := cli.StyleDim.Render
			warn := cli.StyleWarning.Render

			left := ok("●") + " daemon · " + warn("2 stale") + " · " + dim("ox 0.9.0")
			right := dim("Tab pane · ? help · q quit")

			gap := width - len(stripANSI(left)) - len(stripANSI(right))
			if gap < 1 {
				gap = 1
			}
			return left + strings.Repeat(" ", gap) + right
		},
	})
}

// stripANSI is a tiny helper for spacing math in demos. The real
// dashboard does this via lipgloss.Width() — keeping it inline here so
// the catalog entry stays self-contained.
func stripANSI(s string) string {
	out := make([]byte, 0, len(s))
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
			i++
			continue
		}
		out = append(out, s[i])
		i++
	}
	return string(out)
}
