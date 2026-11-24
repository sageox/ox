package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/theme"
)

// TestMarkSource_IsTheWholeBitmap pins the drawing's size: braille packs it 2
// by 4, so a row or column added or dropped silently crops the mark.
func TestMarkSource_IsTheWholeBitmap(t *testing.T) {
	require.Len(t, markSource, MarkRows*4)
	for i, row := range markSource {
		assert.Len(t, []rune(row), MarkWidth*2, "row %d", i)
	}
}

// TestRenderMark_KeepsEveryDot verifies the braille carries the bitmap exactly:
// as many raised dots as the source has ink.
// Failure prevented: a wrong dot bit mirroring or shearing the mark while it
// still looks like "some logo".
func TestRenderMark_KeepsEveryDot(t *testing.T) {
	ink := 0
	for _, row := range markSource {
		ink += len(row) - strings.Count(row, " ")
	}
	dots := 0
	for _, line := range markCells() {
		for _, r := range line {
			if r >= 0x2800 {
				for b := r - 0x2800; b != 0; b &= b - 1 {
					dots++
				}
			}
		}
	}
	assert.Equal(t, ink, dots)

	// the top-left ink of the source is its top-right tip, not the left edge
	assert.Equal(t, []string{
		" ⢠⣤⣤⣀⣀⡀      ⢀⣀⣤⣤⣶⣶⣿⡷",
		" ⠈⠻⣿⠟⣋⣥⣶⣶⣾⣿⣷⣶⣶⣬⣙⠻⣿⡿⠋ ",
		"   ⢀⣾⣿⣿⠋⠉   ⠉⠙⣿⣿⣷⠈⠁  ",
		"  ⢀⡈⢿⣿⣿⣄⣀   ⣀⣠⣿⣿⡿⠁   ",
		" ⣠⣾⣿⣦⣍⣛⠿⠿⢿⣿⡿⠿⠿⢛⣩⣴⣿⣦⡀ ",
		"⢾⣿⠿⠿⠛⠛⠉⠁      ⠈⠉⠉⠛⠛⠃ ",
	}, markCells())
}

// TestRenderMark_SameShapeWithAndWithoutColor verifies every row is MarkWidth
// columns in both renderings, and that no-color output carries no escape.
// Failure prevented: rows of uneven width shearing the text drawn beside the
// mark, or an escape sequence reaching a NO_COLOR terminal.
func TestRenderMark_SameShapeWithAndWithoutColor(t *testing.T) {
	prev := theme.Profile
	t.Cleanup(func() { theme.Profile = prev })

	for _, profile := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ASCII} {
		theme.Profile = profile
		lines := RenderMark()
		require.Len(t, lines, MarkRows)
		for _, line := range lines {
			assert.Equal(t, MarkWidth, ansi.StringWidth(line))
			if profile == colorprofile.ASCII {
				assert.Equal(t, ansi.Strip(line), line)
			}
		}
	}
}
