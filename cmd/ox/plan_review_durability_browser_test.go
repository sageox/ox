//go:build browser

// plan_review_durability_browser_test.go drives the real review.js in headless
// Chrome against a scripted /feedback and /approve, to prove the page's half of
// "review feedback is durable": one Submit is one round however the send is
// interrupted, two tabs never overwrite each other's marks, and the reviewer is
// told what actually happened to their feedback. The page itself is served by
// the real live-review handler; only the POST answers are scripted, so each
// server outcome (synced, saved-not-synced, an older server, an error) can be
// produced on demand.
package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/sageox/ox/internal/plan"
)

// scriptedPost records every POST the page makes to one endpoint and answers
// each with the next scripted reply. A reply with hold set blocks until the
// test closes hold, standing in for a slow commit+push.
type scriptedPost struct {
	mu      sync.Mutex
	bodies  []map[string]any
	replies []scriptedReply
	got     chan struct{}
}

type scriptedReply struct {
	status int
	body   func(id string) string // id: the request's round id
	hold   chan struct{}
}

func newScriptedPost(replies ...scriptedReply) *scriptedPost {
	return &scriptedPost{replies: replies, got: make(chan struct{}, 16)}
}

func (s *scriptedPost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Review-Token") != "secret" {
		http.Error(w, "bad token", http.StatusForbidden)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	s.mu.Lock()
	n := len(s.bodies)
	s.bodies = append(s.bodies, body)
	reply := scriptedReply{status: 500, body: func(string) string { return "no scripted reply" }}
	if n < len(s.replies) {
		reply = s.replies[n]
	}
	s.mu.Unlock()
	s.got <- struct{}{}
	if reply.hold != nil {
		<-reply.hold
	}
	id, _ := body["id"].(string)
	if reply.status != http.StatusOK {
		http.Error(w, reply.body(id), reply.status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, reply.body(id))
}

func (s *scriptedPost) requests() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.bodies...)
}

func (s *scriptedPost) waitRequests(t *testing.T, n int, within time.Duration) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if r := s.requests(); len(r) >= n {
			return r
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected %d POST(s), got %d", n, len(s.requests()))
	return nil
}

func reply(body string) scriptedReply {
	return scriptedReply{status: http.StatusOK, body: func(id string) string { return strings.ReplaceAll(body, "$ID", id) }}
}

const (
	replyPushed   = `{"ok":true,"saved":true,"round_id":"$ID","duplicate":false,"committed":true,"pushed":true,"notified":true}`
	replyDupe     = `{"ok":true,"saved":true,"round_id":"$ID","duplicate":true,"committed":true,"pushed":true,"notified":true}`
	replyUnpushed = `{"ok":true,"saved":true,"round_id":"$ID","duplicate":false,"committed":true,"pushed":false,"notified":true}`
	replyOld      = `{"ok":true,"notified":true}`
	replyWrongID  = `{"ok":true,"saved":true,"round_id":"someone-elses-round","committed":true,"pushed":true}`
)

// serveScriptedPlanReview serves the real plan page (real handler, real
// review.js, SSE) with /feedback and /approve answered by the given scripts.
func serveScriptedPlanReview(t *testing.T, feedback, approve *scriptedPost) (base string, bc *broadcaster) {
	t.Helper()
	gitRoot := newPlanStatusTestRepo(t)
	md := "# Durable Review\n\n## Risks\n\nThe retry path can double-fire under load.\n\n## Rollout\n\nShip behind a flag.\n"
	dir, _, err := plan.Save(gitRoot, plan.Input{Raw: md}, plan.Result{}, nil, plan.Meta{Topic: "Durable Review", Slug: "durable-review"})
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}
	ln := mustLoopbackListener(t)
	base = "http://" + ln.Addr().String()
	bc = newBroadcaster()
	mux := http.NewServeMux()
	mux.Handle("/", liveReviewHandler(gitRoot, "durable-review", dir, base, "secret", bc, make(chan int, 8), make(chan struct{}, 1)))
	if feedback != nil {
		mux.Handle("/feedback", feedback)
	}
	if approve != nil {
		mux.Handle("/approve", approve)
	}
	go serveUntilClosed(t, ln, mux)
	return base, bc
}

