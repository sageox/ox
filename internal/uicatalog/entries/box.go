package entries

import (
	"github.com/sageox/ox/internal/ui"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "box",
		Family:  uicatalog.FamilyLayout,
		Package: "internal/ui",
		Exports: []string{"RenderBox", "RenderSummaryBox", "BoxVariant"},
		Since:   "0.4.0",
		WhenToUse: "Group a small payload (a tip, a callout, a summary) that benefits " +
			"from a visible boundary. Variants color the border by intent (info/warn/error/success).",
		WhenNotTo: "Wrapping long-form output — boxes around paragraphs add cost without information. " +
			"For multi-step results use a Timeline instead.",
		Render: func() string {
			info := ui.RenderBox("Heads up",
				"This is an Info box — used for callouts that aren't a problem,\njust worth a beat of attention.",
				ui.BoxInfo)
			success := ui.RenderBox("",
				"Health check passed.",
				ui.BoxSuccess)
			summary := ui.RenderSummaryBox(7, 1, 0, 2,
				"Run `ox doctor --fix` to clear the warning.")
			return info + "\n\n" + success + "\n\n" + summary
		},
	})
}
