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
