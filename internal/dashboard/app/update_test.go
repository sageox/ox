package app

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/dashboard/effects"
	"github.com/sageox/ox/internal/dashboard/overlays"
	"github.com/sageox/ox/internal/dashboard/state"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testOverlay struct {
	id       overlays.OverlayID
	updated  overlays.Overlay
	cmd      tea.Cmd
	consumed bool
}

func (o testOverlay) ID() overlays.OverlayID { return o.id }

func (o testOverlay) Update(tea.Msg) (overlays.Overlay, tea.Cmd, bool) {
	return o.updated, o.cmd, o.consumed
}

func (o testOverlay) View(int, int) string { return "test overlay" }

func TestReduceEffectsRejectsStaleGeneration(t *testing.T) {
	original := []session.SessionInfo{{Filename: "current.jsonl"}}
	m := Model{effectGen: 4}
	m.store.Sessions = original

	stale, cmd := m.reduceEffects(effects.SessionsLoadedMsg{
		Gen:      3,
		Sessions: []session.SessionInfo{{Filename: "stale.jsonl"}},
	})

	assert.Nil(t, cmd)
	assert.Equal(t, original, stale.store.Sessions)
	assert.Zero(t, stale.store.SessionsLoadedAt, "discarded responses must not claim freshness")
}

func TestReduceEffectsAppliesCurrentGenerationIncludingErrors(t *testing.T) {
	wantErr := errors.New("session load failed")
	want := []session.SessionInfo{{Filename: "current.jsonl"}}
	m := Model{effectGen: 4}

	got, cmd := m.reduceEffects(effects.SessionsLoadedMsg{Gen: 4, Sessions: want, Err: wantErr})

	assert.Nil(t, cmd)
	assert.Equal(t, want, got.store.Sessions)
	assert.ErrorIs(t, got.store.SessionsErr, wantErr)
	assert.Zero(t, got.store.SessionsLoadedAt, "failed responses must not claim freshness")
}

func TestReduceEffectsMaintainsMurmurSearchInvariants(t *testing.T) {
	m := Model{}
	m.store.TimelineCursorPos = 5

	m, _ = m.reduceEffects(MurmurSearchOpenMsg{})
	assert.True(t, m.store.MurmurQueryOpen)

	m, _ = m.reduceEffects(MurmurSearchQueryMsg{Query: "sync"})
	assert.Equal(t, "sync", m.store.MurmurQuery)
	assert.Zero(t, m.store.TimelineCursorPos)

	m, _ = m.reduceEffects(MurmurSearchCloseMsg{})
	assert.False(t, m.store.MurmurQueryOpen)
	assert.Empty(t, m.store.MurmurQuery)
}

func TestUpdateDoesNotPropagateOverlayConsumedMessages(t *testing.T) {
	original := &domain.InspectorTarget{Kind: domain.TargetSession}
	replacement := &domain.InspectorTarget{Kind: domain.TargetCodeDB}
	m := Model{listLens: &[int(sectionCount)]int{}}
	m.store.Selected = original
	m.overlays.Push(testOverlay{consumed: true})

	updated, cmd := m.Update(SelectionChangedMsg{Target: replacement})
	got := updated.(Model)

	assert.Nil(t, cmd)
	assert.Same(t, original, got.store.Selected, "consumed message must not reach effect reducers")
	assert.True(t, got.overlays.IsEmpty(), "nil updated overlay closes the top overlay")
}

func TestUpdatePropagatesMessagesDeclinedByOverlay(t *testing.T) {
	target := &domain.InspectorTarget{Kind: domain.TargetCodeDB}
	overlay := testOverlay{consumed: false}
	m := Model{listLens: &[int(sectionCount)]int{}}
	m.overlays.Push(overlay)

	updated, cmd := m.Update(SelectionChangedMsg{Target: target})
	got := updated.(Model)

	assert.Nil(t, cmd)
	assert.Same(t, target, got.store.Selected)
	require.NotNil(t, got.overlays.Top())
	assert.Equal(t, overlay.ID(), got.overlays.Top().ID())
}

