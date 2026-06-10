package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sageox/ox/internal/plan"
)

// TestReviewHandler_TokenGatedSubmit verifies the ephemeral review server serves
// the plan, rejects a bad token, and on a valid token writes the round to the
// ledger dir and signals the waiting agent. Failure prevented: any local process
// could POST feedback, or a submit is lost instead of reaching the agent.
func TestReviewHandler_TokenGatedSubmit(t *testing.T) {
	dir := t.TempDir()
	done := make(chan plan.FeedbackSet, 1)
	html := []byte("<html>plan</html>")
	srv := httptest.NewServer(reviewHandler(html, "secret", "my-plan", "", dir, done))
	defer srv.Close()

	// GET / serves the plan
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := new(bytes.Buffer)
	_, _ = body.ReadFrom(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(body.Bytes(), html) {
		t.Error("GET / should serve the rendered plan")
	}

	payload := `{"items":[{"anchor":"h1","status":"request-change","note":"bound it"}]}`

	// bad token → 403, nothing written
	bad, _ := http.Post(srv.URL+"/feedback", "application/json", bytes.NewBufferString(payload))
	if bad.StatusCode != http.StatusForbidden {
		t.Errorf("missing token should be 403, got %d", bad.StatusCode)
	}
	bad.Body.Close()
	if sets, _ := plan.LoadAllFeedback(dir); len(sets) != 0 {
		t.Error("a forbidden submit must not write feedback")
	}

	// good token → 200, written + signaled
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/feedback", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Review-Token", "secret")
	ok, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("valid submit should be 200, got %d", ok.StatusCode)
	}
	ok.Body.Close()

	select {
	case set := <-done:
		if set.Slug != "my-plan" || len(set.Items) != 1 {
			t.Errorf("done signal carried wrong set: %+v", set)
		}
	default:
		t.Error("valid submit must signal the waiting agent")
	}
	if sets, _ := plan.LoadAllFeedback(dir); len(sets) != 1 {
		t.Errorf("valid submit must persist one round, got %d", len(sets))
	}
}

// TestReviewHandler_RejectsBadJSON verifies a malformed body is a 400, not a
// panic or a silent accept.
func TestReviewHandler_RejectsBadJSON(t *testing.T) {
	dir := t.TempDir()
	done := make(chan plan.FeedbackSet, 1)
	srv := httptest.NewServer(reviewHandler([]byte("x"), "tok", "p", "", dir, done))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/feedback", bytes.NewBufferString("{not json"))
	req.Header.Set("X-Review-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed body should be 400, got %d", resp.StatusCode)
	}
}