// newChrome starts headless Chrome, skipping when none is installed.
func newChrome(t *testing.T) context.Context {
	t.Helper()
	if testing.Short() {
		t.Skip("short: launches a real headless Chrome")
	}
	chromePath := findChromePath()
	if chromePath == "" {
		t.Skip("no Chrome/Chromium binary found — skipping real-browser E2E")
	}
	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chromePath))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	t.Cleanup(cancelAlloc)
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	t.Cleanup(cancelCtx)
	ctx, cancelTimeout := context.WithTimeout(ctx, 60*time.Second)
	t.Cleanup(cancelTimeout)
	return ctx
}

// openReviewer loads the plan with a reviewer name seeded, ready to mark up.
func openReviewer(t *testing.T, ctx context.Context, base string) {
	t.Helper()
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery),
		chromedp.Evaluate(`localStorage.setItem('ox-plan-reviewer','Devon');localStorage.setItem('ox-plan-rev-seen','1');window.oldPage=true`, nil),
		chromedp.Reload(),
	); err != nil {
		t.Fatalf("open page: %v", err)
	}
	if !waitForNewPage(ctx, `!!document.querySelector('.rev-toggle')`, 10*time.Second) {
		t.Fatal("page did not reload with the reviewer seeded")
	}
}

// markSection leaves a request-change note on a section, unsent.
func markSection(t *testing.T, ctx context.Context, sel, note string) {
	t.Helper()
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`document.body.classList.contains('rev-on') || document.querySelector('.rev-toggle').click()`, nil),
		chromedp.Click(sel, chromedp.ByQuery),
		chromedp.WaitVisible(".rev-pop .rev-save", chromedp.ByQuery),
		chromedp.SendKeys(".rev-pop .rev-note", note, chromedp.ByQuery),
		chromedp.Click(".rev-pop .rev-save", chromedp.ByQuery),
	); err != nil {
		t.Fatalf("mark %s: %v", sel, err)
	}
}

// markSectionJS is markSection driven by DOM events instead of the mouse: a
// background tab gets no input events, and the two-tab test needs both tabs
// live at once. append adds to an existing note rather than replacing it.
func markSectionJS(t *testing.T, ctx context.Context, sel, note string) {
	t.Helper()
	js := `(function(sel,note){if(!document.body.classList.contains('rev-on'))document.querySelector('.rev-toggle').click();` +
		`document.querySelector(sel).click();var p=document.querySelector('.rev-pop');if(!p||!p.querySelector('.rev-save'))return false;` +
		`var n=p.querySelector('.rev-note');n.value=n.value+note;p.querySelector('.rev-save').click();return true;})(` + jsString(sel) + `,` + jsString(note) + `)`
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &ok)); err != nil || !ok {
		t.Fatalf("mark %s: ok=%v err=%v", sel, ok, err)
	}
}

// waitJS polls until cond is true on the current page.
func waitJS(ctx context.Context, cond string, within time.Duration) bool {
	var ok bool
	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if chromedp.Run(ctx, chromedp.Evaluate(`!!(`+cond+`)`, &ok)) == nil && ok {
			return true
		}
	}
	return false
}

func evalString(t *testing.T, ctx context.Context, expr string) string {
	t.Helper()
	var s string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`String(`+expr+`)`, &s)); err != nil {
		t.Fatalf("evaluate %s: %v", expr, err)
	}
	return s
}

const (
	jsSyncText = `((document.querySelector('.rev-sync .rev-sync-msg')||{}).textContent||'')`
	jsOutbox   = `(localStorage.getItem('ox-plan-fb:durable-review:outbox')||'')`
	jsMarks    = `(localStorage.getItem('ox-plan-fb:durable-review')||'')`
)

