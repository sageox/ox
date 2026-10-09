package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/stretchr/testify/require"
)

func importTUITestCandidates() []*importCandidate {
	return []*importCandidate{
		{Session: nativeimport.Session{NativeID: "first-session", Agent: nativeimport.AgentClaude, StartedAt: time.Now()}, State: stateReady, Selected: true},
		{Session: nativeimport.Session{NativeID: "second-session", Agent: nativeimport.AgentCodex, StartedAt: time.Now()}, State: stateReady, Selected: false},
		{Session: nativeimport.Session{NativeID: "already-imported", Agent: nativeimport.AgentClaude}, State: stateAlreadyImported, Reason: "already in the Ledger", Selected: true},
	}
}

func importTUIKey(m *importReviewModel, code rune) tea.Cmd {
	_, command := m.Update(tea.KeyPressMsg{Code: code})
	return command
}

// A filtered invocation may start with only one ready row selected. Keyboard
// interaction must preserve that exact set and never enable skipped rows.
func TestImportTerminalSelectionKeyboard(t *testing.T) {
	m := newImportReviewModel(context.Background(), importDestination{}, importTUITestCandidates(), nil)
	require.Equal(t, []string{"first-session"}, m.selectedIDs())
	importTUIKey(m, tea.KeySpace)
	require.Empty(t, m.selectedIDs())
	importTUIKey(m, tea.KeyDown)
	importTUIKey(m, tea.KeySpace)
	require.Equal(t, []string{"second-session"}, m.selectedIDs())
	importTUIKey(m, tea.KeyDown)
	importTUIKey(m, tea.KeySpace)
	require.Equal(t, []string{"second-session"}, m.selectedIDs())
	importTUIKey(m, 'a')
	require.Equal(t, []string{"first-session", "second-session"}, m.selectedIDs())
	importTUIKey(m, 'x')
	require.Empty(t, m.selectedIDs())
	require.NotNil(t, importTUIKey(m, tea.KeyEnter))
	require.True(t, m.finished)
	require.False(t, m.canceled, "an empty reviewed selection must not silently become all ready sessions")
}

func TestImportTerminalExitIntents(t *testing.T) {
	for _, tc := range []struct {
		name     string
		key      rune
		canceled bool
		browse   bool
	}{
		{name: "cancel", key: 'q', canceled: true},
		{name: "escape", key: tea.KeyEsc, canceled: true},
		{name: "browser", key: 'b', browse: true},
		{name: "review", key: tea.KeyEnter},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newImportReviewModel(context.Background(), importDestination{}, importTUITestCandidates(), nil)
			require.NotNil(t, importTUIKey(m, tc.key))
			require.Equal(t, tc.canceled, m.canceled)
			require.Equal(t, tc.browse, m.browse)
			require.True(t, m.finished)
			require.Equal(t, "", m.View().Content)
			importTUIKey(m, 'x')
			require.Equal(t, []string{"first-session"}, m.selectedIDs(), "late input after exit must not mutate the handoff")
		})
	}
}

// Native-file reads can finish out of order after navigation. Their response
// belongs to the source session, not whichever row currently has focus.
func TestImportTerminalLatePreviewKeepsFocusAndSelection(t *testing.T) {
	m := newImportReviewModel(context.Background(), importDestination{}, importTUITestCandidates(), func(context.Context, string) (*importContentPreview, error) {
		return nil, errors.New("native session unavailable")
	})
	m.Init()
	importTUIKey(m, tea.KeyDown)
	importTUIKey(m, tea.KeySpace)
	m.Update(importPreviewLoadedMsg{id: "second-session", preview: &importContentPreview{
		NativeID: "second-session", OpeningRequest: "Correct the scoring algorithm", LastReply: "Scoring now follows the requested rules.",
	}})
	m.Update(importPreviewLoadedMsg{id: "first-session", preview: &importContentPreview{
		NativeID: "first-session", OpeningRequest: "Build the terminal game", LastReply: "The game can be played in a terminal.",
	}})
	require.Equal(t, 1, m.cursor)
	require.Equal(t, []string{"first-session", "second-session"}, m.selectedIDs())
	detail := strings.Join(m.detailLines(80), "\n")
	require.Contains(t, detail, "Correct the scoring algorithm")
	require.NotContains(t, detail, "Build the terminal game")
	importTUIKey(m, tea.KeyUp)
	require.Contains(t, strings.Join(m.detailLines(80), "\n"), "Build the terminal game")
}

