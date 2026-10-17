//go:build browser

// plan_review_browser_test.go drives a REAL headless Chrome against a live
// `ox plan review` server to prove the browser leg of the review round-trip end
// to end: the actual review.js running in a real browser submits a reviewer's
// mark, the authoring coworker acts on it, and the reviewer's page live-reloads
// to show it addressed — no mock of the page, no mock of the transport.
//
// It is gated behind the `browser` build tag so it stays OUT of the default
// `make test` / `make test-all` path (the repo deliberately keeps no
// browser-automation harness in the standard suite). Run it with:
//
//	make test-browser        # or: go test -tags browser ./cmd/ox/ -run TestBrowser
//
// It skips cleanly when no Chrome/Chromium binary is present, and under -short.
//
// The HTTP contract this exercises (POST /feedback with the page's review token,
// SSE /events reload) is also proven hermetically, without a browser, in
// plan_review_roundtrip_test.go — that is the CI proof; this is the reality check.
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/sageox/ox/internal/plan"
	"github.com/sageox/ox/pkg/planreview"
)

// mustLoopbackListener binds an ephemeral loopback port, closed on test cleanup.
func mustLoopbackListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// serveUntilClosed serves h on ln until the listener is closed (test cleanup).
func serveUntilClosed(_ *testing.T, ln net.Listener, h http.Handler) {
	srv := &http.Server{Handler: h}
	_ = srv.Serve(ln) // returns ErrServerClosed-equivalent when ln is closed
}

