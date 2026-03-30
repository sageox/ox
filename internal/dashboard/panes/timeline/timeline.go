package timeline

import (
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

// compile-time interface check
var _ panes.Pane = (*Pane)(nil)

// Pane implements the centre activity-feed timeline panel.
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
		switch {
		case key.Matches(m, p.keys.Up):
			p.adjustScroll(ctx)
			return p, func() tea.Msg { return app.TimelineCursorUpMsg{} }
		case key.Matches(m, p.keys.Down):
			p.adjustScroll(ctx)
			return p, func() tea.Msg { return app.TimelineCursorDownMsg{} }
		case key.Matches(m, p.keys.Select):
			entries := ctx.Store.Timeline()
			cursor := ctx.Store.TimelineCursor()
			if cursor >= 0 && cursor < len(entries) && entries[cursor].Target != nil {
				target := entries[cursor].Target
				return p, func() tea.Msg { return app.SelectionChangedMsg{Target: target} }
			}
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

	title := theme.PaneTitle("⟳ Activity", ctx.Focused)

	var sb strings.Builder
	sb.WriteString(title)
	sb.WriteString("\n")

	// Sparkline header — summarises activity density over the last 4 hours.
	entries := ctx.Store.Timeline()
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
	sb.WriteString(theme.TimelineDivider.Render(spark))
	sb.WriteString("\n")

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
		// Section divider: "── LABEL ─────────────"
		dashCount := innerW - len(label) - 4
		if dashCount < 0 {
			dashCount = 0
		}
		divider := "── " + label + " " + strings.Repeat("─", dashCount)
		rows = append(rows, row{labelStyle.Render(divider)})

		for i, e := range grpEntries {
			globalIdx := startIdx + i
			icon := EntryIcon(e.Kind)
			relT := RelativeTime(e.Timestamp)

			// Truncate summary so the line fits in innerW: icon(1) + space(1) + summary + space(2) + relT
			overhead := len(icon) + 1 + 2 + len(relT)
			maxSummary := innerW - overhead
			if maxSummary < 0 {
				maxSummary = 0
			}
			summary := e.Summary
			if len(summary) > maxSummary {
				summary = summary[:maxSummary]
			}

			line := icon + " " + summary + "  " + relT

			var s lipgloss.Style
			switch {
			case globalIdx == cursor:
				s = theme.TimelineSelectedStyle
			case e.Kind == domain.TimelineMurmur:
				s = theme.TimelineMurmurStyle
			default:
				s = entryStyle
			}
			rows = append(rows, row{s.Width(innerW).Render(line)})
		}
	}

	appendGroup("NOW", theme.TimelineNowLabel, theme.TimelineEntryActive, grouped.Now, 0)
	appendGroup("RECENT", theme.TimelineRecentLabel, theme.TimelineEntryActive, grouped.Recent, len(grouped.Now))
	appendGroup("EARLIER", theme.TimelineEarlierLabel, theme.TimelineEntryMuted, grouped.Earlier, len(grouped.Now)+len(grouped.Recent))

	// title(1) + sparkline(1) = 2 chrome rows consumed above the scroll area.
	visibleRows := innerH - 2
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

// adjustScroll keeps the cursor row within the visible viewport.
// Chrome: border(2) + title(1) + sparkline(1) = 4 rows.
func (p *Pane) adjustScroll(ctx panes.Context) {
	cursor := ctx.Store.TimelineCursor()
	visibleRows := p.rect.Height - 4
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
