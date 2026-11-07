package entries

import (
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/theme"
	"github.com/sageox/ox/internal/uicatalog"
)

// TestLoginScreenReference verifies the catalog shows the login `ox login`
// really has: a device code beside the URL, a three-line success block, a retry
// line when denied, and nothing from the old browser-callback flow.
// Failure prevented: the catalog and login-flow.md describing a
// "sageox.ai/cli/auth" page and a local listener on 127.0.0.1:7392 that the code
// no longer has, which is what they said until this test.
func TestLoginScreenReference(t *testing.T) {
	entry, ok := uicatalog.Get("login-screen")
	require.True(t, ok)
	view := entry.Render()
	text := ansi.Strip(view)

	assert.Contains(t, text, "Visit: https://sageox.ai/device?user_code=WXYZ-1234")
	assert.Contains(t, text, "Code:  WXYZ-1234  (match this in your browser)")
	assert.Contains(t, text, "Waiting for authorization...")
	assert.Contains(t, text, "Welcome back, Person (person.a@company.test)")
	assert.Contains(t, text, "Welcome, Person (person.a@company.test)", "a first login is not welcomed back")
	assert.Contains(t, text, "Next:     ox init")
	assert.Contains(t, text, "Nothing was shared. Run `ox login` to try again")

	for _, gone := range []string{"/cli/auth", "127.0.0.1:7392", "Local listener", "Tip:", "designed initially for Claude Code"} {
		assert.NotContains(t, text, gone)
	}
	for _, line := range strings.Split(view, "\n") {
		assert.LessOrEqual(t, ansi.StringWidth(line), 80, line)
	}
}

// TestLoginScreenReference_ShowsOxyAtAColorTerminal verifies the catalog draws
// Oxy beside the block when the terminal has color, the one audience `ox login`
// draws him for, and still fits 80 columns with him there.
// Failure prevented: the catalog documenting a picture the command no longer
// draws, or a picture that pushes the block past the terminal's edge.
func TestLoginScreenReference_ShowsOxyAtAColorTerminal(t *testing.T) {
	prev := theme.Profile
	theme.Profile = colorprofile.TrueColor
	t.Cleanup(func() { theme.Profile = prev })

	entry, ok := uicatalog.Get("login-screen")
	require.True(t, ok)
	view := entry.Render()

	assert.Contains(t, view, "▀", "Oxy's half-block picture")
	assert.Contains(t, ansi.Strip(view), "Welcome back, Person (person.a@company.test)")
	for _, line := range strings.Split(view, "\n") {
		assert.LessOrEqual(t, ansi.StringWidth(line), 80, line)
	}
}
