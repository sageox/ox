package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestParseFeedback_ValidatesStatusAndSlug verifies bad status is rejected, blank
// defaults to comment, and a path-y slug is refused (the one untrusted input that
// flows toward filesystem paths). Failure prevented: a crafted export steers
// writes outside the plans tree, or a typo'd verdict is silently kept.
func TestParseFeedback_ValidatesStatusAndSlug(t *testing.T) {
	if _, err := ParseFeedback([]byte(`{"items":[{"anchor":"a","status":"approveee"}]}`)); err == nil {
		t.Error("expected error on unknown status")
	}
	set, err := ParseFeedback([]byte(`{"items":[{"anchor":"a","status":""}]}`))
	if err != nil || set.Items[0].Status != FeedbackComment {
		t.Errorf("blank status should default to comment, got %v / %q", err, set.Items[0].Status)
	}
	for _, bad := range []string{"../escape", "a/b", "..", "x/../y"} {
		js := `{"slug":"` + bad + `","items":[{"anchor":"a","status":"flag"}]}`
		if _, err := ParseFeedback([]byte(js)); err == nil {
			t.Errorf("path-y slug %q should be rejected", bad)
		}
	}
	if _, err := ParseFeedback([]byte(`{"slug":"ok-slug","items":[{"anchor":"a","status":"flag"}]}`)); err != nil {
		t.Errorf("clean slug should pass: %v", err)
	}
}

// TestReviewLifecycle exercises the full loop: a round is raised, the agent
// resolves it (addressed+commit), then the human re-raises it (reopen), and the
// agent verifies. Failure prevented: resolution state doesn't track, or a
// re-raised item stays closed.
func TestReviewLifecycle(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

	// round 1: two items
	if _, err := SaveFeedback(dir, FeedbackSet{Slug: "p", Items: []FeedbackItem{
		{Anchor: "h1", Section: "Approach", Label: "use a cache", Status: FeedbackRequestChange, Note: "bound it"},
		{Anchor: "h2", Section: "Risks", Label: "CDN", Status: FeedbackApprove},
	}}, t0); err != nil {
		t.Fatalf("save round1: %v", err)
	}

	items, _ := AssembleReview(dir)
	if openCount(items) != 1 { // approve is not actionable-open
		t.Fatalf("expected 1 open actionable item, got %d", openCount(items))
	}

	// agent addresses h1
	if err := AppendResolution(dir, Resolution{Anchor: "h1", State: ResolutionAddressed, Commit: "abc123", Note: "added LRU bound"}, t0.Add(time.Hour)); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	items, _ = AssembleReview(dir)
	if openCount(items) != 0 {
		t.Errorf("after addressing, expected 0 open, got %d", openCount(items))
	}
	if mi := find(items, "h1"); mi == nil || mi.Open || mi.Resolution == nil || mi.Resolution.Commit != "abc123" {
		t.Errorf("h1 should be closed with commit linkage, got %+v", mi)
	}

	// human re-raises h1 in a later round → reopen
	if _, err := SaveFeedback(dir, FeedbackSet{Slug: "p", Items: []FeedbackItem{
		{Anchor: "h1", Section: "Approach", Label: "use a cache", Status: FeedbackRequestChange, Note: "still unbounded on path X"},
	}}, t0.Add(2*time.Hour)); err != nil {
		t.Fatalf("save round2: %v", err)
	}
	items, _ = AssembleReview(dir)
	if mi := find(items, "h1"); mi == nil || !mi.Open {
		t.Errorf("re-raised item must reopen, got %+v", mi)
	}

	// agent verifies later
	if err := AppendResolution(dir, Resolution{Anchor: "h1", State: ResolutionVerified, Note: "now bounded everywhere"}, t0.Add(3*time.Hour)); err != nil {
		t.Fatalf("verify: %v", err)
	}
	items, _ = AssembleReview(dir)
	if mi := find(items, "h1"); mi == nil || mi.Open || mi.Resolution.State != ResolutionVerified {
		t.Errorf("h1 should be verified+closed, got %+v", mi)
	}

	digest := FeedbackDigest(items)
	if !strings.Contains(digest, "Review:") || !strings.Contains(digest, "verified") {
		t.Errorf("digest missing lifecycle summary: %s", digest)
	}
}

