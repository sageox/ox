package app

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/dashboard/effects"
	"github.com/sageox/ox/internal/dashboard/overlays/palette"
	"github.com/sageox/ox/internal/dashboard/state"
)

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	var cmd tea.Cmd

	// Stage 1: Global messages (resize, quit, section nav)
	m, cmd = m.reduceGlobal(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	// Stage 2: Overlays consume input first
	if !m.overlays.IsEmpty() {
		m, cmd = m.reduceOverlays(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	// Stage 3: Async data-load completions
	m, cmd = m.reduceEffects(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

// reduceGlobal handles global messages: resize, quit, section navigation, list navigation.
func (m Model) reduceGlobal(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.layout = ComputeLayout(m.width, m.height, m.inspectorOpen)

	case tea.KeyMsg:
		// Overlays consume keys exclusively when visible
		if !m.overlays.IsEmpty() {
			return m, nil
		}

		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit

		// section navigation
		case key.Matches(msg, m.keys.NextSection):
			if !m.inspectorOpen {
				m.section = NextSection(m.section)
			}
		case key.Matches(msg, m.keys.PrevSection):
			if !m.inspectorOpen {
				m.section = PrevSection(m.section)
			}
		case key.Matches(msg, m.keys.Section1):
			if !m.inspectorOpen {
				m.section = SectionOverview
			}
		case key.Matches(msg, m.keys.Section2):
			if !m.inspectorOpen {
				m.section = SectionSync
			}
		case key.Matches(msg, m.keys.Section3):
			if !m.inspectorOpen {
				m.section = SectionCode
			}
		case key.Matches(msg, m.keys.Section4):
			if !m.inspectorOpen {
				m.section = SectionSessions
			}
		case key.Matches(msg, m.keys.Section5):
			if !m.inspectorOpen {
				m.section = SectionFeed
			}

		// list navigation
		case key.Matches(msg, m.keys.Up):
			if m.inspectorOpen {
				if m.inspectorScroll > 0 {
					m.inspectorScroll--
				}
			} else {
				if m.cursors[m.section] > 0 {
					m.cursors[m.section]--
				}
			}
		case key.Matches(msg, m.keys.Down):
			if m.inspectorOpen {
				m.inspectorScroll++
			} else {
				maxCursor := m.listLens[m.section] - 1
				if maxCursor < 0 {
					maxCursor = 0
				}
				if m.cursors[m.section] < maxCursor {
					m.cursors[m.section]++
				}
			}

		// inspector toggle
		case key.Matches(msg, m.keys.CloseInspector):
			if m.inspectorOpen {
				m.inspectorOpen = false
				m.inspectorScroll = 0
				m.layout = ComputeLayout(m.width, m.height, false)
			}
		case key.Matches(msg, m.keys.Select):
			if !m.inspectorOpen && m.listLens[m.section] > 0 {
				m = m.openInspectorForCursor()
			}

		case key.Matches(msg, m.keys.Refresh):
			m.store = state.IncrementGeneration(m.store)
			m.effectGen = m.store.Generation
			return m, tea.Batch(
				LoadAllCmd(m.client, m.effectGen),
				StartRefreshTickCmd(m.effectGen),
			)

		case key.Matches(msg, m.keys.Help):
			if m.helpFactory != nil {
				m.overlays.Push(m.helpFactory())
			}

		case key.Matches(msg, m.keys.OpenBrowser):
			target := m.store.Inspector()
			if url := sessionBrowserURL(m.client, target); url != "" {
				return m, openBrowserCmd(url)
			}

		case key.Matches(msg, m.keys.Palette):
			m.overlays.Push(palette.New(buildPaletteItems(m.store.Nav())))
		}

	case ShowHelpMsg:
		if m.helpFactory != nil {
			m.overlays.Push(m.helpFactory())
		}

	case QuitMsg:
		return m, tea.Quit
	case RefreshMsg:
		m.store = state.IncrementGeneration(m.store)
		m.effectGen = m.store.Generation
		return m, tea.Batch(
			LoadAllCmd(m.client, m.effectGen),
			StartRefreshTickCmd(m.effectGen),
		)
	}

	return m, nil
}

// openInspectorForCursor sets the inspector target based on current section + cursor.
func (m Model) openInspectorForCursor() Model {
	cursor := m.cursors[m.section]
	var target *domain.InspectorTarget

	switch m.section {
	case SectionSync:
		target = m.syncTargetAtCursor(cursor)
	case SectionSessions:
		target = m.sessionTargetAtCursor(cursor)
	case SectionFeed:
		target = m.feedTargetAtCursor(cursor)
	default:
		return m
	}

	if target != nil {
		m.store = state.ApplySelection(m.store, target)
		m.inspectorOpen = true
		m.inspectorScroll = 0
		m.layout = ComputeLayout(m.width, m.height, true)
	}
	return m
}

// syncTargetAtCursor returns the inspector target for a workspace at the given cursor index.
func (m Model) syncTargetAtCursor(cursor int) *domain.InspectorTarget {
	ds := m.store.DaemonStatus
	if ds == nil {
		return nil
	}
	idx := 0
	for _, wsList := range ds.Workspaces {
		for i := range wsList {
			if idx == cursor {
				ws := wsList[i]
				return &domain.InspectorTarget{
					Kind:      domain.TargetWorkspace,
					Workspace: &ws,
				}
			}
			idx++
		}
	}
	return nil
}

// sessionTargetAtCursor returns the inspector target for a session at the given cursor index.
func (m Model) sessionTargetAtCursor(cursor int) *domain.InspectorTarget {
	sessions := m.store.Sessions
	if cursor < 0 || cursor >= len(sessions) {
		return nil
	}
	s := sessions[cursor]
	return &domain.InspectorTarget{
		Kind:    domain.TargetSession,
		Session: &s,
	}
}

// feedTargetAtCursor returns the inspector target for a feed item at the given cursor index.
func (m Model) feedTargetAtCursor(cursor int) *domain.InspectorTarget {
	// build same feed order as renderFeed
	murmurs := m.store.Murmurs
	discussions := m.store.Discussions
	whispers := m.store.WhisperHistory

	type item struct {
		kind string
		idx  int
	}
	var items []item
	for i := range murmurs {
		items = append(items, item{"murmur", i})
	}
	for i := range discussions {
		items = append(items, item{"discussion", i})
	}
	for i := range whispers {
		items = append(items, item{"whisper", i})
	}

	if cursor < 0 || cursor >= len(items) {
		return nil
	}

	it := items[cursor]
	switch it.kind {
	case "murmur":
		entry := murmurs[it.idx]
		return &domain.InspectorTarget{
			Kind:   domain.TargetMurmur,
			Murmur: &entry,
		}
	case "discussion":
		entry := discussions[it.idx]
		return &domain.InspectorTarget{
			Kind:       domain.TargetTeamDiscussion,
			Discussion: &entry,
		}
	case "whisper":
		entry := whispers[it.idx]
		return &domain.InspectorTarget{
			Kind:           domain.TargetWhisperHistory,
			WhisperHistory: []domain.WhisperHistoryEntry{entry},
		}
	}
	return nil
}

// reduceOverlays forwards the message to the topmost overlay.
func (m Model) reduceOverlays(msg tea.Msg) (Model, tea.Cmd) {
	top := m.overlays.Top()
	if top == nil {
		return m, nil
	}

	updated, cmd, consumed := top.Update(msg)
	if !consumed {
		return m, nil
	}

	m.overlays.Pop()
	if updated != nil {
		m.overlays.Push(updated)
	}

	return m, cmd
}

// reduceEffects processes async data-load completions.
func (m Model) reduceEffects(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case effects.DaemonStatusLoadedMsg:
		if msg.Gen != m.effectGen {
			return m, nil
		}
		m.store = state.ApplyDaemonStatus(m.store, msg.Data, msg.Err)

	case effects.SessionsLoadedMsg:
		if msg.Gen != m.effectGen {
			return m, nil
		}
		m.store = state.ApplySessions(m.store, msg.Sessions, msg.Err)

	case effects.MurmursLoadedMsg:
		if msg.Gen != m.effectGen {
			return m, nil
		}
		m.store = state.ApplyMurmurs(m.store, msg.Murmurs, msg.Err)

	case effects.TeamDiscussionsLoadedMsg:
		if msg.Gen != m.effectGen {
			return m, nil
		}
		m.store = state.ApplyDiscussions(m.store, msg.Discussions, msg.Err)

	case effects.InstancesLoadedMsg:
		if msg.Gen != m.effectGen {
			return m, nil
		}
		m.store = state.ApplyInstances(m.store, msg.Instances, msg.Err)

	case effects.StoredErrorsLoadedMsg:
		if msg.Gen != m.effectGen {
			return m, nil
		}
		m.store = state.ApplyStoredErrors(m.store, msg.Errors, msg.Err)

	case effects.TeamContextsLoadedMsg:
		if msg.Gen != m.effectGen {
			return m, nil
		}
		m.store = state.ApplyTeamContexts(m.store, msg.TeamContexts, msg.Err)

	case effects.CodeIndexStatsLoadedMsg:
		if msg.Gen != m.effectGen {
			return m, nil
		}
		m.store = state.ApplyCodeIndexStats(m.store, msg.Stats, msg.Err)

	case effects.WhisperHistoryLoadedMsg:
		if msg.Gen != m.effectGen {
			return m, nil
		}
		m.store = state.ApplyWhisperHistory(m.store, msg.Entries, msg.Err)

	case OpenBrowserMsg:
		if msg.URL != "" {
			_ = cli.OpenInBrowser(msg.URL)
		}

	case effects.RefreshTickMsg:
		if msg.Gen != m.effectGen {
			return m, nil
		}
		m.store = state.IncrementGeneration(m.store)
		m.effectGen = m.store.Generation
		return m, tea.Batch(
			LoadAllCmd(m.client, m.effectGen),
			StartRefreshTickCmd(m.effectGen),
		)

	case SelectionChangedMsg:
		m.store = state.ApplySelection(m.store, msg.Target)

	// keep cursor movement messages for palette compatibility
	case NavCursorUpMsg, NavCursorDownMsg:
		// no-op in new architecture
	case TimelineCursorUpMsg, TimelineCursorDownMsg:
		// no-op in new architecture
	case MurmurFilterMsg:
		m.store = state.SetMurmurFilter(m.store, msg.Filter)
	case MurmurSearchOpenMsg:
		m.store = state.SetMurmurSearchActive(m.store, true)
	case MurmurSearchCloseMsg:
		m.store = state.SetMurmurSearchActive(m.store, false)
	case MurmurSearchQueryMsg:
		m.store = state.SetMurmurSearch(m.store, msg.Query)
	}

	return m, nil
}

// sessionBrowserURL returns the web URL for the given inspector target.
func sessionBrowserURL(client effects.Client, target domain.InspectorTarget) string {
	switch target.Kind {
	case domain.TargetSession:
		if target.Session == nil {
			return ""
		}
		name := target.Session.SessionName
		if name == "" {
			name = target.Session.Filename
		}
		return client.BuildSessionURL(name)
	}
	return ""
}

// openBrowserCmd returns a tea.Cmd that sends an OpenBrowserMsg.
func openBrowserCmd(url string) tea.Cmd {
	return func() tea.Msg { return OpenBrowserMsg{URL: url} }
}

// buildPaletteItems converts store data into palette items.
func buildPaletteItems(nodes []domain.NavNode) []palette.Item {
	var items []palette.Item
	for _, n := range nodes {
		if n.Target == nil || n.Kind == domain.NavNodeSection || n.Kind == domain.NavNodeHint {
			continue
		}
		target := n.Target
		items = append(items, palette.Item{
			Icon:  palette.KindIcon(n.Kind),
			Label: n.Label,
			Sub:   palette.KindSection(n.Kind),
			Cmd: func() tea.Msg {
				return SelectionChangedMsg{Target: target}
			},
		})
	}
	items = append(items, palette.Item{
		Icon:  "⟳",
		Label: "Refresh all data",
		Sub:   "action",
		Cmd:   func() tea.Msg { return RefreshMsg{} },
	})
	items = append(items, palette.Item{
		Icon:  "?",
		Label: "Show help",
		Sub:   "action",
		Cmd:   func() tea.Msg { return ShowHelpMsg{} },
	})
	return items
}
