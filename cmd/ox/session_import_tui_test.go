package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sageox/ox/internal/session"
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

// importTUIProgramOptions keeps the real Bubble Tea input decoder and event loop
// while supplying deterministic streams and disabling terminal rendering/signals.
func importTUIProgramOptions(input io.Reader) importTerminalOptions {
	return importTerminalOptions{program: []tea.ProgramOption{
		tea.WithInput(input), tea.WithOutput(io.Discard), tea.WithoutRenderer(),
		tea.WithoutSignalHandler(), tea.WithEnvironment([]string{"TERM=dumb", "NO_COLOR=1"}),
	}}
}

// Run the actual input decoder, event loop and program cleanup. A cleared
// selection must stay empty, and a skipped row must never reach the handoff.
func TestImportTerminalProgramSelectionAndCancel(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    string
		ids      []string
		canceled bool
	}{
		{name: "initial exact selection", input: "\r", ids: []string{"first-session"}},
		{name: "narrow by keyboard", input: "x\x1b[B \r", ids: []string{"second-session"}},
		{name: "select all excludes skipped", input: "a\r", ids: []string{"first-session", "second-session"}},
		{name: "clear remains empty", input: "x\r", ids: []string{}},
		{name: "cancel", input: "q", ids: []string{"first-session"}, canceled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			result, err := runImportTerminal(ctx, importDestination{}, importTUITestCandidates(), nil, importTUIProgramOptions(strings.NewReader(tc.input)))
			require.NoError(t, err)
			require.Equal(t, tc.ids, result.IDs)
			require.Equal(t, tc.canceled, result.Canceled)
		})
	}
}

// Browser launching happens only after Bubble Tea has returned. Preserve the
// selected IDs and original context, and propagate the browser's result/error.
func TestImportTerminalProgramBrowserHandoff(t *testing.T) {
	for _, browserFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("browser failure %t", browserFails), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			dest := importDestination{Team: "Math Blitz", RepoID: "repo_math_blitz", Visibility: "private"}
			cands := importTUITestCandidates()
			load := func(context.Context, string) (*importContentPreview, error) { return nil, nil }
			options := importTUIProgramOptions(strings.NewReader("x\x1b[B b"))
			launches := 0
			launchErr := errors.New("browser could not open")
			options.browser = func(browserCtx context.Context, browserDest importDestination, browserCands []*importCandidate, browserLoad importPreviewLoader) (importReviewResult, error) {
				launches++
				require.Same(t, ctx, browserCtx, "canceled preview context must not be passed to the browser")
				require.NoError(t, browserCtx.Err())
				require.Equal(t, dest, browserDest)
				require.Equal(t, cands, browserCands)
				require.Len(t, selectedCandidates(browserCands), 1)
				require.Equal(t, "second-session", selectedCandidates(browserCands)[0].Session.NativeID)
				require.NotNil(t, browserLoad)
				if browserFails {
					return importReviewResult{}, launchErr
				}
				return importReviewResult{IDs: []string{"first-session"}}, nil
			}
			result, err := runImportTerminal(ctx, dest, cands, load, options)
			require.Equal(t, 1, launches)
			if browserFails {
				require.ErrorIs(t, err, launchErr)
				require.Empty(t, result.IDs)
			} else {
				require.NoError(t, err)
				require.Equal(t, []string{"first-session"}, result.IDs, "browser choices must become the final review result")
			}
		})
	}
}

// Cancel while a real preview command is running. The program and its reader
// must stop without inventing approval or trying to launch the browser.
func TestImportTerminalProgramContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan context.Context, 2)
	load := func(readCtx context.Context, _ string) (*importContentPreview, error) {
		started <- readCtx
		<-readCtx.Done()
		return nil, readCtx.Err()
	}
	options := importTUIProgramOptions(nil)
	options.browser = func(context.Context, importDestination, []*importCandidate, importPreviewLoader) (importReviewResult, error) {
		return importReviewResult{}, errors.New("unexpected browser launch")
	}
	type outcome struct {
		result importReviewResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := runImportTerminal(ctx, importDestination{}, importTUITestCandidates(), load, options)
		done <- outcome{result: result, err: err}
	}()
	select {
	case readCtx := <-started:
		require.NoError(t, readCtx.Err())
	case <-time.After(3 * time.Second):
		t.Fatal("preview reader did not start")
	}
	cancel()
	select {
	case got := <-done:
		require.ErrorIs(t, got.err, context.Canceled)
		require.ErrorIs(t, got.err, tea.ErrProgramKilled)
		require.Empty(t, got.result.IDs)
	case <-time.After(3 * time.Second):
		t.Fatal("terminal review did not stop after context cancellation")
	}
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

// Cancel, browser handoff and selection completion must remain distinct exit
// intents; each ends rendering without silently substituting another action.
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

// Opening a large history must load only visible rows with at most two pending
// reads, rather than reading every native session before the coworker navigates.
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

// An unreadable preview must show the failure while preserving selection;
// missing content cannot be replaced by a fabricated final reply.
func TestImportTerminalUnavailablePreview(t *testing.T) {
	m := newImportReviewModel(context.Background(), importDestination{}, importTUITestCandidates(), nil)
	m.Update(importPreviewLoadedMsg{id: "first-session", err: errors.New("file disappeared")})
	view := ansi.Strip(m.View().Content)
	require.Contains(t, view, "Content unavailable")
	require.Contains(t, view, "file disappeared")
	require.Equal(t, []string{"first-session"}, m.selectedIDs())
	require.NotContains(t, view, "Last AI reply")
}

