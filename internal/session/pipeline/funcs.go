package pipeline

import (
	"strings"
	"time"

	"github.com/sageox/ox/internal/session/adapters"
)

// FilterEntriesAfterStart removes entries with timestamps before the session
// recording start time. Entries with zero timestamps are preserved (defensive:
// don't drop entries just because they lack timestamps).
func FilterEntriesAfterStart(entries []adapters.RawEntry, startedAt time.Time) []adapters.RawEntry {
	filtered := make([]adapters.RawEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Timestamp.IsZero() || !entry.Timestamp.Before(startedAt) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

// IsAuthRelatedError checks if an error message indicates an auth/credential problem.
// Used to surface targeted "run ox login" guidance instead of a generic error dump.
func IsAuthRelatedError(msg string) bool {
	authPatterns := []string{
		"authentication required",
		"credentials expired",
		"no git credentials",
		"empty token",
		"run 'ox login'",
		"auth token",
		"HTTP 401",
		"HTTP 403",
		"credential",
		"PAT rejected",
	}
	lower := strings.ToLower(msg)
	for _, p := range authPatterns {
		if strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// IsGenericAdapter returns true if the adapter name indicates the generic adapter.
func IsGenericAdapter(adapterName string) bool {
	return adapterName == "generic"
}
