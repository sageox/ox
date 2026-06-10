package plan

import (
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
