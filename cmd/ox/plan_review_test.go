package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

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
