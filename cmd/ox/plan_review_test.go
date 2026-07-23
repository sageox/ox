package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sageox/ox/internal/agenttask"
	"github.com/sageox/ox/internal/plan"
)

// newTestReviewServer wires the live handler against a temp plan dir (no ledger
// git, so commits are best-effort no-ops) and returns it plus the round/approve
// channels.
func newTestReviewServer(t *testing.T, planDir string) (*httptest.Server, chan int, chan struct{}) {
	t.Helper()
	rounds := make(chan int, 8)
	approved := make(chan struct{}, 1)
	h := liveReviewHandler("", "p", planDir, "http://x", "secret", newBroadcaster(), rounds, approved)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, rounds, approved
}

func reviewPOST(t *testing.T, url, token, body string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Review-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestReviewLoop_FeedbackEndpointTokenGated verifies a round POST is token-gated,
// persisted, and signaled. Failure prevented: any local process posting feedback,
// or a submit lost instead of reaching the agent.
func TestReviewLoop_FeedbackEndpointTokenGated(t *testing.T) {
	dir := t.TempDir()
	srv, rounds, _ := newTestReviewServer(t, dir)
	payload := `{"items":[{"anchor":"h1","status":"request-change","note":"bound it"}]}`

	if code := reviewPOST(t, srv.URL+"/feedback", "", payload); code != http.StatusForbidden {
		t.Errorf("missing token should be 403, got %d", code)
	}
	if sets, _ := plan.LoadAllFeedback(dir); len(sets) != 0 {
		t.Error("a forbidden submit must not write feedback")
	}
	if code := reviewPOST(t, srv.URL+"/feedback", "secret", payload); code != http.StatusOK {
		t.Fatalf("valid submit should be 200, got %d", code)
	}
	select {
	case n := <-rounds:
		if n != 1 {
			t.Errorf("round signal should carry item count 1, got %d", n)
		}
	default:
		t.Error("valid submit must signal a round")
	}
	if sets, _ := plan.LoadAllFeedback(dir); len(sets) != 1 {
		t.Errorf("valid submit must persist one round, got %d", len(sets))
	}
}

// TestReviewLoop_AcceptAndReopen verifies the human close-the-loop actions:
// Accept writes a verified resolution; Reopen raises a new round that reopens the
// item. Failure prevented: the human can't close or re-raise an addressed item.
func TestReviewLoop_AcceptAndReopen(t *testing.T) {
	dir := t.TempDir()
	srv, rounds, _ := newTestReviewServer(t, dir)

	if code := reviewPOST(t, srv.URL+"/accept", "secret", `{"anchor":"h1"}`); code != http.StatusOK {
		t.Fatalf("accept should be 200, got %d", code)
	}
	res, _ := plan.LoadResolutions(dir)
	if len(res) != 1 || res[0].State != plan.ResolutionVerified || res[0].Anchor != "h1" {
		t.Errorf("accept must append a verified resolution, got %+v", res)
	}

	if code := reviewPOST(t, srv.URL+"/reopen", "secret", `{"anchor":"h1","note":"still broken"}`); code != http.StatusOK {
		t.Fatalf("reopen should be 200, got %d", code)
	}
	select {
	case <-rounds:
	default:
		t.Error("reopen must signal a round")
	}
	sets, _ := plan.LoadAllFeedback(dir)
	if len(sets) != 1 || sets[0].Items[0].Status != plan.FeedbackRequestChange {
		t.Errorf("reopen must raise a request-change round, got %+v", sets)
	}
	// merged view: the item is open again (re-raised after the resolution)
	items, _ := plan.AssembleReview(dir)
	if len(items) != 1 || !items[0].Open {
		t.Errorf("re-raised item must be open, got %+v", items)
	}
}

// TestReviewLoop_ApproveReroutesThroughLifecycleEngine verifies /approve now
// goes through plan.AppendPlanEvent — the same engine `ox plan approve` uses
// (internal/plan/lifecycle.go) — instead of calling plan.SetStatus directly:
// a browser Approve click and the CLI verb are one mechanism, never two. The
// HTTP response shape and the approved-channel signal must stay identical to
// the prior SetStatus-based behavior.
func TestReviewLoop_ApproveReroutesThroughLifecycleEngine(t *testing.T) {
	dir := t.TempDir()
	seed := plan.Event{PlanID: "pln_reviewtest00000000001", Kind: plan.EventCreated, Status: plan.PlanStatusDraft}
	if err := plan.AppendEvent(context.Background(), dir, seed); err != nil {
		t.Fatalf("seed created event: %v", err)
	}

	srv, _, approved := newTestReviewServer(t, dir)
	if code := reviewPOST(t, srv.URL+"/approve", "secret", ""); code != http.StatusOK {
		t.Fatalf("approve should be 200, got %d", code)
	}
	select {
	case <-approved:
	default:
		t.Error("approve must signal the approved channel, exactly like the prior SetStatus-based handler")
	}

	events, err := plan.LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 2 || events[1].Kind != plan.EventApproved || events[1].Status != plan.PlanStatusApproved {
		t.Fatalf("want an approved event appended via the lifecycle engine, got %+v", events)
	}
}

// TestReviewLoop_ApproveTwiceStillReturns200 verifies a duplicate/stale
// Approve click (changed:false under the hood) still reports success — the
// handler intentionally ignores AppendPlanEvent's changed return value,
// matching SetStatus's prior always-succeeds contract.
func TestReviewLoop_ApproveTwiceStillReturns200(t *testing.T) {
	dir := t.TempDir()
	seed := plan.Event{PlanID: "pln_reviewtest00000000002", Kind: plan.EventCreated, Status: plan.PlanStatusDraft}
	if err := plan.AppendEvent(context.Background(), dir, seed); err != nil {
		t.Fatalf("seed created event: %v", err)
	}
	srv, _, _ := newTestReviewServer(t, dir)

	if code := reviewPOST(t, srv.URL+"/approve", "secret", ""); code != http.StatusOK {
		t.Fatalf("first approve should be 200, got %d", code)
	}
	if code := reviewPOST(t, srv.URL+"/approve", "secret", ""); code != http.StatusOK {
		t.Fatalf("second (no-op) approve should still be 200, got %d", code)
	}

	events, err := plan.LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("a duplicate approve must not append a duplicate event: got %d events, want 2", len(events))
	}
}

