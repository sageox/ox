package plan

import (
	"strings"
	"testing"
	"time"
)

// These cover the multi-user review model: distinct reviewers on the same anchor
// are both preserved (not last-write-wins), conflicting verdicts are flagged
// contested, and concurrent same-instant submits never clobber each other.

func TestAssembleReview_MultiReviewerSameAnchorBothKept(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	if _, err := SaveFeedback(dir, FeedbackSet{Reviewer: "ryan", Items: []FeedbackItem{
		{Anchor: "h1", Section: "Auth", Label: "token bucket", Status: FeedbackRequestChange, Note: "per-IP too"},
	}}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveFeedback(dir, FeedbackSet{Reviewer: "sam", Items: []FeedbackItem{
		{Anchor: "h1", Section: "Auth", Label: "token bucket", Status: FeedbackApprove, Note: "fine as-is"},
	}}, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	items, err := AssembleReview(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items (one per reviewer on the same anchor), got %d", len(items))
	}
	got := map[string]FeedbackStatus{}
	for _, it := range items {
		if it.Reviewer == "" {
			t.Errorf("item missing reviewer attribution: %+v", it)
		}
		got[it.Reviewer] = it.Status
	}
	if got["ryan"] != FeedbackRequestChange || got["sam"] != FeedbackApprove {
		t.Errorf("reviewer marks not preserved distinctly: %+v", got)
	}
}

func TestAssembleReview_SingleReviewerStillCollapsesPerAnchor(t *testing.T) {
	// backward compatibility: with no reviewer set, a later mark on an anchor still
	// supersedes the earlier one (per-anchor), exactly as before multi-user.
	dir := t.TempDir()
	t0 := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	_, _ = SaveFeedback(dir, FeedbackSet{Items: []FeedbackItem{{Anchor: "h1", Status: FeedbackFlag, Note: "first"}}}, t0)
	_, _ = SaveFeedback(dir, FeedbackSet{Items: []FeedbackItem{{Anchor: "h1", Status: FeedbackRequestChange, Note: "second"}}}, t0.Add(time.Second))
	items, err := AssembleReview(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != FeedbackRequestChange || items[0].Note != "second" {
		t.Fatalf("single-reviewer collapse broke: %+v", items)
	}
}

func TestContestedAnchors_ConflictingVerdicts(t *testing.T) {
	items := []MergedItem{
		{FeedbackItem: FeedbackItem{Anchor: "h1", Status: FeedbackApprove, Reviewer: "sam"}, Open: true},
		{FeedbackItem: FeedbackItem{Anchor: "h1", Status: FeedbackRequestChange, Reviewer: "ryan"}, Open: true},
		{FeedbackItem: FeedbackItem{Anchor: "h2", Status: FeedbackApprove, Reviewer: "sam"}, Open: true},
		{FeedbackItem: FeedbackItem{Anchor: "h2", Status: FeedbackApprove, Reviewer: "ryan"}, Open: true},
	}
	c := ContestedAnchors(items)
	if !c["h1"] {
		t.Error("h1 (approve vs request-change) should be contested")
	}
	if c["h2"] {
		t.Error("h2 (two approvals) should NOT be contested")
	}
}

func TestContestedAnchors_ClosedItemsNeverContested(t *testing.T) {
	items := []MergedItem{
		{FeedbackItem: FeedbackItem{Anchor: "h1", Status: FeedbackApprove}, Open: false},
		{FeedbackItem: FeedbackItem{Anchor: "h1", Status: FeedbackRequestChange}, Open: false},
	}
	if len(ContestedAnchors(items)) != 0 {
		t.Error("closed items must not be contested")
	}
}

func TestSaveFeedback_ConcurrentSameInstantUniqueFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	p1, err := SaveFeedback(dir, FeedbackSet{Reviewer: "a", Items: []FeedbackItem{{Anchor: "h1", Status: FeedbackFlag}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := SaveFeedback(dir, FeedbackSet{Reviewer: "b", Items: []FeedbackItem{{Anchor: "h1", Status: FeedbackFlag}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if p1 == p2 {
		t.Fatalf("same-instant submits collided on filename: %s", p1)
	}
	sets, err := LoadAllFeedback(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 2 {
		t.Fatalf("want 2 rounds preserved, got %d", len(sets))
	}
}

func TestSaveFeedback_StampsReviewerOntoItems(t *testing.T) {
	dir := t.TempDir()
	if _, err := SaveFeedback(dir, FeedbackSet{Reviewer: "ryan", Items: []FeedbackItem{{Anchor: "h1", Status: FeedbackComment}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	sets, _ := LoadAllFeedback(dir)
	if len(sets) != 1 || len(sets[0].Items) != 1 || sets[0].Items[0].Reviewer != "ryan" {
		t.Fatalf("reviewer not stamped onto item: %+v", sets)
	}
}

func TestFeedbackDigest_ShowsReviewerAndContested(t *testing.T) {
	items := []MergedItem{
		{FeedbackItem: FeedbackItem{Anchor: "h1", Status: FeedbackApprove, Reviewer: "sam", Label: "x"}, Open: true},
		{FeedbackItem: FeedbackItem{Anchor: "h1", Status: FeedbackRequestChange, Reviewer: "ryan", Label: "x", Note: "fix"}, Open: true},
	}
	d := FeedbackDigest(items)
	if !strings.Contains(d, "@ryan") {
		t.Errorf("digest should attribute the open item to its reviewer: %q", d)
	}
	if !strings.Contains(d, "contested") {
		t.Errorf("digest should flag the contested anchor: %q", d)
	}
}
