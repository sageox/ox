//go:build browser

package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This flag belongs to the test binary only; no product environment variable
// or CLI flag is introduced. Screenshots are optional local QA artifacts.
var importBrowserScreenshots = flag.String("import-browser-screenshots", "", "directory for session import browser QA screenshots")

// importBrowserChrome owns a fresh browser and its deadline so a failed DOM
// assertion cannot leave Chrome or its contexts running after test cleanup.
func importBrowserChrome(t *testing.T) context.Context {
	t.Helper()
	if testing.Short() {
		t.Skip("short: real Chrome session import roundtrip")
	}
	chrome := findChromePath()
	if chrome == "" {
		t.Skip("Chrome/Chromium is not installed")
	}
	options := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chrome), chromedp.Flag("headless", true), chromedp.Flag("disable-gpu", true))
	allocator, cancelAllocator := chromedp.NewExecAllocator(context.Background(), options...)
	t.Cleanup(cancelAllocator)
	ctx, cancel := chromedp.NewContext(allocator)
	t.Cleanup(cancel)
	ctx, timeout := context.WithTimeout(ctx, 25*time.Second)
	t.Cleanup(timeout)
	return ctx
}

// serveImportBrowserChrome exercises the actual loopback handler and token URL;
// only the native content reader is replaced for the browser fixture.
func serveImportBrowserChrome(t *testing.T, load importPreviewLoader) (*importBrowser, string) {
	t.Helper()
	ln := mustLoopbackListener(t)
	cands := importBrowserCandidates()
	cands = append(cands, &importCandidate{Session: nativeimport.Session{NativeID: "cccccccc-3333-4333-8333-cccccccccccc", Agent: nativeimport.AgentCodex, StartedAt: cands[0].Session.StartedAt, Prompts: 1, Replies: 1}, State: stateReady, Selected: true})
	b, err := newImportBrowser(ln.Addr().String(), "secret", importDestination{Team: "Math Blitz", RepoID: "repo_math_blitz", Visibility: "private"}, cands, load)
	require.NoError(t, err)
	go serveUntilClosed(t, ln, b.handler())
	return b, b.origin + "/#token=secret"
}

// browserFixturePreview includes literal HTML, ordered prompts and a failed tool
// call so the page must render source text safely without inventing completion.
func browserFixturePreview(_ context.Context, id string) (*importContentPreview, error) {
	opening := "Next for Math Blitz: a persistent high-score table."
	last := "The concurrency test reproduced lost updates. File locking now serializes writers; all 96 tests pass. Nothing is committed."
	second := "Can two games in separate terminals lose a high score? Prove it with concurrent writers in separate processes."
	if id == importBrowserOtherID {
		opening = `<img src=x onerror="window.importInjection=true"> Keep this literal text.`
		last = "This is a retained reply, not an inferred outcome."
	}
	if id == "cccccccc-3333-4333-8333-cccccccccccc" {
		opening = "Make Math Blitz adapt its difficulty to the player."
	}
	return &importContentPreview{
		NativeID: id, OpeningRequest: opening, LastReply: last,
		Prompts: []importPromptAnchor{{EntryIndex: 0, Content: opening}, {EntryIndex: 3, Content: second}},
		Entries: []session.Entry{
			{Type: session.EntryTypeUser, Content: opening},
			{Type: session.EntryTypeAssistant, Content: "Compare JSON, SQLite, and shelve. The chosen format needs atomic replacement and versioning."},
			{Type: session.EntryTypeTool, ToolName: "test", ToolInput: "pytest tests/test_scores.py", ToolOutput: "Concurrent writers lost a score.", IsError: true},
			{Type: session.EntryTypeUser, Content: second},
			{Type: session.EntryTypeAssistant, Content: last},
		},
	}, nil
}

