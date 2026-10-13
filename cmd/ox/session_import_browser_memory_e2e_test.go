//go:build browser

package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Prevents long pasted opening requests from accumulating in the row cache or
// DOM. A match beyond the bounded label must still find the complete request.
func TestImportBrowserChromeLargeOpeningsStayBoundedAndSearchFullText(t *testing.T) {
	ctx := importBrowserChrome(t)
	const tail = "tail-only-import-match"
	_, link := serveImportBrowserChrome(t, func(ctx context.Context, id string) (*importContentPreview, error) {
		preview, err := browserFixturePreview(ctx, id)
		opening := "A long pasted request " + strings.Repeat("additional source text ", 16*1024)
		if id == importBrowserOtherID {
			opening += tail
		}
		preview.OpeningRequest = opening
		preview.Prompts[0].Content = opening
		preview.Entries[0].Content = opening
		return preview, err
	})
	// Record only retained string lengths, never the source strings. This observes
	// the real application cache without adding a product debugging hook.
	require.NoError(t, chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(`
window.importOpeningCacheMax = 0;
const originalMapSet = Map.prototype.set;
Map.prototype.set = function(key, value) {
  if (typeof key === 'string' && /^[a-f0-9-]{36}$/.test(key) && typeof value === 'string') {
    window.importOpeningCacheMax = Math.max(window.importOpeningCacheMax, value.length);
  }
  return originalMapSet.call(this, key, value);
};`).Do(ctx)
			return err
		}),
		chromedp.Navigate(link), chromedp.WaitVisible(".quote", chromedp.ByQuery),
		chromedp.Poll(`document.querySelectorAll('[data-title]').length === 3 && Array.from(document.querySelectorAll('[data-title]')).every(x => !x.textContent.startsWith('Loading'))`, nil),
	))
	var cached, rendered int
	require.NoError(t, chromedp.Run(ctx,
		chromedp.Evaluate(`window.importOpeningCacheMax`, &cached),
		chromedp.Evaluate(`Math.max(...Array.from(document.querySelectorAll('[data-title]')).map(x => x.textContent.length))`, &rendered),
	))
	assert.Positive(t, cached, "real application row-cache writes must be observed")
	assert.LessOrEqual(t, cached, 240, "tab cache must retain bounded labels only")
	assert.LessOrEqual(t, rendered, 240, "row DOM must not retain pasted requests")
	require.NoError(t, chromedp.Run(ctx,
		chromedp.SetValue("#search", tail, chromedp.ByQuery),
		chromedp.Evaluate(`document.getElementById('search').dispatchEvent(new Event('input',{bubbles:true}))`, nil),
		chromedp.Poll(`document.getElementById('list-count').textContent === '1 shown · 3 total' && document.getElementById('scan-status').textContent === ''`, nil),
		chromedp.Click(`[data-focus="`+importBrowserOtherID+`"]`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.quote')?.textContent.endsWith('`+tail+`')`, nil),
	))
	var count string
	require.NoError(t, chromedp.Run(ctx,
		chromedp.Text("#selected-count", &count, chromedp.ByQuery),
		chromedp.Evaluate(`window.importOpeningCacheMax`, &cached),
	))
	assert.Equal(t, "2 sessions selected", count, "search and focus must preserve hidden selections")
	assert.LessOrEqual(t, cached, 240, "focused full detail must not enter the row cache")
	// Change the query after every label has loaded. A cached snippet alone is
	// insufficient to answer a new search, including a negative one.
	require.NoError(t, chromedp.Run(ctx,
		chromedp.SetValue("#search", "unfindable-import-request", chromedp.ByQuery),
		chromedp.Evaluate(`document.getElementById('search').dispatchEvent(new Event('input',{bubbles:true}))`, nil),
		chromedp.Poll(`document.getElementById('list-count').textContent === '0 shown · 3 total' && document.getElementById('scan-status').textContent === ''`, nil),
		chromedp.SetValue("#search", tail, chromedp.ByQuery),
		chromedp.Evaluate(`document.getElementById('search').dispatchEvent(new Event('input',{bubbles:true}))`, nil),
		chromedp.Poll(`document.getElementById('list-count').textContent === '1 shown · 3 total' && document.getElementById('scan-status').textContent === ''`, nil),
	))
}

// Superseded queries and a finished review must release both active search
// requests. Aborted searches cannot mark sessions unreadable or replace the
// new query's matches.
func TestImportBrowserChromeSearchCancellationReleasesRequests(t *testing.T) {
	ctx := importBrowserChrome(t)
	var block atomic.Bool
	started, canceled := make(chan struct{}, 4), make(chan struct{}, 4)
	b, link := serveImportBrowserChrome(t, func(ctx context.Context, id string) (*importContentPreview, error) {
		if block.Load() {
			started <- struct{}{}
			<-ctx.Done()
			canceled <- struct{}{}
			return nil, ctx.Err()
		}
		return browserFixturePreview(ctx, id)
	})
	require.NoError(t, chromedp.Run(ctx,
		chromedp.Navigate(link), chromedp.WaitVisible(".quote", chromedp.ByQuery),
		chromedp.Poll(`document.querySelectorAll('[data-title]').length === 3 && Array.from(document.querySelectorAll('[data-title]')).every(x => !x.textContent.startsWith('Loading'))`, nil),
	))
	waitBoth := func(events <-chan struct{}, message string) {
		t.Helper()
		deadline := time.NewTimer(2 * time.Second)
		defer deadline.Stop()
		for range 2 {
			select {
			case <-events:
			case <-deadline.C:
				t.Fatal(message)
			}
		}
	}
	search := func(query string) {
		t.Helper()
		require.NoError(t, chromedp.Run(ctx,
			chromedp.SetValue("#search", query, chromedp.ByQuery),
			chromedp.Evaluate(`document.getElementById('search').dispatchEvent(new Event('input',{bubbles:true}))`, nil),
		))
	}
	block.Store(true)
	search("old-absent-import-query")
	waitBoth(started, "search did not start two bounded workers")
	block.Store(false)
	search("literal text")
	waitBoth(canceled, "superseded search requests were not canceled")
	require.NoError(t, chromedp.Run(ctx,
		chromedp.Poll(`document.getElementById('list-count').textContent === '1 shown · 3 total' && document.getElementById('scan-status').textContent === ''`, nil),
	))
	var count string
	require.NoError(t, chromedp.Run(ctx, chromedp.Text("#selected-count", &count, chromedp.ByQuery)))
	assert.Equal(t, "2 sessions selected", count)
	block.Store(true)
	search("pending-at-review-cancel")
	waitBoth(started, "final search did not start")
	require.NoError(t, chromedp.Run(ctx, chromedp.Click("#cancel", chromedp.ByQuery)))
	select {
	case result := <-b.result:
		assert.True(t, result.Canceled)
		assert.Empty(t, result.IDs)
	case <-ctx.Done():
		t.Fatal("cancel did not return to the terminal")
	}
	waitBoth(canceled, "finished review left search requests running")
}
