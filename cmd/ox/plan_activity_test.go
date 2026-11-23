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
	"regexp"
	"sync"
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

// planActivityWireBody is the endpoint's documented request shape for
// POST .../plans/{plan_id}/activity, spelled out independently of
// api.PlanActivityNotification so a mistyped json tag cannot pass by decoding
// into the type that produced it.
type planActivityWireBody struct {
	Event  string `json:"event"`
	Status string `json:"status"`
	RepoID string `json:"repo_id"`
	Dir    string `json:"dir"`
}

// planDirNameRe is the plan folder name ox writes: a YYYY-MM-DD date, then a
// slug of letters, digits, dashes and underscores. The ping's dir must be that
// name, not a path, or SageOx cannot use it to find the plan.
var planDirNameRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-[A-Za-z0-9](?:[A-Za-z0-9_-]*[A-Za-z0-9])?$`)

// newPlanActivityEventRepo is newPlanActivityRepo with a server that records
// every notification body, in order, answering each with status. Bodies are
// decoded strictly: the endpoint answers an unknown field with a 400, so it is
// a test error here. Returns the repo root and an accessor for the
// recorded bodies.
func newPlanActivityEventRepo(t *testing.T, teamID string, status int) (string, func() []planActivityWireBody) {
	t.Helper()
	root := newPlanCaptureTestRepo(t)

	var mu sync.Mutex
	var bodies []planActivityWireBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body planActivityWireBody
		dec := json.NewDecoder(r.Body)
		// strict-reader-ok: asserting what ox itself writes, against a server
		// that rejects unknown fields.
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			t.Errorf("plan activity body does not decode strictly into the endpoint's shape: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	cfg, err := json.Marshal(map[string]any{
		"config_version": "2",
		"repo_id":        captureTestRepoID,
		"endpoint":       srv.URL,
		"team_id":        teamID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sageox", "config.json"), cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveTokenForEndpoint(srv.URL, &auth.StoredToken{AccessToken: "test-token"}); err != nil {
		t.Fatalf("save token: %v", err)
	}

	return root, func() []planActivityWireBody {
		mu.Lock()
		defer mu.Unlock()
		return append([]planActivityWireBody(nil), bodies...)
	}
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

// TestSavePlanArtifacts_NotifiesCreatedThenRevised is the end-to-end proof that
// the third defect is fixed and correctly wired: a plan save reaches the
// server's activity index at all (it previously did not — only the lifecycle
// verbs notified), and the event it reports is the one this save performed.
//
// Asserts the request BODY, not just a count: reporting every revision as a
// creation would misstate every plan's age to the index while a
// count-only assertion stayed green.
//
// It also asserts WHERE the plan lives: repo_id from the project config and
// the plan folder name. Without them SageOx has to find a just-saved plan on
// its own, and its link shows "not found" until it does.
//
// Red-first: delete the postPlanActivityBestEffort call in savePlanArtifacts →
// zero requests; thread plan.EventCreated unconditionally → the second body
// says "created"; drop RepoID/Dir from the notification → both bodies carry
// empty location fields.
func TestSavePlanArtifacts_NotifiesCreatedThenRevised(t *testing.T) {
	root, notified := newPlanActivityEventRepo(t, "team_abc", http.StatusAccepted)
	initPlanTestLedger(t, root)
	t.Setenv("SAGEOX_AGENT_ID", "")

	in := plan.Input{Raw: "# Notify Kind Wiring\n"}
	firstDir := savePlanArtifacts(root, in, plan.Result{}, nil, "")
	if firstDir == "" {
		t.Fatal("first save produced no plan dir")
	}
	secondDir := savePlanArtifacts(root, in, plan.Result{}, nil, "")
	if secondDir == "" {
		t.Fatal("second save produced no plan dir")
	}

	got := notified()
	wantEvents := []string{"created", "revised"}
	if len(got) != len(wantEvents) {
		t.Fatalf("notified %d time(s) (%+v), want events %v — a plan save that notifies nothing is invisible in the console until the next ledger sweep", len(got), got, wantEvents)
	}
	savedDirs := []string{firstDir, secondDir}
	for i, body := range got {
		if body.Event != wantEvents[i] {
			t.Errorf("notification %d event = %q, want %q", i, body.Event, wantEvents[i])
		}
		if body.RepoID != captureTestRepoID {
			t.Errorf("notification %d repo_id = %q, want the project config's %q", i, body.RepoID, captureTestRepoID)
		}
		if want := filepath.Base(savedDirs[i]); body.Dir != want {
			t.Errorf("notification %d dir = %q, want the saved plan's folder %q", i, body.Dir, want)
		}
		if !planDirNameRe.MatchString(body.Dir) {
			t.Errorf("notification %d dir = %q is not a plan folder name (%s)", i, body.Dir, planDirNameRe)
		}
	}
}

// TestSavePlanArtifacts_ServerRejectsNotificationSaveSucceeds pins the
// best-effort contract on the failure side: a server that 400s the ping (an
// older server, or a field it does not accept) must never fail or roll back the save.
// The notification is advisory; the plan on disk is the product.
func TestSavePlanArtifacts_ServerRejectsNotificationSaveSucceeds(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Ledger git commit + push")
	}
	root, notified := newPlanActivityEventRepo(t, "team_abc", http.StatusBadRequest)
	initPlanTestLedger(t, root)
	t.Setenv("SAGEOX_AGENT_ID", "")

	dir := savePlanArtifacts(root, plan.Input{Raw: "# Rejected Ping\n"}, plan.Result{}, nil, "")
	if dir == "" {
		t.Fatal("save produced no plan dir — a rejected advisory ping must not fail the save")
	}

	// Positive control: without a request the 400 path was never exercised.
	if got := notified(); len(got) != 1 {
		t.Fatalf("server received %d notification(s), want exactly 1 — the 400 path was not exercised", len(got))
	}

	events, err := plan.LoadEvents(dir)
	if err != nil {
		t.Fatalf("load events after rejected ping: %v", err)
	}
	if len(events) == 0 || events[len(events)-1].Kind != plan.EventCreated {
		t.Errorf("events after rejected ping = %+v, want a trailing %q event on disk", events, plan.EventCreated)
	}
}
