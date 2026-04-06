package timeline

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/dashboard/app"
	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/dashboard/panes"
	"github.com/sageox/ox/internal/dashboard/theme"
	"github.com/sageox/ox/internal/tui"
)

// filterTabLabel returns the display string for a murmur topic filter tab.
func filterTabLabel(f domain.MurmurTopicFilter) string {
	switch f {
	case domain.MurmurFilterAll:
		return "All"
	case domain.MurmurFilterWIP:
		return "WIP"
	case domain.MurmurFilterBlocked:
		return "Blocked"
	case domain.MurmurFilterDecision:
		return "Decisions"
	case domain.MurmurFilterReview:
		return "Reviews"
	default:
		return string(f)
	}
}

// renderFilterBar renders the topic filter tab row and optional search line.
func renderFilterBar(ctx panes.Context, innerW int) string {
	active := ctx.Store.MurmurFilter()
	searchActive := ctx.Store.MurmurSearchActive()
	searchQuery := ctx.Store.MurmurSearch()

	// Build tab row: [1]All  [2]WIP  [3]Blocked  [4]Decisions  [5]Reviews
	var tabs []string
	for i, f := range domain.AllMurmurFilters {
		numKey := fmt.Sprintf("[%d]", i+1)
		label := filterTabLabel(f)
		var tab string
		if f == active {
			tab = theme.NavSelectedStyle.Render(numKey + label)
		} else {
			tab = theme.NavDimStyle.Render(numKey) + label
		}
		tabs = append(tabs, tab)
	}
	tabRow := strings.Join(tabs, "  ")

	if searchActive {
		searchLine := theme.NavSectionStyle.Render("/") + " " + searchQuery + "█"
		return tabRow + "\n" + searchLine
	}
	if searchQuery != "" {
		// Search closed but query still active — show dimmed query.
		searchLine := theme.NavDimStyle.Render("/ "+searchQuery+"  ") + theme.InspectorHintStyle.Render("[/] search  [esc] clear")
		return tabRow + "\n" + searchLine
	}
	return tabRow
}

// compile-time interface check
var _ panes.Pane = (*Pane)(nil)

// Pane implements the center activity-feed timeline panel.
type Pane struct {
	rect      panes.Rect
	keys      app.PaneKeyMap
	scrollTop int // index of the first visible row in the flat row list
}

// New creates an initialized timeline Pane.
func New() *Pane {
	return &Pane{keys: app.DefaultPaneKeys()}
}

func (p *Pane) ID() panes.PaneID { return panes.PaneTimeline }

func (p *Pane) SetSize(r panes.Rect) { p.rect = r }

func (p *Pane) Update(msg tea.Msg, ctx panes.Context) (panes.Pane, tea.Cmd) {
	if !ctx.Focused {
		// Keep scroll position consistent even when this pane is not focused.
		p.adjustScroll(ctx)
		return p, nil
	}

	switch m := msg.(type) {
	case tea.KeyMsg:
		// When search is active, most keys feed the search query.
		if ctx.Store.MurmurSearchActive() {
			switch m.String() {
			case "esc":
				return p, func() tea.Msg { return app.MurmurSearchCloseMsg{} }
			case "enter":
				return p, func() tea.Msg { return app.MurmurSearchCloseMsg{} }
			case "backspace":
				q := ctx.Store.MurmurSearch()
				if runes := []rune(q); len(runes) > 0 {
					q = string(runes[:len(runes)-1])
				}
				return p, func() tea.Msg { return app.MurmurSearchQueryMsg{Query: q} }
			default:
				// Only accept single printable characters (not ctrl sequences).
				s := m.String()
				if len([]rune(s)) == 1 && s != " " {
					q := ctx.Store.MurmurSearch() + s
					return p, func() tea.Msg { return app.MurmurSearchQueryMsg{Query: q} }
				}
			}
			return p, nil
		}

		switch {
		case key.Matches(m, p.keys.Up):
			p.adjustScroll(ctx)
			return p, func() tea.Msg { return app.TimelineCursorUpMsg{} }
		case key.Matches(m, p.keys.Down):
			p.adjustScroll(ctx)
			return p, func() tea.Msg { return app.TimelineCursorDownMsg{} }
		case key.Matches(m, p.keys.Select):
			entries := ctx.TimelineEntries
			cursor := ctx.Store.TimelineCursor()
			if cursor >= 0 && cursor < len(entries) && entries[cursor].Target != nil {
				target := entries[cursor].Target
				return p, func() tea.Msg { return app.SelectionChangedMsg{Target: target} }
			}
		case m.String() == "/":
			return p, func() tea.Msg { return app.MurmurSearchOpenMsg{} }
		case m.String() == "1":
			return p, func() tea.Msg { return app.MurmurFilterMsg{Filter: domain.MurmurFilterAll} }
		case m.String() == "2":
			return p, func() tea.Msg { return app.MurmurFilterMsg{Filter: domain.MurmurFilterWIP} }
		case m.String() == "3":
			return p, func() tea.Msg { return app.MurmurFilterMsg{Filter: domain.MurmurFilterBlocked} }
		case m.String() == "4":
			return p, func() tea.Msg { return app.MurmurFilterMsg{Filter: domain.MurmurFilterDecision} }
		case m.String() == "5":
			return p, func() tea.Msg { return app.MurmurFilterMsg{Filter: domain.MurmurFilterReview} }
		}
	case app.TimelineCursorUpMsg, app.TimelineCursorDownMsg:
		// Cursor already moved by the root model; sync scroll to the new position.
		p.adjustScroll(ctx)
	}

	return p, nil
}

