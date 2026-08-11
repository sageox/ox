package main

// plan_activity_test.go covers postPlanActivityBestEffort's off-by-default
// gate (see plan_activity.go): the CLIENT half of the caller-driven
// plan_id -> plan index (bead sageox-gqgkg).
//
// These tests assert the EFFECT — how many HTTP requests reached a server —
// rather than how long the call took. The previous versions asserted only
// "returned within 2 seconds", which is a timing proxy that proves nothing:
// a machine with no route to the endpoint fails Dial in milliseconds, so
// deleting the gate entirely left both tests green, while a loaded CI runner
// could flake them with the gate intact. Passing for the wrong reason and
// failing for a non-reason is the worst quadrant a test can occupy.
//
// The positive control is not optional. Without it a "zero requests" assertion
// also passes when the notification is broken for some unrelated reason, and
// the negative tests certify nothing.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/plan"
)

// newPlanActivityRepo builds an isolated repo whose .sageox/config.json points
// at a local httptest server, optionally with a team id and a saved auth token.
// Returns the repo root and the probe.
func newPlanActivityRepo(t *testing.T, teamID string, authenticated bool) (string, *atomic.Int32) {
	t.Helper()
	root := newPlanCaptureTestRepo(t)

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	cfg := map[string]any{
		"config_version": "2",
		"repo_id":        captureTestRepoID,
		"endpoint":       srv.URL,
	}
	if teamID != "" {
		cfg["team_id"] = teamID
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sageox", "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	if authenticated {
		if err := auth.SaveTokenForEndpoint(srv.URL, &auth.StoredToken{AccessToken: "test-token"}); err != nil {
			t.Fatalf("save token: %v", err)
		}
	}
	return root, &hits
}

// seedPlanWithEvents saves a real plan so LoadEvents/Fold can resolve a plan id.
func seedPlanWithEvents(t *testing.T, root, topic string) string {
	t.Helper()
	dir, _, err := plan.Save(root, plan.Input{Raw: "# " + topic + "\n"}, plan.Result{}, nil, plan.Meta{Topic: topic})
	if err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	return dir
}

// TestPostPlanActivityBestEffort_NoTeamIDAttemptsNoRequest proves the
// off-by-default gate by effect: a project with no team_id must not reach the
// network at all.
//
// Red-first: delete the `cfg.TeamID == ""` early return in plan_activity.go and
// this goes red, while the old 2s timing assertion stayed green.
func TestPostPlanActivityBestEffort_NoTeamIDAttemptsNoRequest(t *testing.T) {
	root, hits := newPlanActivityRepo(t, "", true)
	dir := seedPlanWithEvents(t, root, "No team id")

	postPlanActivityBestEffort(root, dir, plan.EventCreated)

	if got := hits.Load(); got != 0 {
		t.Errorf("server received %d request(s), want 0 — an unlinked project must never attempt a network call", got)
	}
}

// TestPostPlanActivityBestEffort_UnauthenticatedAttemptsNoRequest is the second
// half of the gate: linked to a team, but no token on this machine.
func TestPostPlanActivityBestEffort_UnauthenticatedAttemptsNoRequest(t *testing.T) {
	root, hits := newPlanActivityRepo(t, "team_abc", false)
	dir := seedPlanWithEvents(t, root, "No token")

	postPlanActivityBestEffort(root, dir, plan.EventCreated)

	if got := hits.Load(); got != 0 {
		t.Errorf("server received %d request(s), want 0 — an unauthenticated machine must skip before any network call", got)
	}
}

// TestPostPlanActivityBestEffort_EmptyPlanDirAttemptsNoRequest covers the third
// gate: a plan dir with no events.jsonl has no plan id to index against.
//
// The old version of this test claimed to cover exactly this and never reached
// the code — coverage profiling showed it returning at the project-config gate,
// byte-identical to the previous test, because its fixture had no .sageox at
// all. A fully-configured, authenticated project is what makes the assertion
// meaningful.
func TestPostPlanActivityBestEffort_EmptyPlanDirAttemptsNoRequest(t *testing.T) {
	root, hits := newPlanActivityRepo(t, "team_abc", true)

	postPlanActivityBestEffort(root, t.TempDir(), plan.EventWorked)

	if got := hits.Load(); got != 0 {
		t.Errorf("server received %d request(s), want 0 — no events.jsonl means no plan_id to report", got)
	}
}

// TestPostPlanActivityBestEffort_ConfiguredProjectNotifies is the POSITIVE
// CONTROL. Without it every "zero requests" assertion above passes vacuously —
// including in a world where the notification never works at all.
func TestPostPlanActivityBestEffort_ConfiguredProjectNotifies(t *testing.T) {
	root, hits := newPlanActivityRepo(t, "team_abc", true)
	dir := seedPlanWithEvents(t, root, "Fully configured notify")

	postPlanActivityBestEffort(root, dir, plan.EventCreated)

	if got := hits.Load(); got != 1 {
		t.Fatalf("server received %d request(s), want exactly 1 — the gates are supposed to be OPEN here", got)
	}
}
