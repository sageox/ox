package app

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sageox/ox/internal/dashboard/state"
	"github.com/sageox/ox/internal/dashboard/theme"
)

// RenderSection renders the content for the given section into the available width/height.
// cursor is the current selection index within the section's list; listLen receives
// the total number of selectable items so the caller can clamp the cursor.
func RenderSection(section Section, store state.ReadOnlyStore, width, height, cursor int) (content string, listLen int) {
	switch section {
	case SectionOverview:
		return renderOverview(store, width, height)
	case SectionSync:
		return renderSync(store, width, height, cursor)
	case SectionCode:
		return renderCode(store, width, height)
	case SectionSessions:
		return renderSessions(store, width, height, cursor)
	case SectionFeed:
		return renderFeed(store, width, height, cursor)
	default:
		return "", 0
	}
}

// ---------------------------------------------------------------------------
// Overview
// ---------------------------------------------------------------------------

func renderOverview(store state.ReadOnlyStore, width, _ int) (string, int) {
	var b strings.Builder

	ds := store.GetDaemonStatus()

	// -- system health --
	b.WriteString(sectionHeader("SYSTEM HEALTH", width))
	b.WriteByte('\n')

	if ds != nil && ds.Running {
		uptime := formatDuration(ds.Uptime)
		b.WriteString(row("Daemon", healthIndicator(true, "")+" running    pid "+fmt.Sprintf("%d", ds.Pid)+"    uptime "+uptime))
	} else {
		b.WriteString(row("Daemon", healthIndicator(false, "stopped")+" stopped"))
	}
	b.WriteByte('\n')

	if ds != nil && ds.AuthenticatedUser != nil {
		email := ds.AuthenticatedUser.Email
		b.WriteString(row("Auth", healthIndicator(true, "")+" valid      "+email))
	} else {
		b.WriteString(row("Auth", healthIndicator(false, "not authenticated")+" not authenticated"))
	}
	b.WriteByte('\n')

	// -- sync status --
	b.WriteByte('\n')
	b.WriteString(sectionHeader("SYNC STATUS", width))
	b.WriteByte('\n')

	if ds != nil && len(ds.Workspaces) > 0 {
		var ledgers, teamCtxs int
		var lastLedgerSync, lastTCSync time.Time

		for _, wsList := range ds.Workspaces {
			for _, ws := range wsList {
				switch ws.Type {
				case "ledger":
					ledgers++
					if ws.LastSync.After(lastLedgerSync) {
						lastLedgerSync = ws.LastSync
					}
				default:
					teamCtxs++
					if ws.LastSync.After(lastTCSync) {
						lastTCSync = ws.LastSync
					}
				}
			}
		}

		if ledgers > 0 {
			age := relativeAge(lastLedgerSync)
			b.WriteString(row("Ledger", healthIndicator(true, "")+" synced     last pull "+age))
		} else {
			b.WriteString(row("Ledger", progressIndicator()+" no ledger"))
		}
		b.WriteByte('\n')

		if teamCtxs > 0 {
			age := relativeAge(lastTCSync)
			b.WriteString(row("Team Ctx", healthIndicator(true, "")+" synced     "+fmt.Sprintf("%d contexts", teamCtxs)+"    last pull "+age))
		} else {
			b.WriteString(row("Team Ctx", theme.InspectorDimStyle.Render("none")))
		}
		b.WriteByte('\n')
	} else {
		b.WriteString(row("Ledger", theme.InspectorDimStyle.Render("no data")))
		b.WriteByte('\n')
		b.WriteString(row("Team Ctx", theme.InspectorDimStyle.Render("no data")))
		b.WriteByte('\n')
	}

	// -- code index --
	b.WriteByte('\n')
	b.WriteString(sectionHeader("CODE INDEX", width))
	b.WriteByte('\n')

	codeStats := store.GetCodeIndexStats()
	if codeStats != nil {
		if codeStats.LastError != "" {
			b.WriteString(row("Status", errorIndicator()+" "+codeStats.LastError))
		} else if codeStats.IndexingNow {
			b.WriteString(row("Status", progressIndicator()+" indexing..."))
		} else {
			symbols := fmt.Sprintf("%d symbols", codeStats.Symbols)
			age := relativeAge(codeStats.LastIndexed)
			b.WriteString(row("Status", healthIndicator(true, "")+" ready      "+symbols+"    built "+age))
		}
	} else {
		b.WriteString(row("Status", unknownIndicator()+" no index"))
	}
	b.WriteByte('\n')

	// -- issues --
	b.WriteByte('\n')
	storedErrors := store.GetStoredErrors()
	issueLabel := fmt.Sprintf("ISSUES                                  (%d errors)", len(storedErrors))
	b.WriteString(sectionHeader(issueLabel, width))
	b.WriteByte('\n')

	if len(storedErrors) == 0 {
		b.WriteString(theme.InspectorDimStyle.Render("  No issues detected."))
		b.WriteByte('\n')
	} else {
		limit := 5
		if len(storedErrors) < limit {
			limit = len(storedErrors)
		}
		for i := 0; i < limit; i++ {
			e := storedErrors[i]
			b.WriteString("  " + errorIndicator() + " " + theme.InspectorValueStyle.Render(e.Message))
			b.WriteByte('\n')
		}
		if len(storedErrors) > 5 {
			b.WriteString(theme.InspectorDimStyle.Render(fmt.Sprintf("  ... and %d more", len(storedErrors)-5)))
			b.WriteByte('\n')
		}
	}

	return b.String(), 0
}