func (p *Pane) View(ctx panes.Context) string {
	w, h := ctx.Width, ctx.Height
	if w < 2 || h < 2 {
		return ""
	}

	// Inner dimensions subtract the 1-cell border on each side.
	innerW := w - 2
	innerH := h - 2

	title := theme.PaneTitle("◉ Team Pulse", ctx.Focused)

	var sb strings.Builder
	sb.WriteString(title)
	sb.WriteString("\n")

	// Team Pulse sub-header: counts of active coworkers and teams from murmurs in the last 30 min.
	coworkers := ctx.Store.ActiveMurmurCoworkers()
	teams := ctx.Store.ActiveMurmurTeams()
	pulseDetail := theme.TeamPulseMetaStyle.Render(
		fmt.Sprintf("  %d coworker%s  ·  %d team%s",
			coworkers, pluralS(coworkers),
			teams, pluralS(teams),
		),
	)
	sb.WriteString(pulseDetail)
	sb.WriteString("\n")

	// Filter tabs: All | WIP | Blocked | Decisions | Reviews, plus inline search line.
	filterBar := renderFilterBar(ctx, innerW)
	filterLines := strings.Count(filterBar, "\n") + 1
	sb.WriteString(filterBar)
	sb.WriteString("\n")

	// Sparkline header — summarizes activity density over the last 4 hours.
	entries := ctx.TimelineEntries
	timestamps := make([]time.Time, len(entries))
	for i, e := range entries {
		timestamps[i] = e.Timestamp
	}
	sparkBuckets := innerW - 2
	if sparkBuckets > tui.SparklineBuckets {
		sparkBuckets = tui.SparklineBuckets
	}
	if sparkBuckets < 1 {
		sparkBuckets = 1
	}
	spark := tui.RenderSparkline(timestamps, sparkBuckets, tui.SparklineWindow)
	sb.WriteString(theme.SparklineActiveStyle.Render(spark))
	sb.WriteString("\n")

	// Build per-agent murmur timestamps for mini sparklines in each row.
	agentMurmurTS := make(map[string][]time.Time)
	for _, e := range entries {
		if e.Kind == domain.TimelineMurmur && e.Actor != "" {
			agentMurmurTS[e.Actor] = append(agentMurmurTS[e.Actor], e.Timestamp)
		}
	}

	// Per-agent activity sparklines from heartbeat data (top 3 most recently active agents).
	var agentSparkSection strings.Builder
	agentSparkLines := 0
	if status := ctx.Store.GetDaemonStatus(); status != nil && status.Activity != nil {
		agents := status.Activity.Agents
		shown := agents
		if len(shown) > 3 {
			shown = shown[:3]
		}
		for _, agent := range shown {
			agentShort := agent.Key
			if len(agentShort) > 5 {
				agentShort = agentShort[len(agentShort)-5:]
			}
			agentSpark := tui.RenderSparkline(agent.Timestamps, 24, 2*time.Hour)
			age := time.Since(agent.Last)
			var sparkStyle lipgloss.Style
			switch {
			case age < 5*time.Minute:
				sparkStyle = theme.MurmurHotStyle
			case age < 30*time.Minute:
				sparkStyle = theme.MurmurWarmStyle
			default:
				sparkStyle = theme.MurmurCoolStyle
			}
			agentLabel := theme.NavDimStyle.Render(agentShort)
			agentSparkSection.WriteString(agentLabel + " " + sparkStyle.Render(agentSpark) + "\n")
			agentSparkLines++
		}
	}
	sb.WriteString(agentSparkSection.String())

	// Group entries by recency and build a flat list of rendered rows for scrolling.
	grouped := GroupEntries(entries)
	cursor := ctx.Store.TimelineCursor()

	type row struct{ text string }
	var rows []row

	appendGroup := func(
		label string,
		labelStyle lipgloss.Style,
		entryStyle lipgloss.Style,
		grpEntries []domain.TimelineEntry,
		startIdx int,
	) {
		if len(grpEntries) == 0 {
			return
		}
		// Section divider: " LABEL ·············"
		dashCount := innerW - len(label) - 3
		if dashCount < 0 {
			dashCount = 0
		}
		divider := " " + label + " " + strings.Repeat("·", dashCount)
		rows = append(rows, row{labelStyle.Width(innerW).Render(divider)})

		for i, e := range grpEntries {
			globalIdx := startIdx + i
			relT := RelativeTime(e.Timestamp)

			var line string
			if e.Kind == domain.TimelineMurmur {
				line = renderMurmurRow(e, relT, innerW, agentMurmurTS[e.Actor])
			} else {
				icon := EntryIcon(e.Kind)
				// Truncate summary so the line fits: icon(1) + space(1) + summary + space(2) + relT
				overhead := len(icon) + 1 + 2 + len(relT)
				maxSummary := innerW - overhead
				if maxSummary < 0 {
					maxSummary = 0
				}
				summary := e.Summary
				if len(summary) > maxSummary {
					summary = summary[:maxSummary]
				}
				line = icon + " " + summary + "  " + relT
			}

			var s lipgloss.Style
			if globalIdx == cursor {
				s = theme.TimelineSelectedStyle
			} else if e.Kind == domain.TimelineMurmur {
				s = murmurAgeStyle(e.Timestamp)
			} else {
				s = entryStyle
			}
			rows = append(rows, row{s.Width(innerW).Render(line)})
		}
	}

	appendGroup("NOW", theme.TimelineNowLabel, theme.TimelineEntryActive, grouped.Now, 0)
	appendGroup("RECENT", theme.TimelineRecentLabel, theme.TimelineEntryActive, grouped.Recent, len(grouped.Now))
	appendGroup("EARLIER", theme.TimelineEarlierLabel, theme.TimelineEntryMuted, grouped.Earlier, len(grouped.Now)+len(grouped.Recent))

	// title(1) + pulse-detail(1) + filter-bar(filterLines) + sparkline(1) + agent-sparks(agentSparkLines) = chrome rows.
	chromeRows := 3 + filterLines + agentSparkLines
	visibleRows := innerH - chromeRows
	if visibleRows < 0 {
		visibleRows = 0
	}

	p.adjustScroll(ctx)

	start := p.scrollTop
	end := start + visibleRows
	if end > len(rows) {
		end = len(rows)
	}

	for i := start; i < end; i++ {
		sb.WriteString("\n")
		sb.WriteString(rows[i].text)
	}

	// Pad remaining lines so the border fills completely.
	rendered := end - start
	for rendered < visibleRows {
		sb.WriteString("\n")
		sb.WriteString(lipgloss.NewStyle().Width(innerW).Render(""))
		rendered++
	}

	borderStyle := theme.PaneBorderUnfocused
	if ctx.Focused {
		borderStyle = theme.PaneBorderFocused
	}

	return borderStyle.Width(innerW).Height(innerH).Render(sb.String())
}