func TestReduceOverlaysReplacesTopAndReturnsCommand(t *testing.T) {
	wantMsg := RefreshMsg{}
	wantCmd := func() tea.Msg { return wantMsg }
	replacement := testOverlay{id: overlays.OverlayConfirm, consumed: true}
	m := Model{}
	m.overlays.Push(testOverlay{
		id:       overlays.OverlayHelp,
		updated:  replacement,
		cmd:      wantCmd,
		consumed: true,
	})

	got, cmd, consumed := m.reduceOverlays(ShowHelpMsg{})

	assert.True(t, consumed)
	require.NotNil(t, cmd)
	assert.IsType(t, wantMsg, cmd())
	require.NotNil(t, got.overlays.Top())
	assert.Equal(t, overlays.OverlayConfirm, got.overlays.Top().ID())
}

func TestReduceOverlaysWithoutOverlayDeclinesMessage(t *testing.T) {
	m := Model{}

	got, cmd, consumed := m.reduceOverlays(ShowHelpMsg{})

	assert.False(t, consumed)
	assert.Nil(t, cmd)
	assert.True(t, got.overlays.IsEmpty())
}

func TestRefreshTickSupersedesOutstandingGeneration(t *testing.T) {
	m := Model{effectGen: 7}
	m.store.Generation = 7

	stale, cmd := m.reduceEffects(effects.RefreshTickMsg{Gen: 6})
	assert.Nil(t, cmd)
	assert.Equal(t, 7, stale.effectGen)
	assert.Equal(t, 7, stale.store.Generation)

	current, cmd := m.reduceEffects(effects.RefreshTickMsg{Gen: 7})
	assert.NotNil(t, cmd, "current tick should schedule data loads and the next tick")
	assert.Equal(t, 8, current.effectGen)
	assert.Equal(t, 8, current.store.Generation)
}

func TestBuildPaletteItemsTargetsTheSelectedNode(t *testing.T) {
	first := &domain.InspectorTarget{Kind: domain.TargetSession}
	second := &domain.InspectorTarget{Kind: domain.TargetCodeDB}
	nodes := []domain.NavNode{
		{ID: "section", Kind: domain.NavNodeSection, Label: "Sessions"},
		{ID: "first", Kind: domain.NavNodeSession, Label: "First", Target: first},
		{ID: "second", Kind: domain.NavNodeCodeIndex, Label: "Second", Target: second},
		{ID: "hint", Kind: domain.NavNodeHint, Label: "Nothing here", Target: first},
	}

	items := buildPaletteItems(nodes)
	require.Len(t, items, 4, "two selectable nodes plus refresh and help actions")

	firstMsg, ok := items[0].Cmd().(SelectionChangedMsg)
	require.True(t, ok)
	require.Same(t, first, firstMsg.Target)
	secondMsg, ok := items[1].Cmd().(SelectionChangedMsg)
	require.True(t, ok)
	require.Same(t, second, secondMsg.Target, "each command must retain its own loop target")
	_, ok = items[2].Cmd().(RefreshMsg)
	assert.True(t, ok)
	_, ok = items[3].Cmd().(ShowHelpMsg)
	assert.True(t, ok)
}

func TestOpenInspectorForCursorRejectsOutOfRangeSelection(t *testing.T) {
	m := Model{section: SectionSessions, listLens: &[int(sectionCount)]int{}}
	m.store.Sessions = []session.SessionInfo{{Filename: "one.jsonl"}}
	m.cursors[SectionSessions] = 1

	got := m.openInspectorForCursor()
	assert.False(t, got.inspectorOpen)
	assert.Nil(t, got.store.Selected)

	m.cursors[SectionSessions] = 0
	got = m.openInspectorForCursor()
	assert.True(t, got.inspectorOpen)
	require.NotNil(t, got.store.Selected)
	assert.Equal(t, domain.TargetSession, got.store.Selected.Kind)
	assert.Equal(t, "one.jsonl", got.store.Selected.Session.Filename)
}