// findChromePath returns a usable Chrome/Chromium executable, or "" if none is
// installed — so the test can skip rather than fail on a machine without a
// browser.
func findChromePath() string {
	candidates := []string{
		"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome",
	}
	if runtime.GOOS == "darwin" {
		candidates = append(candidates,
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		)
	}
	for _, c := range candidates {
		if strings.ContainsRune(c, os.PathSeparator) {
			if info, err := os.Stat(c); err == nil && !info.IsDir() {
				return c
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

// serveLivePlanReview saves a small markdown plan into a real ledger and starts
// the real live-review HTTP server on a loopback port, returning the served URL,
// the plan dir, and the broadcaster the SSE reload rides on. It mirrors what
// `ox plan review` wires at runtime (a real listener whose address is the review
// endpoint the page posts back to), so review.js talks same-origin.
func serveLivePlanReview(t *testing.T) (url, planDir string, bc *broadcaster) {
	t.Helper()
	// a real git repo + isolated HOME/XDG + a .sageox/config.json repo_id, so the
	// default ledger resolver points at a scratch ledger plan.Save can create —
	// the same setup the other plan cmd tests use for Save/Load round-trips.
	gitRoot := newPlanStatusTestRepo(t)
	md := "# Browser Roundtrip\n\n## Risks\n\nThe retry path can double-fire under load.\n\n## Rollout\n\nShip behind a flag.\n"
	dir, _, err := plan.Save(gitRoot, plan.Input{Raw: md}, plan.Result{}, nil, plan.Meta{
		Topic: "Browser Roundtrip", Slug: "browser-roundtrip",
	})
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}
	return serveSavedPlanReview(t, gitRoot, "browser-roundtrip", dir)
}

// serveAuthoredPlanReview is serveLivePlanReview for an AUTHORED HTML plan of
// record: no scaffold — the page gets only the injected ox chrome (chrome.js +
// review.js), which is exactly what a coworker-authored plan runs in a browser.
// The page carries its own Esc handling, the way an authored inspector would: a
// #inspector panel its document-level listener closes on Esc (marking the key
// handled), and a window-level counter of Esc presses nothing handled. Its
// figure's image is a click target that, like any image, leaves a text
// selection in place, and its table's cells — like the two lines around its
// <br> — sit side by side with no whitespace between them, as generated markup
// often does.
func serveAuthoredPlanReview(t *testing.T) (url, planDir string, bc *broadcaster) {
	t.Helper()
	gitRoot := newPlanStatusTestRepo(t)
	html := []byte(`<!doctype html><html><head><meta charset="utf-8"><title>Authored Roundtrip</title>` +
		`<meta name="ox-plan-slug" content="authored-roundtrip"></head><body><h1>Authored Roundtrip</h1>` +
		`<section id="risks"><h2>Risks</h2><p>The retry path can double-fire under load.</p>` +
		`<figure><img alt="retry budget" width="160" height="60" src="data:image/gif;base64,R0lGODlhAQABAIAAAP///wAAACH5BAEAAAAALAAAAAABAAEAAAICRAEAOw=="></figure>` +
		`<table><tr><td>Budget</td><td>three</td></tr></table><p>limit<br>five</p></section>` +
		`<aside id="inspector" hidden>retry budget: 3</aside>` +
		`<script>document.addEventListener('keydown',function(e){var p=document.getElementById('inspector');` +
		`if(e.key==='Escape'&&!e.defaultPrevented&&!p.hidden){p.hidden=true;e.preventDefault();}});` +
		`window.addEventListener('keydown',function(e){if(e.key==='Escape'&&!e.defaultPrevented)` +
		`document.body.dataset.pageEsc=String((+document.body.dataset.pageEsc||0)+1);});</script></body></html>`)
	dir := savePlanArtifacts(gitRoot, plan.Input{Raw: "# Authored Roundtrip\n\n## Risks\n\nThe retry path can double-fire under load.\n"}, plan.Result{}, html, plan.PrimaryHTML)
	if dir == "" {
		t.Fatal("savePlanArtifacts returned empty dir — the authored plan was not saved")
	}
	return serveSavedPlanReview(t, gitRoot, "authored-roundtrip", dir)
}

// serveSavedPlanReview starts the real live-review server for an already-saved
// plan. Shared by the markdown and authored-HTML harnesses above.
func serveSavedPlanReview(t *testing.T, gitRoot, slug, dir string) (url, planDir string, bc *broadcaster) {
	t.Helper()
	if _, _, _, err := plan.Load(gitRoot, slug); err != nil {
		t.Fatalf("plan must be loadable for the live server to render it: %v", err)
	}

	ln := mustLoopbackListener(t)
	base := "http://" + ln.Addr().String()
	bc = newBroadcaster()
	h := liveReviewHandler(gitRoot, slug, dir, base, "secret", bc, make(chan int, 8), make(chan struct{}, 1))
	go serveUntilClosed(t, ln, h)

	// broadcast a reload whenever the plan dir changes, exactly as `ox plan review`
	// does — this is what turns an agent resolve into a live page reload.
	wctx, wcancel := context.WithCancel(context.Background())
	t.Cleanup(wcancel)
	go watchPlanDir(wctx, dir, bc)

	return base, dir, bc
}

// TestBrowser_ReviewRoundTripInRealChrome is the headline real-browser proof.
// In a real headless Chrome: toggle Review, mark a section, add a note, Submit —
// then assert the ledger received it and the agent's `await` surfaces it; then
// the agent addresses it and the reviewer's OPEN page live-reloads to show the
// item addressed, with no reader action.
// Failure prevented: the review page looks interactive but its submit never
// reaches the ledger, or an agent resolve never reaches the open page.
func TestBrowser_ReviewRoundTripInRealChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("short: launches a real headless Chrome")
	}
	chromePath := findChromePath()
	if chromePath == "" {
		t.Skip("no Chrome/Chromium binary found — skipping real-browser E2E")
	}

	base, planDir, _ := serveLivePlanReview(t)

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chromePath))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	t.Cleanup(cancelAlloc)
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	t.Cleanup(cancelCtx)
	ctx, cancelTimeout := context.WithTimeout(ctx, 45*time.Second)
	t.Cleanup(cancelTimeout)

	const note = "bound the blast radius in the browser"
	var seeded bool

	// Load the page, seed the reviewer identity (so Submit doesn't block on a
	// name prompt), reload so review.js reads it, then mark up a section and
	// Submit — every step is a real click/keystroke against the injected review.js.
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
		chromedp.Evaluate(`(function(){try{localStorage.setItem('ox-plan-reviewer','Devon');localStorage.setItem('ox-plan-rev-seen','1');return true;}catch(e){return false;}})()`, &seeded),
		chromedp.Reload(),
		chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
		chromedp.Click(".rev-toggle", chromedp.ByQuery),   // enter Review mode
		chromedp.Click("section#sec-1", chromedp.ByQuery), // click the first section (Risks)
		chromedp.WaitVisible(".rev-pop .rev-save", chromedp.ByQuery),
		chromedp.SendKeys(".rev-pop .rev-note", note, chromedp.ByQuery),
		chromedp.Click(".rev-pop .rev-save", chromedp.ByQuery), // save the mark (default: request-change)
		chromedp.Click(".rev-submit", chromedp.ByQuery),        // Submit -> POST /feedback
	); err != nil {
		t.Fatalf("browser review interaction failed: %v", err)
	}
	if !seeded {
		t.Fatal("could not seed reviewer identity in the browser")
	}

	// The submit reached the ledger as a round carrying the section + note the
	// reviewer left. Poll: the fetch is async after the click returns.
	it := waitForOneRound(t, planDir, 15*time.Second)
	if it.Section != "Risks" || it.Note != note || it.Reviewer != "Devon" {
		t.Fatalf("browser mark lost its context in transit: %+v", it)
	}
	anchor := it.Anchor

	// The authoring coworker's `await` read surfaces it — proof it reached the agent.
	if res, done := awaitSnapshot(planDir); !done || len(res.Open) != 1 || res.Open[0].Section != "Risks" || res.Open[0].Reviewer != "Devon" {
		t.Fatalf("agent await did not surface the browser mark: done=%v %+v", done, res.Open)
	}

	// The agent addresses it, exactly as `ox plan feedback resolve` does.
	resolveForTest(t, planDir, anchor, planreview.StateAddressed, "guarded the retry")

	// The reviewer's still-open page live-reloads (SSE) and repaints the item as
	// addressed — no reader action. review.js sets data-revstate="addressed" on
	// the element once the reloaded page's review state shows the resolution.
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`[data-revstate="addressed"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("reviewer page did not live-reload to show the item addressed: %v", err)
	}
}

// TestBrowser_UnsentMarkSurvivesReconnect proves the Quinn continuity beat: a mark
// saved in the browser but NOT yet submitted survives a dropped-and-restored
// connection (a page reload) and can then be submitted, reaching the authoring
// workflow. This is pure client-side draft persistence (localStorage in
// review.js) — only a real browser exercises it; a regression in save/restore
// would pass every hermetic test, which POST already-submitted feedback.
// Failure prevented: a reviewer loses in-progress feedback when the review server
// blips and the page reloads.
func TestBrowser_UnsentMarkSurvivesReconnect(t *testing.T) {
	if testing.Short() {
		t.Skip("short: launches a real headless Chrome")
	}
	chromePath := findChromePath()
	if chromePath == "" {
		t.Skip("no Chrome/Chromium binary found — skipping real-browser E2E")
	}

	base, planDir, _ := serveLivePlanReview(t)

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chromePath))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	t.Cleanup(cancelAlloc)
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	t.Cleanup(cancelCtx)
	ctx, cancelTimeout := context.WithTimeout(ctx, 45*time.Second)
	t.Cleanup(cancelTimeout)

	const note = "draft that must survive a reconnect"
	var seeded bool
	var unsentBefore, unsentAfter, draftAfter string

	// Mark a section but DO NOT submit — the mark lives only in the browser.
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
		chromedp.Evaluate(`(function(){try{localStorage.setItem('ox-plan-reviewer','Quinn');localStorage.setItem('ox-plan-rev-seen','1');return true;}catch(e){return false;}})()`, &seeded),
		chromedp.Reload(),
		chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
		chromedp.Click(".rev-toggle", chromedp.ByQuery),
		chromedp.Click("section#sec-1", chromedp.ByQuery),
		chromedp.WaitVisible(".rev-pop .rev-save", chromedp.ByQuery),
		chromedp.SendKeys(".rev-pop .rev-note", note, chromedp.ByQuery),
		chromedp.Click(".rev-pop .rev-save", chromedp.ByQuery), // saved as an unsent draft, NOT submitted
		chromedp.Text(".rev-count", &unsentBefore, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("browser draft interaction failed: %v", err)
	}
	if !seeded {
		t.Fatal("could not seed reviewer identity in the browser")
	}
	if strings.TrimSpace(unsentBefore) != "1 unsent" {
		t.Fatalf("a saved mark must show as exactly one unsent draft, counter=%q", unsentBefore)
	}
	// nothing has reached the ledger — it is only a local draft
	if sets := plan.ReadReview(planDir).Rounds; len(sets) != 0 {
		t.Fatalf("an unsent draft must not reach the ledger, got %d round(s)", len(sets))
	}

	// The connection drops and returns: reload the page (same stable origin).
	if err := chromedp.Run(ctx,
		chromedp.Reload(),
		chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
		chromedp.Text(".rev-count", &unsentAfter, chromedp.ByQuery),
		chromedp.Evaluate(jsMarks, &draftAfter),
	); err != nil {
		t.Fatalf("reload after reconnect failed: %v", err)
	}
	// the unsent draft is restored — the counter still shows it and localStorage
	// kept the note through the reload
	if strings.TrimSpace(unsentAfter) != "1 unsent" {
		t.Fatalf("exactly one unsent mark must be restored after a reconnect, counter=%q", unsentAfter)
	}
	if !strings.Contains(draftAfter, note) {
		t.Fatalf("the restored draft lost its note: %q", draftAfter)
	}

	// Back online, the reviewer submits the restored draft and the authoring
	// workflow receives it.
	if err := chromedp.Run(ctx,
		chromedp.Click(".rev-submit", chromedp.ByQuery),
	); err != nil {
		t.Fatalf("submit after reconnect failed: %v", err)
	}
	it := waitForOneRound(t, planDir, 15*time.Second)
	if it.Note != note || it.Reviewer != "Quinn" {
		t.Fatalf("the restored-then-submitted draft lost its content: %+v", it)
	}
	if res, done := awaitSnapshot(planDir); !done || len(res.Open) != 1 || res.Open[0].Anchor != it.Anchor {
		t.Fatalf("the submitted draft must reach the agent, done=%v %+v", done, res.Open)
	}
}

// TestBrowser_ReviewModeExitIsVisibleAndEscapable proves a reviewer can always
// tell they are in review mode and always get out: entering announces itself
// and relabels the toggle to the way out, and the announcement keeps its full
// lifetime across a quick exit and re-entry; Esc closes an open note first and
// the mode second (even from inside the note's textarea); any click outside a
// note dismisses it, including one on the comments rail; and the rail — itself
// a list — is never a mark-up target, by click or by hover styling.
// Failure prevented: a reviewer clicks Review, sees only a green button, and has
// no visible way back to reading the plan.
func TestBrowser_ReviewModeExitIsVisibleAndEscapable(t *testing.T) {
	if testing.Short() {
		t.Skip("short: launches a real headless Chrome")
	}
	chromePath := findChromePath()
	if chromePath == "" {
		t.Skip("no Chrome/Chromium binary found — skipping real-browser E2E")
	}

	base, _, _ := serveLivePlanReview(t)

	// A desktop-width window: above 1400px the comments rail is the fixed
	// top-right panel a reviewer actually meets, clear of the bottom-left bar
	// and toast that would otherwise sit over it at the default 800x600.
	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chromePath), chromedp.WindowSize(1600, 1000))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	t.Cleanup(cancelAlloc)
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	t.Cleanup(cancelCtx)
	ctx, cancelTimeout := context.WithTimeout(ctx, 45*time.Second)
	t.Cleanup(cancelTimeout)

	const inMode = `document.body.classList.contains('rev-on')`
	const noteOpen = `!!document.querySelector('.rev-pop')`
	const toastText = `(function(){var t=document.querySelector('.rev-toast');return t?t.textContent:'';})()`
	// The toast's 8s timers are captured instead of scheduled, so the test fires
	// each one when it chooses rather than waiting out the lifetime.
	const toastTimerHook = `(function(){window.__toastTimers=[];var real=window.setTimeout;` +
		`window.setTimeout=function(fn,ms){if(ms===8000){window.__toastTimers.push(fn);return 0;}return real.apply(window,arguments);};return true;})()`
	var seeded, hooked, onAfterEnter, noteAfterEsc, onAfterEsc, onAfterSecondEsc, toastAfterStaleTimer, toastAfterOwnTimer, noteAfterClickAway, noteAfterRailClick, railFlashed bool
	var labelOn, labelOff, toast, railHover string
	var timersAfterEnter int
	var railBox struct{ X, Y float64 }

	// Entering: the mode announces itself and the button becomes the exit.
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
		chromedp.Evaluate(`(function(){try{localStorage.setItem('ox-plan-reviewer','Devon');localStorage.setItem('ox-plan-rev-seen','1');return true;}catch(e){return false;}})()`, &seeded),
		chromedp.Reload(),
		chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
		chromedp.Evaluate(toastTimerHook, &hooked),
		chromedp.Click(".rev-toggle", chromedp.ByQuery),
		chromedp.Evaluate(toastText, &toast),
		chromedp.Evaluate(`window.__toastTimers.length`, &timersAfterEnter),
		chromedp.Text(".rev-toggle", &labelOn, chromedp.ByQuery),
		chromedp.Evaluate(inMode, &onAfterEnter),
	); err != nil {
		t.Fatalf("entering review mode failed: %v", err)
	}
	if !seeded || !hooked {
		t.Fatalf("could not prepare the browser: seeded=%v hooked=%v", seeded, hooked)
	}
	if timersAfterEnter != 1 {
		t.Fatalf("the toast-timer hook caught %d timers on entry, want 1 — if the toast lifetime changed, update the hook", timersAfterEnter)
	}
	if !onAfterEnter || strings.TrimSpace(labelOn) != "Exit review" {
		t.Fatalf("entering review mode must relabel the toggle to the exit: on=%v label=%q", onAfterEnter, labelOn)
	}
	if !strings.Contains(toast, "Esc") || !strings.Contains(toast, "Exit review") {
		t.Fatalf("entering review mode must say how to leave, toast=%q", toast)
	}

	// Esc from inside a note closes only the note; a second Esc leaves the mode.
	if err := chromedp.Run(ctx,
		chromedp.Click("section#sec-1", chromedp.ByQuery),
		chromedp.WaitVisible(".rev-pop .rev-note", chromedp.ByQuery),
		chromedp.Focus(".rev-pop .rev-note", chromedp.ByQuery),
		chromedp.KeyEvent(kb.Escape),
		chromedp.Evaluate(noteOpen, &noteAfterEsc),
		chromedp.Evaluate(inMode, &onAfterEsc),
		chromedp.KeyEvent(kb.Escape),
		chromedp.Evaluate(inMode, &onAfterSecondEsc),
		chromedp.Text(".rev-toggle", &labelOff, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("Esc interaction failed: %v", err)
	}
	if noteAfterEsc || !onAfterEsc {
		t.Fatalf("first Esc must close the note and keep the mode: note=%v on=%v", noteAfterEsc, onAfterEsc)
	}
	if onAfterSecondEsc || strings.TrimSpace(labelOff) != "Review" {
		t.Fatalf("second Esc must leave review mode and restore the label: on=%v label=%q", onAfterSecondEsc, labelOff)
	}

	// Re-entering replaces the entry toast. The first entry's timer firing late
	// must not take the replacement down with it; the replacement's own timer does.
	if err := chromedp.Run(ctx,
		chromedp.Click(".rev-toggle", chromedp.ByQuery),
		chromedp.Evaluate(`window.__toastTimers[0]();!!document.querySelector('.rev-toast')`, &toastAfterStaleTimer),
		chromedp.Evaluate(`window.__toastTimers[1]();!!document.querySelector('.rev-toast')`, &toastAfterOwnTimer),
	); err != nil {
		t.Fatalf("re-entering review mode failed: %v", err)
	}
	if !toastAfterStaleTimer {
		t.Fatal("an earlier toast's timer must not remove the toast that replaced it")
	}
	if toastAfterOwnTimer {
		t.Fatal("a toast's own timer must remove it")
	}

	// Clicking away from an open note dismisses it (the title is not a target).
	if err := chromedp.Run(ctx,
		chromedp.Click("section#sec-1", chromedp.ByQuery),
		chromedp.WaitVisible(".rev-pop .rev-note", chromedp.ByQuery),
		chromedp.Click("main > h1", chromedp.ByQuery),
		chromedp.Evaluate(noteOpen, &noteAfterClickAway),
	); err != nil {
		t.Fatalf("click-away interaction failed: %v", err)
	}
	if noteAfterClickAway {
		t.Fatal("clicking away from an open note must dismiss it")
	}

	// The comments rail is review chrome: with another note open, clicking a row
	// dismisses that note like any click outside it, scrolls to the row's mark,
	// and opens no note on the row itself. Hovering a row shows no target styling.
	if err := chromedp.Run(ctx,
		chromedp.Click("section#sec-1", chromedp.ByQuery),
		chromedp.WaitVisible(".rev-pop .rev-save", chromedp.ByQuery),
		chromedp.Click(".rev-pop .rev-save", chromedp.ByQuery),
		chromedp.WaitVisible(".rev-rail-item", chromedp.ByQuery),
		chromedp.Click("section#sec-2", chromedp.ByQuery),
		chromedp.WaitVisible(".rev-pop .rev-note", chromedp.ByQuery),
		chromedp.Click(".rev-rail-item", chromedp.ByQuery),
		chromedp.Evaluate(noteOpen, &noteAfterRailClick),
		chromedp.Evaluate(`document.querySelector('section#sec-1').classList.contains('rev-flash')`, &railFlashed),
		chromedp.Evaluate(`(function(){var r=document.querySelector('.rev-rail-item').getBoundingClientRect();return {x:r.left+r.width/2,y:r.top+r.height/2};})()`, &railBox),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return chromedp.MouseEvent("mouseMoved", railBox.X, railBox.Y).Do(ctx)
		}),
		chromedp.Evaluate(`(function(){var s=getComputedStyle(document.querySelector('.rev-rail-item'));return s.outlineStyle+' '+s.cursor;})()`, &railHover),
	); err != nil {
		t.Fatalf("rail interaction failed: %v", err)
	}
	if noteAfterRailClick {
		t.Fatal("a rail-row click must leave no note open — neither the one already open nor a new one on the row")
	}
	if !railFlashed {
		t.Fatal("the rail click never reached the row's own scroll-to handler — either something covered the row (vacuous) or review mode swallowed the click")
	}
	if railHover != "none pointer" {
		t.Fatalf("a hovered rail row must not look like a mark-up target (want no outline, pointer cursor), got %q", railHover)
	}
}

// TestBrowser_FirstVisitHintLeavesTheBarClickable: on Avery's first visit to
// ox's live page a hint points at Review, and every button on the review bar
// stays clickable under it.
// Failure prevented: the hint sits on the bar's row, so a first-time
// reviewer's click on Review, or a button beside it, lands on the hint and
// does nothing.
func TestBrowser_FirstVisitHintLeavesTheBarClickable(t *testing.T) {
	ctx := frameBrowser(t)
	base, _, _ := serveLivePlanReview(t)
	if err := chromedp.Run(ctx, chromedp.Navigate(base+"/"), chromedp.WaitVisible(".rev-hint", chromedp.ByQuery)); err != nil {
		t.Fatalf("the first visit must show the hint: %v", err)
	}
	// the buttons whose center the browser hit-tests to something else
	if got := evalString(t, ctx, `Array.prototype.filter.call(document.querySelectorAll('.rev-bar button'),function(b){var r=b.getBoundingClientRect();`+
		`return !b.contains(document.elementFromPoint(r.left+r.width/2,r.top+r.height/2))}).map(function(b){return b.textContent}).join(', ')`); got != "" {
		t.Errorf("the hint covers %s", got)
	}
}

// TestBrowser_ReviewModeSurvivesLiveReload proves the continuity beat of the
// live loop: the page reloads whenever the agent addresses an item, and a
// reviewer mid-review must land back IN review mode — silently, with no entry
// toast — while a fresh tab still opens in reading mode. It enters with the `r`
// key, which also proves the key is handled exactly once on the scaffold page
// (a second handler would toggle it straight back off).
// Failure prevented: every agent fix kicks the reviewer out of review mode with
// no signal, so they re-click Review after each reload or stop marking up.
func TestBrowser_ReviewModeSurvivesLiveReload(t *testing.T) {
	if testing.Short() {
		t.Skip("short: launches a real headless Chrome")
	}
	chromePath := findChromePath()
	if chromePath == "" {
		t.Skip("no Chrome/Chromium binary found — skipping real-browser E2E")
	}

	base, _, _ := serveLivePlanReview(t)

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chromePath))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	t.Cleanup(cancelAlloc)
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	t.Cleanup(cancelCtx)
	ctx, cancelTimeout := context.WithTimeout(ctx, 45*time.Second)
	t.Cleanup(cancelTimeout)

	const inMode = `document.body.classList.contains('rev-on')`
	const toastText = `(function(){var t=document.querySelector('.rev-toast');return t?t.textContent:'';})()`
	var seeded, onAfterKey, onAfterReload, onInNewTab bool
	var labelAfterReload, toastAfterReload string

	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
		chromedp.Evaluate(`(function(){try{localStorage.setItem('ox-plan-reviewer','Quinn');localStorage.setItem('ox-plan-rev-seen','1');return true;}catch(e){return false;}})()`, &seeded),
		chromedp.Reload(),
		chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
		chromedp.KeyEvent("r"),
		chromedp.Evaluate(inMode, &onAfterKey),
	); err != nil {
		t.Fatalf("entering review mode by key failed: %v", err)
	}
	if !seeded {
		t.Fatal("could not seed reviewer identity in the browser")
	}
	if !onAfterKey {
		t.Fatal("pressing r must enter review mode (exactly one handler)")
	}

	// The live reload the agent's fixes trigger.
	if err := chromedp.Run(ctx,
		chromedp.Reload(),
		chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
		chromedp.Evaluate(inMode, &onAfterReload),
		chromedp.Text(".rev-toggle", &labelAfterReload, chromedp.ByQuery),
		chromedp.Evaluate(toastText, &toastAfterReload),
	); err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if !onAfterReload || strings.TrimSpace(labelAfterReload) != "Exit review" {
		t.Fatalf("a live reload must land the reviewer back in review mode: on=%v label=%q", onAfterReload, labelAfterReload)
	}
	if strings.Contains(toastAfterReload, "Review mode") {
		t.Fatalf("a restored mode must not re-announce itself as a fresh entry, toast=%q", toastAfterReload)
	}

	// A fresh tab on the same plan starts in reading mode.
	tabCtx, cancelTab := chromedp.NewContext(ctx)
	t.Cleanup(cancelTab)
	if err := chromedp.Run(tabCtx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
		chromedp.Evaluate(inMode, &onInNewTab),
	); err != nil {
		t.Fatalf("fresh tab failed: %v", err)
	}
	if onInNewTab {
		t.Fatal("a fresh tab must open in reading mode, not inherit another tab's review mode")
	}
}

// TestBrowser_ReviewKeysWorkOnAuthoredPlan proves the keyboard entry and exit on
// an AUTHORED HTML plan — the page kind that carries no scaffold and therefore
// no scaffold key map, only the injected chrome — and that Esc shares the key
// with the page's own Esc handling one layer per press: the page's open
// inspector closes first, the next Esc leaves review mode without also reaching
// the page, and an Esc review mode has no use for still reaches the page.
// Asserts the page really is the authored one (no scaffold nav) so a scaffold
// fallback could not pass it.
// Failure prevented: `r`/Esc work on markdown plans and silently do nothing on
// the authored pages the plan skill steers coworkers toward — or one Esc closes
// the page's inspector AND drops the reviewer out of review mode.
func TestBrowser_ReviewKeysWorkOnAuthoredPlan(t *testing.T) {
	if testing.Short() {
		t.Skip("short: launches a real headless Chrome")
	}
	chromePath := findChromePath()
	if chromePath == "" {
		t.Skip("no Chrome/Chromium binary found — skipping real-browser E2E")
	}

	base, _, _ := serveAuthoredPlanReview(t)

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chromePath))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	t.Cleanup(cancelAlloc)
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	t.Cleanup(cancelCtx)
	ctx, cancelTimeout := context.WithTimeout(ctx, 45*time.Second)
	t.Cleanup(cancelTimeout)

	const inMode = `document.body.classList.contains('rev-on')`
	const inspectorOpen = `!document.getElementById('inspector').hidden`
	const pageEsc = `+(document.body.dataset.pageEsc||0)`
	var authored, onAfterKey, noteAfterEsc0, onAfterEsc0, inspectorAfterEsc1, onAfterEsc1, onAfterEsc2 bool
	var labelOn string
	var pageEscAfterEsc0, pageEscAfterEsc2, pageEscAfterEsc3 int

	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
		chromedp.Evaluate(`!document.querySelector('nav.toc') && !!document.querySelector('section#risks')`, &authored),
		chromedp.KeyEvent("r"),
		chromedp.Evaluate(inMode, &onAfterKey),
		chromedp.Text(".rev-toggle", &labelOn, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("authored-plan key interaction failed: %v", err)
	}
	if !authored {
		t.Fatal("the served page is not the authored one — this test would be proving the scaffold")
	}
	if !onAfterKey || strings.TrimSpace(labelOn) != "Exit review" {
		t.Fatalf("r must enter review mode on an authored plan: on=%v label=%q", onAfterKey, labelOn)
	}

	// Esc #0 closes an open note and is marked handled: the page never sees it.
	// (The authored-page selector targets the paragraph, not the section.)
	if err := chromedp.Run(ctx,
		chromedp.Click("section#risks p", chromedp.ByQuery),
		chromedp.WaitVisible(".rev-pop .rev-note", chromedp.ByQuery),
		chromedp.KeyEvent(kb.Escape),
		chromedp.Evaluate(`!!document.querySelector('.rev-pop')`, &noteAfterEsc0),
		chromedp.Evaluate(inMode, &onAfterEsc0),
		chromedp.Evaluate(pageEsc, &pageEscAfterEsc0),
	); err != nil {
		t.Fatalf("authored-plan note Esc interaction failed: %v", err)
	}
	if noteAfterEsc0 || !onAfterEsc0 {
		t.Fatalf("Esc must close the open note and keep review mode: note=%v on=%v", noteAfterEsc0, onAfterEsc0)
	}
	if pageEscAfterEsc0 != 0 {
		t.Fatalf("the Esc that closed the note must be marked handled, not also reach the page: page saw %d", pageEscAfterEsc0)
	}

	// The reader has the page's own inspector open while reviewing. Esc #1 is the
	// page's: its inspector closes and review mode stays. Esc #2 leaves review
	// mode and is marked handled, so the page does not also act on it. Esc #3 has
	// no review layer left to close, so it reaches the page.
	var ignored any
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`document.getElementById('inspector').hidden=false`, &ignored),
		chromedp.KeyEvent(kb.Escape),
		chromedp.Evaluate(inspectorOpen, &inspectorAfterEsc1),
		chromedp.Evaluate(inMode, &onAfterEsc1),
		chromedp.KeyEvent(kb.Escape),
		chromedp.Evaluate(inMode, &onAfterEsc2),
		chromedp.Evaluate(pageEsc, &pageEscAfterEsc2),
		chromedp.KeyEvent(kb.Escape),
		chromedp.Evaluate(pageEsc, &pageEscAfterEsc3),
	); err != nil {
		t.Fatalf("authored-plan Esc interaction failed: %v", err)
	}
	if inspectorAfterEsc1 || !onAfterEsc1 {
		t.Fatalf("an Esc the page used to close its inspector must not also leave review mode: inspector=%v on=%v", inspectorAfterEsc1, onAfterEsc1)
	}
	if onAfterEsc2 {
		t.Fatal("Esc must leave review mode on an authored plan")
	}
	if pageEscAfterEsc2 != 0 {
		t.Fatalf("the Esc that left review mode must be marked handled, not also reach the page: page saw %d", pageEscAfterEsc2)
	}
	if pageEscAfterEsc3 != 1 {
		t.Fatalf("an Esc review mode has no use for must still reach the page: page saw %d", pageEscAfterEsc3)
	}
}

// Failure prevented: on the CLI's review page a reviewer turns review on and
// clicks a diagram before Mermaid has drawn it, and the mark anchors on the
// diagram's source, lost once the diagram draws.
func TestBrowser_ReviewClicksWaitForDiagrams(t *testing.T) {
	if testing.Short() {
		t.Skip("short: launches a real headless Chrome")
	}
	chromePath := findChromePath()
	if chromePath == "" {
		t.Skip("no Chrome/Chromium binary found — skipping real-browser E2E")
	}
	gitRoot := newPlanStatusTestRepo(t)
	html := []byte(`<!doctype html><html><head><meta charset="utf-8"><title>Late Diagram</title>` +
		`<meta name="ox-plan-slug" content="late-diagram"></head><body><h1>Late Diagram</h1>` +
		`<section data-ox-section="Flow"><pre class="mermaid">graph TD; A--&gt;B</pre></section>` +
		`<script>setTimeout(function(){document.querySelector('.mermaid').innerHTML='<svg><g><text>A</text><text>B</text></g></svg>'},2000)</script>` +
		`</body></html>`)
	dir := savePlanArtifacts(gitRoot, plan.Input{Raw: "# Late Diagram\n"}, plan.Result{}, html, plan.PrimaryHTML)
	if dir == "" {
		t.Fatal("savePlanArtifacts returned empty dir — the authored plan was not saved")
	}
	base, _, _ := serveSavedPlanReview(t, gitRoot, "late-diagram", dir)

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chromePath), chromedp.WSURLReadTimeout(time.Minute))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	t.Cleanup(cancelAlloc)
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	t.Cleanup(cancelCtx)
	ctx, cancelTimeout := context.WithTimeout(ctx, 2*time.Minute)
	t.Cleanup(cancelTimeout)

	const click = `(()=>{document.querySelector('.mermaid').dispatchEvent(new MouseEvent('click',{bubbles:true,cancelable:true}));return !!document.querySelector('.rev-pop')})()`
	var drawn, earlyNote, lateNote bool
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
		chromedp.KeyEvent("r"),
		chromedp.Evaluate(`!!document.querySelector('.mermaid > svg')`, &drawn),
		chromedp.Evaluate(click, &earlyNote),
	); err != nil {
		t.Fatalf("early click failed: %v", err)
	}
	if drawn {
		t.Fatal("the diagram drew before the early click — this test would be proving nothing")
	}
	if earlyNote {
		t.Fatal("a click before the diagram drew opened a note: its mark would anchor on the diagram's source")
	}
	// the diagram draws at 2 s, well before the 5 s fallback, so the click
	// below proves the draw lifted the wait, not the fallback
	if err := chromedp.Run(ctx, chromedp.WaitReady(".mermaid > svg", chromedp.ByQuery)); err != nil {
		t.Fatalf("the diagram never drew: %v", err)
	}
	if err := chromedp.Run(ctx, chromedp.Poll(click, &lateNote, chromedp.WithPollingTimeout(2*time.Second))); err != nil || !lateNote {
		t.Fatalf("a click after the diagram drew never opened a note: note=%v err=%v", lateNote, err)
	}
}

// TestBrowser_CommentsRailMinimizes proves a reviewer can get the comments rail
// off the plan it floats over and bring it back: Hide shrinks it to a Show
// button that still carries the count and uncovers the page beneath; a live
// reload keeps it hidden; Show brings the comments back; and in a narrow
// window, where the rail ends the page, the Show button stays clear of the
// review bar. It runs on both page kinds because each carries its own copy of
// the rail styles.
// Failure prevented: the rail covers plan text with no way to read what is
// behind it, or every agent fix pops it back open over the text.
func TestBrowser_CommentsRailMinimizes(t *testing.T) {
	if testing.Short() {
		t.Skip("short: launches a real headless Chrome")
	}
	chromePath := findChromePath()
	if chromePath == "" {
		t.Skip("no Chrome/Chromium binary found — skipping real-browser E2E")
	}

	for _, tc := range []struct {
		name   string
		serve  func(*testing.T) (string, string, *broadcaster)
		target string // an element review mode marks up on this page kind
	}{
		{"markdown plan", serveLivePlanReview, "section#sec-1"},
		{"authored plan", serveAuthoredPlanReview, "section#risks p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, _, _ := tc.serve(t)

			// Above 1400px the rail is the fixed panel floating over the page.
			allocOpts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chromePath), chromedp.WindowSize(1600, 1000))
			allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocOpts...)
			t.Cleanup(cancelAlloc)
			ctx, cancelCtx := chromedp.NewContext(allocCtx)
			t.Cleanup(cancelCtx)
			ctx, cancelTimeout := context.WithTimeout(ctx, 45*time.Second)
			t.Cleanup(cancelTimeout)

			var seeded, coveredOpen, coveredMin, shrunk, coveredAfterReload, coveredReopened bool
			var count string
			var row struct{ X, Y float64 }

			if err := chromedp.Run(ctx,
				chromedp.Navigate(base+"/"),
				chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
				chromedp.Evaluate(`(function(){try{localStorage.setItem('ox-plan-reviewer','Riley');localStorage.setItem('ox-plan-rev-seen','1');return true;}catch(e){return false;}})()`, &seeded),
				chromedp.Reload(),
				chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
				chromedp.Click(".rev-toggle", chromedp.ByQuery),
				chromedp.Click(tc.target, chromedp.ByQuery),
				chromedp.WaitVisible(".rev-pop .rev-save", chromedp.ByQuery),
				chromedp.Click(".rev-pop .rev-save", chromedp.ByQuery),
				chromedp.WaitVisible(".rev-rail-item", chromedp.ByQuery),
				chromedp.Evaluate(`(function(){var r=document.querySelector('.rev-rail-item').getBoundingClientRect();return {x:r.left+r.width/2,y:r.top+r.height/2};})()`, &row),
			); err != nil {
				t.Fatalf("leaving a comment failed: %v", err)
			}
			if !seeded {
				t.Fatal("could not seed reviewer identity in the browser")
			}

			// Whether the rail, rather than the page, is what sits at the spot
			// its first comment occupied while expanded.
			covered := fmt.Sprintf(`(function(){var e=document.elementFromPoint(%f,%f);return !!(e&&e.closest('.rev-rail'));})()`, row.X, row.Y)

			if err := chromedp.Run(ctx,
				chromedp.Evaluate(covered, &coveredOpen),
				chromedp.Click(".rev-rail-hide", chromedp.ByQuery),
				chromedp.Evaluate(covered, &coveredMin),
				chromedp.Evaluate(`(function(){var r=document.querySelector('.rev-rail').getBoundingClientRect(),s=document.querySelector('.rev-rail-show');return !!s&&r.width<=s.getBoundingClientRect().width+2;})()`, &shrunk),
				chromedp.Evaluate(`(document.querySelector('.rev-rail-show')||{}).textContent||''`, &count),
			); err != nil {
				t.Fatalf("hiding the rail failed: %v", err)
			}
			if !coveredOpen {
				t.Fatal("the open rail does not cover its own first comment — the Hide check below would be vacuous")
			}
			if coveredMin {
				t.Fatal("hiding the rail must uncover the page behind it")
			}
			if !shrunk {
				t.Fatal("hidden, the rail must shrink to its Show button")
			}
			if strings.TrimSpace(count) != "1" {
				t.Fatalf("the Show button must still say how many comments there are, got %q", count)
			}

			// The live reload the agent's fixes trigger.
			if err := chromedp.Run(ctx,
				chromedp.Reload(),
				chromedp.WaitVisible(".rev-rail", chromedp.ByQuery),
				chromedp.Evaluate(covered, &coveredAfterReload),
			); err != nil {
				t.Fatalf("reload failed: %v", err)
			}
			if coveredAfterReload {
				t.Fatal("a live reload must keep the rail hidden")
			}

			if err := chromedp.Run(ctx,
				chromedp.Click(".rev-rail-show", chromedp.ByQuery),
				chromedp.Evaluate(covered, &coveredReopened),
			); err != nil {
				t.Fatalf("showing the rail failed: %v", err)
			}
			if !coveredReopened {
				t.Fatal("Show must bring the comments back")
			}

			// By keyboard: Enter on Hide leaves focus on Show, so a second Enter
			// brings the comments straight back.
			var focusOnShow, coveredByKeyboard bool
			if err := chromedp.Run(ctx,
				chromedp.Focus(".rev-rail-hide", chromedp.ByQuery),
				chromedp.KeyEvent(kb.Enter),
				chromedp.Evaluate(`document.activeElement===document.querySelector('.rev-rail-show')`, &focusOnShow),
				chromedp.KeyEvent(kb.Enter),
				chromedp.Evaluate(covered, &coveredByKeyboard),
			); err != nil {
				t.Fatalf("keyboard toggle failed: %v", err)
			}
			if !focusOnShow {
				t.Fatal("after Hide, keyboard focus must land on the Show button that replaced it")
			}
			if !coveredByKeyboard {
				t.Fatal("Enter on Show must bring the comments back")
			}

			// Below 1400px the rail ends the page instead of floating. Hidden
			// there and scrolled to the end, its Show button must sit above the
			// fixed review bar. 'instant' overrides the scaffold's smooth scrolling.
			var countClear bool
			var ignored any
			if err := chromedp.Run(ctx,
				chromedp.Click(".rev-rail-hide", chromedp.ByQuery),
				chromedp.EmulateViewport(1100, 800),
				chromedp.Evaluate(`window.scrollTo({top:document.documentElement.scrollHeight,behavior:'instant'})`, &ignored),
				chromedp.Evaluate(`(function(){var n=document.querySelector('.rev-rail-show').getBoundingClientRect(),b=document.querySelector('.rev-bar').getBoundingClientRect();return n.top>=0&&n.bottom<=b.top;})()`, &countClear),
			); err != nil {
				t.Fatalf("narrow-window check failed: %v", err)
			}
			if !countClear {
				t.Fatal("in a narrow window the Show button must not be hidden under the review bar")
			}
		})
	}
}

// TestBrowser_HighlightCommentsOnExactWords proves a reviewer can comment on
// exactly the words they highlight and still mark a whole section with a click:
// in real Chrome, a drag-selection opens a note quoting those words — whole
// words, even from a drag that starts mid-word; a double-click selects a word
// even though its first click already opened a note; a word the section
// repeats is refused instead of anchored to the wrong place, counting only
// whole-word repeats; a plain click beside the highlight opens a whole-section
// note; a plain click on the highlight reopens its note; the submitted round
// carries the exact words to the authoring coworker; the agent's resolve
// repaints the highlight as addressed; and once the agent rewrites the words,
// the comment stays in the rail, where Devon can still accept the fix.
// Failure prevented: a comment about one phrase reaches the agent as a comment
// on the whole section, the words it was about never arrive at all, or an
// addressed highlight vanishes before the reviewer can verify it.
func TestBrowser_HighlightCommentsOnExactWords(t *testing.T) {
	if testing.Short() {
		t.Skip("short: launches a real headless Chrome")
	}
	chromePath := findChromePath()
	if chromePath == "" {
		t.Skip("no Chrome/Chromium binary found — skipping real-browser E2E")
	}

	// The preamble becomes the TL;DR, outside every section. In Risks, "retry"
	// appears twice as a word and once inside "Retrying"; "storm" and the phrase
	// appear once, the phrase ending its paragraph.
	gitRoot := newPlanStatusTestRepo(t)
	md := "# Highlight Roundtrip\n\nMake the retry path safe.\n\n## Risks\n\nRetrying is capped at five.\n\n" +
		"A retry storm follows each deploy. The retry path can double-fire under load\n\n## Rollout\n\nShip behind a flag.\n"
	dir, _, err := plan.Save(gitRoot, plan.Input{Raw: md}, plan.Result{}, nil, plan.Meta{
		Topic: "Highlight Roundtrip", Slug: "highlight-roundtrip",
	})
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}
	base, planDir, bc := serveSavedPlanReview(t, gitRoot, "highlight-roundtrip", dir)

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chromePath), chromedp.WindowSize(1600, 1000))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	t.Cleanup(cancelAlloc)
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	t.Cleanup(cancelCtx)
	ctx, cancelTimeout := context.WithTimeout(ctx, 45*time.Second)
	t.Cleanup(cancelTimeout)

	const (
		phrase      = "double-fire under load"
		phraseNote  = "idempotency keys, not retries"
		sectionNote = "rank these risks"
	)
	const toastText = `(function(){var t=document.querySelector('.rev-toast');return t?t.textContent:'';})()`
	const noteOpen = `!!document.querySelector('.rev-pop')`
	// tinted returns the text of every range painted under one highlight name.
	const tinted = `(function(n){var h=CSS.highlights.get(n),out=[];if(h)h.forEach(function(r){out.push(r.toString());});return out.join('|');})(%q)`
	type box struct{ X1, Y1, X2, Y2 float64 }
	midX := func(b box) float64 { return (b.X1 + b.X2) / 2 } // for a word on one line
	var retry, storm, safe, words box
	var seeded, openAfterRepeat, sectionNoteQuoted bool
	var repeatToast, stormQuote, safeQuote, phraseQuote, paintedOpen, unsent, reopenedNote, paintedAddressed string

	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
		chromedp.Evaluate(`(function(){try{localStorage.setItem('ox-plan-reviewer','Devon');localStorage.setItem('ox-plan-rev-seen','1');return true;}catch(e){return false;}})()`, &seeded),
		chromedp.Reload(),
		chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
		chromedp.Click(".rev-toggle", chromedp.ByQuery),
		chromedp.Evaluate(fmt.Sprintf(phraseBox, "section#sec-1", "retry"), &retry),
		chromedp.Evaluate(fmt.Sprintf(phraseBox, "section#sec-1", "storm"), &storm),
		chromedp.Evaluate(fmt.Sprintf(phraseBox, "section#sec-1", phrase), &words),
		chromedp.Evaluate(fmt.Sprintf(phraseBox, ".tldr", "safe"), &safe),
	); err != nil {
		t.Fatalf("entering review mode failed: %v", err)
	}
	if !seeded || words.X1 == 0 {
		t.Fatalf("could not prepare the page: seeded=%v phrase box=%+v", seeded, words)
	}

	// Double-click a word the section repeats: the first click opens a
	// whole-section note, the second selects the word under it — and the word
	// is refused, since the agent could not tell which "retry" was meant.
	if err := chromedp.Run(ctx,
		chromedp.MouseClickXY(midX(retry), retry.Y1),
		chromedp.MouseClickXY(midX(retry), retry.Y1, chromedp.ClickCount(2)),
		chromedp.Evaluate(toastText, &repeatToast),
		chromedp.Evaluate(noteOpen, &openAfterRepeat),
	); err != nil {
		t.Fatalf("double-click on a repeated word failed: %v", err)
	}
	if !strings.Contains(repeatToast, "appears 2 times") || openAfterRepeat {
		t.Fatalf("a repeated word must be refused with no note left open: toast=%q noteOpen=%v", repeatToast, openAfterRepeat)
	}

	// Double-click a word that appears once — in a section, or in the TL;DR
	// outside every section: its note quotes it.
	if err := chromedp.Run(ctx,
		chromedp.MouseClickXY(midX(storm), storm.Y1),
		chromedp.MouseClickXY(midX(storm), storm.Y1, chromedp.ClickCount(2)),
		chromedp.WaitVisible(".rev-pop .rev-quote", chromedp.ByQuery),
		chromedp.Text(".rev-pop .rev-quote", &stormQuote, chromedp.ByQuery),
		chromedp.KeyEvent(kb.Escape),
		chromedp.MouseClickXY(midX(safe), safe.Y1),
		chromedp.MouseClickXY(midX(safe), safe.Y1, chromedp.ClickCount(2)),
		chromedp.WaitVisible(".rev-pop .rev-quote", chromedp.ByQuery),
		chromedp.Text(".rev-pop .rev-quote", &safeQuote, chromedp.ByQuery),
		chromedp.KeyEvent(kb.Escape),
	); err != nil {
		t.Fatalf("double-click on a word failed: %v", err)
	}
	if stormQuote != "storm" || safeQuote != "safe" {
		t.Fatalf("a double-clicked word's note must quote it: section=%q tldr=%q", stormQuote, safeQuote)
	}

	// Drag across the phrase, starting inside its first word: its note quotes
	// the whole phrase, and saving tints it.
	if err := chromedp.Run(ctx,
		chromedp.MouseEvent("mousePressed", words.X1+12, words.Y1, chromedp.ButtonLeft, chromedp.ClickCount(1)),
		chromedp.MouseEvent("mouseMoved", words.X2, words.Y2, chromedp.ButtonLeft),
		chromedp.MouseEvent("mouseReleased", words.X2, words.Y2, chromedp.ButtonLeft, chromedp.ClickCount(1)),
		chromedp.WaitVisible(".rev-pop .rev-quote", chromedp.ByQuery),
		chromedp.Text(".rev-pop .rev-quote", &phraseQuote, chromedp.ByQuery),
		chromedp.SendKeys(".rev-pop .rev-note", phraseNote, chromedp.ByQuery),
		chromedp.Click(".rev-pop .rev-save", chromedp.ByQuery), // default verdict: request-change
		chromedp.Evaluate(fmt.Sprintf(tinted, "rev-q-amber"), &paintedOpen),
	); err != nil {
		t.Fatalf("drag-selecting the phrase failed: %v", err)
	}
	if phraseQuote != phrase {
		t.Fatalf("a drag-selection's note must quote exactly the selected words, got %q", phraseQuote)
	}
	if paintedOpen != phrase {
		t.Fatalf("a saved highlight must tint exactly its words, got %q", paintedOpen)
	}

	// A plain click just past the highlight, where its line ends, still marks
	// the whole section; a plain click on the highlight reopens its own note.
	if err := chromedp.Run(ctx,
		chromedp.MouseClickXY(words.X2+60, words.Y2),
		chromedp.WaitVisible(".rev-pop .rev-save", chromedp.ByQuery),
		chromedp.Evaluate(`!!document.querySelector('.rev-pop .rev-quote')`, &sectionNoteQuoted),
		chromedp.SendKeys(".rev-pop .rev-note", sectionNote, chromedp.ByQuery),
		chromedp.Click(".rev-pop .rev-save", chromedp.ByQuery),
		chromedp.Text(".rev-count", &unsent, chromedp.ByQuery),
		chromedp.MouseClickXY(words.X1+4, words.Y1), // inside the phrase's first word
		chromedp.WaitVisible(".rev-pop .rev-quote", chromedp.ByQuery),
		chromedp.Value(".rev-pop .rev-note", &reopenedNote, chromedp.ByQuery),
		chromedp.KeyEvent(kb.Escape),
		chromedp.Click(".rev-submit", chromedp.ByQuery),
	); err != nil {
		t.Fatalf("section mark / highlight reopen / submit failed: %v", err)
	}
	if sectionNoteQuoted {
		t.Fatal("a plain click on the section must open a whole-section note, not a highlight's")
	}
	if strings.TrimSpace(unsent) != "2 unsent" {
		t.Fatalf("the highlight and the section mark must be two separate marks, counter=%q", unsent)
	}
	if reopenedNote != phraseNote {
		t.Fatalf("a plain click on the highlight must reopen its own note, got %q", reopenedNote)
	}

	// Both marks reach the authoring coworker; the highlight carries its words.
	var quoted, whole planreview.Item
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && quoted.Anchor == "" {
		if sets := plan.ReadReview(planDir).Rounds; len(sets) == 1 && len(sets[0].Items) == 2 {
			for _, it := range sets[0].Items {
				if it.Quote != "" {
					quoted = it
				} else {
					whole = it
				}
			}
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if quoted.Quote != phrase || quoted.Section != "Risks" || quoted.Note != phraseNote || quoted.Reviewer != "Devon" {
		t.Fatalf("the highlight must reach the agent with its exact words, section, and note: %+v", quoted)
	}
	if whole.Anchor == "" || whole.Section != "Risks" || whole.Note != sectionNote {
		t.Fatalf("the whole-section mark must reach the agent beside the highlight: %+v", whole)
	}
	if d := plan.FeedbackDigest(plan.ReadReview(planDir).Items); !strings.Contains(d, "“"+phrase+"”") {
		t.Fatalf("the agent's digest must quote the highlighted words:\n%s", d)
	}

	// The agent addresses the highlight; the open page live-reloads and
	// repaints those words as addressed.
	resolveForTest(t, planDir, quoted.Anchor, planreview.StateAddressed, "switched to idempotency keys")
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(".rev-rail-tag.addressed", chromedp.ByQuery),
		chromedp.Evaluate(fmt.Sprintf(tinted, "rev-q-sage"), &paintedAddressed),
	); err != nil {
		t.Fatalf("reviewer page did not live-reload to show the highlight addressed: %v", err)
	}
	if paintedAddressed != phrase {
		t.Fatalf("an addressed highlight must repaint its words as addressed, got %q", paintedAddressed)
	}

	// Addressing it, the agent rewrote those words. Nothing is left to paint,
	// but the rail keeps the comment, and Devon accepts the fix from its row.
	// The re-save's files can land in more than one burst, each pushing a live
	// reload, so the click waits for the pushes to go quiet.
	sub := bc.subscribe()
	defer bc.unsubscribe(sub)
	var ignored any
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.oldPage = true`, &ignored)); err != nil {
		t.Fatalf("flag page: %v", err)
	}
	if _, _, err := plan.Save(gitRoot, plan.Input{Raw: strings.Replace(md, phrase, "fire twice", 1)}, plan.Result{}, nil, plan.Meta{
		Topic: "Highlight Roundtrip", Slug: "highlight-roundtrip",
	}); err != nil {
		t.Fatalf("agent re-save: %v", err)
	}
	select {
	case <-sub:
	case <-time.After(10 * time.Second):
		t.Fatal("the agent's re-save never pushed a live reload")
	}
	drainUntilQuiet(sub, 600*time.Millisecond)
	const goneRow = `(function(){var li=[].filter.call(document.querySelectorAll('.rev-rail-item'),function(l){return l.textContent.indexOf('text changed')>=0;})[0];` +
		`if(!li)return null;var r=li.getBoundingClientRect();return {x1:r.left+r.width/2,y1:r.top+r.height/2};})()`
	if !waitForNewPage(ctx, `!!(`+goneRow+`)`, 20*time.Second) {
		t.Fatal("a highlight whose words were rewritten must keep a rail row")
	}
	var row box
	var acceptQuote string
	if err := chromedp.Run(ctx, chromedp.Evaluate(goneRow, &row)); err != nil {
		t.Fatalf("locate the rail row: %v", err)
	}
	// a separate Run: the click's coordinates are read when the action is built
	if err := chromedp.Run(ctx,
		chromedp.MouseClickXY(row.X1, row.Y1),
		chromedp.WaitVisible(".rev-pop .rev-accept", chromedp.ByQuery),
		chromedp.Text(".rev-pop .rev-quote", &acceptQuote, chromedp.ByQuery),
		chromedp.Evaluate(`window.oldPage = true`, &ignored),
		chromedp.Click(".rev-pop .rev-accept", chromedp.ByQuery),
	); err != nil {
		t.Fatalf("accepting the rewritten highlight from its rail row failed: %v", err)
	}
	if acceptQuote != phrase {
		t.Fatalf("the row must open the highlight's own note, quoting its words, got %q", acceptQuote)
	}
	verified := false
	for deadline := time.Now().Add(10 * time.Second); !verified && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		for _, it := range plan.ReadReview(planDir).Items {
			verified = verified || (it.Anchor == quoted.Anchor && it.Resolution != nil && it.Resolution.State == planreview.StateVerified)
		}
	}
	if !verified {
		t.Fatal("accepting from the rail must record the highlight verified")
	}

	// Outside review mode the rail still opens that comment, and a click
	// elsewhere on the page closes it again.
	select {
	case <-sub:
	case <-time.After(10 * time.Second):
		t.Fatal("the accept never pushed a live reload")
	}
	drainUntilQuiet(sub, 600*time.Millisecond)
	if !waitForNewPage(ctx, `!!(`+goneRow+`)`, 20*time.Second) {
		t.Fatal("the accepted highlight must keep its rail row")
	}
	var closed bool
	if err := chromedp.Run(ctx,
		chromedp.KeyEvent(kb.Escape), // the reload restored review mode; leave it
		chromedp.Evaluate(goneRow, &row),
	); err != nil {
		t.Fatalf("leaving review mode failed: %v", err)
	}
	if err := chromedp.Run(ctx,
		chromedp.MouseClickXY(row.X1, row.Y1),
		chromedp.WaitVisible(".rev-pop .rev-accept", chromedp.ByQuery),
		chromedp.Click("main > h1", chromedp.ByQuery),
		chromedp.Evaluate(`!document.body.classList.contains('rev-on') && !document.querySelector('.rev-pop')`, &closed),
	); err != nil {
		t.Fatalf("opening the comment outside review mode failed: %v", err)
	}
	if !closed {
		t.Fatal("outside review mode, a click elsewhere must close the note a rail row opened")
	}
}

