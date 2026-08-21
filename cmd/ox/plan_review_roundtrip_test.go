package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sageox/ox/internal/plan"
)

// plan_review_roundtrip_test.go proves the browser review ROUND-TRIP end to end,
// from the reviewer's submit on the served page to the authoring coworker acting
// on it and the reviewer seeing it resolve. It is the executable proof behind
// tests/acceptance/features/plan-enrichment/browser-review-roundtrip.feature.
//
// It drives the SAME server + routes review.js POSTs to (the real
// liveReviewHandler, via newTestReviewServer) and the SAME agent-side read the
// `ox plan review await` command uses (awaitSnapshot / plan.AssembleReview), so
// the thing under test is the customer's real path, not a mock of it. The
// browser leg with a real Chrome driving review.js is proven separately, behind
// the `browser` build tag, in plan_review_browser_test.go.

// browserSubmit posts a single mark the way review.js does on Submit: a request
// with the review token and a round body carrying the reviewer, anchor, section,
// and note. Returns the HTTP status the page would see.
func browserSubmit(t *testing.T, srvURL, reviewer, anchor, section, status, note string) int {
	t.Helper()
	body := fmt.Sprintf(
		`{"reviewer":%q,"items":[{"anchor":%q,"section":%q,"label":%q,"status":%q,"note":%q}]}`,
		reviewer, anchor, section, section, status, note,
	)
	return reviewPOST(t, srvURL+"/feedback", "secret", body)
}

// TestBrowserRoundTrip_FeedbackReachesAuthoringAgent proves Rule 1: a mark Devon
// leaves on the served page reaches Avery with its section and note intact,
// without Devon copying anything anywhere. The agent-side read is the exact one
// `ox plan review await` performs.
// Failure prevented: a browser submit that persists but never surfaces to the
// authoring coworker — feedback that silently strands in the ledger.
func TestBrowserRoundTrip_FeedbackReachesAuthoringAgent(t *testing.T) {
	dir := t.TempDir()
	srv, rounds, _ := newTestReviewServer(t, dir)

	if code := browserSubmit(t, srv.URL, "Devon", "h1a2b3c4", "Risks", "request-change", "bound the blast radius"); code != http.StatusOK {
		t.Fatalf("browser submit should be accepted, got %d", code)
	}

	// the page saw a round go through
	select {
	case <-rounds:
	default:
		t.Error("a valid submit must signal a round")
	}

	// the submit is durable as a round the ledger carries
	if sets, _ := plan.LoadAllFeedback(dir); len(sets) != 1 {
		t.Fatalf("submit must persist exactly one round, got %d", len(sets))
	}

	// the authoring coworker's `await` read surfaces the exact item, section+note intact
	res, done := awaitSnapshot(dir)
	if !done || res.Status != "feedback" {
		t.Fatalf("await must have work to hand the agent, got done=%v status=%q", done, res.Status)
	}
	if len(res.Open) != 1 {
		t.Fatalf("agent should see exactly one open item, got %d", len(res.Open))
	}
	item := res.Open[0]
	if item.Anchor != "h1a2b3c4" || item.Section != "Risks" || item.Note != "bound the blast radius" {
		t.Errorf("item lost its context in transit: %+v", item.FeedbackItem)
	}
	if item.Reviewer != "Devon" {
		t.Errorf("item lost its reviewer attribution, got %q", item.Reviewer)
	}
}

// TestBrowserRoundTrip_MultiMarkRoundArrivesIntact proves that several marks in
// one submit reach the agent as one round, each still tied to its own section.
func TestBrowserRoundTrip_MultiMarkRoundArrivesIntact(t *testing.T) {
	dir := t.TempDir()
	srv, _, _ := newTestReviewServer(t, dir)

	body := `{"reviewer":"Devon","items":[` +
		`{"anchor":"haaa1","section":"Approach","status":"comment","note":"clarify step 2"},` +
		`{"anchor":"hbbb2","section":"Risks","status":"request-change","note":"handle the retry"},` +
		`{"anchor":"hccc3","section":"Rollout","status":"flag","note":"needs a flag"}]}`
	if code := reviewPOST(t, srv.URL+"/feedback", "secret", body); code != http.StatusOK {
		t.Fatalf("multi-mark submit should be accepted, got %d", code)
	}

	if sets, _ := plan.LoadAllFeedback(dir); len(sets) != 1 || len(sets[0].Items) != 3 {
		t.Fatalf("three marks must arrive as one round of three items, got %+v", sets)
	}

	res, done := awaitSnapshot(dir)
	if !done {
		t.Fatal("await should surface the round")
	}
	wantSections := map[string]string{"haaa1": "Approach", "hbbb2": "Risks", "hccc3": "Rollout"}
	if len(res.Open) != 3 {
		t.Fatalf("agent should see three open items, got %d", len(res.Open))
	}
	for _, it := range res.Open {
		if want := wantSections[it.Anchor]; it.Section != want {
			t.Errorf("item %s tied to wrong section: got %q want %q", it.Anchor, it.Section, want)
		}
	}
}

