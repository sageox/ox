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
	if _, _, _, err := plan.Load(gitRoot, "browser-roundtrip"); err != nil {
		t.Fatalf("plan must be loadable for the live server to render it: %v", err)
	}

	ln := mustLoopbackListener(t)
	base := "http://" + ln.Addr().String()
	bc = newBroadcaster()
	h := liveReviewHandler(gitRoot, "browser-roundtrip", dir, base, "secret", bc, make(chan int, 8), make(chan struct{}, 1))
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
