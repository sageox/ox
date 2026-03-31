package inspector

import (
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/dashboard/theme"
)

// RenderWhisperHistory renders a single WhisperHistoryEntry in the inspector pane.
// Shows the whisper topic, source, delivery status, and full content.
func RenderWhisperHistory(target domain.InspectorTarget, width int) string {
	entries := target.WhisperHistory
	if len(entries) == 0 {
		return theme.InspectorDimStyle.Render("no whisper data")
	}

	// When invoked from a nav node, entries has exactly one item.
	w := entries[0]

	var lines []string
	lines = append(lines, theme.InspectorTitleStyle.Render("Whisper"))
	lines = append(lines, "")

	if w.AgentID != "" {
		displayID := w.AgentID
		if len(displayID) > 12 {
			displayID = "…" + displayID[len(displayID)-12:]
		}
		lines = append(lines, row("Agent", displayID))
	}
	if w.Topic != "" {
		lines = append(lines, row("Topic", w.Topic))
	}
	if w.Source != "" {
		lines = append(lines, row("Source", w.Source))
	}
	lines = append(lines, row("When", humanTime(w.CreatedAt)))

	deliveredStr := "pending"
	if w.Delivered {
		deliveredStr = "delivered"
	}
	lines = append(lines, row("Status", deliveredStr))

	if w.Content != "" {
		lines = append(lines, "")
		lines = append(lines, theme.InspectorTitleStyle.Render("Content"))
		for _, line := range wrapText(w.Content, width-2) {
			lines = append(lines, theme.InspectorDimStyle.Render(line))
		}
	}

	// Show additional entries when multiple whispers are grouped.
	if len(entries) > 1 {
		lines = append(lines, "")
		lines = append(lines, theme.InspectorTitleStyle.Render(fmt.Sprintf("Related (%d more)", len(entries)-1)))
		for _, e := range entries[1:] {
			summary := e.Content
			if len(summary) > 60 {
				summary = summary[:59] + "…"
			}
			lines = append(lines, theme.InspectorDimStyle.Render("  "+humanTime(e.CreatedAt)+" "+summary))
		}
	}

	return strings.Join(lines, "\n")
}