// TestBrowser_UnsentHighlightSurvivesServerRestart proves a highlight Quinn
// saved but has not submitted outlives a real outage of the review server: it
// stays painted while the server is down; a reload during the outage still shows
// the plan, from the copy the service worker saved at install, with the
// highlight repainted from browser storage; and when the server restarts on the
// plan's same address the tab reconnects by itself, reloads, and submits the
// highlight with its words and note. The plan page loads only once before the
// outage, so the install copy is the only copy the worker has.
// TestBrowser_UnsentMarkSurvivesReconnect covers a whole-element mark across a
// simulated reconnect; this one stops and restarts the server.
// Failure prevented: a highlighted-but-unsent comment silently disappears when
// `ox plan review` exits and is started again, or a reload during the outage
// shows no plan at all.
func TestBrowser_UnsentHighlightSurvivesServerRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("short: launches a real headless Chrome")
	}
	chromePath := findChromePath()
	if chromePath == "" {
		t.Skip("no Chrome/Chromium binary found — skipping real-browser E2E")
	}

	gitRoot := newPlanStatusTestRepo(t)
	md := "# Highlight Restart\n\nKeep retries safe.\n\n## Risks\n\nThe retry path can double-fire under load.\n\n## Rollout\n\nShip behind a flag.\n"
	dir, _, err := plan.Save(gitRoot, plan.Input{Raw: md}, plan.Result{}, nil, plan.Meta{
		Topic: "Highlight Restart", Slug: "highlight-restart",
	})
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}
	// The restarted server binds the same address with the same token, as
	// `ox plan review` does from its persisted state — so the open tab keeps its
	// origin, and with it the localStorage holding the unsent highlight.
	ln := mustLoopbackListener(t)
	addr := ln.Addr().String()
	serve := func(l net.Listener) *http.Server {
		srv := &http.Server{Handler: liveReviewHandler(gitRoot, "highlight-restart", dir, "http://"+addr, "secret",
			newBroadcaster(), make(chan int, 8), make(chan struct{}, 1))}
		go func() { _ = srv.Serve(l) }()
		return srv
	}
	first := serve(ln)

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chromePath), chromedp.WindowSize(1600, 1000))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	t.Cleanup(cancelAlloc)
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	t.Cleanup(cancelCtx)
	ctx, cancelTimeout := context.WithTimeout(ctx, 45*time.Second)
	t.Cleanup(cancelTimeout)

	const (
		phrase = "double-fire under load"
		note   = "survive the outage"
	)
	const painted = `(function(){var h=CSS.highlights.get('rev-q-amber'),out=[];if(h)h.forEach(function(r){out.push(r.toString());});return out.join('|');})()`
	var words struct{ X1, Y1, X2, Y2 float64 }
	var seeded bool
	var paintedDown, paintedReloaded, paintedBack, unsent, railQuote, railNote string

	// Seed the reviewer from /healthz — same origin, not the plan page — so the
	// plan page loads exactly once before the outage.
	if err := chromedp.Run(ctx,
		chromedp.Navigate("http://"+addr+"/healthz"),
		chromedp.Evaluate(`(function(){try{localStorage.setItem('ox-plan-reviewer','Quinn');localStorage.setItem('ox-plan-rev-seen','1');return true;}catch(e){return false;}})()`, &seeded),
		chromedp.Navigate("http://"+addr+"/"),
		chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
		chromedp.Click(".rev-toggle", chromedp.ByQuery),
		chromedp.Evaluate(fmt.Sprintf(phraseBox, "section#sec-1", phrase), &words),
	); err != nil {
		t.Fatalf("entering review mode failed: %v", err)
	}
	if !seeded || words.X1 == 0 {
		t.Fatalf("could not prepare the page: seeded=%v phrase box=%+v", seeded, words)
	}
	// Highlight the phrase and save the note — unsent, so it lives only in the browser.
	if err := chromedp.Run(ctx,
		chromedp.MouseEvent("mousePressed", words.X1, words.Y1, chromedp.ButtonLeft, chromedp.ClickCount(1)),
		chromedp.MouseEvent("mouseMoved", words.X2, words.Y2, chromedp.ButtonLeft),
		chromedp.MouseEvent("mouseReleased", words.X2, words.Y2, chromedp.ButtonLeft, chromedp.ClickCount(1)),
		chromedp.WaitVisible(".rev-pop .rev-quote", chromedp.ByQuery),
		chromedp.SendKeys(".rev-pop .rev-note", note, chromedp.ByQuery),
		chromedp.Click(".rev-pop .rev-save", chromedp.ByQuery),
		// a controlling worker has finished installing, so its copy is saved
		chromedp.Poll(`!!navigator.serviceWorker.controller`, nil),
	); err != nil {
		t.Fatalf("highlighting the phrase failed: %v", err)
	}

	// The server stops: Close also drops the live-reload stream, so the page
	// sees a real disconnect and says it is offline. The highlight stays, and a
	// reload during the outage still shows the plan, highlight included.
	_ = first.Close()
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(".rev-offline-bar", chromedp.ByQuery),
		chromedp.Evaluate(painted, &paintedDown),
		chromedp.Evaluate(`window.oldPage = true`, nil),
		chromedp.Reload(),
	); err != nil {
		t.Fatalf("the page did not show the outage: %v", err)
	}
	if !waitForNewPage(ctx, `!!document.querySelector('.rev-offline-bar')`, 20*time.Second) {
		t.Fatal("a reload while the server is down must still show the plan, marked offline")
	}
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(painted, &paintedReloaded),
		chromedp.Evaluate(`window.oldPage = true`, nil),
	); err != nil {
		t.Fatalf("reading the offline page failed: %v", err)
	}
	if paintedDown != phrase || paintedReloaded != phrase {
		t.Fatalf("an unsent highlight must stay painted while the server is down: before reload=%q after=%q", paintedDown, paintedReloaded)
	}

	// The server restarts on the same address; the tab must reconnect by
	// itself: a new page load that is live again.
	ln2, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("rebind %s: %v", addr, err)
	}
	second := serve(ln2)
	t.Cleanup(func() { _ = second.Close() })
	if !waitForNewPage(ctx, `!!document.querySelector('.rev-conn.ok')`, 20*time.Second) {
		t.Fatal("the open tab never reconnected to the restarted server")
	}
	// read without waiting: a missing highlight must fail on the claim below,
	// not stall until the context deadline
	const textOf = `(function(s){var e=document.querySelector(s);return e?e.textContent:'';})(%q)`
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(painted, &paintedBack),
		chromedp.Evaluate(fmt.Sprintf(textOf, ".rev-count"), &unsent),
		chromedp.Evaluate(fmt.Sprintf(textOf, ".rev-rail-quote"), &railQuote),
		chromedp.Evaluate(fmt.Sprintf(textOf, ".rev-rail-note"), &railNote),
	); err != nil {
		t.Fatalf("reading the reconnected page failed: %v", err)
	}
	if paintedBack != phrase || strings.TrimSpace(unsent) != "1 unsent" || railQuote != phrase || railNote != note {
		t.Fatalf("after the restart the unsent highlight must be back with its note: painted=%q counter=%q rail=%q/%q",
			paintedBack, unsent, railQuote, railNote)
	}
	if err := chromedp.Run(ctx, chromedp.Click(".rev-submit", chromedp.ByQuery)); err != nil {
		t.Fatalf("submit after the restart failed: %v", err)
	}
	it := waitForOneRound(t, dir, 15*time.Second)
	if it.Quote != phrase || it.Note != note || it.Reviewer != "Quinn" {
		t.Fatalf("the highlight submitted after the restart lost its words or note: %+v", it)
	}
}

