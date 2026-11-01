package pet

import (
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/theme"
)

// withProfile swaps the package-level theme profile. Not parallel-safe: the
// tests that call it run serially.
func withProfile(t *testing.T, p colorprofile.Profile) {
	t.Helper()
	prev := theme.Profile
	theme.Profile = p
	t.Cleanup(func() { theme.Profile = prev })
}

// TestTerminalFrame_DrawsAFigureInTheRequestedBox verifies the art is exactly
// rows tall and Columns wide on every line, and that something is drawn.
// Failure prevented: a ragged or empty picture that shears the success block
// it sits beside, or an all-transparent crop that prints blank rows.
func TestTerminalFrame_DrawsAFigureInTheRequestedBox(t *testing.T) {
	withProfile(t, colorprofile.TrueColor)

	for _, rows := range []int{4, 6, 8} {
		art, err := TerminalFrame("confetti", 0.35, rows)
		require.NoError(t, err)
		require.Len(t, art.Lines, rows)
		assert.Positive(t, art.Columns)

		var drawn int
		for i, line := range art.Lines {
			assert.Equal(t, art.Columns, ansi.StringWidth(line), "rows=%d line %d is the box's width", rows, i)
			drawn += strings.Count(ansi.Strip(line), "▀") + strings.Count(ansi.Strip(line), "▄")
		}
		assert.Greater(t, drawn, art.Columns, "rows=%d: a figure fills more than one row of cells", rows)
		assert.Contains(t, art.Lines[0]+art.Lines[rows/2], "\x1b[", "truecolor cells carry color")
	}
}

// TestTerminalFrame_PicksTheFrameByPosition verifies at selects different
// frames: the standing start of the confetti loop is not its arms-up middle.
// Failure prevented: a position that is ignored, so every caller draws frame 0.
func TestTerminalFrame_PicksTheFrameByPosition(t *testing.T) {
	withProfile(t, colorprofile.TrueColor)

	first, err := TerminalFrame("confetti", 0, 8)
	require.NoError(t, err)
	middle, err := TerminalFrame("confetti", 0.35, 8)
	require.NoError(t, err)
	again, err := TerminalFrame("confetti", 0.35, 8)
	require.NoError(t, err)

	assert.NotEqual(t, first.Lines, middle.Lines)
	assert.Equal(t, middle, again, "the same request draws the same picture")
}

// TestTerminalFrame_DegradesColorWithTheTerminal verifies a 256-color terminal
// gets palette codes, never 24-bit ones.
// Failure prevented: a 24-bit code on a terminal that reads its channels as
// background colors, the unreadable-block bug internal/theme exists to prevent.
func TestTerminalFrame_DegradesColorWithTheTerminal(t *testing.T) {
	withProfile(t, colorprofile.ANSI256)

	art, err := TerminalFrame("confetti", 0.35, 6)
	require.NoError(t, err)
	joined := strings.Join(art.Lines, "\n")
	assert.NotContains(t, joined, "38;2;", "no 24-bit foreground on a 256-color terminal")
	assert.NotContains(t, joined, "48;2;", "no 24-bit background on a 256-color terminal")
	assert.Contains(t, joined, "38;5;")
}

func TestTerminalFrame_RejectsBadRequests(t *testing.T) {
	withProfile(t, colorprofile.TrueColor)

	tests := []struct {
		name      string
		animation string
		at        float64
		rows      int
	}{
		{"no rows", "confetti", 0.5, 0},
		{"negative rows", "confetti", 0.5, -2},
		{"position below the loop", "confetti", -0.1, 6},
		{"position past the loop", "confetti", 1.1, 6},
		{"unknown animation", "no-such-animation", 0.5, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			art, err := TerminalFrame(tt.animation, tt.at, tt.rows)
			require.Error(t, err)
			assert.Nil(t, art)
		})
	}
}

// TestTerminalFrame_EveryAnimationShowsTheFigure verifies each animation the
// mod can ask for has a desktop strip whose crop still holds Oxy.
// Failure prevented: a re-imported desktop set whose crop box moves off the
// figure, which would print blank rows only for the animation that shifted.
func TestTerminalFrame_EveryAnimationShowsTheFigure(t *testing.T) {
	if testing.Short() {
		t.Skip("short: decodes every desktop strip")
	}
	withProfile(t, colorprofile.TrueColor)

	for _, name := range Animations {
		art, err := TerminalFrame(name, 0.5, 6)
		require.NoError(t, err, name)
		var drawn int
		for _, line := range art.Lines {
			text := ansi.Strip(line)
			drawn += strings.Count(text, "▀") + strings.Count(text, "▄")
		}
		assert.Positive(t, drawn, name)
	}
}