// TestBrowser_SubmitResendsSameRoundAfterReloadMidFlight: the reviewer
// submits, the server is slow to answer, and the page reloads before it does.
// The reloaded page must resend the SAME round id (so the server can dedupe),
// and a double-click on Submit while the first send is in flight must not
// send a second round.
// Failure prevented: a reload racing a slow commit+push restores the marks,
// and the next Submit lands a duplicate round on the author's plan.
func TestBrowser_SubmitResendsSameRoundAfterReloadMidFlight(t *testing.T) {
	hold := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-hold:
		default:
			close(hold)
		}
	})
	fb := newScriptedPost(scriptedReply{status: http.StatusOK, body: func(id string) string { return strings.ReplaceAll(replyPushed, "$ID", id) }, hold: hold}, reply(replyDupe))
	base, _ := serveScriptedPlanReview(t, fb, nil)
	ctx := newChrome(t)
	openReviewer(t, ctx, base)
	markSection(t, ctx, "section#sec-1", "bound the blast radius")

	if err := chromedp.Run(ctx, chromedp.Click(".rev-submit", chromedp.ByQuery)); err != nil {
		t.Fatalf("submit: %v", err)
	}
	first := fb.waitRequests(t, 1, 10*time.Second)[0]
	id, _ := first["id"].(string)
	if id == "" || len(id) < 8 {
		t.Fatalf("Submit must carry a round id, got %v", first)
	}
	// the outbox holds this round before any answer, and Submit is disabled
	if ob := evalString(t, ctx, jsOutbox); !strings.Contains(ob, id) {
		t.Fatalf("outbox must persist the round before the server answers, got %q", ob)
	}
	if got := evalString(t, ctx, `document.querySelector('.rev-submit').disabled`); got != "true" {
		t.Fatalf("Submit must be disabled while a send is in flight, disabled=%s", got)
	}
	_ = chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('.rev-submit').click(),document.querySelector('.rev-submit').click()`, nil))
	time.Sleep(300 * time.Millisecond)
	if n := len(fb.requests()); n != 1 {
		t.Fatalf("a double-click while sending must not send again, got %d POSTs", n)
	}

	// reload with the first answer still pending: the page resends the round
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.oldPage=true`, nil), chromedp.Reload()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	second := fb.waitRequests(t, 2, 10*time.Second)[1]
	if second["id"] != id {
		t.Fatalf("a reload mid-send must resend the SAME round id: first=%v second=%v", id, second["id"])
	}
	if !waitForNewPage(ctx, jsSyncText+`==='Sent · synced to your team'`, 10*time.Second) {
		t.Fatalf("a duplicate ack must read as plain success, status=%q", evalString(t, ctx, jsSyncText))
	}
	if ob := evalString(t, ctx, jsOutbox); ob != "" {
		t.Fatalf("an acked round must clear the outbox, got %q", ob)
	}
	if c := evalString(t, ctx, `document.querySelector('.rev-count').textContent`); c != "" {
		t.Fatalf("acked marks must be cleared, counter=%q", c)
	}
	close(hold) // the first answer reaches a page that is gone; nothing more is sent
	time.Sleep(300 * time.Millisecond)
	if n := len(fb.requests()); n != 2 {
		t.Fatalf("expected exactly 2 POSTs of one round, got %d", n)
	}
}