func TestUpdateDiscardsAllAsyncResultsFromSupersededRefresh(t *testing.T) {
	m := Model{effectGen: 9}
	m.listLens = &[int(sectionCount)]int{}
	m.store = state.Store{
		DaemonStatus:   &daemon.StatusData{Pid: 1},
		Sessions:       []session.SessionInfo{{Filename: "kept.jsonl"}},
		Murmurs:        []domain.MurmurEntry{{Content: "kept"}},
		Discussions:    []domain.TeamDiscussion{{Title: "kept"}},
		Instances:      []daemon.InstanceInfo{{AgentID: "kept"}},
		StoredErrors:   []daemon.StoredError{{Message: "kept"}},
		TeamContexts:   []domain.TeamContextEntry{{TeamName: "kept"}},
		CodeIndexStats: &daemon.CodeDBStats{Symbols: 1},
		WhisperHistory: []domain.WhisperHistoryEntry{{Content: "kept"}},
	}

	stale := []tea.Msg{
		effects.DaemonStatusLoadedMsg{Gen: 8, Data: &daemon.StatusData{Pid: 2}},
		effects.SessionsLoadedMsg{Gen: 8, Sessions: []session.SessionInfo{{Filename: "stale"}}},
		effects.MurmursLoadedMsg{Gen: 8, Murmurs: []domain.MurmurEntry{{Content: "stale"}}},
		effects.TeamDiscussionsLoadedMsg{Gen: 8, Discussions: []domain.TeamDiscussion{{Title: "stale"}}},
		effects.InstancesLoadedMsg{Gen: 8, Instances: []daemon.InstanceInfo{{AgentID: "stale"}}},
		effects.StoredErrorsLoadedMsg{Gen: 8, Errors: []daemon.StoredError{{Message: "stale"}}},
		effects.TeamContextsLoadedMsg{Gen: 8, TeamContexts: []domain.TeamContextEntry{{TeamName: "stale"}}},
		effects.CodeIndexStatsLoadedMsg{Gen: 8, Stats: &daemon.CodeDBStats{Symbols: 2}},
		effects.WhisperHistoryLoadedMsg{Gen: 8, Entries: []domain.WhisperHistoryEntry{{Content: "stale"}}},
	}

	for _, msg := range stale {
		updated, cmd := m.Update(msg)
		got := updated.(Model)
		assert.Nil(t, cmd)
		assert.Equal(t, m.store, got.store, "%T escaped generation cancellation", msg)
	}
}

func TestUpdateRoutesRefreshAndSupersedesOlderWork(t *testing.T) {
	m := Model{effectGen: 4}
	m.listLens = &[int(sectionCount)]int{}
	m.store.Generation = 4

	updated, cmd := m.Update(RefreshMsg{})
	got := updated.(Model)
	require.NotNil(t, cmd)
	assert.Equal(t, 5, got.effectGen)
	assert.Equal(t, 5, got.store.Generation)

	refresh, ok := cmd().(tea.BatchMsg)
	require.True(t, ok, "refresh must issue both loads and a replacement tick")
	require.Len(t, refresh, 2)
	loads, ok := refresh[0]().(tea.BatchMsg)
	require.True(t, ok)
	assert.Len(t, loads, 9, "every dashboard data source must be refreshed")
}

func TestUpdateRoutesPaletteInputSelectionThroughOverlayStack(t *testing.T) {
	m := NewModel(nil, nil, nil)
	m.store.Sessions = []session.SessionInfo{
		{Filename: "alpha.jsonl", Username: "Alpha"},
		{Filename: "beta.jsonl", Username: "Beta"},
	}
	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	assert.Nil(t, cmd)

	updated, cmd = m.Update(keyPress("ctrl+k"))
	m = updated.(Model)
	assert.Nil(t, cmd)
	require.NotNil(t, m.overlays.Top())
	assert.Equal(t, overlays.OverlayPalette, m.overlays.Top().ID())
	assert.Contains(t, m.View().Content, "Quick Search", "the active overlay must own the composed view")

	for _, r := range "Beta" {
		updated, cmd = m.Update(keyPress(string(r)))
		m = updated.(Model)
		assert.Nil(t, cmd)
	}
	assert.Contains(t, m.View().Content, "Beta")
	assert.NotContains(t, m.View().Content, "Alpha")

	updated, cmd = m.Update(keyPress("enter"))
	m = updated.(Model)
	assert.True(t, m.overlays.IsEmpty(), "selection must close the palette")
	require.NotNil(t, cmd)

	selection, ok := cmd().(SelectionChangedMsg)
	require.True(t, ok)

	updated, cmd = m.Update(selection)
	m = updated.(Model)
	assert.Nil(t, cmd)
	require.NotNil(t, m.store.Selected)
	require.NotNil(t, m.store.Selected.Session)
	assert.Equal(t, "beta.jsonl", m.store.Selected.Session.Filename)
}

