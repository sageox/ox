package plan

import (
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/planreview"
)

// Multi-user review: conflicting open verdicts on one anchor are flagged
// contested, and the digest names each open item's reviewer.

func openItem(anchor string, s planreview.Status, reviewer string) MergedItem {
	return MergedItem{Item: planreview.Item{Anchor: anchor, Status: s, Reviewer: reviewer}, Open: true}
}

func closedItem(anchor string, s planreview.Status) MergedItem {
	return MergedItem{Item: planreview.Item{Anchor: anchor, Status: s}, Open: false}
}

func TestContestedAnchors_Table(t *testing.T) {
	tests := []struct {
		name  string
		items []MergedItem
		want  []string
	}{
		{"approve vs request-change is contested", []MergedItem{
			openItem("h1", planreview.StatusApprove, "sam"), openItem("h1", planreview.StatusRequestChange, "ryan"),
		}, []string{"h1"}},
		{"approve vs flag is contested", []MergedItem{
			openItem("h3", planreview.StatusApprove, "a"), openItem("h3", planreview.StatusFlag, "b"),
		}, []string{"h3"}},
		{"two approvals agree", []MergedItem{
			openItem("h2", planreview.StatusApprove, "sam"), openItem("h2", planreview.StatusApprove, "ryan"),
		}, nil},
		{"comment never contests an approval", []MergedItem{
			openItem("h4", planreview.StatusApprove, "a"), openItem("h4", planreview.StatusComment, "b"),
		}, nil},
		{"closed items never contest", []MergedItem{
			closedItem("h5", planreview.StatusApprove), closedItem("h5", planreview.StatusRequestChange),
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

func TestFeedbackDigest_ShowsReviewerAndContested(t *testing.T) {
	items := []MergedItem{
		openItem("h1", planreview.StatusApprove, "sam"),
		{Item: planreview.Item{Anchor: "h1", Status: planreview.StatusRequestChange, Reviewer: "ryan", Label: "x", Note: "fix"}, Open: true},
	}
	d := FeedbackDigest(items)
	if !strings.Contains(d, "@ryan") {
		t.Errorf("digest should attribute the open item to its reviewer: %q", d)
	}
	if !strings.Contains(d, "contested") {
		t.Errorf("digest should flag the contested anchor: %q", d)
	}
}