// TestBrowser_LiveReloadWaitsForSubmitResult: the server writes the round (the
// watcher pushes an SSE reload) before its slow commit+push answers. The page
// must hold that reload until the answer arrives, then reload — and still show
// the reviewer the result.
// Failure prevented: the reload wipes the in-flight request, and the reviewer
// never learns whether their feedback was saved.
func TestBrowser_LiveReloadWaitsForSubmitResult(t *testing.T) {
	hold := make(chan struct{})
	fb := newScriptedPost(scriptedReply{status: http.StatusOK, body: func(id string) string { return strings.ReplaceAll(replyPushed, "$ID", id) }, hold: hold})
	base, bc := serveScriptedPlanReview(t, fb, nil)
	ctx := newChrome(t)
	openReviewer(t, ctx, base)
	markSection(t, ctx, "section#sec-1", "bound the blast radius")
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.oldPage=true`, nil), chromedp.Click(".rev-submit", chromedp.ByQuery)); err != nil {
		t.Fatalf("submit: %v", err)
	}
	fb.waitRequests(t, 1, 10*time.Second)
	bc.broadcast()
	time.Sleep(700 * time.Millisecond)
	if evalString(t, ctx, `!!window.oldPage`) != "true" {
		t.Fatal("an SSE reload must wait for the in-flight Submit to answer")
	}
	close(hold)
	if !waitForNewPage(ctx, jsSyncText+`==='Sent · synced to your team'`, 10*time.Second) {
		t.Fatalf("after the held reload the result must still show, status=%q", evalString(t, ctx, jsSyncText))
	}
	if n := len(fb.requests()); n != 1 {
		t.Fatalf("a held reload must not resend an acked round, got %d POSTs", n)
	}
}

// TestBrowser_SyncStatusTellsTheTruth: each server outcome reads as what it is.
// Failure prevented: the page says "sent" when the feedback only sits on the
// author's laptop, or an error alert() that the web app's iframe swallows
// leaves the reviewer believing it went through.
func TestBrowser_SyncStatusTellsTheTruth(t *testing.T) {
	cases := []struct {
		name      string
		reply     scriptedReply
		wantText  string
		wantKind  string
		wantClear bool
	}{
		{"pushed", reply(replyPushed), "Sent · synced to your team", "ok", true},
		{"saved-not-pushed", reply(replyUnpushed), "Saved on the author’s machine · not yet synced (will retry)", "warn", true},
		{"old-server", reply(replyOld), "Sent · received by the author’s machine", "ok", true},
		{"mismatched-round", reply(replyWrongID), "Not confirmed — the server acknowledged a different round. Your marks are kept.", "err", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fb := newScriptedPost(c.reply)
			base, _ := serveScriptedPlanReview(t, fb, nil)
			ctx := newChrome(t)
			openReviewer(t, ctx, base)
			markSection(t, ctx, "section#sec-1", "note for "+c.name)
			if err := chromedp.Run(ctx, chromedp.Click(".rev-submit", chromedp.ByQuery)); err != nil {
				t.Fatalf("submit: %v", err)
			}
			if !waitJS(ctx, jsSyncText+`===`+jsString(c.wantText), 10*time.Second) {
				t.Fatalf("status = %q, want %q", evalString(t, ctx, jsSyncText), c.wantText)
			}
			if k := evalString(t, ctx, `document.querySelector('.rev-sync').classList.contains(`+jsString(c.wantKind)+`)`); k != "true" {
				t.Fatalf("status must be styled %s", c.wantKind)
			}
			ob, marks := evalString(t, ctx, jsOutbox), evalString(t, ctx, jsMarks)
			if c.wantClear && (ob != "" || strings.Contains(marks, "note for")) {
				t.Fatalf("an acked round must clear outbox and marks: outbox=%q marks=%q", ob, marks)
			}
			if !c.wantClear && (ob == "" || !strings.Contains(marks, "note for")) {
				t.Fatalf("an unacked round must keep outbox and marks: outbox=%q marks=%q", ob, marks)
			}
		})
	}
}

// TestBrowser_SubmitErrorKeepsMarksAndRetriesSameRound: an HTTP error shows an
// inline error with Retry, keeps the marks, and Retry resends the same round.
// Failure prevented: a 500 surfaces as a blocked alert() (nothing visible in
// an iframe) or the retry mints a new round the server cannot dedupe.
func TestBrowser_SubmitErrorKeepsMarksAndRetriesSameRound(t *testing.T) {
	fb := newScriptedPost(scriptedReply{status: http.StatusInternalServerError, body: func(string) string { return "disk full" }}, reply(replyPushed))
	base, _ := serveScriptedPlanReview(t, fb, nil)
	ctx := newChrome(t)
	openReviewer(t, ctx, base)
	markSection(t, ctx, "section#sec-1", "keep me")
	if err := chromedp.Run(ctx, chromedp.Click(".rev-submit", chromedp.ByQuery)); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !waitJS(ctx, `document.querySelector('.rev-sync.err .rev-sync-act')`, 10*time.Second) {
		t.Fatalf("an HTTP error must show inline with Retry, status=%q", evalString(t, ctx, jsSyncText))
	}
	if s := evalString(t, ctx, jsSyncText); !strings.Contains(s, "Not sent") || !strings.Contains(s, "HTTP 500") {
		t.Fatalf("error status must say it was not sent and why, got %q", s)
	}
	if c := evalString(t, ctx, `document.querySelector('.rev-count').textContent`); c != "1 unsent" {
		t.Fatalf("marks must be kept after an error, counter=%q", c)
	}
	if err := chromedp.Run(ctx, chromedp.Click(".rev-sync.err .rev-sync-act", chromedp.ByQuery)); err != nil {
		t.Fatalf("retry: %v", err)
	}
	reqs := fb.waitRequests(t, 2, 10*time.Second)
	if reqs[0]["id"] != reqs[1]["id"] {
		t.Fatalf("Retry must resend the same round id: %v vs %v", reqs[0]["id"], reqs[1]["id"])
	}
	if !waitJS(ctx, jsSyncText+`==='Sent · synced to your team'`, 10*time.Second) {
		t.Fatalf("retry success must be reported, status=%q", evalString(t, ctx, jsSyncText))
	}
}

// TestBrowser_TwoTabsKeepEachOthersMarks: two tabs on one plan each leave a
// mark; both survive in storage and each tab shows both. A mark deleted in
// one tab is not brought back when the other tab saves.
// Failure prevented: last writer wins — the other tab's unsent marks vanish,
// or a deleted mark reappears.
func TestBrowser_TwoTabsKeepEachOthersMarks(t *testing.T) {
	base, _ := serveScriptedPlanReview(t, nil, nil)
	tabA := newChrome(t)
	openReviewer(t, tabA, base)
	tabB, cancelB := chromedp.NewContext(tabA) // a second tab in the same browser profile
	t.Cleanup(cancelB)
	if err := chromedp.Run(tabB, chromedp.Navigate(base+"/"), chromedp.WaitVisible(".rev-toggle", chromedp.ByQuery)); err != nil {
		t.Fatalf("open tab B: %v", err)
	}

	markSectionJS(t, tabA, "section#sec-1", "from tab A")
	markSectionJS(t, tabB, "section#sec-2", "from tab B")
	stored := evalString(t, tabA, jsMarks)
	if !strings.Contains(stored, "from tab A") || !strings.Contains(stored, "from tab B") {
		t.Fatalf("both tabs' marks must be in storage, got %s", stored)
	}
	if !waitJS(tabA, `document.querySelector('.rev-count').textContent==='2 unsent'`, 5*time.Second) {
		t.Fatalf("tab A must pick up tab B's mark, counter=%q", evalString(t, tabA, `document.querySelector('.rev-count').textContent`))
	}

	// tab A deletes its mark; tab B then edits its own and saves
	var deleted bool
	if err := chromedp.Run(tabA, chromedp.Evaluate(`(function(){document.querySelector('section#sec-1').click();`+
		`var d=document.querySelector('.rev-pop .rev-del');if(!d)return false;d.click();return true;})()`, &deleted)); err != nil || !deleted {
		t.Fatalf("delete in tab A: ok=%v err=%v", deleted, err)
	}
	markSectionJS(t, tabB, "section#sec-2", " (edited)")
	stored = evalString(t, tabB, jsMarks)
	if strings.Contains(stored, "from tab A") {
		t.Fatalf("a mark deleted in tab A must not be resurrected by tab B's save: %s", stored)
	}
	if !strings.Contains(stored, "(edited)") {
		t.Fatalf("tab B's edit must be saved: %s", stored)
	}
	if !strings.Contains(evalString(t, tabB, `localStorage.getItem('ox-plan-fb:durable-review:tombs')`), `"`) {
		t.Fatal("the delete must leave a tombstone")
	}
}

