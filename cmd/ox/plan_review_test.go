package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sageox/ox/internal/agenttask"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/plan"
	"github.com/spf13/cobra"
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
	code, _ := reviewPOSTBody(t, url, token, body)
	return code
}

// reviewPOSTBody is reviewPOST plus the response body, for handlers (like
// /feedback and /reopen) whose JSON carries more than the bare {"ok":true}.
func reviewPOSTBody(t *testing.T, url, token, body string) (int, string) {
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
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestRenderSavedReviewPage_PreservesLegacyAuthoredHTML guards the regression
// where --no-serve and live review replaced a purpose-built architecture page
// with the generic markdown template because old metadata omitted primary=html.
func TestRenderSavedReviewPage_PreservesLegacyAuthoredHTML(t *testing.T) {
	dir := t.TempDir()
	authored := `<!doctype html><html><head><title>Attest map</title></head><body><div id="authored-system-map" class="hero-map">visual flow</div><details><summary>Implementation notes</summary><p>files</p></details></body></html>`
	if err := os.WriteFile(filepath.Join(dir, "plan.html"), []byte(authored), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"topic":"Attest map","slug":"attest-map"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := renderSavedReviewPage("", "attest-map", dir, plan.Parse("# Attest map\n\nprose fallback"), plan.Result{}, nil, plan.RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`id="authored-system-map"`)) {
		t.Fatal("review discarded the legacy authored system map")
	}
	if bytes.Contains(out, []byte(`<div class="brand">OX · PLAN</div>`)) {
		t.Fatal("review regenerated the markdown template instead of preserving authored HTML")
	}
}

func TestRenderSavedReviewPage_RegeneratesKnownMarkdownProjection(t *testing.T) {
	dir := t.TempDir()
	generated := `<!doctype html><html><body><div class="brand">OX · PLAN</div><div class="eyebrow">SageOx · enriched plan</div><div id="stale">old projection</div></body></html>`
	if err := os.WriteFile(filepath.Join(dir, "plan.html"), []byte(generated), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"topic":"Quick plan","slug":"quick-plan"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := renderSavedReviewPage("", "quick-plan", dir, plan.Parse("# Quick plan\n\n## Current\nFresh markdown projection."), plan.Result{}, nil, plan.RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte(`id="stale"`)) || !bytes.Contains(out, []byte("Fresh markdown projection")) {
		t.Fatal("a known generated render must be refreshed from canonical markdown")
	}
}

func TestRenderSavedReviewPage_PreservesProvenanceSessionLink(t *testing.T) {
	projectRoot := createInitializedProjectWithConfig(t, &config.ProjectConfig{Endpoint: "https://sageox.example"})
	sessionID := "ses_01890a5d-ac96-774b-bcce-b302099a8057"
	for _, tc := range []struct {
		name     string
		authored string
		primary  string
	}{
		{name: "authored", authored: `<!doctype html><html><body><div class="hero-map">System topology</div></body></html>`, primary: plan.PrimaryHTML},
		{name: "markdown primary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.authored != "" {
				if err := os.WriteFile(filepath.Join(dir, "plan.html"), []byte(tc.authored), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			meta := fmt.Sprintf(`{"topic":"Saved","slug":"saved","primary":%q,"provenance":{"session_id":%q}}`, tc.primary, sessionID)
			if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
				t.Fatal(err)
			}
			out, err := renderSavedReviewPage(projectRoot, "saved", dir, plan.Parse("# Saved\n\nFallback plan."), plan.Result{}, nil, plan.RenderOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(out, []byte("https://sageox.example/c/"+sessionID)) {
				t.Fatal("saved review page lost its provenance conversation link")
			}
		})
	}
}

func TestRunPlanRenderSavedHTML_PreservesProvenanceSessionLink(t *testing.T) {
	projectRoot := createInitializedProjectWithConfig(t, &config.ProjectConfig{Endpoint: "https://sageox.example"})
	planDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(planDir, "plan.html"), []byte(`<!doctype html><html><body><div class="hero-map">System topology</div></body></html>`), 0o644); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(t.TempDir(), "render.html")
	sessionID := "ses_01890a5d-ac96-774b-bcce-b302099a8057"
	cmd := &cobra.Command{}
	if err := runPlanRenderSavedHTML(cmd, projectRoot, "legacy-plan", plan.PlanInfo{Dir: planDir}, plan.Result{}, plan.Meta{
		Provenance: &plan.Provenance{SessionID: sessionID},
	}, outputPath, false, false); err != nil {
		t.Fatal(err)
	}
	rendered, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rendered, []byte("https://sageox.example/c/"+sessionID)) {
		t.Fatal("saved authored plan lost its provenance conversation link")
	}
}