// TestReviewLoop_RejectsBadJSON verifies a malformed round is a 400, not a panic
// or silent accept.
func TestReviewLoop_RejectsBadJSON(t *testing.T) {
	dir := t.TempDir()
	srv, _, _ := newTestReviewServer(t, dir)
	if code := reviewPOST(t, srv.URL+"/feedback", "secret", "{not json"); code != http.StatusBadRequest {
		t.Errorf("malformed body should be 400, got %d", code)
	}
}

// TestBroadcaster_FansOut verifies a broadcast reaches every subscriber and a
// busy subscriber never blocks the broadcaster. Failure prevented: the live
// reload stalls because one slow SSE client wedges the fan-out.
func TestBroadcaster_FansOut(t *testing.T) {
	b := newBroadcaster()
	a := b.subscribe()
	c := b.subscribe()
	b.broadcast()
	for i, ch := range []chan struct{}{a, c} {
		select {
		case <-ch:
		default:
			t.Errorf("subscriber %d did not receive the broadcast", i)
		}
	}
	// busy subscriber (buffer already full) must not block broadcast
	b.broadcast()
	b.broadcast() // would block if broadcast didn't drop on full
	b.unsubscribe(a)
	b.unsubscribe(c)
}

// --- the live server's notify edge: a submit reaches the authoring coworker ---

// newNotifyingReviewServer wires the live handler with a real project root (so
// the notify path can enqueue) and a plan dir stamped with an authoring agent.
func newNotifyingReviewServer(t *testing.T) (srv *httptest.Server, gitRoot, planDir string) {
	t.Helper()
	gitRoot = t.TempDir()
	planDir = filepath.Join(gitRoot, "plandir")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestPlanMeta(t, planDir, &plan.Provenance{AgentID: "Ox#5", AgentType: "claude-code"})
	h := liveReviewHandler(gitRoot, "p", planDir, "http://x", "secret", newBroadcaster(), make(chan int, 8), make(chan struct{}, 1))
	srv = httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, gitRoot, planDir
}

