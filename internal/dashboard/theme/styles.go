package theme

import (
	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/cli"
)

// Pane chrome — focused pane uses a double border to stand out clearly.
var (
	PaneBorderFocused = lipgloss.NewStyle().
				Border(lipgloss.DoubleBorder()).
				BorderForeground(cli.ColorPrimary)

	PaneBorderUnfocused = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(cli.ColorDim)
)

// Header row styles.
var (
	HeaderStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(cli.ColorPrimary)

	HeaderDimStyle = lipgloss.NewStyle().
			Foreground(cli.ColorDim)
)

// Navigation tree styles.
var (
	NavSectionStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(cli.ColorSecondary)

	NavSelectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#1A1D1C")).
				Background(cli.ColorPrimary)

	NavItemStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#C8D0C8"))

	NavDimStyle = lipgloss.NewStyle().
			Foreground(cli.ColorDim)
)

// Timeline pane styles.
var (
	TimelineNowLabel = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#5fef8f"))

	TimelineRecentLabel = lipgloss.NewStyle().
				Bold(true).
				Foreground(cli.ColorPrimary)

	TimelineEarlierLabel = lipgloss.NewStyle().
				Foreground(cli.ColorDim)

	TimelineDivider = lipgloss.NewStyle().
			Foreground(cli.ColorSecondary)

	TimelineEntryActive = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#C8D0C8"))

	TimelineEntryMuted = lipgloss.NewStyle().
				Foreground(cli.ColorDim)

	TimelineMurmurStyle = lipgloss.NewStyle().
				Foreground(cli.ColorSecondary)

	TimelineSelectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#1A1D1C")).
				Background(cli.ColorSecondary)

	TeamPulseHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(cli.ColorPrimary)

	TeamPulseMetaStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#C8D0C8"))

	MurmurHotStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(cli.ColorSecondary)

	MurmurWarmStyle = lipgloss.NewStyle().
			Foreground(cli.ColorInfo)

	MurmurCoolStyle = lipgloss.NewStyle().
			Foreground(cli.ColorDim)

	// Topic badge styles with background color "pill" look.
	TopicBadgeWIP = lipgloss.NewStyle().
			Background(lipgloss.Color("#2d4a1e")).
			Foreground(lipgloss.Color("#a8d08a")).
			Padding(0, 1)

	TopicBadgeBlocked = lipgloss.NewStyle().
				Background(lipgloss.Color("#4a2010")).
				Foreground(lipgloss.Color("#f09070")).
				Padding(0, 1)

	TopicBadgeDecision = lipgloss.NewStyle().
				Background(lipgloss.Color("#1a3050")).
				Foreground(lipgloss.Color("#7fc0f0")).
				Padding(0, 1)

	TopicBadgeReview = lipgloss.NewStyle().
				Background(lipgloss.Color("#302050")).
				Foreground(lipgloss.Color("#c090f0")).
				Padding(0, 1)

	TopicBadgeDefault = lipgloss.NewStyle().
				Background(lipgloss.Color("#2a2e2a")).
				Foreground(cli.ColorDim).
				Padding(0, 1)

	// SparklineActiveStyle is used for sparklines representing recent/live activity.
	SparklineActiveStyle = lipgloss.NewStyle().Foreground(cli.ColorSecondary)

	// SparklineDimStyle is used for sparklines with little or no activity.
	SparklineDimStyle = lipgloss.NewStyle().Foreground(cli.ColorDim)
)

// Inspector pane styles.
var (
	InspectorTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(cli.ColorSecondary)

	InspectorLabelStyle = lipgloss.NewStyle().
				Foreground(cli.ColorPrimary).
				Width(12)

	InspectorValueStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#C8D0C8"))

	InspectorDimStyle = lipgloss.NewStyle().
				Foreground(cli.ColorDim)

	InspectorHintStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#5a6066")).
				Italic(true)
)

// Status bar styles.
var (
	StatusBarBase = lipgloss.NewStyle().
			Padding(0, 1)

	StatusHealthy = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#5fef8f"))

	StatusWarning = lipgloss.NewStyle().
			Bold(true).
			Foreground(cli.ColorWarning)

	StatusError = lipgloss.NewStyle().
			Bold(true).
			Foreground(cli.ColorError)

	StatusDim = lipgloss.NewStyle().
			Foreground(cli.ColorDim)

	StatusSeparator = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#3a3f3c"))
)

// PaneTitle renders a styled pane title. When focused the title uses the secondary
// (warm amber) color; when unfocused it is dimmed so the active pane stands out.
func PaneTitle(title string, focused bool) string {
	if focused {
		return lipgloss.NewStyle().Bold(true).Foreground(cli.ColorSecondary).Render(title)
	}
	return HeaderDimStyle.Render(title)
}