func TestUpdateRoutesNavigationAndInspectorOwnsFocus(t *testing.T) {
	m := NewModel(nil, nil, nil)
	m.section = SectionSessions
	m.listLens[SectionSessions] = 2
	update := func(msg tea.Msg) {
		updated, cmd := m.Update(msg)
		m = updated.(Model)
		assert.Nil(t, cmd)
	}

	update(keyPress("j"))
	assert.Equal(t, 1, m.cursors[SectionSessions])
	update(keyPress("j"))
	assert.Equal(t, 1, m.cursors[SectionSessions], "cursor must not run past the list")
	update(keyPress("k"))
	assert.Zero(t, m.cursors[SectionSessions])

	m.inspectorOpen = true
	m.inspectorScroll = 0
	update(keyPress("j"))
	assert.Equal(t, 1, m.inspectorScroll)
	assert.Zero(t, m.cursors[SectionSessions], "inspector scrolling must not move list selection")
	update(keyPress("tab"))
	assert.Equal(t, SectionSessions, m.section, "section navigation is disabled while inspecting")
	update(keyPress("esc"))
	assert.False(t, m.inspectorOpen)
	assert.Zero(t, m.inspectorScroll)

	update(keyPress("tab"))
	assert.Equal(t, SectionFeed, m.section)
}

func TestInspectorSelectionMatchesRenderedSyncOrder(t *testing.T) {
	m := NewModel(nil, nil, nil)
	m.section = SectionSync
	m.store.DaemonStatus = &daemon.StatusData{Workspaces: map[string][]daemon.WorkspaceSyncStatus{
		"z-repo": {{Path: "/z"}},
		"a-repo": {{Path: "/a"}},
	}}
	m.listLens[SectionSync] = 2

	m.cursors[SectionSync] = 0
	first := m.openInspectorForCursor()
	require.NotNil(t, first.store.Selected)
	assert.Equal(t, "/a", first.store.Selected.Workspace.Path)

	m.inspectorOpen = false
	m.cursors[SectionSync] = 1
	second := m.openInspectorForCursor()
	require.NotNil(t, second.store.Selected)
	assert.Equal(t, "/z", second.store.Selected.Workspace.Path)
}

func TestDefaultPaneKeysUseBubbleTeaSpaceName(t *testing.T) {
	keys := DefaultPaneKeys()
	assert.Equal(t, []string{"space"}, keys.Expand.Keys())
}

func TestFeedSelectionPreservesMurmurDiscussionWhisperOrder(t *testing.T) {
	m := Model{}
	m.store.Murmurs = []domain.MurmurEntry{{Content: "murmur"}}
	m.store.Discussions = []domain.TeamDiscussion{{Title: "discussion"}}
	m.store.WhisperHistory = []domain.WhisperHistoryEntry{{Content: "whisper"}}

	wantKinds := []domain.InspectorTargetKind{
		domain.TargetMurmur,
		domain.TargetTeamDiscussion,
		domain.TargetWhisperHistory,
	}
	for cursor, want := range wantKinds {
		target := m.feedTargetAtCursor(cursor)
		require.NotNil(t, target)
		assert.Equal(t, want, target.Kind)
	}
	assert.Nil(t, m.feedTargetAtCursor(-1))
	assert.Nil(t, m.feedTargetAtCursor(len(wantKinds)))
}

func keyPress(name string) tea.KeyPressMsg {
	switch name {
	case "ctrl+k":
		return tea.KeyPressMsg(tea.Key{Code: 'k', Mod: tea.ModCtrl})
	case "tab":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyTab})
	case "esc":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})
	case "enter":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	default:
		r := []rune(name)
		return tea.KeyPressMsg(tea.Key{Text: name, Code: r[0]})
	}
}
