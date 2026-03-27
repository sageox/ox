package whatsup

import (
	"fmt"
	"strings"
	"time"
)

// Enrich populates computed presentation fields: headline, guidance, time_ago.
func (d *ActivityData) Enrich() {
	now := time.Now()

	// Compute time_ago for each session
	for i := range d.Authors {
		for j := range d.Authors[i].Sessions {
			d.Authors[i].Sessions[j].TimeAgo = relativeTime(d.Authors[i].Sessions[j].Time, now)
		}
	}

	d.Headline = headline(d)
	d.Guidance = guidance(d)
}

func headline(d *ActivityData) string {
	if d.Stats.TotalSessions == 0 {
		return "No sessions found in this time window."
	}

	parts := []string{fmt.Sprintf("%d sessions by %d people", d.Stats.TotalSessions, d.Stats.TotalAuthors)}

	if n := d.Stats.TotalConflicts; n > 0 {
		if n == 1 {
			parts = append(parts, "1 file conflict detected")
		} else {
			parts = append(parts, fmt.Sprintf("%d file conflicts detected", n))
		}
	} else {
		parts = append(parts, "no file conflicts")
	}

	return strings.Join(parts, ", ") + "."
}

func guidance(d *ActivityData) string {
	if d.Stats.TotalConflicts == 0 && d.Stats.TotalSessions == 0 {
		return ""
	}

	var lines []string

	lines = append(lines, "Present this as a team activity summary. Lead with the headline.")

	if d.Stats.TotalConflicts > 0 {
		// Find the hottest conflict (most authors)
		maxAuthors := 0
		var hotFile string
		for _, c := range d.Conflicts {
			if len(c.Authors) > maxAuthors {
				maxAuthors = len(c.Authors)
				hotFile = c.FilePath
			}
		}

		noun := "files have"
		if d.Stats.TotalConflicts == 1 {
			noun = "file has"
		}
		lines = append(lines, fmt.Sprintf(
			"Highlight conflicts — %d %s multiple authors editing it. The hottest is %s (%d authors).",
			d.Stats.TotalConflicts, noun, hotFile, maxAuthors))
		lines = append(lines, "For each conflict, show which authors and sessions touched the file. Suggest the user coordinate with those coworkers.")
	}

	if d.Stats.TotalConflicts == 0 && d.Stats.TotalSessions > 0 {
		lines = append(lines, "No conflicts — summarize what each person was working on so the user has situational awareness.")
	}

	lines = append(lines, "Use time_ago values (not raw timestamps) when referring to sessions. Keep it concise.")

	return strings.Join(lines, " ")
}

func relativeTime(t time.Time, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1d ago"
		}
		return fmt.Sprintf("%dd ago", days)
	}
}