func TestImportTerminalReadsOnlyVisibleRowsAndBoundsConcurrency(t *testing.T) {
	cands := make([]*importCandidate, 100)
	for i := range cands {
		cands[i] = &importCandidate{Session: nativeimport.Session{NativeID: fmt.Sprintf("session-%03d", i)}, State: stateReady, Selected: true}
	}
	var reads []string
	m := newImportReviewModel(context.Background(), importDestination{}, cands, func(_ context.Context, id string) (*importContentPreview, error) {
		reads = append(reads, id)
		return &importContentPreview{NativeID: id, OpeningRequest: id}, nil
	})
	cmd := m.Init()
	require.Len(t, m.pending, importPreviewConcurrency)
	batch, ok := cmd().(tea.BatchMsg)
	require.True(t, ok)
	require.Len(t, batch, importPreviewConcurrency)
	for _, read := range batch {
		m.Update(read())
		require.LessOrEqual(t, len(m.pending), importPreviewConcurrency)
	}
	require.Equal(t, []string{"session-000", "session-001"}, reads)
	for id := range m.pending {
		require.NotEqual(t, "session-099", id)
	}
	require.Less(t, len(m.previews)+len(m.pending), len(cands), "opening the picker must not read the entire history")
}

func TestImportTerminalUnavailablePreview(t *testing.T) {
	m := newImportReviewModel(context.Background(), importDestination{}, importTUITestCandidates(), nil)
	m.Update(importPreviewLoadedMsg{id: "first-session", err: errors.New("file disappeared")})
	view := ansi.Strip(m.View().Content)
	require.Contains(t, view, "Content unavailable")
	require.Contains(t, view, "file disappeared")
	require.Equal(t, []string{"first-session"}, m.selectedIDs())
	require.NotContains(t, view, "Last AI reply")
}

func TestImportTerminalNarrowViewAndSafeNativeText(t *testing.T) {
	m := newImportReviewModel(context.Background(), importDestination{Team: "test", Visibility: "private"}, importTUITestCandidates(), nil)
	m.previews["first-session"] = &importContentPreview{
		NativeID:       "first-session",
		OpeningRequest: "Build 界界 game\x1b[2J\x1b]0;spoofed-title\x07\u202eevil\u2069\r\x00",
		Prompts:        []importPromptAnchor{{Content: "Preserve unicode 界 and emoji 🎮"}},
		LastReply:      strings.Repeat("A long result containing unicode 界. ", 80),
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 120, Height: 30}, {Width: 43, Height: 24}, {Width: 12, Height: 8}, {Width: 1, Height: 1}} {
		m.Update(size)
		view := m.View()
		require.True(t, view.AltScreen)
		lines := strings.Split(view.Content, "\n")
		require.LessOrEqual(t, len(lines), size.Height)
		for _, line := range lines {
			require.LessOrEqual(t, ansi.StringWidth(line), size.Width, "terminal width must be respected")
		}
		require.NotContains(t, view.Content, "spoofed-title")
		require.NotContains(t, view.Content, "\x1b[2J")
		require.NotContains(t, view.Content, "\u202e")
		require.NotContains(t, view.Content, "\x00")
	}
	m.Update(tea.WindowSizeMsg{Width: 43, Height: 24})
	before := m.scroll
	importTUIKey(m, tea.KeyPgDown)
	require.Greater(t, m.scroll, before)
	importTUIKey(m, tea.KeyEnd)
	require.Contains(t, ansi.Strip(m.detailView()), "Excerpts help you choose")
	importTUIKey(m, tea.KeyHome)
	require.Zero(t, m.scroll)
}

func TestImportTerminalEmptyHistory(t *testing.T) {
	m := newImportReviewModel(context.Background(), importDestination{}, nil, nil)
	require.Nil(t, m.Init())
	importTUIKey(m, tea.KeySpace)
	importTUIKey(m, tea.KeyDown)
	importTUIKey(m, 'a')
	require.Empty(t, m.selectedIDs())
	require.Contains(t, m.View().Content, "No sessions found")
}