// TestBrowser_LeftoverSelectionDoesNotHijackAClick proves a click marks what
// it lands on even while an old selection is still on the page: on an authored
// plan, Quinn double-clicks a word (its note opens) and presses Esc — the word
// stays selected — then clicks the figure's image, which leaves the selection
// in place, and gets the figure's note, not one on the old word.
// Failure prevented: a click on anything that doesn't clear the page selection
// — an image, a button, unselectable text — opens a highlight on words the
// reviewer selected earlier instead of marking what they clicked.
func TestBrowser_LeftoverSelectionDoesNotHijackAClick(t *testing.T) {
	if testing.Short() {
		t.Skip("short: launches a real headless Chrome")
	}
	chromePath := findChromePath()
	if chromePath == "" {
		t.Skip("no Chrome/Chromium binary found — skipping real-browser E2E")
	}

	base, _, _ := serveAuthoredPlanReview(t)
	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chromePath), chromedp.WindowSize(1600, 1000))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	t.Cleanup(cancelAlloc)
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	t.Cleanup(cancelCtx)
	ctx, cancelTimeout := context.WithTimeout(ctx, 45*time.Second)
	t.Cleanup(cancelTimeout)

	const selected = `String(window.getSelection()).trim()`
	var word, img struct{ X1, Y1, X2, Y2 float64 }
	var seeded, quoted bool
	var before, after string
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
		chromedp.Evaluate(`(function(){try{localStorage.setItem('ox-plan-reviewer','Quinn');localStorage.setItem('ox-plan-rev-seen','1');return true;}catch(e){return false;}})()`, &seeded),
		chromedp.Reload(),
		chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
		chromedp.Click(".rev-toggle", chromedp.ByQuery),
		chromedp.Evaluate(fmt.Sprintf(phraseBox, "section#risks p", "retry"), &word),
		chromedp.Evaluate(`(function(){var r=document.querySelector('section#risks figure img').getBoundingClientRect();return {x1:r.left+r.width/2,y1:r.top+r.height/2};})()`, &img),
	); err != nil {
		t.Fatalf("entering review mode failed: %v", err)
	}
	if !seeded || word.X1 == 0 || img.X1 == 0 {
		t.Fatalf("could not prepare the page: seeded=%v word=%+v img=%+v", seeded, word, img)
	}
	if err := chromedp.Run(ctx,
		chromedp.MouseClickXY((word.X1+word.X2)/2, word.Y1),
		chromedp.MouseClickXY((word.X1+word.X2)/2, word.Y1, chromedp.ClickCount(2)),
		chromedp.WaitVisible(".rev-pop .rev-quote", chromedp.ByQuery),
		chromedp.KeyEvent(kb.Escape),
		chromedp.Evaluate(selected, &before),
		chromedp.MouseClickXY(img.X1, img.Y1),
		chromedp.WaitVisible(".rev-pop .rev-save", chromedp.ByQuery),
		chromedp.Evaluate(`!!document.querySelector('.rev-pop .rev-quote')`, &quoted),
		chromedp.Evaluate(selected, &after),
	); err != nil {
		t.Fatalf("clicking the image with a word still selected failed: %v", err)
	}
	// the premise, asserted: the word stayed selected through the click
	if before != "retry" || after != "retry" {
		t.Fatalf("the word must stay selected through the image click, or this proves nothing: before=%q after=%q", before, after)
	}
	if quoted {
		t.Fatal("a click on the image must open the figure's note, not a highlight on the old selection")
	}
}