// TestBrowserRoundTrip_AgentResolveShowsAddressedAndBroadcasts proves Rule 2:
// when Avery addresses an item, (a) the served page is signaled to reload live
// (the broadcast), and (b) the reloaded page's state shows the item addressed,
// no longer open — so Devon sees the fix without reopening anything.
// Failure prevented: the agent resolves an item but the reviewer's open page
// never learns, or still shows it unresolved.
func TestBrowserRoundTrip_AgentResolveShowsAddressedAndBroadcasts(t *testing.T) {
	dir := t.TempDir()
	// explicit broadcaster so we can prove the reviewer's tab is told to reload
	bc := newBroadcaster()
	sub := bc.subscribe()
	t.Cleanup(func() { bc.unsubscribe(sub) })
	h := liveReviewHandler("", "p", dir, "http://x", "secret", bc, make(chan int, 8), make(chan struct{}, 1))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	if code := browserSubmit(t, srv.URL, "Devon", "hopen1", "Risks", "request-change", "fix this"); code != http.StatusOK {
		t.Fatalf("submit: %d", code)
	}
	// a submit repaints every open tab (including other reviewers')
	select {
	case <-sub:
	default:
		t.Error("a submit must broadcast a live reload to open pages")
	}

	// the item is open until the agent acts
	before, _ := plan.AssembleReview(dir)
	if len(before) != 1 || !before[0].Open {
		t.Fatalf("item must be open before the agent addresses it, got %+v", before)
	}

	// Avery addresses it and records the disposition, exactly as
	// `ox plan feedback resolve … --state addressed` does
	if err := plan.AppendResolution(dir, plan.Resolution{
		Anchor: "hopen1", State: plan.ResolutionAddressed, Commit: "abc1234", Note: "handled",
	}, time.Now()); err != nil {
		t.Fatalf("agent resolve: %v", err)
	}

	// the state the reloaded page reads now shows it addressed, not open
	after, _ := plan.AssembleReview(dir)
	if len(after) != 1 || after[0].Open {
		t.Fatalf("item must show resolved after the agent addresses it, got %+v", after)
	}
	if after[0].Resolution == nil || after[0].Resolution.State != plan.ResolutionAddressed {
		t.Errorf("resolved item must carry the agent's addressed disposition, got %+v", after[0].Resolution)
	}

	// and the agent's next `await` has nothing open to hand back
	if _, done := awaitSnapshot(dir); done {
		t.Error("await must not re-surface an addressed item")
	}
}

// TestBrowserRoundTrip_AcceptClearsAndReopenReturnsToAgent proves the human
// close-the-loop actions in the round-trip: Devon accepting an addressed item
// clears it, and Devon reopening one he isn't satisfied with re-opens it AND
// re-notifies the authoring coworker so it doesn't strand after Avery's session.
func TestBrowserRoundTrip_AcceptClearsAndReopenReturnsToAgent(t *testing.T) {
	srv, gitRoot, planDir := newNotifyingReviewServer(t)

	// Devon raises it, Avery addresses it
	if code := browserSubmit(t, srv.URL, "Devon", "hitem1", "Risks", "request-change", "fix"); code != http.StatusOK {
		t.Fatalf("submit: %d", code)
	}
	if err := plan.AppendResolution(planDir, plan.Resolution{Anchor: "hitem1", State: plan.ResolutionAddressed, Note: "done"}, time.Now()); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// Devon accepts the fix — it stops being open
	if code := reviewPOST(t, srv.URL+"/accept", "secret", `{"anchor":"hitem1"}`); code != http.StatusOK {
		t.Fatalf("accept: %d", code)
	}
	if _, done := awaitSnapshot(planDir); done {
		t.Error("an accepted item must not remain open work for the agent")
	}

	// Devon changes his mind and reopens it — it returns to Avery as work to do
	if code := reviewPOST(t, srv.URL+"/reopen", "secret", `{"anchor":"hitem1","note":"still wrong"}`); code != http.StatusOK {
		t.Fatalf("reopen: %d", code)
	}
	res, done := awaitSnapshot(planDir)
	if !done || len(res.Open) != 1 || res.Open[0].Anchor != "hitem1" {
		t.Fatalf("a reopened item must return to the agent as open, got done=%v %+v", done, res.Open)
	}
	// reopen must re-notify the authoring coworker (submit + reopen dedupe to one task)
	if n := len(activeTasks(t, gitRoot)); n != 1 {
		t.Errorf("reopen must leave the authoring coworker notified, got %d tasks", n)
	}
}

