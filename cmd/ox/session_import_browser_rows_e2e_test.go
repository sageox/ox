//go:build browser

package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Prevents each streamed opening from rebuilding a broad query's entire list
// and stealing keyboard focus. Filtering, selecting, and focusing also retain
// existing row nodes; hidden rows and the no-match notice obey author CSS.
func TestImportBrowserChromeSearchPreservesRowsAndKeyboardFocus(t *testing.T) {
	ctx := importBrowserChrome(t)
	const total = 24
	cands := make([]*importCandidate, total)
	for i := range cands {
		cands[i] = &importCandidate{
			Session: nativeimport.Session{NativeID: fmt.Sprintf("%08x-1111-4111-8111-aaaaaaaaaaaa", i+1), Agent: nativeimport.AgentCodex, Prompts: 1, Replies: 1},
			State:   stateReady, Selected: true,
		}
	}
	var scanning atomic.Bool
	release, started := make(chan struct{}), make(chan struct{}, total*2)
	ln := mustLoopbackListener(t)
	b, err := newImportBrowser(ln.Addr().String(), "secret", importDestination{Team: "Test team", RepoID: "repo_rows", Visibility: "private"}, cands,
		func(ctx context.Context, id string) (*importContentPreview, error) {
			if scanning.Load() {
				started <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return browserFixturePreview(ctx, id)
		})
	require.NoError(t, err)
	go serveUntilClosed(t, ln, b.handler())
	require.NoError(t, chromedp.Run(ctx,
		chromedp.Navigate(b.origin+"/#token=secret"), chromedp.WaitVisible(".quote", chromedp.ByQuery),
	))
	scanning.Store(true)
	require.NoError(t, chromedp.Run(ctx,
		chromedp.SetValue("#search", "codex", chromedp.ByQuery),
		chromedp.Evaluate(`document.getElementById('search').dispatchEvent(new Event('input',{bubbles:true}))`, nil),
	))
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for range 2 {
		select {
		case <-started:
		case <-deadline.C:
			t.Fatal("broad search did not start both workers")
		}
	}
	// Observe actual list mutations without modifying the application. Keep a
	// focused checkbox and the original row identities across streamed results.
	require.NoError(t, chromedp.Run(ctx, chromedp.Evaluate(`
window.importOriginalRows = Array.from(document.querySelectorAll('#session-list .session'));
window.importFocusedCheckbox = document.querySelector('[data-select="`+cands[0].Session.NativeID+`"]');
window.importFocusedCheckbox.focus();
window.importRowMutations = {added: 0, removed: 0};
window.importRowObserver = new MutationObserver(records => {
  for (const record of records) {
    window.importRowMutations.added += Array.from(record.addedNodes).filter(x => x.nodeType === 1 && x.matches('.session')).length;
    window.importRowMutations.removed += Array.from(record.removedNodes).filter(x => x.nodeType === 1 && x.matches('.session')).length;
  }
});
window.importRowObserver.observe(document.getElementById('session-list'), {childList: true});`, nil)))
	close(release)
	require.NoError(t, chromedp.Run(ctx,
		chromedp.Poll(`document.getElementById('scan-status').textContent === ''`, nil),
		chromedp.Evaluate(`new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))`, nil),
	))
	var focused, sameRows bool
	var mutations struct {
		Added   int `json:"added"`
		Removed int `json:"removed"`
	}
	require.NoError(t, chromedp.Run(ctx,
		chromedp.Evaluate(`document.activeElement === window.importFocusedCheckbox && window.importFocusedCheckbox.isConnected`, &focused),
		chromedp.Evaluate(`Array.from(document.querySelectorAll('#session-list .session')).every((row, i) => row === window.importOriginalRows[i])`, &sameRows),
		chromedp.Evaluate(`window.importRowMutations`, &mutations),
	))
	assert.True(t, focused, "result arrivals must preserve keyboard focus")
	assert.True(t, sameRows, "broad search must reuse every row")
	assert.Zero(t, mutations.Added, "already visible rows must not be re-created per response")
	assert.Zero(t, mutations.Removed, "already visible rows must not be detached per response")
	require.NoError(t, chromedp.Run(ctx,
		chromedp.Click(`[data-focus="`+cands[1].Session.NativeID+`"]`, chromedp.ByQuery),
		chromedp.WaitVisible(".quote", chromedp.ByQuery),
		chromedp.Click("#none", chromedp.ByQuery),
		chromedp.Click("#all", chromedp.ByQuery),
		chromedp.SetValue("#eligibility", "unavailable", chromedp.ByQuery),
		chromedp.Evaluate(`document.getElementById('eligibility').dispatchEvent(new Event('change',{bubbles:true}))`, nil),
	))
	var visible int
	var count, notice string
	require.NoError(t, chromedp.Run(ctx,
		chromedp.Evaluate(`Array.from(document.querySelectorAll('#session-list .session')).filter(row => getComputedStyle(row).display !== 'none').length`, &visible),
		chromedp.Text("#list-count", &count, chromedp.ByQuery),
		chromedp.Text("#session-list", &notice, chromedp.ByQuery),
	))
	assert.Zero(t, visible, "hidden rows must stay hidden despite display:grid")
	assert.Equal(t, "0 shown · 24 total", count)
	assert.Contains(t, notice, "No matching sessions. Your selection is preserved.")
	require.NoError(t, chromedp.Run(ctx,
		chromedp.SetValue("#eligibility", "ready", chromedp.ByQuery),
		chromedp.Evaluate(`document.getElementById('eligibility').dispatchEvent(new Event('change',{bubbles:true}))`, nil),
		chromedp.Evaluate(`Array.from(document.querySelectorAll('#session-list .session')).filter(row => getComputedStyle(row).display !== 'none').length`, &visible),
		chromedp.Text("#selected-count", &count, chromedp.ByQuery),
		chromedp.Evaluate(`Array.from(document.querySelectorAll('#session-list .session')).every((row, i) => row === window.importOriginalRows[i])`, &sameRows),
	))
	assert.Equal(t, total, visible)
	assert.Equal(t, "24 sessions selected", count)
	assert.True(t, sameRows, "filter, focus, and bulk selection must reuse rows")
}
