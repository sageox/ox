package entries

import (
	"math/rand"
	"time"

	"github.com/sageox/ox/internal/tui"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "sparkline",
		Family:  uicatalog.FamilyViz,
		Package: "internal/tui",
		Exports: []string{"RenderSparkline", "RenderSparklineTimeMarkers"},
		Since:   "0.6.0",
		WhenToUse: "Compact activity-over-time visualization in a single line. " +
			"Default window is 4 hours bucketed into 5-minute slots — perfect for session-cadence and sync-frequency views.",
		WhenNotTo: "Precise numerical readouts (use a table) or windows longer than a few hours " +
			"where 5-minute buckets blur the signal.",
		Render: func() string {
			// Deterministic seed so the catalog output is stable across runs.
			r := rand.New(rand.NewSource(42))
			now := time.Now()
			var ts []time.Time
			for i := 0; i < 60; i++ {
				offset := time.Duration(r.Intn(4*60)) * time.Minute
				ts = append(ts, now.Add(-offset))
			}
			line := tui.RenderSparkline(ts, tui.SparklineBuckets, tui.SparklineWindow)
			return line + "\n" + tui.RenderSparklineTimeMarkers()
		},
	})
}