// TestBrowserRoundTrip_BrowserApproveClosesReview proves Rule 2's closing beat:
// approving from the browser records the plan approved through the SAME lifecycle
// engine as `ox plan approve`, and the agent's `await` then reports the loop is
// done.
func TestBrowserRoundTrip_BrowserApproveClosesReview(t *testing.T) {
	dir := t.TempDir()
	// approve routes through the lifecycle engine, which needs prior plan history
	// and a meta.json to dual-write the approved status onto
	seed := plan.Event{PlanID: "pln_roundtrip000000000001", Kind: plan.EventCreated, Status: plan.PlanStatusDraft}
	if err := plan.AppendEvent(context.Background(), dir, seed); err != nil {
		t.Fatalf("seed created event: %v", err)
	}
	writeTestPlanMeta(t, dir, nil)

	srv, _, approved := newTestReviewServer(t, dir)
	if code := reviewPOST(t, srv.URL+"/approve", "secret", ""); code != http.StatusOK {
		t.Fatalf("approve should be accepted, got %d", code)
	}
	select {
	case <-approved:
	default:
		t.Error("approve must end the loop by signaling the approved channel")
	}

	// recorded through the lifecycle engine
	events, err := plan.LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 2 || events[1].Kind != plan.EventApproved {
		t.Fatalf("browser approve must append an approved lifecycle event, got %+v", events)
	}

	// the agent's await reports the loop is done
	res, done := awaitSnapshot(dir)
	if !done || res.Status != "approved" {
		t.Errorf("await must report the loop approved, got done=%v status=%q", done, res.Status)
	}
}

// TestBrowserRoundTrip_UntrustedSubmissionRejected proves Rule 3's permission
// beat: only feedback carrying the served page's review token is accepted. A
// submit without that token is refused and nothing reaches the agent.
// Failure prevented: any other local process posting feedback into a reviewer's
// plan without the token the review page was handed.
func TestBrowserRoundTrip_UntrustedSubmissionRejected(t *testing.T) {
	dir := t.TempDir()
	srv, _, _ := newTestReviewServer(t, dir)

	payload := `{"reviewer":"attacker","items":[{"anchor":"hx","status":"request-change","note":"injected"}]}`
	if code := reviewPOST(t, srv.URL+"/feedback", "", payload); code != http.StatusForbidden {
		t.Fatalf("a submit without the page token must be refused (403), got %d", code)
	}
	if sets, _ := plan.LoadAllFeedback(dir); len(sets) != 0 {
		t.Error("a refused submit must not persist any feedback")
	}
	if _, done := awaitSnapshot(dir); done {
		t.Error("a refused submit must not surface anything to the agent")
	}
}

// TestBrowserRoundTrip_TwoReviewersAttributedAndContested proves Rule 3's
// multi-user beat: Devon and Riley marking the same spot are BOTH kept and
// attributed, and when they disagree ox surfaces the contested anchor rather
// than silently dropping one verdict.
// Failure prevented: a second reviewer's mark clobbering the first, or a
// reviewer disagreement disappearing before a human can reconcile it.
func TestBrowserRoundTrip_TwoReviewersAttributedAndContested(t *testing.T) {
	dir := t.TempDir()
	srv, _, _ := newTestReviewServer(t, dir)

	if code := browserSubmit(t, srv.URL, "Devon", "hspot", "Rollout", "request-change", "not ready"); code != http.StatusOK {
		t.Fatalf("Devon submit: %d", code)
	}
	if code := browserSubmit(t, srv.URL, "Riley", "hspot", "Rollout", "approve", "ship it"); code != http.StatusOK {
		t.Fatalf("Riley submit: %d", code)
	}

	items, _ := plan.AssembleReview(dir)
	if len(items) != 2 {
		t.Fatalf("both reviewers' marks on the same spot must be kept, got %d", len(items))
	}
	reviewers := map[string]bool{}
	for _, it := range items {
		reviewers[it.Reviewer] = true
	}
	if !reviewers["Devon"] || !reviewers["Riley"] {
		t.Errorf("each mark must stay attributed to its reviewer, got %v", reviewers)
	}
	if !plan.ContestedAnchors(items)["hspot"] {
		t.Error("a reviewer disagreement on one spot must surface as a contested anchor")
	}

	// the agent's await carries the contested anchor forward for a human to reconcile
	res, done := awaitSnapshot(dir)
	if !done {
		t.Fatal("await should surface the open disagreement")
	}
	found := false
	for _, a := range res.Contested {
		if a == "hspot" {
			found = true
		}
	}
	if !found {
		t.Errorf("await must report the contested anchor, got %v", res.Contested)
	}
}
