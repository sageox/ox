package ui

import (
	"fmt"
	"image/color"
	"strings"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/theme"
)

// BoxVariant determines the border color of a box.
type BoxVariant int

const (
	BoxDefault BoxVariant = iota // border: theme.ColorDim
	BoxInfo                      // border: theme.ColorInfo
	BoxWarning                   // border: theme.ColorWarning
	BoxError                     // border: theme.ColorError
	BoxSuccess                   // border: theme.ColorSuccess
)

// variantColor returns the theme color for a given variant.
func variantColor(v BoxVariant) color.Color {
	switch v {
	case BoxInfo:
		return theme.ColorInfo
	case BoxWarning:
		return theme.ColorWarning
	case BoxError:
		return theme.ColorError
	case BoxSuccess:
		return theme.ColorSuccess
	default:
		return theme.ColorDim
	}
}

// RenderBox renders content in a bordered box with an optional title.
// Uses lipgloss.RoundedBorder() for rounded corners.
func RenderBox(title, content string, variant BoxVariant) string {
	color := variantColor(variant)

	body := content
	if title != "" {
		titleLine := lipgloss.NewStyle().Bold(true).Foreground(color).Render(title)
		body = titleLine + "\n" + content
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(color).
		Padding(0, 2).
		Render(body)

	return box
}

// RenderSummaryBox renders a summary box for doctor-style output.
// Shows pass/warn/fail counts with colored icons and an optional hint line.
func RenderSummaryBox(passCount, warnCount, failCount, skipCount int, hint string) string {
	var parts []string

	if passCount > 0 {
		parts = append(parts, fmt.Sprintf("%s %d passed", RenderPassIcon(), passCount))
	}
	if warnCount > 0 {
		parts = append(parts, fmt.Sprintf("%s %d warning", RenderWarnIcon(), warnCount))
	}
	if failCount > 0 {
		parts = append(parts, fmt.Sprintf("%s %d failed", RenderFailIcon(), failCount))
	}
	if skipCount > 0 {
		parts = append(parts, fmt.Sprintf("%s %d skipped", RenderSkipIcon(), skipCount))
	}

	summary := strings.Join(parts, "  ")

	if hint != "" {
		summary += "\n\n" + MutedStyle.Render(hint)
	}

	return RenderBox("", summary, BoxDefault)
}