// Proves the actual page preserves hidden selections, treats source content as
// text, shows tool failures only when expanded, and submits IDs to the terminal.
func TestImportBrowserChromeSelectionRoundtrip(t *testing.T) {
	ctx := importBrowserChrome(t)
	b, link := serveImportBrowserChrome(t, browserFixturePreview)
	require.NoError(t, chromedp.Run(ctx,
		chromedp.Navigate(link),
		chromedp.WaitVisible(".quote", chromedp.ByQuery),
		chromedp.Click(`[data-select="`+importBrowserReadyID+`"]`, chromedp.ByQuery),
		chromedp.SetValue("#search", "absent phrase", chromedp.ByQuery),
		chromedp.Evaluate(`document.getElementById('search').dispatchEvent(new Event('input',{bubbles:true}))`, nil),
	))
	var count, rows string
	require.NoError(t, chromedp.Run(ctx, chromedp.Text("#selected-count", &count, chromedp.ByQuery), chromedp.Text("#list-count", &rows, chromedp.ByQuery)))
	assert.Equal(t, "1 session selected", count)
	assert.Equal(t, "0 shown · 3 total", rows)
	require.NoError(t, chromedp.Run(ctx,
		chromedp.Evaluate(`document.getElementById('search').value='';document.getElementById('search').dispatchEvent(new Event('input',{bubbles:true}))`, nil),
		chromedp.Click(`[data-focus="`+importBrowserOtherID+`"]`, chromedp.ByQuery),
		chromedp.WaitVisible(".quote", chromedp.ByQuery),
	))
	var imageCount int
	var injected bool
	var quote string
	require.NoError(t, chromedp.Run(ctx,
		chromedp.Evaluate(`document.querySelectorAll('#session-detail img').length`, &imageCount),
		chromedp.Evaluate(`Boolean(window.importInjection)`, &injected),
		chromedp.Text(".quote", &quote, chromedp.ByQuery),
	))
	assert.Zero(t, imageCount)
	assert.False(t, injected)
	assert.Contains(t, quote, "<img src=x")
	require.NoError(t, chromedp.Run(ctx,
		chromedp.Click(`[data-focus="`+importBrowserReadyID+`"]`, chromedp.ByQuery),
		chromedp.WaitVisible(".quote", chromedp.ByQuery),
		chromedp.Click("#session-detail > details > summary", chromedp.ByQuery),
	))
	var toolsClosed bool
	require.NoError(t, chromedp.Run(ctx, chromedp.Evaluate(`!document.querySelector('.tool-group').open`, &toolsClosed)))
	assert.True(t, toolsClosed)
	require.NoError(t, chromedp.Run(ctx, chromedp.Click(".tool-group > summary", chromedp.ByQuery), chromedp.WaitVisible(".tool-group .warning", chromedp.ByQuery)))
	require.NoError(t, chromedp.Run(ctx, chromedp.Click("#submit", chromedp.ByQuery)))
	select {
	case result := <-b.result:
		assert.Equal(t, []string{"cccccccc-3333-4333-8333-cccccccccccc"}, result.IDs)
		assert.False(t, result.Canceled)
	case <-ctx.Done():
		t.Fatal("browser did not return a selection")
	}
	var message string
	require.NoError(t, chromedp.Run(ctx,
		chromedp.Poll(`document.getElementById('announcement').textContent.includes('selection returned to the terminal')`, nil),
		chromedp.Text("#announcement", &message, chromedp.ByQuery),
	))
	assert.Contains(t, message, "selection returned to the terminal")
}

// Proves slow responses cannot replace the newly focused session's content.
func TestImportBrowserChromeLatePreviewKeepsFocus(t *testing.T) {
	ctx := importBrowserChrome(t)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	load := func(requestCtx context.Context, id string) (*importContentPreview, error) {
		if id == importBrowserReadyID {
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-requestCtx.Done():
				return nil, requestCtx.Err()
			}
		}
		return browserFixturePreview(requestCtx, id)
	}
	_, link := serveImportBrowserChrome(t, load)
	require.NoError(t, chromedp.Run(ctx, chromedp.Navigate(link), chromedp.WaitVisible(`[data-focus="`+importBrowserOtherID+`"]`, chromedp.ByQuery)))
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("preview did not start")
	}
	require.NoError(t, chromedp.Run(ctx,
		chromedp.Click(`[data-focus="`+importBrowserOtherID+`"]`, chromedp.ByQuery),
		chromedp.WaitVisible(".quote", chromedp.ByQuery),
	))
	close(release)
	var quote string
	require.NoError(t, chromedp.Run(ctx,
		chromedp.Poll(`performance.getEntriesByType('resource').filter(x=>x.name.includes('id=`+importBrowserReadyID+`')).length >= 2`, nil, chromedp.WithPollingTimeout(4*time.Second)),
		chromedp.Evaluate(`new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))`, nil),
		chromedp.Text(".quote", &quote, chromedp.ByQuery),
	))
	assert.Contains(t, quote, "<img src=x")
}

// Proves desktop/mobile layout and optionally captures the real rendered UI
// for visual QA without committing binary screenshots to the repository.
func TestImportBrowserChromeResponsiveAndScreenshots(t *testing.T) {
	ctx := importBrowserChrome(t)
	_, link := serveImportBrowserChrome(t, func(ctx context.Context, id string) (*importContentPreview, error) {
		preview, err := browserFixturePreview(ctx, id)
		if id == importBrowserOtherID {
			preview.OpeningRequest = "Let's make Math Blitz feel like a real game."
			preview.Prompts[0].Content = preview.OpeningRequest
			preview.Entries[0].Content = preview.OpeningRequest
		}
		return preview, err
	})
	for _, viewport := range []struct {
		name, scheme  string
		width, height int64
	}{{"desktop", "light", 1440, 1050}, {"mobile", "light", 390, 844}, {"desktop-dark", "dark", 1440, 1050}} {
		require.NoError(t, chromedp.Run(ctx,
			chromedp.EmulateViewport(viewport.width, viewport.height),
			emulation.SetEmulatedMedia().WithFeatures([]*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: viewport.scheme}}),
			chromedp.Navigate(link), chromedp.WaitVisible(".quote", chromedp.ByQuery),
		))
		var overflow bool
		require.NoError(t, chromedp.Run(ctx, chromedp.Evaluate(`document.documentElement.scrollWidth > window.innerWidth`, &overflow)))
		assert.False(t, overflow, viewport.name+" must not scroll horizontally")
		if *importBrowserScreenshots != "" {
			var png []byte
			require.NoError(t, chromedp.Run(ctx, chromedp.FullScreenshot(&png, 90)))
			require.NoError(t, os.MkdirAll(*importBrowserScreenshots, 0o700))
			path := filepath.Join(*importBrowserScreenshots, "session-import-"+viewport.name+".png")
			require.NoError(t, os.WriteFile(path, png, 0o600))
			t.Logf("visual QA screenshot: %s", path)
		}
	}
}

