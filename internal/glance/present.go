package glance

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Enrich populates computed presentation fields: actions, headline, guidance, time_ago.
func (d *ActivityData) Enrich() {
	now := time.Now()

	for i := range d.Authors {
		for j := range d.Authors[i].Sessions {
			d.Authors[i].Sessions[j].TimeAgo = relativeTime(d.Authors[i].Sessions[j].Time, now)
		}
	}

	d.Actions = buildActions(d)
	d.Headline = headline(d)
	d.Guidance = guidance(d)
}

// buildActions generates concrete recommendations from conflicts and patterns.
func buildActions(d *ActivityData) []Action {
	var actions []Action

	// Group conflicts by author pair to produce "coordinate with X" actions
	// instead of one action per file.
	type pairInfo struct {
		files  []string
		people map[string]bool
	}
	pairFiles := make(map[string]*pairInfo)

	for _, c := range d.Conflicts {
		authors := make([]string, 0, len(c.Authors))
		for a := range c.Authors {
			authors = append(authors, a)
		}
		sort.Strings(authors)

		// Build a key from the sorted author set
		key := strings.Join(authors, "|")
		pi, ok := pairFiles[key]
		if !ok {
			pi = &pairInfo{people: make(map[string]bool)}
			pairFiles[key] = pi
		}
		pi.files = append(pi.files, c.FilePath)
		for _, a := range authors {
			pi.people[a] = true
		}
	}

	for _, pi := range pairFiles {
		people := make([]string, 0, len(pi.people))
		for p := range pi.people {
			people = append(people, p)
		}
		sort.Strings(people)
		sort.Strings(pi.files)

		risk := "medium"
		if len(people) >= 3 || len(pi.files) >= 3 {
			risk = "high"
		}

		actions = append(actions, Action{
			Text: fmt.Sprintf("Coordinate with %s before touching %s",
				strings.Join(people, " and "), strings.Join(pi.files, ", ")),
			Risk:   risk,
			Files:  pi.files,
			People: people,
		})
	}

	// Sort: high risk first, then by number of files descending
	sort.Slice(actions, func(i, j int) bool {
		if actions[i].Risk != actions[j].Risk {
			return actions[i].Risk == "high"
		}
		return len(actions[i].Files) > len(actions[j].Files)
	})

	return actions
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
		parts = append(parts, "all clear — no file conflicts")
	}

	return strings.Join(parts, ", ") + "."
}

func guidance(d *ActivityData) string {
	if d.Stats.TotalConflicts == 0 && d.Stats.TotalSessions == 0 {
		return ""
	}

	var lines []string

	if len(d.Actions) > 0 {
		lines = append(lines, "Lead with actionable recommendations from the actions array — what the user should do RIGHT NOW. Then provide the supporting context: headline, conflicts, who's working on what. The user wants to know what to do before they want to know why.")
	}

	if d.Stats.TotalConflicts > 0 {
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
			"Context: %d %s multiple authors. The hottest is %s (%d authors).",
			d.Stats.TotalConflicts, noun, hotFile, maxAuthors))
	}

	if d.Stats.TotalConflicts == 0 && d.Stats.TotalSessions > 0 {
		lines = append(lines, "All clear — the team is working in parallel with no file overlap. This is a good state. Briefly summarize what each person is working on so the user has situational awareness. Keep the tone positive.")
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
