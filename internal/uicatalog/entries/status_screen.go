package entries

import (
	"math/rand"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/theme"
	"github.com/sageox/ox/internal/tui"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "status-screen",
		Family:  uicatalog.FamilyScreen,
		Package: "cmd/ox/status.go",
		Exports: []string{"renderTable"},
		Since:   "0.4.0",
		WhenToUse: "Tufte-minimal section-table pattern used by `ox status`, " +
			"`ox teams`, `ox murmur status`. Bold-secondary section header, " +
			"dim left-column labels, semantic-colored values, no decorative borders. " +
			"Reach for this shape any time you have grouped key/value facts to display.",
		WhenNotTo: "Truly tabular data with > 3 columns — use Columns. Long-form " +
			"or hierarchical state — use Timeline.",
		Render: func() string {
			brand := cli.StyleBrand.Render
			sect := cli.StyleSecondary.Bold(true).Render
			label := cli.StyleDim.Render
			val := lipgloss.NewStyle().Foreground(theme.ColorAccent).Render
			ok := cli.StyleSuccess.Render
			warn := cli.StyleWarning.Render
			dim := cli.StyleDim.Render

			heading := brand("ox status") + "  " + dim("workspace: ") + val("ox") + "\n"

			pad := func(s string, w int) string {
				v := lipgloss.Width(s)
				if v >= w {
					return s
				}
				return s + strings.Repeat(" ", w-v)
			}
			row := func(k, v string) string {
				return "  " + pad(label(k), 22) + v + "\n"
			}

			workspace := sect("WORKSPACE") + "\n" +
				row("path", val("~/Code/sageox/ox")) +
				row("endpoint", val("sageox.ai")) +
				row("team", val("sageox-core"))

			ledger := sect("LEDGER") + "\n" +
				row("status", ok("✓ synced")) +
				row("last sync", val("2m ago")) +
				row("entries", val("1,284")) +
				row("size", val("48 MB"))

			daemon := sect("DAEMON") + "\n" +
				row("status", ok("● running")) +
				row("uptime", val("3h 21m")) +
				row("queue", warn("2 stale repos"))

			// AI coworker session blocks. The "paused" row shows the design
			// for adapter-detected terminal stops (rate limit / quota / agent
			// error). Recording==false plus a non-empty FormatStopReason
			// produces the trailing label — user-initiated stops (stopped /
			// canceled / recovered) suppress the suffix entirely.
			coworkers := sect("AI COWORKERS") + "\n" +
				row("claude-code (Ox1A)", ok("● recording — 47 turns")) +
				row("claude-code (Ox2B)", warn("⏸ paused — rate limit (resets ~3h). Resume when ready.")) +
				row("codex (Ox3C)", val("idle"))

			r := rand.New(rand.NewSource(7))
			now := time.Now()
			var ts []time.Time
			for i := 0; i < 80; i++ {
				ts = append(ts, now.Add(-time.Duration(r.Intn(4*60))*time.Minute))
			}
			activity := sect("ACTIVITY  ") + dim("(last 4h)") + "\n  " +
				cli.StyleAccent.Render(tui.RenderSparkline(ts, tui.SparklineBuckets, tui.SparklineWindow)) + "\n  " +
				dim(tui.RenderSparklineTimeMarkers())

			return heading + "\n" +
				workspace + "\n" +
				ledger + "\n" +
				daemon + "\n" +
				coworkers + "\n" +
				activity
		},
	})
}