// TestReviewLoop_ServesAllowlistedCompanions verifies the /companions/ route:
// a companion listed in meta.json is served byte-for-byte, while an unlisted
// file in the same subdir and any non-basename path 404 — the allowlist is
// meta.Companions, never a directory listing. Failure prevented: the review
// loop rendering a companion card whose link dead-ends, or the route serving
// arbitrary plan-dir files.
func TestReviewLoop_ServesAllowlistedCompanions(t *testing.T) {
	planDir := t.TempDir()
	compDir := filepath.Join(planDir, plan.CompanionsDir)
	if err := os.MkdirAll(compDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(compDir, "deep-dive.html"), []byte("<html>rich</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(compDir, "unlisted.html"), []byte("<html>no</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	// meta.json allowlists only deep-dive.html
	if err := os.WriteFile(filepath.Join(planDir, "meta.json"),
		[]byte(`{"topic":"t","slug":"p","companions":["deep-dive.html"]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	srv, _, _ := newTestReviewServer(t, planDir)

	get := func(path string) (int, string) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return resp.StatusCode, buf.String()
	}

	if code, body := get("/companions/deep-dive.html"); code != http.StatusOK || body != "<html>rich</html>" {
		t.Errorf("allowlisted companion: code=%d body=%q", code, body)
	}
	for _, path := range []string{"/companions/unlisted.html", "/companions/", "/companions/../meta.json"} {
		if code, _ := get(path); code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, code)
		}
	}
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

// TestReviewLoop_MissingAnchorIsBadRequest verifies /accept and /reopen both
// reject a body with no anchor as 400, not a panic or a silent no-op.
func TestReviewLoop_MissingAnchorIsBadRequest(t *testing.T) {
	dir := t.TempDir()
	srv, _, _ := newTestReviewServer(t, dir)
	for _, path := range []string{"/accept", "/reopen"} {
		if code := reviewPOST(t, srv.URL+path, "secret", "{}"); code != http.StatusBadRequest {
			t.Errorf("%s with no anchor should be 400, got %d", path, code)
		}
	}
}

// TestReviewLoop_FeedbackAndReopenSurfaceSaveFailure verifies /feedback and
// /reopen both report 500 — not a false 200 — when the underlying
// plan.SaveFeedback write fails, so a human is never told a round was saved
// when it wasn't.
func TestReviewLoop_FeedbackAndReopenSurfaceSaveFailure(t *testing.T) {
	dir := t.TempDir()
	// A regular file where SaveFeedback expects to MkdirAll a "feedback"
	// subdirectory fails deterministically on every platform (unlike chmod,
	// which no-ops on Windows).
	if err := os.WriteFile(filepath.Join(dir, "feedback"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, _, _ := newTestReviewServer(t, dir)
	if code := reviewPOST(t, srv.URL+"/feedback", "secret", `{"items":[{"anchor":"h1","status":"comment"}]}`); code != http.StatusInternalServerError {
		t.Errorf("/feedback with a blocked feedback dir should be 500, got %d", code)
	}
	if code := reviewPOST(t, srv.URL+"/reopen", "secret", `{"anchor":"h1"}`); code != http.StatusInternalServerError {
		t.Errorf("/reopen with a blocked feedback dir should be 500, got %d", code)
	}
}

// TestReviewLoop_AcceptSurfacesAppendResolutionFailure mirrors the above for
// /accept's plan.AppendResolution write, which MkdirAlls the same "feedback"
// subdirectory.
func TestReviewLoop_AcceptSurfacesAppendResolutionFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "feedback"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, _, _ := newTestReviewServer(t, dir)
	if code := reviewPOST(t, srv.URL+"/accept", "secret", `{"anchor":"h1"}`); code != http.StatusInternalServerError {
		t.Errorf("/accept with a blocked feedback dir should be 500, got %d", code)
	}
}

// TestReviewLoop_ApproveSurfacesAppendPlanEventFailure verifies /approve
// reports 500 — not a false "approved" — when the plan has no recorded
// history to append the approval event to.
func TestReviewLoop_ApproveSurfacesAppendPlanEventFailure(t *testing.T) {
	dir := t.TempDir() // no plan.Save / AppendEvent ever ran here — no history
	srv, _, _ := newTestReviewServer(t, dir)
	if code := reviewPOST(t, srv.URL+"/approve", "secret", "{}"); code != http.StatusInternalServerError {
		t.Errorf("/approve with no plan history should be 500, got %d", code)
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

// TestReviewLoop_FeedbackReportsNotifiedTrueOnSuccess verifies the /feedback
// response carries notified:true when the enqueue succeeds, so the browser
// only ever warns on an actual failure, not on every round.
func TestReviewLoop_FeedbackReportsNotifiedTrueOnSuccess(t *testing.T) {
	srv, _, _ := newNotifyingReviewServer(t)
	code, body := reviewPOSTBody(t, srv.URL+"/feedback", "secret", `{"items":[{"anchor":"h1","status":"comment","note":"x"}]}`)
	if code != http.StatusOK {
		t.Fatalf("submit: %d", code)
	}
	if !strings.Contains(body, `"notified":true`) {
		t.Errorf("response must report notified:true on a successful enqueue, got %q", body)
	}
}

// TestReviewLoop_FeedbackReportsNotifiedFalseOnEnqueueFailure verifies the
// /feedback and /reopen responses tell the browser when the authoring
// coworker could NOT be notified, so the review page can warn the human
// instead of looking like the loop is fully closed.
// Failure prevented: a human submits feedback, the round is saved, the
// browser shows success, and the human never learns the coworker was never
// told — which is exactly what ox#968 reported (silently swallowed at Debug).
func TestReviewLoop_FeedbackReportsNotifiedFalseOnEnqueueFailure(t *testing.T) {
	gitRoot := t.TempDir()
	planDir := filepath.Join(gitRoot, "plandir")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestPlanMeta(t, planDir, &plan.Provenance{AgentID: "Ox#5", AgentType: "claude-code"})
	// A regular file at .sageox makes agenttask.NewStore's MkdirAll fail on
	// every attempt (structural, not transient) — a deterministic enqueue
	// failure without touching the plan's own files.
	if err := os.WriteFile(filepath.Join(gitRoot, ".sageox"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := liveReviewHandler(gitRoot, "p", planDir, "http://x", "secret", newBroadcaster(), make(chan int, 8), make(chan struct{}, 1))
	srv := httptest.NewServer(h)
	defer srv.Close()

	code, body := reviewPOSTBody(t, srv.URL+"/feedback", "secret", `{"items":[{"anchor":"h1","status":"flag","note":"y"}]}`)
	if code != http.StatusOK {
		t.Fatalf("submit: %d", code)
	}
	if !strings.Contains(body, `"notified":false`) {
		t.Errorf("response must report notified:false when enqueue fails, got %q", body)
	}
	if sets, _ := plan.LoadAllFeedback(planDir); len(sets) != 1 {
		t.Errorf("the round must still persist even though notify failed, got %d", len(sets))
	}

	code, body = reviewPOSTBody(t, srv.URL+"/reopen", "secret", `{"anchor":"h1","note":"again"}`)
	if code != http.StatusOK {
		t.Fatalf("reopen: %d", code)
	}
	if !strings.Contains(body, `"notified":false`) {
		t.Errorf("reopen response must report notified:false when enqueue fails, got %q", body)
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
