package inspector

import (
	"fmt"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/dashboard/theme"
)

// RenderDaemonErrors renders the stored error list in the inspector pane.
func RenderDaemonErrors(target domain.InspectorTarget, width int) string {
	var lines []string
	lines = append(lines, theme.InspectorTitleStyle.Render("Stored Errors"))
	lines = append(lines, "")

	errors := target.StoredErrors
	if len(errors) == 0 {
		lines = append(lines, theme.InspectorDimStyle.Render("no unviewed errors"))
		lines = append(lines, "")
		lines = append(lines, theme.InspectorHintStyle.Render("[r] refresh"))
		return strings.Join(lines, "\n")
	}

	for _, e := range errors {
		severityStyle := storedErrorSeverityStyle(e.Severity)
		lines = append(lines, theme.InspectorLabelStyle.Render(humanTime(e.Timestamp))+" "+severityStyle.Render("["+strings.ToUpper(e.Severity)+"]"))
		if e.Code != "" {
			lines = append(lines, theme.InspectorDimStyle.Render("  code: "+e.Code))
		}
		for _, line := range wrapText(e.Message, width-4) {
			lines = append(lines, "  "+theme.InspectorDimStyle.Render(line))
		}
		lines = append(lines, "")
	}

	lines = append(lines, theme.InspectorHintStyle.Render("[r] refresh to check for new errors"))
	return strings.Join(lines, "\n")
}

// storedErrorSeverityStyle returns a colored style for the error severity level.
func storedErrorSeverityStyle(severity string) lipgloss.Style {
	switch severity {
	case "error", "critical":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5555"))
	case "warning":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#FFB86C"))
	default:
		return lipgloss.NewStyle()
	}
}

// RenderAgentWork renders the agent work queue status in the inspector pane.
func RenderAgentWork(target domain.InspectorTarget, width int) string {
	aw := target.AgentWork
	if aw == nil {
		return theme.InspectorDimStyle.Render("agent work not available")
	}

	var lines []string
	lines = append(lines, theme.InspectorTitleStyle.Render("Agent Work"))
	lines = append(lines, "")

	enabledStr := "no"
	if aw.Enabled {
		enabledStr = "yes"
	}
	availStr := "no"
	if aw.AgentAvail {
		availStr = "yes"
	}
	lines = append(lines, row("Enabled", enabledStr))
	if aw.AgentType != "" {
		lines = append(lines, row("Agent type", aw.AgentType))
	}
	lines = append(lines, row("Agent available", availStr))
	lines = append(lines, row("Queue depth", fmt.Sprintf("%d", aw.QueueDepth)))

	lines = append(lines, "")
	lines = append(lines, theme.InspectorTitleStyle.Render("Stats"))
	lines = append(lines, row("Total invocations", fmt.Sprintf("%d", aw.Stats.TotalInvocations)))
	lines = append(lines, row("Successes", fmt.Sprintf("%d", aw.Stats.Successes)))
	lines = append(lines, row("Failures", fmt.Sprintf("%d", aw.Stats.Failures)))

	if len(aw.Active) > 0 {
		lines = append(lines, "")
		lines = append(lines, theme.InspectorTitleStyle.Render("Active"))
		for _, p := range aw.Active {
			dur := time.Since(p.StartedAt).Round(time.Second)
			label := fmt.Sprintf("%s · %s · %s", p.WorkType, shortenPath(p.Target, width-30), dur)
			lines = append(lines, theme.InspectorDimStyle.Render("  "+label))
		}
	}

	recent := aw.Recent
	if len(recent) > 5 {
		recent = recent[:5]
	}
	if len(recent) > 0 {
		lines = append(lines, "")
		lines = append(lines, theme.InspectorTitleStyle.Render("Recent"))
		for _, p := range recent {
			status := p.Status
			label := fmt.Sprintf("%s · %s · %s", p.WorkType, status, p.Duration.Round(time.Second))
			if p.Error != "" {
				label += " · " + p.Error
			}
			lines = append(lines, theme.InspectorDimStyle.Render("  "+label))
		}
	}

	return strings.Join(lines, "\n")
}

// RenderCallers renders the list of connected clones/worktrees in the inspector pane.
func RenderCallers(target domain.InspectorTarget, width int) string {
	var lines []string
	lines = append(lines, theme.InspectorTitleStyle.Render("Connected Clones"))
	lines = append(lines, "")

	callers := target.Callers
	if len(callers) == 0 {
		lines = append(lines, theme.InspectorDimStyle.Render("no connected clones"))
		return strings.Join(lines, "\n")
	}

	for _, c := range callers {
		lines = append(lines, row("Path", shortenPath(c.Path, width-8)))
		if c.AgentID != "" {
			lines = append(lines, row("Agent", c.AgentID))
		}
		lines = append(lines, row("Last seen", humanTime(c.LastSeen)))
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