// TestBrowser_ApproveConfirmsInlineAndReportsSync: Approve asks inline (no
// confirm(), which an iframe blocks) and reports the sync truth.
// Failure prevented: in the web app's iframe, Approve silently does nothing.
func TestBrowser_ApproveConfirmsInlineAndReportsSync(t *testing.T) {
	ap := newScriptedPost(reply(`{"ok":true,"saved":true,"committed":true,"pushed":false}`))
	base, _ := serveScriptedPlanReview(t, nil, ap)
	ctx := newChrome(t)
	openReviewer(t, ctx, base)
	if err := chromedp.Run(ctx,
		chromedp.Click(".rev-approve", chromedp.ByQuery),
		chromedp.WaitVisible(".rev-sync.ask .rev-sync-act", chromedp.ByQuery),
	); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if n := len(ap.requests()); n != 0 {
		t.Fatalf("Approve must wait for the inline confirmation, got %d POSTs", n)
	}
	if err := chromedp.Run(ctx, chromedp.Click(".rev-sync.ask .rev-sync-act", chromedp.ByQuery)); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	ap.waitRequests(t, 1, 10*time.Second)
	if !waitJS(ctx, jsSyncText+`.indexOf('not yet synced')>=0`, 10*time.Second) {
		t.Fatalf("approve must report saved-not-synced, status=%q", evalString(t, ctx, jsSyncText))
	}
}

func jsString(s string) string { b, _ := json.Marshal(s); return string(b) }