// TestBrowser_PlanControlsWorkInReviewMode: with review mode on, Devon picks
// a paragraph, then switches the plan's tab, ticks its checkbox and opens its
// notes. Each does what it does outside review mode and closes the composer
// instead of picking; a link inside a summary is still picked.
// Failure prevented: a click on a tab opens the composer on its section
// instead, so the reviewer must leave review mode to reach another tab's
// content; or a link inside a summary is followed, away from the review.
func TestBrowser_PlanControlsWorkInReviewMode(t *testing.T) {
	ctx := frameBrowser(t)
	gitRoot := newPlanStatusTestRepo(t)
	html := `<!doctype html><html><head><meta charset="utf-8"><title>Controls</title><meta name="ox-plan-slug" content="controls">` +
		`<style>.panel{display:none}#tab-a:checked~#panel-a,#tab-b:checked~#panel-b{display:block}</style></head><body><h1>Controls</h1>` +
		`<section data-ox-section="Rollout"><h2>Rollout</h2>` +
		`<input type="radio" name="tab" id="tab-a" checked><label for="tab-a">Canary</label>` +
		`<input type="radio" name="tab" id="tab-b"><label for="tab-b">Full</label>` +
		`<div class="panel" id="panel-a"><p>Canary first.</p></div><div class="panel" id="panel-b"><p>Everyone at once.</p></div>` +
		`<p><input type="checkbox" id="box"></p>` +
		`<details><summary>Runbook: <a id="link" href="https://example.com/">open it</a></summary><p>Step one.</p></details>` +
		`<details id="notes"><summary>Implementation notes</summary><p>Flag name: rollout-v2.</p></details>` +
		`<p id="para">Ship behind a flag.</p></section></body></html>`
	dir := savePlanArtifacts(gitRoot, plan.Input{Raw: "# Controls\n"}, plan.Result{}, []byte(html), plan.PrimaryHTML)
	base, _, _ := serveSavedPlanReview(t, gitRoot, "controls", dir)
	openReviewer(t, ctx, base)
	if err := chromedp.Run(ctx, chromedp.Click(".rev-toggle", chromedp.ByQuery),
		chromedp.Click("#para", chromedp.ByQuery), chromedp.WaitVisible(".rev-pop", chromedp.ByQuery),
		chromedp.Click(`label[for="tab-b"]`, chromedp.ByQuery), chromedp.Click("#box", chromedp.ByQuery), chromedp.Click("#notes summary", chromedp.ByQuery)); err != nil {
		t.Fatalf("click the plan's controls in review mode: %v", err)
	}
	if got := evalString(t, ctx, `[document.getElementById('tab-b').checked,document.getElementById('box').checked,document.getElementById('notes').open,!!document.querySelector('.rev-pop')].join()`); got != "true,true,true,false" {
		t.Errorf("in review mode the plan's controls must do their job and pick nothing (tab,box,notes,composer): %s", got)
	}
	// a followed link would leave the page: poll briefly rather than wait out the context
	if err := chromedp.Run(ctx, chromedp.Click("#link", chromedp.ByQuery)); err != nil {
		t.Fatalf("click the link: %v", err)
	}
	if !waitJS(ctx, `document.querySelector('.rev-pop')`, 5*time.Second) {
		t.Error("a link inside a summary must be picked, not followed")
	}
}

