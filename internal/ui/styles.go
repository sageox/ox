package ui

import (
	"strings"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/theme"
)

// Color palette - unified SageOx brand colors for CLI
// True Color (24-bit) hex values, degrades to ANSI 256 automatically
//
// Colors are sourced from the sageox-design system.
// See: internal/theme/generated.go

// Re-export hex constants for backward compatibility and glamour integration
const (
	HexPrimary = theme.HexPrimary
	HexAccent  = theme.HexAccent
	HexPass    = theme.HexPass
	HexWarn    = theme.HexWarn
	HexFail    = theme.HexFail
	HexMuted   = theme.HexMuted
	HexText    = theme.HexText
	HexTextDim = theme.HexTextDim
	HexBgDark  = theme.HexBgDark
	HexBgCode  = theme.HexBgCode
	HexPublic  = theme.HexPublic
	HexPrivate = theme.HexPrivate
)

var (
	// Brand primary - the core SageOx identity color
	ColorPrimary = lipgloss.Color(HexPrimary) // sage green - brand identity

	// Semantic status colors (aligned with brand)
	ColorPass   = lipgloss.Color(HexPass)   // sage green - brand success
	ColorWarn   = lipgloss.Color(HexWarn)   // copper gold - brand warning
	ColorFail   = lipgloss.Color(HexFail)   // ox red - brand error
	ColorMuted  = lipgloss.Color(HexMuted)  // charcoal gray - recessive
	ColorAccent = lipgloss.Color(HexAccent) // info blue - brand accent

	// Text colors
	ColorText    = lipgloss.Color(HexText)    // soft gray-white - dark terminal optimized
	ColorTextDim = lipgloss.Color(HexTextDim) // dim text - pairs with sage/charcoal

	// Visibility colors (from sageox-mono public/private semantic tokens)
	ColorPublic  = lipgloss.Color(HexPublic)  // teal - public visibility
	ColorPrivate = lipgloss.Color(HexPrivate) // amber - private visibility
)

// Status styles - consistent across all commands
var (
	PassStyle   = lipgloss.NewStyle().Foreground(ColorPass)
	WarnStyle   = lipgloss.NewStyle().Foreground(ColorWarn)
	FailStyle   = lipgloss.NewStyle().Foreground(ColorFail)
	MutedStyle  = lipgloss.NewStyle().Foreground(ColorMuted)
	AccentStyle = lipgloss.NewStyle().Foreground(ColorAccent)
)

// Visibility styles
var (
	PublicStyle  = lipgloss.NewStyle().Foreground(ColorPublic)
	PrivateStyle = lipgloss.NewStyle().Foreground(ColorPrivate)
)

// Category header style - using accent color for brand identity
var CategoryStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)

// Status icons
const (
	IconPass  = "✓"
	IconWarn  = "⚠"
	IconFail  = "✖" // heavier icon for better visibility
	IconSkip  = "-"
	IconInfo  = "ℹ"
	IconAgent = "★" // agent-required indicator (star for special attention)
)

// Tree characters for hierarchical display
const (
	TreeChild    = "⎿ "  // child indicator
	TreeLast     = "└─ " // last child / detail line
	TreeBranch   = "├─ " // non-last sibling
	TreeVertical = "│  " // vertical bar carried by non-last ancestors
	TreeIndent   = "  "  // 2-space indent per level
)

// Separators
const (
	SeparatorLight = "──────────────────────────────────────────"
	SeparatorHeavy = "══════════════════════════════════════════"
)

// RenderPass renders text with pass (green) styling
func RenderPass(s string) string {
	return PassStyle.Render(s)
}

// RenderWarn renders text with warning (yellow) styling
func RenderWarn(s string) string {
	return WarnStyle.Render(s)
}

// RenderFail renders text with fail (red) styling
func RenderFail(s string) string {
	return FailStyle.Render(s)
}

// RenderMuted renders muted/dimmed text
func RenderMuted(s string) string {
	return MutedStyle.Render(s)
}

// RenderAccent renders text with accent (blue) styling
func RenderAccent(s string) string {
	return AccentStyle.Render(s)
}

// RenderCategory renders a category header in uppercase with accent color
func RenderCategory(s string) string {
	return CategoryStyle.Render(strings.ToUpper(s))
}

// RenderSeparator renders the light separator line
func RenderSeparator() string {
	return MutedStyle.Render(SeparatorLight)
}

// RenderPassIcon renders the pass icon with styling
func RenderPassIcon() string {
	return PassStyle.Render(IconPass)
}

// RenderWarnIcon renders the warning icon with styling
func RenderWarnIcon() string {
	return WarnStyle.Render(IconWarn)
}

// RenderFailIcon renders the fail icon with styling
func RenderFailIcon() string {
	return FailStyle.Render(IconFail)
}

// RenderSkipIcon renders the skip icon with styling
func RenderSkipIcon() string {
	return MutedStyle.Render(IconSkip)
}

// RenderInfoIcon renders the info icon with styling
func RenderInfoIcon() string {
	return AccentStyle.Render(IconInfo)
}

// RenderAgentIcon renders the agent icon with styling
func RenderAgentIcon() string {
	return AccentStyle.Render(IconAgent)
}

// RenderPublic renders text with public (teal) styling
func RenderPublic(s string) string {
	return PublicStyle.Render(s)
}

// RenderPrivate renders text with private (amber) styling
func RenderPrivate(s string) string {
	return PrivateStyle.Render(s)
}

// RenderVisibility renders a visibility value with semantic color
func RenderVisibility(visibility string) string {
	switch strings.ToLower(visibility) {
	case "public":
		return PublicStyle.Render(visibility)
	case "private":
		return PrivateStyle.Render(visibility)
	default:
		return visibility
	}
}