// Proves a server-side source-change response is shown as unavailable rather
// than silently displaying old content or resetting the selected set.
func TestImportBrowserChromeSourceChangeIsVisible(t *testing.T) {
	ctx := importBrowserChrome(t)
	b, link := serveImportBrowserChrome(t, func(context.Context, string) (*importContentPreview, error) { return nil, errImportSourceChanged })
	require.NoError(t, chromedp.Run(ctx, chromedp.Navigate(link), chromedp.WaitVisible("#session-detail button", chromedp.ByQuery)))
	var text, count string
	require.NoError(t, chromedp.Run(ctx, chromedp.Text("#session-detail", &text, chromedp.ByQuery), chromedp.Text("#selected-count", &count, chromedp.ByQuery)))
	assert.Contains(t, text, "changed since you started reviewing")
	assert.Equal(t, "2 sessions selected", count)
	select {
	case result := <-b.result:
		t.Fatalf("preview failure submitted selection: %+v", result)
	default:
	}
	assert.Equal(t, http.StatusConflict, importBrowserRequest(b, "GET", "/api/preview?id="+importBrowserReadyID, "").Code)
}

// Prevents vendor bootstrap instructions, retained for fidelity, from being
// presented as a human request after exclusion from the prompt map.
func TestImportBrowserChromeRetainedBootstrapIsCollapsedContext(t *testing.T) {
	ctx := importBrowserChrome(t)
	_, link := serveImportBrowserChrome(t, func(ctx context.Context, id string) (*importContentPreview, error) {
		preview, err := browserFixturePreview(ctx, id)
		preview.Entries = append([]session.Entry{{Type: session.EntryTypeUser, Content: "# AGENTS.md instructions\nProject context"}}, preview.Entries...)
		for i := range preview.Prompts {
			preview.Prompts[i].EntryIndex++
		}
		return preview, err
	})
	require.NoError(t, chromedp.Run(ctx,
		chromedp.Navigate(link), chromedp.WaitVisible(".quote", chromedp.ByQuery),
		chromedp.Click("#session-detail > details > summary", chromedp.ByQuery),
	))
	var requests int
	var contextCollapsed bool
	require.NoError(t, chromedp.Run(ctx,
		chromedp.Evaluate(`Array.from(document.querySelectorAll('.conversation > article > .eyebrow')).filter(x=>x.textContent==='Human request').length`, &requests),
		chromedp.Evaluate(`!document.querySelector('.conversation > details:not(.tool-group)').open`, &contextCollapsed),
	))
	assert.Equal(t, 2, requests)
	assert.True(t, contextCollapsed)
	require.NoError(t, chromedp.Run(ctx, chromedp.Click(".conversation > details:not(.tool-group) > summary", chromedp.ByQuery)))
	var contextText string
	require.NoError(t, chromedp.Run(ctx, chromedp.Text(".conversation > details:not(.tool-group)", &contextText, chromedp.ByQuery)))
	assert.Contains(t, contextText, "# AGENTS.md instructions")
}

// Prevents a client cache from bypassing the CLI's source snapshot check when
// a previously visited session is focused again.
func TestImportBrowserChromeRefocusRechecksChangedSource(t *testing.T) {
	ctx := importBrowserChrome(t)
	var changed atomic.Bool
	_, link := serveImportBrowserChrome(t, func(ctx context.Context, id string) (*importContentPreview, error) {
		if id == importBrowserReadyID && changed.Load() {
			return nil, errImportSourceChanged
		}
		return browserFixturePreview(ctx, id)
	})
	require.NoError(t, chromedp.Run(ctx, chromedp.Navigate(link), chromedp.WaitVisible(".quote", chromedp.ByQuery)))
	changed.Store(true)
	require.NoError(t, chromedp.Run(ctx,
		chromedp.Click(`[data-focus="`+importBrowserReadyID+`"]`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('#session-detail h2')?.textContent === 'Preview unavailable'`, nil, chromedp.WithPollingTimeout(2*time.Second)),
	))
	var text, count string
	require.NoError(t, chromedp.Run(ctx, chromedp.Text("#session-detail", &text, chromedp.ByQuery), chromedp.Text("#selected-count", &count, chromedp.ByQuery)))
	assert.Contains(t, text, "changed since you started reviewing")
	assert.Equal(t, "2 sessions selected", count)
}
