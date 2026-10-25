package plan

// Remap tests — the sacred-data contract: a human's review marks survive the
// plan being edited and re-rendered. The rebind rules themselves are
// pkg/planreview's (TestRebind_*); these pin what the CLI records and reads.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/pkg/planreview"
)

func renderFor(t *testing.T, md string) []byte {
	t.Helper()
	html, err := RenderHTMLOpts(Parse(md), Result{}, RenderOptions{Slug: "t"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return html
}

// TestRemapFeedback_RecordsAMoveTheReviewFollows verifies the core promise:
// the agent rewords a section heading (which changes every anchor under it),
// and the reviewer's open mark follows its element to the new anchor through
// one immutable remap record; remapping the same render again records nothing.
// Failure prevented: a plan update silently orphans feedback that was never
// addressed, or every save piles up another copy of the same move.
func TestRemapFeedback_RecordsAMoveTheReviewFollows(t *testing.T) {
	dir := t.TempDir()
	oldAnchor := planreview.AnchorFor("Rollout", "Ship the CLI first")
	writeRound(t, dir, "ryan", time.Now(), planreview.Item{
		Anchor: oldAnchor, Section: "Rollout", Label: "Ship the CLI first",
		Status: planreview.StatusRequestChange, Note: "daemon must go first",
	})

	// the update: heading reworded, bullet untouched.
	v2 := renderFor(t, "# T\n\n## Rollout Plan\n\n- Ship the CLI first\n- Then the daemon\n")
	moves, err := RemapFeedback(dir, v2, time.Now())
	if err != nil {
		t.Fatalf("remap: %v", err)
	}
	newAnchor := planreview.AnchorFor("Rollout Plan", "Ship the CLI first")
	if len(moves) != 1 || moves[0].From != oldAnchor || moves[0].To != newAnchor {
		t.Fatalf("moves = %+v, want %s -> %s", moves, oldAnchor, newAnchor)
	}
	records, _ := filepath.Glob(filepath.Join(dir, "feedback", "remaps", "*.json"))
	if _, err := os.Stat(filepath.Join(dir, "feedback", "remaps.json")); len(records) != 1 || !os.IsNotExist(err) {
		t.Fatalf("want one remap record and no shared remaps.json, got %v (remaps.json: %v)", records, err)
	}

	rev := ReadReview(dir)
	if len(rev.Items) != 1 || !rev.Complete() {
		t.Fatalf("want 1 merged item from a complete read, got %+v", rev)
	}
	it := rev.Items[0]
	if it.Anchor != newAnchor || it.RemappedFrom != oldAnchor || !it.Open || it.Note != "daemon must go first" {
		t.Errorf("the human's words must follow the move intact: %+v", it)
	}
	if d := FeedbackDigest(rev.Items); !strings.Contains(d, newAnchor) || !strings.Contains(d, "remapped from") {
		t.Errorf("digest must show the current anchor and the remap: %q", d)
	}

	if again, err := RemapFeedback(dir, v2, time.Now()); err != nil || len(again) != 0 {
		t.Fatalf("a second remap over the same render must record nothing, got %+v (err %v)", again, err)
	}
}

// TestRemapFeedback_SkipsAnIncompleteReview verifies a save records no remap
// while part of the review cannot be read. Failure prevented: an item whose
// resolution is unreadable looks open, and its reviewer's words are rebound to
// another element for good.
// TestRemapFeedback_NeverMovesAMarkOnTheWholePlan saves a plan that gained an
// element labeled like a whole-plan mark.
// Failure prevented: saving a plan moves a reviewer's comment on the whole plan
// onto that element, for good.
func TestRemapFeedback_NeverMovesAMarkOnTheWholePlan(t *testing.T) {
	dir := t.TempDir()
	writeRound(t, dir, "sam", time.Now(), planreview.Item{
		Anchor: planreview.WholePlanAnchor, Label: "Whole plan",
		Status: planreview.StatusComment, Note: "split the rollout",
	})
	moves, err := RemapFeedback(dir, renderFor(t, "# T\n\n## Scope\n\n- Whole plan\n"), time.Now())
	if err != nil {
		t.Fatalf("remap: %v", err)
	}
	if records, _ := filepath.Glob(filepath.Join(dir, "feedback", "remaps", "*.json")); len(moves) != 0 || len(records) != 0 {
		t.Fatalf("a mark on the whole plan must stay put, got moves %+v and records %v", moves, records)
	}
}

func TestRemapFeedback_SkipsAnIncompleteReview(t *testing.T) {
	dir := t.TempDir()
	writeRound(t, dir, "ryan", time.Now(), planreview.Item{
		Anchor: planreview.AnchorFor("Rollout", "Ship the CLI first"), Section: "Rollout",
		Label: "Ship the CLI first", Status: planreview.StatusRequestChange,
	})
	if err := os.MkdirAll(filepath.Join(dir, "feedback", "resolutions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "feedback", "resolutions", "20261001-000000.000000000-torn.json"), []byte("{torn"), 0o644); err != nil {
		t.Fatal(err)
	}

	moves, err := RemapFeedback(dir, renderFor(t, "# T\n\n## Rollout Plan\n\n- Ship the CLI first\n"), time.Now())
	if err == nil || len(moves) != 0 {
		t.Fatalf("an incomplete review must not be remapped, got %+v (err %v)", moves, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "feedback", "remaps")); !os.IsNotExist(err) {
		t.Fatalf("no remap record may be written, stat: %v", err)
	}
}
