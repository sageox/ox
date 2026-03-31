package inspector

import (
	"fmt"
	"strings"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/dashboard/theme"
)

// RenderInstance renders an AI coworker instance in the inspector pane.
// Shows agent identity, status (with color), workspace, and activity metrics.
func RenderInstance(target domain.InspectorTarget, width int) string {
	if target.Instance == nil {
		return theme.InspectorDimStyle.Render("no instance data")
	}
	inst := target.Instance

	var lines []string
	title := "AI Coworker"
	if inst.AgentType != "" {
		title = inst.AgentType
	}
	lines = append(lines, theme.InspectorTitleStyle.Render(title))
	lines = append(lines, "")

	lines = append(lines, row("Agent ID", inst.AgentID))
	if inst.AgentType != "" {
		lines = append(lines, row("Type", inst.AgentType))
	}

	statusStyle := instanceStatusStyle(inst.Status)
	lines = append(lines, theme.InspectorLabelStyle.Render("Status")+" "+statusStyle.Render(inst.Status))

	if inst.WorkspacePath != "" {
		lines = append(lines, row("Workspace", shortenPath(inst.WorkspacePath, width-14)))
	}
	if inst.ParentAgentID != "" {
		lines = append(lines, row("Parent", inst.ParentAgentID))
	}

	lines = append(lines, "")
	lines = append(lines, theme.InspectorTitleStyle.Render("Activity"))
	lines = append(lines, row("Last heartbeat", humanTime(inst.LastHeartbeat)))
	if inst.HeartbeatCount > 0 {
		lines = append(lines, row("Heartbeats", fmt.Sprintf("%d", inst.HeartbeatCount)))
	}
	if inst.CommandCount > 0 {
		lines = append(lines, row("Commands", fmt.Sprintf("%d", inst.CommandCount)))
	}
	if inst.CumulativeContextTokens > 0 {
		lines = append(lines, row("Context tokens", formatTokenCount(inst.CumulativeContextTokens)))
	}
	if !inst.LastWhisper.IsZero() {
		lines = append(lines, row("Last whisper", humanTime(inst.LastWhisper)))
	}

	return strings.Join(lines, "\n")
}

// instanceStatusStyle returns a colored lipgloss style for the given status.
func instanceStatusStyle(status string) lipgloss.Style {
	switch status {
	case "active":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#5faf5f")).Bold(true)
	case "idle":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#FFB86C"))
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
	}
}

// formatTokenCount formats a token count as a human-readable string.
func formatTokenCount(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	k := float64(n) / 1000
	if k < 10 {
		return fmt.Sprintf("%.1fk", k)
	}
	return fmt.Sprintf("%dk", int(k))
}
