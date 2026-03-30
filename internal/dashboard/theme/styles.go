package theme

import (
	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/cli"
)

// Pane chrome — borders differ by focus state (TokenBorderFocused / TokenBorderUnfocused).
var (
	PaneBorderFocused = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(cli.ColorPrimary)

	PaneBorderUnfocused = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(cli.ColorDim)
)

// Header row styles (TokenHeader).
var (
	// HeaderStyle is the primary pane title — bold so it reads at a glance.
	HeaderStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(cli.ColorPrimary)

	// HeaderDimStyle is used for secondary / subtitle text alongside the header.
	HeaderDimStyle = lipgloss.NewStyle().
			Foreground(cli.ColorDim)
)

// Navigation tree styles (TokenNavSection / TokenNavSelected / TokenNavLeaf).
var (
	// NavSectionStyle renders collapsible section headings.
	NavSectionStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(cli.ColorPrimary)

	// NavSelectedStyle marks the cursor row — copper gold draws the eye.
	NavSelectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(cli.ColorSecondary)

	// NavItemStyle is the default style for all non-selected leaf nodes.
	NavItemStyle = lipgloss.NewStyle()

	// NavDimStyle de-emphasises metadata or disabled nodes.
	NavDimStyle = lipgloss.NewStyle().
			Foreground(cli.ColorDim)
)

// Timeline pane styles (TokenTimeline*).
var (
	// TimelineNowLabel highlights events that happened in the last few minutes.
	TimelineNowLabel = lipgloss.NewStyle().
				Bold(true).
				Foreground(cli.ColorSuccess)

	// TimelineRecentLabel for events within the last hour or so.
	TimelineRecentLabel = lipgloss.NewStyle().
				Foreground(cli.ColorPrimary)

	// TimelineEarlierLabel for older entries that need less visual weight.
	TimelineEarlierLabel = lipgloss.NewStyle().
				Foreground(cli.ColorDim)

	// TimelineDivider is the separator between time buckets.
	TimelineDivider = lipgloss.NewStyle().
			Foreground(cli.ColorDim)

	// TimelineEntryActive is used for entries that belong to live / in-progress events.
	TimelineEntryActive = lipgloss.NewStyle()

	// TimelineEntryMuted de-emphasises background or completed events.
	TimelineEntryMuted = lipgloss.NewStyle().
				Foreground(cli.ColorDim)

	// TimelineMurmurStyle renders murmur (WIP broadcast) entries in copper gold
	// to distinguish AI coworker activity from sync and session events.
	TimelineMurmurStyle = lipgloss.NewStyle().
				Foreground(cli.ColorSecondary)

	// TimelineSelectedStyle marks the cursor row in the timeline.
	TimelineSelectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(cli.ColorSecondary)
)

// Inspector pane styles (TokenInspector*).
var (
	// InspectorTitleStyle renders the entity name at the top of the inspector.
	InspectorTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(cli.ColorPrimary)

	// InspectorLabelStyle renders field labels. Fixed width aligns the value column.
	InspectorLabelStyle = lipgloss.NewStyle().
				Foreground(cli.ColorSecondary).
				Width(12)

	// InspectorValueStyle renders field values — normal weight for readability.
	InspectorValueStyle = lipgloss.NewStyle()

	// InspectorDimStyle is for supplementary detail that shouldn't compete with values.
	InspectorDimStyle = lipgloss.NewStyle().
				Foreground(cli.ColorDim)

	// InspectorHintStyle surfaces keyboard hints and contextual tips.
	InspectorHintStyle = lipgloss.NewStyle().
				Foreground(cli.ColorDim).
				Italic(true)
)

// Status bar styles.
var (
	// StatusBarBase provides consistent horizontal padding across the full-width bar.
	StatusBarBase = lipgloss.NewStyle().
			Padding(0, 1)

	// StatusHealthy renders HealthOK indicators.
	StatusHealthy = lipgloss.NewStyle().
			Foreground(cli.ColorSuccess)

	// StatusWarning renders HealthWarn indicators.
	StatusWarning = lipgloss.NewStyle().
			Foreground(cli.ColorWarning)

	// StatusError renders HealthError indicators.
	StatusError = lipgloss.NewStyle().
			Foreground(cli.ColorError)

	// StatusDim de-emphasises secondary status bar segments.
	StatusDim = lipgloss.NewStyle().
			Foreground(cli.ColorDim)

	// StatusSeparator renders the dividers between status bar segments.
	StatusSeparator = lipgloss.NewStyle().
				Foreground(cli.ColorDim)
)

// PaneTitle renders a styled pane title. When focused the title uses the primary
// color; when unfocused it is dimmed so the active pane stands out clearly.
func PaneTitle(title string, focused bool) string {
	if focused {
		return HeaderStyle.Render(title)
	}
	return HeaderDimStyle.Render(title)
}