// TestReviewLoop_FeedbackEnqueuesAuthorTask verifies a submitted round notifies
// the authoring coworker via a plan-feedback task carrying the slug.
// Failure prevented: feedback lands in the ledger but never reaches the agent.
func TestReviewLoop_FeedbackEnqueuesAuthorTask(t *testing.T) {
	srv, gitRoot, _ := newNotifyingReviewServer(t)
	if code := reviewPOST(t, srv.URL+"/feedback", "secret", `{"items":[{"anchor":"h1","status":"request-change","note":"x"}]}`); code != http.StatusOK {
		t.Fatalf("submit: %d", code)
	}
	tasks := activeTasks(t, gitRoot)
	if len(tasks) != 1 || tasks[0].Kind != agenttask.KindPlanFeedback || tasks[0].Payload["plan_slug"] != "p" {
		t.Errorf("submit must enqueue a plan-feedback task for slug p, got %+v", tasks)
	}
}

// TestReviewLoop_ReopenEnqueuesAuthorTask verifies reopening an item ALSO
// re-notifies — feedback raised after the coworker's session ended must not strand.
func TestReviewLoop_ReopenEnqueuesAuthorTask(t *testing.T) {
	srv, gitRoot, _ := newNotifyingReviewServer(t)
	if code := reviewPOST(t, srv.URL+"/reopen", "secret", `{"anchor":"h1","note":"again"}`); code != http.StatusOK {
		t.Fatalf("reopen: %d", code)
	}
	if n := len(activeTasks(t, gitRoot)); n != 1 {
		t.Errorf("reopen must enqueue a notify task, got %d", n)
	}
}

// TestReviewLoop_FeedbackPersistsWhenNotifyIsNoop verifies the round is saved and
// signaled even when there's no authoring coworker to notify (an unlinked plan):
// the ledger write (step N-1) survives the notify (step N) being a no-op.
func TestReviewLoop_FeedbackPersistsWhenNotifyIsNoop(t *testing.T) {
	gitRoot := t.TempDir()
	planDir := filepath.Join(gitRoot, "plandir")
	if err := os.MkdirAll(planDir, 0o755); err != nil { // NO meta.json → unlinked
		t.Fatal(err)
	}
	rounds := make(chan int, 4)
	h := liveReviewHandler(gitRoot, "p", planDir, "http://x", "secret", newBroadcaster(), rounds, make(chan struct{}, 1))
	srv := httptest.NewServer(h)
	defer srv.Close()

	if code := reviewPOST(t, srv.URL+"/feedback", "secret", `{"items":[{"anchor":"h1","status":"flag","note":"y"}]}`); code != http.StatusOK {
		t.Fatalf("submit: %d", code)
	}
	if sets, _ := plan.LoadAllFeedback(planDir); len(sets) != 1 {
		t.Errorf("round must persist even with no agent to notify, got %d", len(sets))
	}
	select {
	case <-rounds:
	default:
		t.Error("round must still signal")
	}
	if agenttask.QueueExists(gitRoot) {
		t.Error("an unlinked plan must not enqueue a task")
	}
}

// TestReviewLoop_ConcurrentFeedbackDedupes verifies a burst of simultaneous
// submits collapses to ONE active notify task (dedup is transactional) — the
// coworker isn't flooded with duplicate chores.
func TestReviewLoop_ConcurrentFeedbackDedupes(t *testing.T) {
	srv, gitRoot, planDir := newNotifyingReviewServer(t)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"items":[{"anchor":"h%d","status":"comment","note":"n"}]}`, i)
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/feedback", bytes.NewBufferString(body))
			req.Header.Set("X-Review-Token", "secret")
			if resp, err := http.DefaultClient.Do(req); err == nil {
				resp.Body.Close()
			}
		}(i)
	}
	wg.Wait()
	if n := len(activeTasks(t, gitRoot)); n != 1 {
		t.Errorf("concurrent submits must dedup to one task, got %d", n)
	}
	if sets, _ := plan.LoadAllFeedback(planDir); len(sets) < 1 {
		t.Errorf("submits must persist their rounds, got %d", len(sets))
	}
}
