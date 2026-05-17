package entries

import (
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "multi-select",
		Family:  uicatalog.FamilyInput,
		Package: "internal/cli",
		Exports: []string{"SelectMany", "MultiSelectOption"},
		Since:   "0.4.0",
		WhenToUse: "Pick zero-or-more from a known set — `ox init` agent-hook " +
			"installation, multi-team enablement, feature opt-ins. Renders as a " +
			"checkbox group; space toggles, enter confirms.",
		WhenNotTo: "Exactly-one selection (use Select for radio-style). " +
			"Free text (use Prompt). Sets larger than ~15 items — paginate or filter.",
		Render: func() string {
			title := cli.StyleBrand.Render("Pick agent hooks to install")
			dim := cli.StyleDim.Render
			hi := cli.StyleSecondary.Render
			ok := cli.StyleSuccess.Render

			cursor := hi("▸ ")
			pad := "  "
			lines := []string{
				title,
				"",
				cursor + ok("[x]") + " Claude Code   " + dim("session recording + prime hooks"),
				pad + ok("[x]") + " Codex CLI     " + dim("session recording"),
				pad + "[ ] Gemini CLI    " + dim("session recording"),
				pad + ok("[x]") + " Cursor        " + dim("rules + session import"),
				pad + "[ ] Aider         " + dim("session import"),
				"",
				dim("  ↑/↓ navigate · space toggle · enter confirm · esc cancel"),
			}
			out := ""
			for _, l := range lines {
				out += l + "\n"
			}
			return out
		},
	})
}
