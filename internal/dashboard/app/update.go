package app

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/sageox/ox/internal/dashboard/effects"
	"github.com/sageox/ox/internal/dashboard/panes"
	"github.com/sageox/ox/internal/dashboard/state"
)

// Update implements tea.Model. It runs a 5-stage reducer pipeline so that
// message precedence is deterministic: global → overlays → effects → focused
// pane → passive broadcast.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	var cmd tea.Cmd

	// Stage 1: Global messages (resize, quit, focus cycling, global key bindings).
	m, cmd = m.reduceGlobal(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	// Stage 2: Overlays consume input first when visible; propagation stops if consumed.
	if !m.overlays.IsEmpty() {
		m, cmd = m.reduceOverlays(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	// Stage 3: Async data-load completions update the store.
	m, cmd = m.reduceEffects(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	// Stage 4: Focused pane receives the message for interactive handling.
	m, cmd = m.reduceFocusedPane(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	// Stage 5: All panes receive passive-only messages (e.g. resize).
	m, cmd = m.reduceAllPanes(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

// reduceGlobal handles messages that apply regardless of pane focus: terminal
// resize, quit, focus cycling, refresh, and help overlay toggling.
func (m Model) reduceGlobal(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.layout = ComputeLayout(m.width, m.height)
		m = m.recomputePaneSizes()

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.FocusNext):
			m.focus = NextFocus(m.focus)
		case key.Matches(msg, m.keys.FocusPrev):
			m.focus = PrevFocus(m.focus)
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
		}

	case QuitMsg:
		return m, tea.Quit

	case FocusNextMsg:
		m.focus = NextFocus(m.focus)
	case FocusPrevMsg:
		m.focus = PrevFocus(m.focus)
	case FocusPaneMsg:
		m.focus = msg.Target

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

// reduceOverlays forwards the message to the topmost overlay. If the overlay
// returns nil it signals that it should close and is popped from the stack.
// Messages not consumed by the overlay continue propagating to later stages.
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

// reduceEffects processes async data-load completions and refresh ticks.
// Stale responses (wrong generation) are silently dropped.
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

	case NavCursorUpMsg:
		nodes := (&m.store).Nav()
		m.store = state.MoveNavCursor(m.store, -1, len(nodes))
	case NavCursorDownMsg:
		nodes := (&m.store).Nav()
		m.store = state.MoveNavCursor(m.store, +1, len(nodes))

	case TimelineCursorUpMsg:
		entries := (&m.store).Timeline()
		m.store = state.MoveTimelineCursor(m.store, -1, len(entries))
	case TimelineCursorDownMsg:
		entries := (&m.store).Timeline()
		m.store = state.MoveTimelineCursor(m.store, +1, len(entries))
	}

	return m, nil
}

// reduceFocusedPane routes the message to whichever pane currently holds
// keyboard focus. The statusbar (index 3) is passive — it is not a FocusTarget.
func (m Model) reduceFocusedPane(msg tea.Msg) (Model, tea.Cmd) {
	idx := int(m.focus)
	if idx < 0 || idx >= len(m.panes) {
		return m, nil
	}

	ctx := m.paneCtx(m.focus)
	updated, cmd := m.panes[idx].Update(msg, ctx)
	m.panes[idx] = updated

	return m, cmd
}

// reduceAllPanes broadcasts passive messages (currently only WindowSizeMsg) to
// every pane including the status bar. Panes use this to cache their allocated
// dimensions and pre-render static layout elements.
func (m Model) reduceAllPanes(msg tea.Msg) (Model, tea.Cmd) {
	switch msg.(type) {
	case tea.WindowSizeMsg:
		var cmds []tea.Cmd
		for i, p := range m.panes {
			f := FocusTarget(i)
			ctx := panes.Context{
				Store:   &m.store,
				Focused: m.focus == f,
			}
			updated, cmd := p.Update(msg, ctx)
			m.panes[i] = updated
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return m, tea.Batch(cmds...)
	}

	return m, nil
}

// recomputePaneSizes calls SetSize on every pane after the layout is updated.
// This keeps pane-local geometry (border widths, scroll limits) in sync with
// the terminal dimensions without requiring panes to recompute their own rects.
func (m Model) recomputePaneSizes() Model {
	rects := []panes.Rect{
		{X: m.layout.Nav.X, Y: m.layout.Nav.Y, Width: m.layout.Nav.Width, Height: m.layout.Nav.Height},
		{X: m.layout.Timeline.X, Y: m.layout.Timeline.Y, Width: m.layout.Timeline.Width, Height: m.layout.Timeline.Height},
		{X: m.layout.Inspector.X, Y: m.layout.Inspector.Y, Width: m.layout.Inspector.Width, Height: m.layout.Inspector.Height},
		{X: m.layout.StatusBar.X, Y: m.layout.StatusBar.Y, Width: m.layout.StatusBar.Width, Height: m.layout.StatusBar.Height},
	}

	for i, r := range rects {
		if i < len(m.panes) {
			m.panes[i].SetSize(r)
		}
	}

	return m
}
