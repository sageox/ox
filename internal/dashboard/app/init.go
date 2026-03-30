package app

import tea "charm.land/bubbletea/v2"

// Init implements tea.Model. Fires the initial data load + starts refresh ticker.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		LoadAllCmd(m.client, m.effectGen),
		StartRefreshTickCmd(m.effectGen),
	)
}
