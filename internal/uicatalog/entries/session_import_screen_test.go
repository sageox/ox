package entries

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/sageox/ox/internal/uicatalog"
	"github.com/stretchr/testify/require"
)

// The composed screen's reference must carry the selection and confirmation
// contract without overflowing an ordinary 80-column terminal.
func TestSessionImportScreenReference(t *testing.T) {
	entry, ok := uicatalog.Get("session-import-screen")
	require.True(t, ok)
	require.Equal(t, uicatalog.FamilyScreen, entry.Family)
	require.Equal(t, uicatalog.RendererFreeze, entry.Renderer)
	view := entry.Render()
	text := ansi.Strip(view)
	require.Contains(t, text, "2 selected")
	require.Contains(t, text, "Nothing uploads until you confirm in this terminal")
	require.Contains(t, text, "Opening request")
	require.Contains(t, text, "Human prompts (1)")
	require.Contains(t, text, "Last AI reply")
	require.Contains(t, text, "enter review · b browser · q cancel")
	for _, line := range strings.Split(view, "\n") {
		require.LessOrEqual(t, ansi.StringWidth(line), 80)
	}
}
