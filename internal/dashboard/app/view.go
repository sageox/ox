package app

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/dashboard/theme"
)

// View implements tea.Model.
func (m Model) View() tea.View {
	if !m.ready {
		v := tea.NewView("Loading…")
		v.AltScreen = true
		return v
	}

	content := m.render()

	if !m.overlays.IsEmpty() {
		overlay := m.overlays.Top()
		overlayContent := overlay.View(m.width, m.height)
		// overlay compositing: discard the base frame and show the overlay fullscreen.
		// a future canvas-based compositor would blend the two layers.
		_ = content
		v := tea.NewView(overlayContent)
		v.AltScreen = true
		return v
	}

	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// render builds the full dashboard layout as a string by compositing
// header, three content panes, and the status bar using lipgloss joins.
func (m Model) render() string {
	header := m.renderHeader()

	navStr := m.panes[int(FocusNav)].View(m.paneCtx(FocusNav))
	timelineStr := m.panes[int(FocusTimeline)].View(m.paneCtx(FocusTimeline))
	inspStr := m.panes[int(FocusInspector)].View(m.paneCtx(FocusInspector))

	contentRow := lipgloss.JoinHorizontal(lipgloss.Top, navStr, timelineStr, inspStr)

	// statusbar is always the last pane (index 3); panes.PaneStatusBar == 3
	statusBar := m.panes[3].View(m.statusBarCtx())

	return lipgloss.JoinVertical(lipgloss.Left, header, contentRow, statusBar)
}

// renderHeader builds the single-row header bar containing the brand name,
// the authenticated user's email (when available), and key hints.
func (m Model) renderHeader() string {
	brand := theme.HeaderStyle.Render(" ox ")

	// secondary info: show email when the daemon reports an authenticated user
	syncInfo := ""
	if m.store.DaemonStatus != nil && m.store.DaemonStatus.AuthenticatedUser != nil {
		if email := m.store.DaemonStatus.AuthenticatedUser.Email; email != "" {
			syncInfo = " · " + email
		}
	}
	dim := theme.HeaderDimStyle.Render(syncInfo)

	hint := theme.HeaderDimStyle.Render("[?] help  [q] quit")

	leftPart := brand + dim
	pad := m.layout.Header.Width - lipgloss.Width(leftPart) - lipgloss.Width(hint)
	if pad < 0 {
		pad = 0
	}

	return leftPart + strings.Repeat(" ", pad) + hint
}

