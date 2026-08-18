package cli

import (
	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/theme"
)

// SageOx brand color palette (unified, CLI-optimized)
// Primary: Sage green - earthy, wise, calm
// Secondary: Copper gold - premium, trustworthy
// Accent: Forest green - depth, nature
// AdaptiveColor automatically adjusts for light/dark terminals
//
// Colors are sourced from the sageox-design system.
// See: internal/theme/generated.go

// Re-export theme colors for backward compatibility.
//
// theme.Adapt downgrades each adaptive color to the terminal's real color
// depth. The generated tokens are 24-bit, and Style.Render() no longer degrades
// them in lipgloss v2 — see internal/theme/profile.go for what that does to a
// 256-color terminal.
var (
	ColorPrimary   = theme.Adapt(theme.ColorPrimary)
	ColorSecondary = theme.Adapt(theme.ColorSecondary)
	ColorAccent    = theme.Adapt(theme.ColorAccent)
	ColorSuccess   = theme.Adapt(theme.ColorSuccess)
	ColorWarning   = theme.Adapt(theme.ColorWarning)
	ColorError     = theme.Adapt(theme.ColorError)
	ColorInfo      = theme.Adapt(theme.ColorInfo)
	ColorDim       = theme.Adapt(theme.ColorDim)
	ColorPublic    = theme.Adapt(theme.ColorPublic)
	ColorPrivate   = theme.Adapt(theme.ColorPrivate)
)

// Text styles
var (
	StyleBrand = lipgloss.NewStyle().
			Foreground(ColorPrimary).
			Bold(true)

	StyleSecondary = lipgloss.NewStyle().
			Foreground(ColorSecondary)

	StyleAccent = lipgloss.NewStyle().
			Foreground(ColorAccent)

	StyleSuccess = lipgloss.NewStyle().
			Foreground(ColorSuccess)

	StyleWarning = lipgloss.NewStyle().
			Foreground(ColorWarning)

	StyleError = lipgloss.NewStyle().
			Foreground(ColorError)

	StyleInfo = lipgloss.NewStyle().
			Foreground(ColorInfo)

	StyleDim = lipgloss.NewStyle().
			Foreground(ColorDim)

	StyleBold = lipgloss.NewStyle().
			Bold(true)

	StyleCommand = lipgloss.NewStyle().
			Foreground(ColorSecondary)

	StyleFlag = lipgloss.NewStyle().
			Foreground(ColorInfo)

	StyleGroupHeader = lipgloss.NewStyle().
				Foreground(ColorPrimary).
				Bold(true)

	// StyleFile for file/directory paths (uses accent color for visual distinction)
	StyleFile = lipgloss.NewStyle().
			Foreground(ColorAccent)

	// SpinnerStyle for spinner animations
	SpinnerStyle = lipgloss.NewStyle().
			Foreground(ColorPrimary)

	// StyleCallout for contextual highlights (stars, step indicators, guided actions)
	// Used to draw attention to recommended next actions based on user state
	StyleCallout = lipgloss.NewStyle().
			Foreground(ColorAccent)

	// StyleCalloutBold for emphasized callout elements (command names in guided actions)
	StyleCalloutBold = lipgloss.NewStyle().
				Foreground(ColorSecondary).
				Bold(true)

	// Visibility styles (public=teal, private=amber)
	StylePublic = lipgloss.NewStyle().
			Foreground(ColorPublic)

	StylePrivate = lipgloss.NewStyle().
			Foreground(ColorPrivate)
)

// Wordmark returns the two-tone "SageOx" brand wordmark as a rendered string.
func Wordmark() string {
	sage := lipgloss.NewStyle().Foreground(theme.Adapt(theme.ColorWordmarkSage)).Bold(true)
	ox := lipgloss.NewStyle().Foreground(theme.Adapt(theme.ColorWordmarkOx)).Bold(true)
	return sage.Render("Sage") + ox.Render("Ox")
}
