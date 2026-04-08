package main

import (
	"fmt"
	"time"
)

// session_start.go contains helper functions used by session commands.
// The actual start command is under ox agent <id> session start.

// getSessionUsername returns a username for session filenames.
// DEPRECATED: Use identity.AttributionUsername(ep, config.GetDisplayName()) instead.
// Kept for test compatibility only.
func getSessionUsername() string {
	// try git user first
	slug := getUserSlug()
	if slug != "" && slug != "anonymous" {
		return slug
	}

	return "user"
}

// formatDurationHuman formats a duration in human-readable form.
func formatDurationHuman(d time.Duration) string {
	if d < time.Minute {
		secs := int(d.Seconds())
		if secs == 1 {
			return "1 second"
		}
		return fmt.Sprintf("%d seconds", secs)
	}

	if d < time.Hour {
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", mins)
	}

	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60

	if mins == 0 {
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	}

	if hours == 1 {
		return fmt.Sprintf("1 hour %d minutes", mins)
	}
	return fmt.Sprintf("%d hours %d minutes", hours, mins)
}