// Untrusted controls cannot escape into the terminal, even in narrow layouts.
// Unicode content and scrolling must remain usable without exceeding its bounds.
func TestImportTerminalNarrowViewAndSafeNativeText(t *testing.T) {
	m := newImportReviewModel(context.Background(), importDestination{Team: "test", Visibility: "private"}, importTUITestCandidates(), nil)
	m.rememberPreview("first-session", &importContentPreview{
		NativeID:       "first-session",
		OpeningRequest: "Build 界界 game\x1b[2J\x1b]0;spoofed-title\x07\u202eevil\u2069\r\x00\n\tPreserve a second line.",
		Prompts:        []importPromptAnchor{{Content: "Preserve unicode 界 and emoji 🎮"}},
		LastReply:      strings.Repeat("A long result containing unicode 界. ", 80),
	})
	require.Contains(t, strings.Join(m.detailLines(120), "\n"), "\n Preserve a second line.", "readable multiline formatting must survive control stripping")
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
	before = m.scroll
	importTUIKey(m, tea.KeyPgUp)
	require.Less(t, m.scroll, before)
	importTUIKey(m, tea.KeyHome)
	require.Zero(t, m.scroll)
}

// Visiting many sessions must not retain their tool output or long messages.
// Eviction keeps the opening-request label and does not alter selection.
func TestImportTerminalExcerptCacheIsBoundedAndReloadsOnFocus(t *testing.T) {
	cands := importTUITestCandidates()
	for i := 0; i < 12; i++ {
		cands = append(cands, &importCandidate{Session: nativeimport.Session{NativeID: fmt.Sprintf("extra-%d", i)}, State: stateReady})
	}
	m := newImportReviewModel(context.Background(), importDestination{}, cands, func(context.Context, string) (*importContentPreview, error) {
		return nil, errors.New("reader not invoked by model scheduling test")
	})
	p := &importContentPreview{
		OpeningRequest: strings.Repeat("long request ", 1000), LastReply: strings.Repeat("long reply ", 1000),
		Entries: []session.Entry{{Type: session.EntryTypeTool, ToolOutput: strings.Repeat("large tool output ", 1000)}},
	}
	for i := 0; i < 100; i++ {
		p.Prompts = append(p.Prompts, importPromptAnchor{Content: strings.Repeat("long prompt ", 1000)})
	}
	for _, c := range cands {
		m.rememberPreview(c.Session.NativeID, p)
	}
	require.Len(t, m.previews, importTerminalPreviewLimit)
	require.NotNil(t, m.previews["first-session"], "focused content stays available as other rows load")
	require.Nil(t, m.previews["second-session"])
	require.NotEmpty(t, m.labels["second-session"], "evicting excerpts must not erase a row label")
	cached := m.previews["first-session"]
	require.Len(t, cached.Prompts, importTerminalPromptLimit)
	require.Equal(t, 100, cached.PromptCount)
	require.LessOrEqual(t, len([]rune(cached.OpeningRequest)), 2049)
	require.LessOrEqual(t, len([]rune(cached.LastReply)), 2049)
	require.LessOrEqual(t, len([]rune(cached.Prompts[0])), 181)
	require.Contains(t, strings.Join(m.detailLines(80), "\n"), "36 more human prompts")
	m.width, m.height = 120, 60
	require.Nil(t, m.Init(), "visible rows with retained labels must not repeatedly reload evicted content")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	require.NotNil(t, cmd, "focusing an evicted row must schedule fresh content")
	require.True(t, m.pending["second-session"])
	require.Equal(t, []string{"first-session"}, m.selectedIDs())
}

// An already imported session remains inspectable with its Ledger reference,
// but its explanation must not make it selectable for another import.
func TestImportTerminalSkippedSessionExplanation(t *testing.T) {
	c := importTUITestCandidates()[2]
	c.Covered = "2026-10-01T10-30-devon-OxOLD2"
	m := newImportReviewModel(context.Background(), importDestination{}, []*importCandidate{c}, nil)
	m.rememberPreview(c.Session.NativeID, &importContentPreview{OpeningRequest: "Fix the scoring algorithm"})
	detail := strings.Join(m.detailLines(80), "\n")
	require.Contains(t, detail, "Skipped: already in the Ledger")
	require.Contains(t, detail, "Coverage: 2026-10-01T10-30-devon-OxOLD2")
	require.Empty(t, m.selectedIDs(), "readable skipped content must stay unselectable")
}

// Public visibility and absent excerpts must be explicit. An empty native reply
// or a nil loader response cannot imply that the session completed successfully.
func TestImportTerminalPublicDestinationAndMissingExcerpts(t *testing.T) {
	m := newImportReviewModel(context.Background(), importDestination{Team: "Math Blitz", Visibility: "public"}, importTUITestCandidates(), nil)
	m.rememberPreview("first-session", &importContentPreview{})
	view := ansi.Strip(m.View().Content)
	require.Contains(t, view, "anyone can read the Ledger")
	require.Contains(t, view, "No human request available")
	importTUIKey(m, tea.KeyEnd)
	require.Contains(t, ansi.Strip(m.detailView()), "No AI reply available")
	m.Update(importPreviewLoadedMsg{id: "second-session"})
	importTUIKey(m, tea.KeyDown)
	require.Contains(t, ansi.Strip(m.detailView()), "session reader returned no content")
}

// An empty history must accept navigation and selection keys without inventing
// a candidate, scheduling content reads or panicking.
func TestImportTerminalEmptyHistory(t *testing.T) {
	m := newImportReviewModel(context.Background(), importDestination{}, nil, nil)
	require.Nil(t, m.Init())
	importTUIKey(m, tea.KeySpace)
	importTUIKey(m, tea.KeyDown)
	importTUIKey(m, 'a')
	require.Empty(t, m.selectedIDs())
	require.Contains(t, m.View().Content, "No sessions found")
}
