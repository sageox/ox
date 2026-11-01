package entries

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/ui"
	"github.com/sageox/ox/internal/uicatalog"
)

// TestInitDockReference verifies the catalog shows what `ox init` ends on: who
// can read the sessions, the plan prompt, and the once-only add-on offer.
func TestInitDockReference(t *testing.T) {
	entry, ok := uicatalog.Get("init-dock")
	require.True(t, ok)
	require.Equal(t, uicatalog.FamilyScreen, entry.Family)
	view := entry.Render()
	text := ansi.Strip(view)

	assert.Contains(t, text, "Repo  sageox/ox  →  Team  Team A")
	assert.Contains(t, text, "Sessions go to Team A's Ledger; teammates can read them.")
	assert.Contains(t, text, "Sessions stay on this machine until you upload them to Team A's Ledger.")
	assert.Contains(t, text, "TRY THIS FIRST")
	assert.Contains(t, text, "Plan the next thing you're building.")
	assert.Contains(t, text, "ox pet enable")
	assert.Contains(t, text, "Shown once on this machine.")

	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, ui.InitPlanPrompt) {
			// deliberately one unwrapped line: the terminal soft-wraps it, and a
			// click-and-copy then takes the sentence, not a border or a hard break
			continue
		}
		assert.LessOrEqual(t, ansi.StringWidth(line), 80, line)
	}
}