// TestBrowser_HighlightStopsAtElementEdges proves a highlight takes only the
// words the reviewer picked when the markup runs elements together: on an
// authored plan whose table cells, and the lines around a <br>, sit side by
// side with no whitespace between them, Quinn double-clicks a word next to each
// edge, and each note quotes that word alone; saving the cell's tints it.
// Failure prevented: the note, and the agent, get the word glued to the text
// across the edge — words that appear nowhere on the page.
func TestBrowser_HighlightStopsAtElementEdges(t *testing.T) {
	if testing.Short() {
		t.Skip("short: launches a real headless Chrome")
	}
	chromePath := findChromePath()
	if chromePath == "" {
		t.Skip("no Chrome/Chromium binary found — skipping real-browser E2E")
	}

	base, _, _ := serveAuthoredPlanReview(t)
	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chromePath), chromedp.WindowSize(1600, 1000))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	t.Cleanup(cancelAlloc)
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	t.Cleanup(cancelCtx)
	ctx, cancelTimeout := context.WithTimeout(ctx, 45*time.Second)
	t.Cleanup(cancelTimeout)

	var cell struct{ X1, Y1, X2, Y2 float64 }
	var seeded bool
	var quote, toast, painted string
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
		chromedp.Evaluate(`(function(){try{localStorage.setItem('ox-plan-reviewer','Quinn');localStorage.setItem('ox-plan-rev-seen','1');return true;}catch(e){return false;}})()`, &seeded),
		chromedp.Reload(),
		chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
		chromedp.Click(".rev-toggle", chromedp.ByQuery),
		chromedp.Evaluate(fmt.Sprintf(phraseBox, "section#risks table", "three"), &cell),
	); err != nil {
		t.Fatalf("entering review mode failed: %v", err)
	}
	if !seeded || cell.X1 == 0 {
		t.Fatalf("could not prepare the page: seeded=%v cell=%+v", seeded, cell)
	}
	// read, not wait: a glued-on word can also be refused, opening no note
	if err := chromedp.Run(ctx,
		chromedp.MouseClickXY((cell.X1+cell.X2)/2, cell.Y1),
		chromedp.MouseClickXY((cell.X1+cell.X2)/2, cell.Y1, chromedp.ClickCount(2)),
		chromedp.Evaluate(`(function(){var q=document.querySelector('.rev-pop .rev-quote');return q?q.textContent:'';})()`, &quote),
		chromedp.Evaluate(`(function(){var t=document.querySelector('.rev-toast');return t?t.textContent:'';})()`, &toast),
	); err != nil {
		t.Fatalf("double-clicking the cell's word failed: %v", err)
	}
	if quote != "three" {
		t.Fatalf("the note must quote the cell's word alone, got %q (toast %q)", quote, toast)
	}
	if err := chromedp.Run(ctx,
		chromedp.Click(".rev-pop .rev-save", chromedp.ByQuery),
		chromedp.Evaluate(`(function(){var h=CSS.highlights.get('rev-q-amber'),out=[];if(h)h.forEach(function(r){out.push(r.toString());});return out.join('|');})()`, &painted),
	); err != nil {
		t.Fatalf("saving the highlight failed: %v", err)
	}
	if painted != "three" {
		t.Fatalf("saving must tint the cell's word alone, got %q", painted)
	}

	// a <br> is an edge too: it sits beside the text, not around it
	var line struct{ X1, Y1, X2, Y2 float64 }
	if err := chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(phraseBox, "section#risks", "five"), &line)); err != nil || line.X1 == 0 {
		t.Fatalf("locate the word after the <br>: %v %+v", err, line)
	}
	if err := chromedp.Run(ctx,
		chromedp.MouseClickXY((line.X1+line.X2)/2, line.Y1),
		chromedp.MouseClickXY((line.X1+line.X2)/2, line.Y1, chromedp.ClickCount(2)),
		chromedp.Evaluate(`(function(){var q=document.querySelector('.rev-pop .rev-quote');return q?q.textContent:'';})()`, &quote),
		chromedp.Evaluate(`(function(){var t=document.querySelector('.rev-toast');return t?t.textContent:'';})()`, &toast),
	); err != nil {
		t.Fatalf("double-clicking the word after the <br> failed: %v", err)
	}
	if quote != "five" {
		t.Fatalf("the note must quote the word after the <br> alone, got %q (toast %q)", quote, toast)
	}
}