// TestFeedbackDigest_ListsOpenWithAnchor verifies open items expose the anchor id
// (so the agent knows what to pass to `resolve`) and the resolve hint appears.
func TestFeedbackDigest_ListsOpenWithAnchor(t *testing.T) {
	items := []MergedItem{
		{FeedbackItem: FeedbackItem{Anchor: "habc", Section: "Approach", Label: "cache", Status: FeedbackRequestChange, Note: "bound it"}, Open: true},
	}
	d := FeedbackDigest(items)
	if !strings.Contains(d, "(habc)") {
		t.Errorf("open item must show its anchor id: %s", d)
	}
	if !strings.Contains(d, "feedback resolve <slug> <anchor>") {
		t.Errorf("digest should hint how to resolve: %s", d)
	}
}

// TestRenderHTML_CarriesReviewState verifies the render injects committed review
// state + the server endpoint/token when provided, and shows the summary strip.
func TestRenderHTML_CarriesReviewState(t *testing.T) {
	in := Parse("# T\n\n## Approach\n\nbody\n")
	review := []MergedItem{
		{FeedbackItem: FeedbackItem{Anchor: "h1", Section: "Approach", Label: "x", Status: FeedbackRequestChange}, Open: true},
		{FeedbackItem: FeedbackItem{Anchor: "h2", Section: "Approach", Label: "y", Status: FeedbackFlag},
			Resolution: &Resolution{Anchor: "h2", State: ResolutionAddressed, Commit: "abc"}, Open: false},
	}
	out, err := RenderHTMLOpts(in, Result{}, RenderOptions{Slug: "p", Review: review, ReviewEndpoint: "http://127.0.0.1:9/feedback", ReviewToken: "tok"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		`id="ox-review-state"`, // JSON island
		`data-review-endpoint="http://127.0.0.1:9/feedback"`,
		`data-review-token="tok"`,
		"rev-status",          // summary strip
		`"anchor":"h1"`,       // island carries items
		`"state":"addressed"`, // resolved state surfaced
	} {
		if !strings.Contains(s, want) {
			t.Errorf("render missing %q", want)
		}
	}
}

// --- B. Pull discovery: which saved plans still owe a human response ---

// savePlanWithFeedback saves a plan to the temp ledger and lays one feedback
// round on it, returning the plan dir — a small fixture so the discovery tests
// read like the loop they exercise.
func savePlanWithFeedback(t *testing.T, gitRoot, topic string, prov *Provenance, items []FeedbackItem) string {
	t.Helper()
	meta := Meta{Topic: topic, CreatedAt: time.Now().UTC(), Provenance: prov}
	dir, err := Save(gitRoot, Input{Raw: "# " + topic}, Result{}, nil, meta)
	if err != nil {
		t.Fatalf("save %q: %v", topic, err)
	}
	if len(items) > 0 {
		if _, err := SaveFeedback(dir, FeedbackSet{Slug: Slugify(topic), Items: items}, time.Now()); err != nil {
			t.Fatalf("feedback %q: %v", topic, err)
		}
	}
	return dir
}

// TestOpenFeedbackPlans_ListsOnlyPlansWithOpenItems verifies the PULL discovery
// path returns exactly the plans a human still owes a response on — open
// request-changes/flags/comments — and omits all-approved, fully-resolved, and
// feedback-free plans, carrying the authoring coworker type.
// Failure prevented: the discovery surface nags about settled plans or, worse,
// hides ones that need attention.
func TestOpenFeedbackPlans_ListsOnlyPlansWithOpenItems(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)
	const gitRoot = "/any" // ledgerResolver is overridden by withLedger

	savePlanWithFeedback(t, gitRoot, "Open plan", &Provenance{AgentID: "Ox#1", AgentType: "claude-code"},
		[]FeedbackItem{{Anchor: "a1", Status: FeedbackRequestChange, Note: "fix this"}})
	savePlanWithFeedback(t, gitRoot, "Approved plan", nil,
		[]FeedbackItem{{Anchor: "b1", Status: FeedbackApprove}}) // approvals close, not open
	cDir := savePlanWithFeedback(t, gitRoot, "Resolved plan", nil,
		[]FeedbackItem{{Anchor: "c1", Status: FeedbackRequestChange, Note: "do x"}})
	if err := AppendResolution(cDir, Resolution{Anchor: "c1", State: ResolutionAddressed, Commit: "sha"}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	savePlanWithFeedback(t, gitRoot, "Quiet plan", nil, nil) // no feedback at all

	open, err := OpenFeedbackPlans(gitRoot)
	if err != nil {
		t.Fatalf("OpenFeedbackPlans: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("want exactly 1 plan with open feedback, got %d: %+v", len(open), open)
	}
	if got := open[0]; got.Slug != "open-plan" || got.Open != 1 || got.AgentType != "claude-code" {
		t.Errorf("surfaced summary wrong: %+v", got)
	}
}

// TestOpenFeedbackPlans_ReRaisedCountsAsOpen verifies a resolved item the human
// re-raised (reopen) re-surfaces — the verify loop must not bury a re-opened item.
func TestOpenFeedbackPlans_ReRaisedCountsAsOpen(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)
	t0 := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	dir, err := Save("/any", Input{Raw: "# P"}, Result{}, nil, Meta{Topic: "Reopen plan", CreatedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SaveFeedback(dir, FeedbackSet{Slug: "reopen-plan", Items: []FeedbackItem{{Anchor: "h1", Status: FeedbackRequestChange, Note: "v1"}}}, t0); err != nil {
		t.Fatal(err)
	}
	if err := AppendResolution(dir, Resolution{Anchor: "h1", State: ResolutionAddressed, Commit: "sha"}, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := CountOpenFeedback(dir); n != 0 {
		t.Fatalf("addressed item should be closed, open=%d", n)
	}
	// reopen: a later round re-raises h1
	if _, err := SaveFeedback(dir, FeedbackSet{Slug: "reopen-plan", Items: []FeedbackItem{{Anchor: "h1", Status: FeedbackRequestChange, Note: "still broken"}}}, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := CountOpenFeedback(dir); n != 1 {
		t.Errorf("re-raised item must reopen, open=%d", n)
	}
	if open, _ := OpenFeedbackPlans("/any"); len(open) != 1 || open[0].Slug != "reopen-plan" {
		t.Errorf("re-raised plan must surface in discovery, got %+v", open)
	}
}

// TestOpenFeedbackPlans_FailOpenOnCorruptPlan verifies one unreadable plan dir
// doesn't sink the whole discovery sweep — a healthy open plan still surfaces.
// Failure prevented: a single corrupt feedback file blinds the human to ALL
// pending feedback.
func TestOpenFeedbackPlans_FailOpenOnCorruptPlan(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)
	savePlanWithFeedback(t, "/any", "Good plan", nil,
		[]FeedbackItem{{Anchor: "g1", Status: FeedbackFlag, Note: "look here"}})
	bad, err := Save("/any", Input{Raw: "# B"}, Result{}, nil, Meta{Topic: "Bad plan", CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bad, feedbackSubdir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, feedbackSubdir, "round-x.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	open, err := OpenFeedbackPlans("/any")
	if err != nil {
		t.Fatalf("discovery must not error on a corrupt plan: %v", err)
	}
	if len(open) != 1 || open[0].Slug != "good-plan" {
		t.Errorf("good plan must still surface despite a corrupt sibling, got %+v", open)
	}
}

func openCount(items []MergedItem) int {
	n := 0
	for _, it := range items {
		if it.Open && it.Status != FeedbackApprove {
			n++
		}
	}
	return n
}

func find(items []MergedItem, anchor string) *MergedItem {
	for i := range items {
		if items[i].Anchor == anchor {
			return &items[i]
		}
	}
	return nil
}
