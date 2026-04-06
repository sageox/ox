package app

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/cli"
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
		_ = content
		v := tea.NewView(overlayContent)
		v.AltScreen = true
		return v
	}

	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// render builds the full dashboard layout.
func (m *Model) render() string {
	statusBar := m.renderStatusBar()
	tabBar := m.renderTabBar()

	// render main section content
	mainW := m.layout.Main.Width
	mainH := m.layout.Main.Height
	cursor := m.cursors[m.section]
	sectionContent, listLen := RenderSection(m.section, &m.store, mainW, mainH, cursor)
	m.listLens[m.section] = listLen

	// clamp to visible height
	mainLines := strings.Split(sectionContent, "\n")
	if len(mainLines) > mainH {
		mainLines = mainLines[:mainH]
	}
	// pad to fill height
	for len(mainLines) < mainH {
		mainLines = append(mainLines, "")
	}
	mainStr := strings.Join(mainLines, "\n")

	var contentRow string
	if m.inspectorOpen && m.inspectorPane != nil {
		inspW := m.layout.Inspector.Width
		inspH := m.layout.Inspector.Height
		inspContent := m.inspectorPane.View(&m.store, inspW, inspH, m.inspectorScroll)

		// separator
		sep := lipgloss.NewStyle().
			Foreground(cli.ColorDim).
			Render(strings.TrimSuffix(strings.Repeat("│\n", mainH), "\n"))

		contentRow = lipgloss.JoinHorizontal(lipgloss.Top, mainStr, sep, inspContent)
	} else {
		contentRow = mainStr
	}

	inputBar := m.renderInputBar()

	return lipgloss.JoinVertical(lipgloss.Left, statusBar, tabBar, contentRow, inputBar)
}

// renderStatusBar renders the top health indicator row.
func (m Model) renderStatusBar() string {
	ds := m.store.DaemonStatus

	brand := theme.HeaderStyle.Render(" ox dashboard ")

	var indicators []string

	// daemon
	if ds != nil && ds.Running {
		indicators = append(indicators, theme.StatusHealthy.Render("●")+" daemon")
	} else {
		indicators = append(indicators, theme.StatusError.Render("✕")+" daemon")
	}

	// auth
	if ds != nil && ds.AuthenticatedUser != nil {
		indicators = append(indicators, theme.StatusHealthy.Render("●")+" auth")
	} else {
		indicators = append(indicators, theme.StatusError.Render("✕")+" auth")
	}

	// sync
	syncOK := false
	if ds != nil {
		for _, wsList := range ds.Workspaces {
			for _, ws := range wsList {
				if ws.LastErr == "" && !ws.LastSync.IsZero() {
					syncOK = true
				}
			}
		}
	}
	if syncOK {
		indicators = append(indicators, theme.StatusHealthy.Render("●")+" sync")
	} else if ds != nil && len(ds.Workspaces) > 0 {
		indicators = append(indicators, theme.StatusWarning.Render("◐")+" sync")
	} else {
		indicators = append(indicators, theme.StatusDim.Render("○")+" sync")
	}

	// code index
	codeStats := m.store.CodeIndexStats
	if codeStats != nil && codeStats.LastError == "" && codeStats.IndexExists {
		indicators = append(indicators, theme.StatusHealthy.Render("●")+" code")
	} else if codeStats != nil && codeStats.IndexingNow {
		indicators = append(indicators, theme.StatusWarning.Render("◐")+" code")
	} else if codeStats != nil && codeStats.LastError != "" {
		indicators = append(indicators, theme.StatusError.Render("✕")+" code")
	} else {
		indicators = append(indicators, theme.StatusDim.Render("○")+" code")
	}

	indicatorStr := strings.Join(indicators, "  ")
	left := brand + "  " + indicatorStr

	// right side: user email if available
	right := ""
	if ds != nil && ds.AuthenticatedUser != nil && ds.AuthenticatedUser.Email != "" {
		right = theme.HeaderDimStyle.Render(ds.AuthenticatedUser.Email + " ")
	}

	pad := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 0 {
		pad = 0
	}

	result := left + strings.Repeat(" ", pad) + right
	if lipgloss.Width(result) > m.width {
		runes := []rune(result)
		for lipgloss.Width(string(runes)) > m.width && len(runes) > 0 {
			runes = runes[:len(runes)-1]
		}
		result = string(runes)
	}
	return result
}

// renderTabBar renders the section tab row.
func (m Model) renderTabBar() string {
	var tabs []string
	for i := 0; i < int(sectionCount); i++ {
		s := Section(i)
		label := " " + s.Key() + " " + s.Label() + " "
		if s == m.section {
			tabs = append(tabs, theme.NavSelectedStyle.Render(label))
		} else {
			tabs = append(tabs, theme.NavDimStyle.Render(label))
		}
	}
	row := strings.Join(tabs, " ")

	pad := m.width - lipgloss.Width(row)
	if pad < 0 {
		pad = 0
	}
	result := row + strings.Repeat(" ", pad)
	if lipgloss.Width(result) > m.width {
		runes := []rune(result)
		for lipgloss.Width(string(runes)) > m.width && len(runes) > 0 {
			runes = runes[:len(runes)-1]
		}
		result = string(runes)
	}
	return result
}

// renderInputBar renders context-sensitive key hints.
func (m Model) renderInputBar() string {
	var hints []string

	if m.inspectorOpen {
		hints = append(hints, "esc close", "j/k scroll")
	} else {
		hints = append(hints, "tab section", "1-5 jump")
		if m.listLens[m.section] > 0 {
			hints = append(hints, "j/k navigate", "enter inspect")
		}
	}
	hints = append(hints, "r refresh", "? help", "q quit")

	result := theme.HeaderDimStyle.Render(" " + strings.Join(hints, "  "))
	if lipgloss.Width(result) > m.width {
		runes := []rune(result)
		for lipgloss.Width(string(runes)) > m.width && len(runes) > 0 {
			runes = runes[:len(runes)-1]
		}
		result = string(runes)
	}
	return result
}
