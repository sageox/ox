// Package timeline implements the center activity feed pane for the dashboard TUI.
package timeline

import (
	"fmt"
	"time"

	"github.com/sageox/ox/internal/dashboard/domain"
)

// TimeGroup categorizes timeline entries by recency.
type TimeGroup int

const (
	GroupNow     TimeGroup = iota // < 30 seconds ago
	GroupRecent                   // 30s – 10 minutes ago
	GroupEarlier                  // > 10 minutes ago
)

// GroupLabel returns the display label for a time group.
func GroupLabel(g TimeGroup) string {
	switch g {
	case GroupNow:
		return "NOW"
	case GroupRecent:
		return "RECENT"
	default:
		return "EARLIER"
	}
}

// GroupedEntries holds timeline entries partitioned by recency.
type GroupedEntries struct {
	Now     []domain.TimelineEntry
	Recent  []domain.TimelineEntry
	Earlier []domain.TimelineEntry
}

// GroupEntries partitions entries into NOW/RECENT/EARLIER groups.
// Input must already be sorted newest-first.
func GroupEntries(entries []domain.TimelineEntry) GroupedEntries {
	now := time.Now()
	var g GroupedEntries
	for _, e := range entries {
		age := now.Sub(e.Timestamp)
		switch {
		case age < 30*time.Second:
			g.Now = append(g.Now, e)
		case age < 10*time.Minute:
			g.Recent = append(g.Recent, e)
		default:
			g.Earlier = append(g.Earlier, e)
		}
	}
	return g
}

// EntryIcon returns a visual icon for a timeline entry kind.
func EntryIcon(kind domain.TimelineEntryKind) string {
	switch kind {
	case domain.TimelineSync:
		return "●"
	case domain.TimelineSession:
		return "◎"
	case domain.TimelineMurmur:
		return "◈"
	case domain.TimelineAgent:
		return "◉"
	case domain.TimelineIssue:
		return "⚠"
	default:
		return "·"
	}
}

// RelativeTime formats a time as a human-readable relative age string (e.g. "5m", "2h", "3d").
func RelativeTime(t time.Time) string {
	age := time.Since(t)
	if age < 0 {
		age = 0
	}
	if age < time.Minute {
		return fmt.Sprintf("%ds", int(age.Seconds()))
	}
	if age < time.Hour {
		return fmt.Sprintf("%dm", int(age.Minutes()))
	}
	if age < 24*time.Hour {
		return fmt.Sprintf("%dh", int(age.Hours()))
	}
	return fmt.Sprintf("%dd", int(age.Hours()/24))
}