// renderMurmurRow builds the display line for a murmur timeline entry.
// Format: ◈ [topic-badge] agent-short sparkline content-preview relT
func renderMurmurRow(e domain.TimelineEntry, relT string, innerW int, agentTS []time.Time) string {
	icon := EntryIcon(e.Kind)

	// Parse topic and content from the pre-formatted Summary "[topic] content".
	topic, content := parseMurmurSummary(e.Summary)

	badge := topicBadge(topic)
	// Agent short: last 4 chars of Actor, dimmed.
	agentShort := e.Actor
	if len(agentShort) > 4 {
		agentShort = agentShort[len(agentShort)-4:]
	}
	agentPart := theme.NavDimStyle.Render(agentShort)

	// Mini sparkline: 8 buckets over 1h window shows per-agent activity density.
	miniSpark := tui.RenderSparkline(agentTS, 8, time.Hour)
	miniSparkStyled := murmurAgeStyle(e.Timestamp).Render(miniSpark)

	suffix := "  " + relT
	// icon(1) + space(1) + badge + space(1) + agentShort(4) + space(1) + miniSpark(8) + space(1) + content + suffix
	// miniSpark visual width is 8 chars; the 10 accounts for spark(8) + surrounding spaces(2).
	// badge and suffix use len() which is safe here because they contain only ASCII.
	overhead := 1 + 1 + len(badge) + 1 + 4 + 1 + 10 + len(suffix)
	maxContent := innerW - overhead
	if maxContent < 0 {
		maxContent = 0
	}
	if len(content) > maxContent {
		content = content[:maxContent]
	}

	return icon + " " + badge + " " + agentPart + " " + miniSparkStyled + " " + content + suffix
}