// phraseBox (a fmt template taking a selector and a phrase) finds the phrase's
// first occurrence inside the element and returns viewport points just inside
// its first and last characters — each on its own line, should the phrase wrap.
const phraseBox = `(function(sel,p){var w=document.createTreeWalker(document.querySelector(sel),NodeFilter.SHOW_TEXT),n;` +
	`while((n=w.nextNode())){var i=n.data.indexOf(p);if(i<0)continue;var r=document.createRange();` +
	`r.setStart(n,i);r.setEnd(n,i+1);var a=r.getBoundingClientRect();` +
	`r.setStart(n,i+p.length-1);r.setEnd(n,i+p.length);var b=r.getBoundingClientRect();` +
	`return {x1:a.left+1,y1:(a.top+a.bottom)/2,x2:b.right-1,y2:(b.top+b.bottom)/2};}return null;})(%q,%q)`

// waitForNewPage polls until a page load after the one flagged with
// window.oldPage satisfies cond. Neither kind of reload can be awaited as one
// chromedp step: chromedp.Reload returns before the new page has run review.js,
// and a live reload lands whenever the server pushes one.
func waitForNewPage(ctx context.Context, cond string, within time.Duration) bool {
	var ok bool
	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		// an error just means the page is mid-load
		if chromedp.Run(ctx, chromedp.Evaluate(`!window.oldPage && (`+cond+`)`, &ok)) == nil && ok {
			return true
		}
	}
	return false
}

