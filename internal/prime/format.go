package prime

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// FormatAge returns a human-readable relative time string.
func FormatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1m ago"
		}
		return fmt.Sprintf("%dm ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", h)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1d ago"
		}
		return fmt.Sprintf("%dd ago", days)
	}
}

// SortOtherTeamsByAge sorts OtherTeamEntry slices by content age.
// Entries with a known age are sorted newest-first; entries without age go last.
func SortOtherTeamsByAge(entries []OtherTeamEntry) {
	// parse age strings back to approximate durations for sorting
	parseDuration := func(age string) time.Duration {
		if age == "" {
			return time.Duration(1<<63 - 1) // max duration, sort last
		}
		if age == "just now" {
			return 0
		}
		// parse "Nm ago", "Nh ago", "Nd ago"
		var n int
		var unit string
		if _, err := fmt.Sscanf(age, "%d%s", &n, &unit); err != nil {
			return time.Duration(1<<63 - 1)
		}
		switch {
		case strings.HasPrefix(unit, "m"):
			return time.Duration(n) * time.Minute
		case strings.HasPrefix(unit, "h"):
			return time.Duration(n) * time.Hour
		case strings.HasPrefix(unit, "d"):
			return time.Duration(n) * 24 * time.Hour
		default:
			return time.Duration(1<<63 - 1)
		}
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return parseDuration(entries[i].Age) < parseDuration(entries[j].Age)
	})
}