// parseMurmurSummary extracts topic and content from the "[topic] content" format
// stored in TimelineEntry.Summary for murmur entries.
func parseMurmurSummary(summary string) (topic, content string) {
	if len(summary) > 0 && summary[0] == '[' {
		end := strings.IndexByte(summary, ']')
		if end > 0 {
			topic = summary[1:end]
			content = strings.TrimSpace(summary[end+1:])
			return topic, content
		}
	}
	return "", summary
}

// topicBadge returns a styled "[topic]" badge for known topic values.
func topicBadge(topic string) string {
	badge := "[" + topic + "]"
	switch strings.ToLower(topic) {
	case "wip":
		return theme.TopicBadgeWIP.Render(badge)
	case "blocked":
		return theme.TopicBadgeBlocked.Render(badge)
	case "decision":
		return theme.TopicBadgeDecision.Render(badge)
	case "review":
		return theme.TopicBadgeReview.Render(badge)
	default:
		return theme.TopicBadgeDefault.Render(badge)
	}
}

// murmurAgeStyle returns a progressively dimmer style for older murmurs:
// < 5 min → hot (bright), 5-15 min → warm, > 15 min → cool (dim).
func murmurAgeStyle(t time.Time) lipgloss.Style {
	age := time.Since(t)
	switch {
	case age < 5*time.Minute:
		return theme.MurmurHotStyle
	case age < 15*time.Minute:
		return theme.MurmurWarmStyle
	default:
		return theme.MurmurCoolStyle
	}
}

// pluralS returns "s" when n != 1 for simple English pluralisation.
func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// adjustScroll keeps the cursor row within the visible viewport.
// Chrome: border(2) + title(1) + pulse-detail(1) + filter-bar(1 or 2) + sparkline(1) = 6–7 rows.
// Use 7 as a safe upper bound (filter bar with search line open).
func (p *Pane) adjustScroll(ctx panes.Context) {
	cursor := ctx.Store.TimelineCursor()
	// Filter bar is 2 lines when search is active, 1 otherwise.
	filterLines := 1
	if ctx.Store.MurmurSearchActive() || ctx.Store.MurmurSearch() != "" {
		filterLines = 2
	}
	chrome := 4 + filterLines // border(2) + title(1) + pulse(1) + filter + sparkline(1)
	visibleRows := p.rect.Height - chrome
	if visibleRows < 1 {
		return
	}
	if cursor < p.scrollTop {
		p.scrollTop = cursor
	}
	if cursor >= p.scrollTop+visibleRows {
		p.scrollTop = cursor - visibleRows + 1
	}
	if p.scrollTop < 0 {
		p.scrollTop = 0
	}
}
