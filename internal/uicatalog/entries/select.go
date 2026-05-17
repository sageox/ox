package entries

import (
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "select",
		Family:  uicatalog.FamilyInput,
		Package: "internal/cli",
		Exports: []string{"SelectOne", "SelectOneValue", "SelectMany"},
		Since:   "0.4.0",
		WhenToUse: "Radio-button single-pick from a known set of options — `ox init` " +
			"endpoint picker, team chooser, IDE selector. Arrow-key navigation in TTY; " +
			"numbered-prompt fallback in pipes and CI. Generic SelectOneValue avoids index-juggling.",
		WhenNotTo: "Multi-select (use multi-select / SelectMany — the checkbox cousin). " +
			"Free-form text (use Prompt) or yes/no (use Confirm). Lists longer than ~20 items — " +
			"paginate or filter at the caller level first.",
		Render: func() string {
			// Static snapshot of the rendered menu. The .cast captures arrow-key motion.
			title := cli.StyleBrand.Render("Pick a team")
			hi := cli.StyleSecondary.Render
			dim := cli.StyleDim.Render
			lines := []string{
				title,
				"",
				hi("▸ ") + hi("sageox-core") + dim("    primary team"),
				"  sageox-design  " + dim("UI + brand"),
				"  ox-test-harness " + dim("CI fixtures"),
				"",
				dim("  ↑/↓ navigate · enter select · esc cancel"),
			}
			out := ""
			for _, l := range lines {
				out += l + "\n"
			}
			return out
		},
	})
}
