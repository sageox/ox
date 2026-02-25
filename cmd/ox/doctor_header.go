package main

import (
	"fmt"
	"io"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/sageox/ox/internal/theme"
	"github.com/sageox/ox/internal/version"
)

// doctorASCIILines contains the 4-line SageOx ASCII wordmark.
// "Sage" occupies runes 0-12, "Ox" occupies runes 13+.
var doctorASCIILines = []string{
	"▞▀▖         ▞▀▖   ",
	"▚▄ ▝▀▖▞▀▌▞▀▖▌ ▌▚▗▘",
	"▖ ▌▞▀▌▚▄▌▛▀ ▌ ▌▗▚ ",
	"▝▀ ▝▀▘▗▄▘▝▀▘▝▀ ▘ ▘",
}

// artSplitPoint is the rune index where "Ox" begins in the ASCII art.
// "Sage" = runes 0..11, "Ox" = runes 12+.
const artSplitPoint = 12

// renderDoctorHeader writes the branded header with ASCII art and version info.
// Uses the same two-tone wordmark colors as the Wordmark() function:
// "Sage" in ColorWordmarkSage (lighter sage), "Ox" in ColorWordmarkOx (darker sage).
// Set fixMode to true to show "— fix mode" after the version.
func renderDoctorHeader(w io.Writer, fixMode bool) {
	sageStyle := lipgloss.NewStyle().Foreground(theme.ColorWordmarkSage)
	oxStyle := lipgloss.NewStyle().Foreground(theme.ColorWordmarkOx)

	fmt.Fprintln(w)
	for _, line := range doctorASCIILines {
		runes := []rune(line)
		split := artSplitPoint
		if split > len(runes) {
			split = len(runes)
		}
		sagePart := string(runes[:split])
		oxPart := string(runes[split:])
		fmt.Fprintln(w, sageStyle.Render(sagePart)+oxStyle.Render(oxPart))
	}

	// version line in sage green (primary brand color)
	ver := "ox " + version.Version
	if fixMode {
		ver += lipgloss.NewStyle().Foreground(theme.ColorDim).Render(" — fix mode")
	}
	fmt.Fprintln(w, sageStyle.Render(ver))
	fmt.Fprintln(w)
}
