package ui

import (
	"fmt"
	"io"
	"strings"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/theme"
)

// wordmarkLines is the 4-line two-tone SageOx ASCII wordmark.
// "Sage" occupies runes 0..wordmarkSplit, "Ox" occupies runes wordmarkSplit..end.
var wordmarkLines = []string{
	"▞▀▖         ▞▀▖   ",
	"▚▄ ▝▀▖▞▀▌▞▀▖▌ ▌▚▗▘",
	"▖ ▌▞▀▌▚▄▌▛▀ ▌ ▌▗▚ ",
	"▝▀ ▝▀▘▗▄▘▝▀▘▝▀ ▘ ▘",
}

const wordmarkSplit = 12

// RenderWordmark returns the SageOx two-tone ASCII wordmark followed
// by an optional version line. Pass version="" to suppress the version
// line entirely; pass fixMode=true to append a dim "— fix mode" tag.
//
// Sage half uses theme.ColorWordmarkSage; Ox half uses theme.ColorWordmarkOx.
// Both adapt to light/dark terminals via lipgloss.
func RenderWordmark(version string, fixMode bool) string {
	sageStyle := lipgloss.NewStyle().Foreground(theme.Adapt(theme.ColorWordmarkSage))
	oxStyle := lipgloss.NewStyle().Foreground(theme.Adapt(theme.ColorWordmarkOx))
	dimStyle := lipgloss.NewStyle().Foreground(theme.Adapt(theme.ColorDim))

	var b strings.Builder
	for _, line := range wordmarkLines {
		runes := []rune(line)
		split := wordmarkSplit
		if split > len(runes) {
			split = len(runes)
		}
		fmt.Fprintln(&b, sageStyle.Render(string(runes[:split]))+oxStyle.Render(string(runes[split:])))
	}
	if version != "" {
		ver := "ox " + version
		if fixMode {
			ver += dimStyle.Render(" — fix mode")
		}
		fmt.Fprintln(&b, sageStyle.Render(ver))
	}
	return b.String()
}

// WriteWordmark writes the wordmark to w. Convenience for callers that
// already have an io.Writer (e.g., os.Stderr in doctor's header path).
func WriteWordmark(w io.Writer, version string, fixMode bool) {
	fmt.Fprintln(w)
	fmt.Fprint(w, RenderWordmark(version, fixMode))
	fmt.Fprintln(w)
}

// markSource is the SageOx mark as a 42 by 24 bitmap: any non-space rune is
// ink. It is the drawing itself, kept legible so it can be redrawn by hand;
// what a terminal shows is derived from it.
var markSource = []string{
	"                                      --- ",
	"                                  --------",
	"   -----                      ------------",
	"   ----------              -------------- ",
	"   ----------      ----      -----------  ",
	"    -------   --------------   --------   ",
	"     ----   ------------------   -----    ",
	"      --  ----------------------  ---     ",
	"         ---------      ---------  --     ",
	"        -------            -------        ",
	"        ------              ------        ",
	"       -------              ------        ",
	"       -------              -------       ",
	"        ------              ------        ",
	"        -------            -------        ",
	"     --  ---------      ---------         ",
	"     ---  ----------------------  --      ",
	"    -----   ------------------   ----     ",
	"   --------   --------------   -------    ",
	"  ------------     ----      ----------   ",
	" --------------              ----------   ",
	"------------                      -----   ",
	"--------                                  ",
	" ---                                      ",
}

// Each braille cell holds a 2 by 4 block of the bitmap, so the mark fits in
// MarkWidth by MarkRows cells with nothing lost.
const (
	MarkWidth = 21
	MarkRows  = 6
)

// brailleDots maps a position inside a cell, [row][column], to its dot bit.
var brailleDots = [4][2]rune{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}

// markCells is the mark in braille, one string per terminal row, every row
// MarkWidth cells wide. A cell with no ink is a space, not a blank braille
// rune, so the rows read as plain padding wherever braille is unavailable.
func markCells() []string {
	ink := func(x, y int) bool {
		if y >= len(markSource) {
			return false
		}
		row := []rune(markSource[y])
		return x < len(row) && row[x] != ' '
	}
	out := make([]string, MarkRows)
	for cy := range out {
		cells := make([]rune, MarkWidth)
		for cx := range cells {
			var dots rune
			for dy, bits := range brailleDots {
				for dx, bit := range bits {
					if ink(cx*2+dx, cy*4+dy) {
						dots |= bit
					}
				}
			}
			cells[cx] = ' '
			if dots != 0 {
				cells[cx] = 0x2800 + dots
			}
		}
		out[cy] = string(cells)
	}
	return out
}

// RenderMark returns the SageOx mark for a spot beside other text: MarkRows
// strings, each MarkWidth columns wide, in the brand sage. Without color it is
// the same characters, unstyled.
func RenderMark() []string {
	style := lipgloss.NewStyle().Foreground(theme.Adapt(theme.ColorPrimary))
	lines := markCells()
	if !theme.ColorEnabled() {
		return lines
	}
	for i, line := range lines {
		lines[i] = style.Render(line)
	}
	return lines
}
