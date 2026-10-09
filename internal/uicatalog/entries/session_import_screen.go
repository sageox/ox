package entries

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/uicatalog"
)

func init() {
	uicatalog.Register(uicatalog.Entry{
		Name:    "session-import-screen",
		Family:  uicatalog.FamilyScreen,
		Package: "cmd/ox/session_import_tui.go",
		Exports: []string{"runImportTerminal"},
		Since:   "0.20.0",
		WhenToUse: "Review native sessions before importing them: opening-request " +
			"labels, eligibility, checkboxes, and read-only conversation excerpts. " +
			"Reference for a session picker with asynchronous content and exact selection.",
		WhenNotTo: "A small checkbox group without content previews (use Multi-select). " +
			"Already imported Ledger sessions (use Session-list screen). " +
			"A generic table (use Columns).",
		// A static screen reference uses freeze. The live command owns keyboard
		// navigation and loading; recording this frame would imply fake motion.
		Renderer: uicatalog.RendererFreeze,
		Render: func() string {
			brand := cli.StyleBrand.Bold(true).Render
			dim := cli.StyleDim.Render
			// At 80 columns the live picker stacks its list above the excerpts.
			// Wider terminals use a split view, without extra frame chrome.
			lines := []string{
				brand("Choose sessions to import · 2 selected"),
				"Math Blitz · repo_math_blitz · private",
				dim("Preview only. Nothing uploads until you confirm in this terminal."),
				"",
				brand("> [x] Add a persistent high-score table"),
				"      Oct 01 10:30 · claude · ready",
				"  [x] Make difficulty adapt to the player",
				"      Oct 02 09:15 · codex · ready",
				"",
				dim(strings.Repeat("─", 80)),
				"claude · 21bb267b-05af-43ce-b42f-f850b70d2ef4",
				"",
				"Opening request",
				"Add a persistent high-score table.",
				"",
				"Human prompts (1)",
				"1. Add a persistent high-score table.",
				"",
				"Last AI reply",
				"The high-score table is implemented and tests pass.",
				dim("↑/↓ browse · space toggle · a all · x clear · enter review · b browser · q cancel"),
				dim("pgup/pgdown scroll · home/end first/last excerpt"),
			}
			for i, line := range lines {
				lines[i] = ansi.Truncate(line, 80, "…")
			}
			return strings.Join(lines, "\n")
		},
	})
}