// ---------------------------------------------------------------------------
// Sync
// ---------------------------------------------------------------------------

func renderSync(store state.ReadOnlyStore, width, height, cursor int) (string, int) {
	var b strings.Builder
	b.WriteString(sectionHeader("SYNC WORKSPACES", width))
	b.WriteByte('\n')

	ds := store.GetDaemonStatus()
	if ds == nil || len(ds.Workspaces) == 0 {
		b.WriteString(theme.InspectorDimStyle.Render("  No workspaces syncing."))
		b.WriteByte('\n')
		return b.String(), 0
	}

	// flatten workspaces into a stable list
	var items []string

	repoIDs := make([]string, 0, len(ds.Workspaces))
	for id := range ds.Workspaces {
		repoIDs = append(repoIDs, id)
	}
	sort.Strings(repoIDs)
	for _, repoID := range repoIDs {
		wsList := ds.Workspaces[repoID]
		for _, ws := range wsList {
			indicator := healthIndicator(ws.LastErr == "", ws.LastErr)
			status := "synced"
			if ws.Syncing {
				status = "syncing"
				indicator = progressIndicator()
			}
			if ws.LastErr != "" {
				status = "error"
			}

			age := relativeAge(ws.LastSync)
			label := ws.Type
			if ws.TeamName != "" {
				label = ws.TeamName
			}
			if label == "" {
				label = repoID
			}

			line := fmt.Sprintf("  %s %-14s %-10s last sync %s", indicator, label, status, age)
			if ws.LastErr != "" {
				line += "  " + theme.StatusError.Render(ws.LastErr)
			}
			items = append(items, line)
		}
	}

	listLen := len(items)
	visible := visibleWindow(items, cursor, height-2)

	for i, line := range visible.items {
		idx := visible.offset + i
		if idx == cursor {
			b.WriteString(theme.NavSelectedStyle.Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteByte('\n')
	}

	return b.String(), listLen
}

// ---------------------------------------------------------------------------
// Code
// ---------------------------------------------------------------------------

func renderCode(store state.ReadOnlyStore, width, _ int) (string, int) {
	var b strings.Builder
	b.WriteString(sectionHeader("CODE INDEX", width))
	b.WriteByte('\n')

	stats := store.GetCodeIndexStats()
	if stats == nil {
		b.WriteString(theme.InspectorDimStyle.Render("  No code index available."))
		b.WriteByte('\n')
		return b.String(), 0
	}

	if stats.LastError != "" {
		b.WriteString(row("Status", errorIndicator()+" "+stats.LastError))
		b.WriteByte('\n')
	} else if stats.IndexingNow {
		b.WriteString(row("Status", progressIndicator()+" indexing..."))
		b.WriteByte('\n')
	} else {
		b.WriteString(row("Status", healthIndicator(true, "")+" ready"))
		b.WriteByte('\n')
	}

	b.WriteString(row("Symbols", fmt.Sprintf("%d", stats.Symbols)))
	b.WriteByte('\n')
	b.WriteString(row("Commits", fmt.Sprintf("%d", stats.Commits)))
	b.WriteByte('\n')
	b.WriteString(row("Blobs", fmt.Sprintf("%d", stats.Blobs)))
	b.WriteByte('\n')
	b.WriteString(row("Comments", fmt.Sprintf("%d", stats.Comments)))
	b.WriteByte('\n')
	b.WriteString(row("PRs", fmt.Sprintf("%d", stats.PRs)))
	b.WriteByte('\n')
	b.WriteString(row("Issues", fmt.Sprintf("%d", stats.Issues)))
	b.WriteByte('\n')

	if !stats.LastIndexed.IsZero() {
		b.WriteString(row("Last Built", relativeAge(stats.LastIndexed)))
		b.WriteByte('\n')
	}

	// per-repo breakdown
	if len(stats.Repos) > 0 {
		b.WriteByte('\n')
		b.WriteString(sectionHeader("REPOSITORIES", width))
		b.WriteByte('\n')
		for _, repo := range stats.Repos {
			fmt.Fprintf(&b, "  %-30s %d commits  %d blobs",
				repo.Name, repo.Commits, repo.Blobs)
			b.WriteByte('\n')
		}
	}

	return b.String(), 0
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

func renderSessions(store state.ReadOnlyStore, width, height, cursor int) (string, int) {
	var b strings.Builder

	sessions := store.GetSessions()
	instances := store.GetInstances()

	// -- session list --
	b.WriteString(sectionHeader(fmt.Sprintf("SESSIONS (%d)", len(sessions)), width))
	b.WriteByte('\n')

	if len(sessions) == 0 {
		b.WriteString(theme.InspectorDimStyle.Render("  No sessions recorded."))
		b.WriteByte('\n')
	} else {
		// reserve space for instances section
		instanceRows := 0
		if len(instances) > 0 {
			instanceRows = len(instances) + 3 // header + divider + entries
		}
		availableHeight := height - 3 - instanceRows
		if availableHeight < 3 {
			availableHeight = 3
		}

		var lines []string
		for _, s := range sessions {
			indicator := theme.InspectorDimStyle.Render("○")
			if s.Recording {
				indicator = theme.StatusHealthy.Render("●")
			}

			ts := s.CreatedAt.Format("15:04")
			// compute duration from ModTime - CreatedAt
			dur := formatDurationShort(s.ModTime.Sub(s.CreatedAt))
			summary := s.Summary
			if summary == "" {
				summary = s.SessionName
			}
			if summary == "" {
				summary = s.Filename
			}
			// truncate summary to fit
			maxSummary := width - 20
			if maxSummary > 0 && len(summary) > maxSummary {
				summary = summary[:maxSummary-1] + "~"
			}

			line := fmt.Sprintf("  %s %s  %5s  %s", indicator, ts, dur, summary)
			lines = append(lines, line)
		}

		visible := visibleWindow(lines, cursor, availableHeight)
		for i, line := range visible.items {
			idx := visible.offset + i
			if idx == cursor {
				b.WriteString(theme.NavSelectedStyle.Render(line))
			} else {
				b.WriteString(line)
			}
			b.WriteByte('\n')
		}
	}

	// -- active AI coworkers --
	if len(instances) > 0 {
		b.WriteByte('\n')
		b.WriteString(sectionHeader(fmt.Sprintf("AI COWORKERS (%d)", len(instances)), width))
		b.WriteByte('\n')
		for _, inst := range instances {
			indicator := theme.StatusHealthy.Render("●")
			label := inst.AgentID
			if inst.AgentType != "" {
				label += "  " + theme.InspectorDimStyle.Render(inst.AgentType)
			}
			fmt.Fprintf(&b, "  %s %s", indicator, label)
			b.WriteByte('\n')
		}
	}

	return b.String(), len(sessions)
}

// ---------------------------------------------------------------------------
// Feed
// ---------------------------------------------------------------------------

// feedItem represents a single entry in the unified activity feed.
type feedItem struct {
	kind      string // "murmur", "discussion", "whisper"
	timestamp time.Time
	line      string
}

func renderFeed(store state.ReadOnlyStore, width, height, cursor int) (string, int) {
	var b strings.Builder

	murmurs := store.GetMurmurs()
	discussions := store.GetDiscussions()
	whispers := store.GetWhisperHistory()

	// build a unified feed sorted by time (newest first)
	var items []feedItem

	for _, m := range murmurs {
		age := relativeAge(m.Timestamp)
		topicBadge := topicStyle(m.Topic)
		line := fmt.Sprintf("  %s %s %s  %s",
			theme.StatusHealthy.Render("●"),
			age,
			topicBadge,
			truncate(m.Content, width-30))
		items = append(items, feedItem{kind: "murmur", timestamp: m.Timestamp, line: line})
	}

	for _, d := range discussions {
		age := relativeAge(d.ModTime)
		line := fmt.Sprintf("  %s %s %s  %s",
			theme.InspectorDimStyle.Render("◇"),
			age,
			theme.InspectorLabelStyle.Render("discussion"),
			truncate(d.Title, width-30))
		items = append(items, feedItem{kind: "discussion", timestamp: d.ModTime, line: line})
	}

	for _, w := range whispers {
		age := relativeAge(w.CreatedAt)
		delivered := theme.InspectorDimStyle.Render("○")
		if w.Delivered {
			delivered = theme.StatusHealthy.Render("●")
		}
		line := fmt.Sprintf("  %s %s whisper  %s  %s",
			delivered,
			age,
			theme.InspectorDimStyle.Render(w.AgentID),
			truncate(w.Content, width-40))
		items = append(items, feedItem{kind: "whisper", timestamp: w.CreatedAt, line: line})
	}

	// sort newest first
	sortFeedItems(items)

	totalItems := len(items)
	headerLabel := fmt.Sprintf("FEED (%d)", totalItems)

	// show active coworker + team counts in header
	coworkers := store.ActiveMurmurCoworkers()
	teams := store.ActiveMurmurTeams()
	if coworkers > 0 || teams > 0 {
		headerLabel += fmt.Sprintf("    %d coworkers  %d teams", coworkers, teams)
	}

	b.WriteString(sectionHeader(headerLabel, width))
	b.WriteByte('\n')

	if totalItems == 0 {
		b.WriteString(theme.InspectorDimStyle.Render("  No activity yet."))
		b.WriteByte('\n')
		return b.String(), 0
	}

	var lines []string
	for _, item := range items {
		lines = append(lines, item.line)
	}

	visible := visibleWindow(lines, cursor, height-2)
	for i, line := range visible.items {
		idx := visible.offset + i
		if idx == cursor {
			b.WriteString(theme.NavSelectedStyle.Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteByte('\n')
	}

	return b.String(), totalItems
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// healthIndicator returns a styled green dot for ok, red X for error.
func healthIndicator(ok bool, _ string) string {
	if ok {
		return theme.StatusHealthy.Render("●")
	}
	return theme.StatusError.Render("✕")
}

// progressIndicator returns a styled yellow half-dot for in-progress states.
func progressIndicator() string {
	return theme.StatusWarning.Render("◐")
}

// errorIndicator returns a styled red X.
func errorIndicator() string {
	return theme.StatusError.Render("✕")
}

// unknownIndicator returns a styled dim circle.
func unknownIndicator() string {
	return theme.StatusDim.Render("○")
}

// relativeAge returns a human-friendly relative time string.
func relativeAge(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("Jan 2")
	}
}

// sectionHeader renders a bold label followed by a horizontal rule to fill width.
func sectionHeader(label string, width int) string {
	styled := theme.InspectorTitleStyle.Render(" " + label + " ")
	ruleLen := width - len(label) - 3
	if ruleLen < 3 {
		ruleLen = 3
	}
	return styled + theme.InspectorDimStyle.Render(strings.Repeat("─", ruleLen))
}

// row renders a fixed-width label + value pair.
func row(label, value string) string {
	return " " + theme.InspectorLabelStyle.Render(label) + theme.InspectorValueStyle.Render(value)
}

// formatDuration returns a human-friendly duration like "3h 12m" or "45s".
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

// formatDurationShort returns a compact duration like "1:42" or "0:05".
func formatDurationShort(d time.Duration) string {
	total := int(d.Seconds())
	if total < 0 {
		total = 0
	}
	m := total / 60
	s := total % 60
	return fmt.Sprintf("%d:%02d", m, s)
}

// truncate shortens s to maxLen runes, appending "~" if truncated.
func truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	if maxLen < 2 {
		return string([]rune(s)[:maxLen])
	}
	return string([]rune(s)[:maxLen-1]) + "~"
}

// topicStyle returns a styled topic badge for murmur topics.
func topicStyle(topic string) string {
	switch topic {
	case "wip":
		return theme.TopicBadgeWIP.Render("wip")
	case "blocked":
		return theme.TopicBadgeBlocked.Render("blocked")
	case "decision":
		return theme.TopicBadgeDecision.Render("decision")
	case "review":
		return theme.TopicBadgeReview.Render("review")
	default:
		return theme.TopicBadgeDefault.Render(topic)
	}
}

// visibleSlice is the result of windowing a list for scrollable display.
type visibleSlice struct {
	items  []string
	offset int // index of the first visible item in the original list
}

// visibleWindow returns the slice of items that should be visible given
// the current cursor position and available height.
func visibleWindow(items []string, cursor, maxVisible int) visibleSlice {
	if len(items) == 0 || maxVisible <= 0 {
		return visibleSlice{}
	}
	if maxVisible >= len(items) {
		return visibleSlice{items: items, offset: 0}
	}

	// keep cursor roughly centered, clamped to bounds
	half := maxVisible / 2
	start := cursor - half
	if start < 0 {
		start = 0
	}
	end := start + maxVisible
	if end > len(items) {
		end = len(items)
		start = end - maxVisible
		if start < 0 {
			start = 0
		}
	}

	return visibleSlice{items: items[start:end], offset: start}
}

// sortFeedItems sorts feed items newest-first using insertion sort
// (good enough for the small lists we display).
func sortFeedItems(items []feedItem) {
	for i := 1; i < len(items); i++ {
		key := items[i]
		j := i - 1
		for j >= 0 && items[j].timestamp.Before(key.timestamp) {
			items[j+1] = items[j]
			j--
		}
		items[j+1] = key
	}
}
