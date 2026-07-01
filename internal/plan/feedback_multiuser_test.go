package plan

import (
	"strings"
	"testing"
	"time"
)

// Multi-user review model: distinct reviewers on the same anchor are both
// preserved (not last-write-wins), conflicting verdicts are flagged contested,
// concurrent same-instant submits never clobber each other, and the ingest path
// rejects malformed rounds before they reach the merge.

func openItem(anchor string, s FeedbackStatus, reviewer string) MergedItem {
	return MergedItem{FeedbackItem: FeedbackItem{Anchor: anchor, Status: s, Reviewer: reviewer}, Open: true}
}

func closedItem(anchor string, s FeedbackStatus) MergedItem {
	return MergedItem{FeedbackItem: FeedbackItem{Anchor: anchor, Status: s}, Open: false}
}

func TestAssembleReview_Multiuser(t *testing.T) {
	t0 := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	type round struct {
		reviewer string
		at       time.Time
		items    []FeedbackItem
	}
	tests := []struct {
		name      string
		rounds    []round
		wantItems int
		wantByRev map[string]FeedbackStatus // reviewer -> expected latest status
	}{
		{
			name: "two reviewers on the same anchor are both kept",
			rounds: []round{
				{"ryan", t0, []FeedbackItem{{Anchor: "h1", Status: FeedbackRequestChange, Note: "per-IP too"}}},
				{"sam", t0.Add(time.Second), []FeedbackItem{{Anchor: "h1", Status: FeedbackApprove, Note: "fine"}}},
			},
			wantItems: 2,
			wantByRev: map[string]FeedbackStatus{"ryan": FeedbackRequestChange, "sam": FeedbackApprove},
		},
		{
			name: "single anonymous reviewer still collapses per-anchor (backward compatible)",
			rounds: []round{
				{"", t0, []FeedbackItem{{Anchor: "h1", Status: FeedbackFlag, Note: "first"}}},
				{"", t0.Add(time.Second), []FeedbackItem{{Anchor: "h1", Status: FeedbackRequestChange, Note: "second"}}},
			},
			wantItems: 1,
			wantByRev: map[string]FeedbackStatus{"": FeedbackRequestChange},
		},
		{
			name: "same reviewer re-marking an anchor keeps only their latest",
			rounds: []round{
				{"ryan", t0, []FeedbackItem{{Anchor: "h1", Status: FeedbackComment}}},
				{"ryan", t0.Add(time.Second), []FeedbackItem{{Anchor: "h1", Status: FeedbackRequestChange}}},
			},
			wantItems: 1,
			wantByRev: map[string]FeedbackStatus{"ryan": FeedbackRequestChange},
		},
		{
			name: "distinct anchors from distinct reviewers all survive",
			rounds: []round{
				{"ryan", t0, []FeedbackItem{{Anchor: "h1", Status: FeedbackRequestChange}}},
				{"sam", t0.Add(time.Second), []FeedbackItem{{Anchor: "h2", Status: FeedbackFlag}}},
			},
			wantItems: 2,
			wantByRev: map[string]FeedbackStatus{"ryan": FeedbackRequestChange, "sam": FeedbackFlag},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, r := range tt.rounds {
				if _, err := SaveFeedback(dir, FeedbackSet{Reviewer: r.reviewer, Items: r.items}, r.at); err != nil {
					t.Fatalf("SaveFeedback: %v", err)
				}
			}
			items, err := AssembleReview(dir)
			if err != nil {
				t.Fatalf("AssembleReview: %v", err)
			}
			if len(items) != tt.wantItems {
				t.Fatalf("item count = %d, want %d (%+v)", len(items), tt.wantItems, items)
			}
			got := map[string]FeedbackStatus{}
			for _, it := range items {
				if it.Reviewer == "" && len(tt.wantByRev) > 1 {
					t.Errorf("item missing reviewer attribution: %+v", it)
				}
				got[it.Reviewer] = it.Status
			}
			for rev, want := range tt.wantByRev {
				if got[rev] != want {
					t.Errorf("reviewer %q status = %q, want %q", rev, got[rev], want)
				}
			}
		})
	}
}

func TestContestedAnchors_Table(t *testing.T) {
	tests := []struct {
		name  string
		items []MergedItem
		want  []string
	}{
		{"approve vs request-change is contested", []MergedItem{
			openItem("h1", FeedbackApprove, "sam"), openItem("h1", FeedbackRequestChange, "ryan"),
		}, []string{"h1"}},
		{"approve vs flag is contested", []MergedItem{
			openItem("h3", FeedbackApprove, "a"), openItem("h3", FeedbackFlag, "b"),
		}, []string{"h3"}},
		{"two approvals agree", []MergedItem{
			openItem("h2", FeedbackApprove, "sam"), openItem("h2", FeedbackApprove, "ryan"),
		}, nil},
		{"comment never contests an approval", []MergedItem{
			openItem("h4", FeedbackApprove, "a"), openItem("h4", FeedbackComment, "b"),
		}, nil},
		{"closed items never contest", []MergedItem{
			closedItem("h5", FeedbackApprove), closedItem("h5", FeedbackRequestChange),
		}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContestedAnchors(tt.items)
			if len(got) != len(tt.want) {
				t.Fatalf("contested count = %d %v, want %d %v", len(got), got, len(tt.want), tt.want)
			}
			for _, a := range tt.want {
				if !got[a] {
					t.Errorf("anchor %q should be contested", a)
				}
			}
		})
	}
}

// error path: the ingest guard rejects a malformed round before it can reach
// SaveFeedback / the merge, so a crafted export can't inject an unknown status.
func TestParseFeedback_RejectsUnknownStatus(t *testing.T) {
	_, err := ParseFeedback([]byte(`{"slug":"p","items":[{"anchor":"h1","status":"bogus"}]}`))
	if err == nil {
		t.Fatal("expected an error for an unknown status, got nil")
	}
	if !strings.Contains(err.Error(), "unknown status") {
		t.Errorf("error = %v, want it to mention the unknown status", err)
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
		openItem("h1", FeedbackApprove, "sam"),
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
