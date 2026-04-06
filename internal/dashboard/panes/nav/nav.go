// Package nav implements the left-hand navigation tree pane for the dashboard TUI.
package nav

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/dashboard/app"
	"github.com/sageox/ox/internal/dashboard/panes"
	"github.com/sageox/ox/internal/dashboard/theme"
)

// compile-time interface check
var _ panes.Pane = (*Pane)(nil)

// Pane implements the left-hand navigation tree panel.
type Pane struct {
	rect      panes.Rect
	keys      app.PaneKeyMap
	scrollTop int // index of the first visible row
}

// New creates an initialized nav Pane.
func New() *Pane {
	return &Pane{keys: app.DefaultPaneKeys()}
}

func (p *Pane) ID() panes.PaneID { return panes.PaneNav }

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
			return p, func() tea.Msg { return app.NavCursorUpMsg{} }
		case key.Matches(m, p.keys.Down):
			return p, func() tea.Msg { return app.NavCursorDownMsg{} }
		case key.Matches(m, p.keys.Select):
			nodes := ctx.NavNodes
			cursor := ctx.Store.NavCursor()
			if cursor >= 0 && cursor < len(nodes) && nodes[cursor].Target != nil {
				target := nodes[cursor].Target
				return p, func() tea.Msg { return app.SelectionChangedMsg{Target: target} }
			}
		case key.Matches(m, p.keys.Expand):
			return p, func() tea.Msg { return app.NavExpandMsg{} }
		}
	case app.NavCursorUpMsg, app.NavCursorDownMsg:
		// Cursor already moved; sync scroll to the updated position.
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

	title := theme.PaneTitle("◉ Navigator", ctx.Focused)

	var sb strings.Builder
	sb.WriteString(title)
	sb.WriteString("\n")

	nodes := ctx.NavNodes
	cursor := ctx.Store.NavCursor()

	// One row is consumed by the title line.
	visibleRows := innerH - 1
	if visibleRows < 1 {
		visibleRows = 1
	}

	p.adjustScroll(ctx)

	start := p.scrollTop
	end := start + visibleRows
	if end > len(nodes) {
		end = len(nodes)
	}

	for i := start; i < end; i++ {
		row := RenderNode(nodes[i], i == cursor, innerW)
		sb.WriteString(row)
		if i < end-1 {
			sb.WriteString("\n")
		}
	}

	// Pad any remaining lines with blank rows so the border fills completely.
	rendered := end - start
	for rendered < visibleRows {
		sb.WriteString(lipgloss.NewStyle().Width(innerW).Render(""))
		rendered++
		if rendered < visibleRows {
			sb.WriteString("\n")
		}
	}

	borderStyle := theme.PaneBorderUnfocused
	if ctx.Focused {
		borderStyle = theme.PaneBorderFocused
	}

	return borderStyle.Width(innerW).Height(innerH).Render(sb.String())
}

// adjustScroll ensures the cursor row stays within the visible viewport.
func (p *Pane) adjustScroll(ctx panes.Context) {
	cursor := ctx.Store.NavCursor()
	// border(2) + title(1) = 3 rows of chrome
	visibleRows := p.rect.Height - 3
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
