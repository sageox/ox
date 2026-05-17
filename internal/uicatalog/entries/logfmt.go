package entries

import (
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "log-formatter",
		Family:  uicatalog.FamilyDataDisplay,
		Package: "internal/cli",
		Exports: []string{"FormatSlogLine", "StreamFormattedLogs", "ParseSlogLine"},
		Since:   "0.6.0",
		WhenToUse: "Rendering structured slog output for humans without giving up grep. " +
			"Levels get colored, timestamps compacted, key=value pairs preserved. Pipe daemon logs through it.",
		WhenNotTo: "JSON-mode output (`--json`) or anything that downstream tooling parses. " +
			"Pretty-printing breaks aggregators.",
		Render: func() string {
			lines := []string{
				`time=2026-05-17T14:32:01Z level=INFO  msg="daemon started" addr=127.0.0.1:5471 version=0.9.0`,
				`time=2026-05-17T14:32:02Z level=DEBUG msg="loaded config" path=~/.sageox/config endpoint=api.sageox.ai`,
				`time=2026-05-17T14:32:03Z level=WARN  msg="ledger stale" repo=ox age=2h32m`,
				`time=2026-05-17T14:32:04Z level=ERROR msg="sync failed" repo=ox err="LFS pointer missing"`,
			}
			out := ""
			for _, l := range lines {
				out += cli.FormatSlogLine(l) + "\n"
			}
			return out
		},
	})
}