// waitForOneRound polls the plan dir until exactly one review round with one item
// has landed, then returns that item. Fails if nothing arrives — a submit that
// never reached the ledger is the failure this guards.
func waitForOneRound(t *testing.T, planDir string, within time.Duration) planreview.Item {
	t.Helper()
	deadline := time.Now().Add(within)
	var last plan.Review
	for time.Now().Before(deadline) {
		if last = plan.ReadReview(planDir); len(last.Rounds) == 1 && len(last.Rounds[0].Items) == 1 {
			return last.Rounds[0].Items[0]
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("browser Submit never reached the ledger (last read failures: %+v)", last.Failures)
	return planreview.Item{}
}

// TestBrowser_ReopenedHighlightKeepsItsWords proves a reopen names what it
// reopens: Avery addressed Devon's highlight, Devon reopens it on the page
// with a note, and the item that returns to Avery quotes the highlighted
// words, in their section, under Devon's name. Failure prevented: the reopened
// highlight reaches the agent as a bare anchor, which nothing can turn back
// into the words it was about.
func TestBrowser_ReopenedHighlightKeepsItsWords(t *testing.T) {
	ctx := frameBrowser(t)
	gitRoot := newPlanStatusTestRepo(t)
	md := "# Reopen\n\nShip it.\n\n## Risks\n\nThe retry path can double-fire under load.\n\n## Rollout\n\nShip behind a flag.\n"
	dir, _, err := plan.Save(gitRoot, plan.Input{Raw: md}, plan.Result{}, nil, plan.Meta{Topic: "Reopen", Slug: "reopen"})
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}
	const phrase = "double-fire under load"
	anchor := "q" + strings.TrimPrefix(planreview.AnchorFor("Risks", phrase), "h")
	roundForTest(t, dir, time.Now(), planreview.Item{Anchor: anchor, Section: "Risks", Label: phrase, Quote: phrase, Status: planreview.StatusRequestChange, Note: "why twice?"})
	resolveForTest(t, dir, anchor, planreview.StateAddressed, "done")
	base, planDir, _ := serveSavedPlanReview(t, gitRoot, "reopen", dir)
	openReviewer(t, ctx, base)

	var words struct{ X1, Y1, X2, Y2 float64 }
	if err := chromedp.Run(ctx,
		chromedp.Click(".rev-toggle", chromedp.ByQuery),
		chromedp.Evaluate(fmt.Sprintf(phraseBox, "section#sec-1", phrase), &words),
	); err != nil {
		t.Fatalf("enter review mode: %v", err)
	}
	if err := chromedp.Run(ctx,
		chromedp.MouseClickXY((words.X1+words.X2)/2, words.Y1),
		chromedp.WaitVisible(".rev-pop .rev-reopen", chromedp.ByQuery),
		chromedp.SendKeys(".rev-pop .rev-note", "still fires twice", chromedp.ByQuery),
		chromedp.Click(".rev-pop .rev-reopen", chromedp.ByQuery),
	); err != nil {
		t.Fatalf("reopen the addressed highlight: %v", err)
	}

	var it plan.MergedItem
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && it.Anchor == ""; time.Sleep(100 * time.Millisecond) {
		if res, done := awaitSnapshot(planDir); done && len(res.Open) == 1 {
			it = res.Open[0]
		}
	}
	if it.Quote != phrase || it.Section != "Risks" || it.Note != "still fires twice" || it.Reviewer != "Devon" {
		t.Fatalf("the reopened highlight must return to Avery with its words, section and Devon's note: %+v", it)
	}
	if d := plan.FeedbackDigest(plan.ReadReview(planDir).Items); !strings.Contains(d, "“"+phrase+"”") {
		t.Fatalf("the agent's digest must quote the reopened highlight's words:\n%s", d)
	}
}

// TestBrowser_WholePlanCommentReachesTheLedger: Devon comments on the plan as
// a whole from the rail and submits it. It reaches the Ledger as his mark on
// the whole plan, and the page that reloads lists it once, first in the rail,
// with no "text changed" tag and no banner of notes no longer anchored. Once
// the author addresses it, the button still starts a new comment.
// Failure prevented: a comment on the plan's direction or scope has to be
// pinned to some section; once stored, it reads as a note whose text is gone;
// or, once one is addressed, the button offers to resolve it instead.
func TestBrowser_WholePlanCommentReachesTheLedger(t *testing.T) {
	ctx := frameBrowser(t)
	base, planDir, _ := serveLivePlanReview(t)
	openReviewer(t, ctx, base)
	const note = "Split the rollout into two sprints."
	markSection(t, ctx, ".rev-rail-plan", note)
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.oldPage=true`, nil), chromedp.Click(".rev-submit", chromedp.ByQuery)); err != nil {
		t.Fatalf("submit: %v", err)
	}
	it := waitForOneRound(t, planDir, 15*time.Second)
	if it.Anchor != "plan" || it.Label != "Whole plan" || it.Section != "" || it.Note != note || it.Reviewer != "Devon" {
		t.Fatalf("the round must carry Devon's mark on the whole plan: %+v", it)
	}
	// the rail's first row: its heading, its tags, and its note
	const firstRow = `(function(){var li=document.querySelector('.rev-rail-item');if(!li)return '';` +
		`var tags=Array.prototype.map.call(li.querySelectorAll('.rev-rail-tag'),function(x){return x.textContent}).join(',');` +
		`return li.querySelector('.rev-rail-sec-t').textContent+'|'+tags+'|'+((li.querySelector('.rev-rail-note')||{}).textContent||'')})()`
	if !waitForNewPage(ctx, firstRow+`==='Whole plan||'+`+jsString(note), 10*time.Second) {
		t.Fatalf("the reloaded page must list the stored comment first, untagged (heading|tags|note): %q", evalString(t, ctx, firstRow))
	}
	if got := evalString(t, ctx, `document.querySelectorAll('.rev-rail-item').length+'|'+!!document.querySelector('.rev-orphans')`); got != "1|false" {
		t.Fatalf("the rail must list it once, with no banner of notes no longer anchored (rows|banner): %q", got)
	}

	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.oldPage=true`, nil)); err != nil {
		t.Fatalf("mark the page: %v", err)
	}
	resolveForTest(t, planDir, "plan", planreview.StateAddressed, "Split into two sprints.")
	if !waitForNewPage(ctx, `!!document.querySelector('.rev-rail-tag.addressed')`, 10*time.Second) {
		t.Fatal("the page must reload with the comment addressed")
	}
	if err := chromedp.Run(ctx, chromedp.Click(".rev-rail-plan", chromedp.ByQuery), chromedp.WaitVisible(".rev-pop", chromedp.ByQuery)); err != nil {
		t.Fatalf("comment on the whole plan again: %v", err)
	}
	if got := evalString(t, ctx, `['rev-accept','rev-reopen','rev-save'].filter(function(c){return document.querySelector('.rev-pop .'+c)}).join(' ')`); got != "rev-save" {
		t.Errorf("once a whole-plan comment is addressed, the button must still start a new one; it offered %q", got)
	}
}
