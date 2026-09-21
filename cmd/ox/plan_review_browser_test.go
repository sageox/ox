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
// handled), and a window-level counter of Esc presses nothing handled.
func serveAuthoredPlanReview(t *testing.T) (url, planDir string, bc *broadcaster) {
	t.Helper()
	gitRoot := newPlanStatusTestRepo(t)
	html := []byte(`<!doctype html><html><head><meta charset="utf-8"><title>Authored Roundtrip</title>` +
		`<meta name="ox-plan-slug" content="authored-roundtrip"></head><body><h1>Authored Roundtrip</h1>` +
		`<section id="risks"><h2>Risks</h2><p>The retry path can double-fire under load.</p></section>` +
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
	if err := plan.AppendResolution(planDir, plan.Resolution{
		Anchor: anchor, State: plan.ResolutionAddressed, Commit: "abc1234", Note: "guarded the retry",
	}, time.Now()); err != nil {
		t.Fatalf("agent resolve: %v", err)
	}

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
	sets, err := plan.LoadAllFeedback(planDir)
	if err != nil {
		t.Fatalf("read feedback: %v", err)
	}
	if len(sets) != 0 {
		t.Fatalf("an unsent draft must not reach the ledger, got %d round(s)", len(sets))
	}

	// The connection drops and returns: reload the page (same stable origin).
	if err := chromedp.Run(ctx,
		chromedp.Reload(),
		chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
		chromedp.Text(".rev-count", &unsentAfter, chromedp.ByQuery),
		chromedp.Evaluate(`(localStorage.getItem('ox-plan-fb:'+(document.body.getAttribute('data-slug')||''))||'')`, &draftAfter),
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

// waitForOneRound polls the plan dir until exactly one review round with one item
// has landed, then returns that item. Fails if nothing arrives — a submit that
// never reached the ledger is the failure this guards.
func waitForOneRound(t *testing.T, planDir string, within time.Duration) plan.FeedbackItem {
	t.Helper()
	deadline := time.Now().Add(within)
	var lastErr error
	for time.Now().Before(deadline) {
		sets, err := plan.LoadAllFeedback(planDir)
		if err != nil {
			lastErr = err
		} else if len(sets) == 1 && len(sets[0].Items) == 1 {
			return sets[0].Items[0]
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("browser Submit never reached the ledger (last read error: %v)", lastErr)
	return plan.FeedbackItem{}
}
