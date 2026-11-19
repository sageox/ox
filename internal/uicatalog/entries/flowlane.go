package entries

import (
	"strings"
	"time"

	"github.com/sageox/ox/internal/flowlane"
	"github.com/sageox/ox/internal/oxactivity"
	"github.com/sageox/ox/internal/theme"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "flow-lane",
		Family:  uicatalog.FamilyViz,
		Package: "internal/flowlane",
		Exports: []string{"Render", "Moving", "Lane", "Options"},
		Since:   "0.27.0",
		WhenToUse: "One line per ox call that moved team data between SageOx and an AI coworker's session: " +
			"which way, how much, how long. Stack several; the fixed-width track makes them small multiples.",
		WhenNotTo: "Calls that move no team data (doctor, status, config), totals over a session " +
			"(use the status line's counts), or anything that needs more than one line.",
		Render: func() string {
			// Still frames of each state. At runtime a running lane's dots
			// march toward where the data is going, and a finished lane
			// animates once (dots travel for 2.4s) before it settles; see the
			// spec doc.
			in, out := oxactivity.DirectionIn, oxactivity.DirectionOut
			lanes := []flowlane.Lane{
				{Direction: in, Label: "searched team memory", Step: "searching team discussions", Running: true},
				{Direction: out, Label: "posted murmur", Step: "posting murmur", Running: true},
				{Direction: in, Label: "read sessions", Step: "reading sessions", Tokens: 1180, Items: 3, ItemsNoun: "sessions", Duration: 800 * time.Millisecond},
				{Direction: in, Label: "pulled plan", Step: "pulling plan", Tokens: 420, Duration: 300 * time.Millisecond},
				{Direction: out, Label: "posted murmur", Step: "posting murmur", Unsized: true, Duration: 400 * time.Millisecond},
				{Direction: in, Label: "searched code index", Step: "searching code", Failed: true, Duration: 1200 * time.Millisecond},
			}
			rows := make([]string, 0, len(lanes)+1)
			for _, l := range lanes {
				rows = append(rows, flowlane.Render(l, flowlane.AnimDuration, flowlane.Options{Columns: 80, Color: theme.ColorEnabled()}))
			}
			// One mid-flight frame, so the still shows what the motion is.
			moving := lanes[2]
			moving.Tokens = 3000
			rows = append(rows, flowlane.Render(moving, 900*time.Millisecond, flowlane.Options{Columns: 80, Color: theme.ColorEnabled()}))
			return strings.Join(rows, "\n")
		},
	})
}
