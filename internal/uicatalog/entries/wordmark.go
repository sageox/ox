package entries

import (
	"github.com/sageox/ox/internal/ui"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "wordmark",
		Family:  uicatalog.FamilyLayout,
		Package: "internal/ui",
		Exports: []string{"RenderWordmark", "WriteWordmark"},
		Since:   "0.4.0",
		WhenToUse: "Branded headers for commands that own a moment — `ox doctor`, " +
			"`ox init` success, release-notes banners. Two-tone color (Sage/Ox) keys the wordmark to the brand palette.",
		WhenNotTo: "Routine command output. The wordmark earns its vertical space " +
			"only when the command is a destination, not a step in a pipeline.",
		Render: func() string {
			return ui.RenderWordmark("0.9.0", false)
		},
	})
}
