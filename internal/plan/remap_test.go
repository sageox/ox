package plan

// Remap tests — the sacred-data contract: a human's review marks survive the
// plan being edited and re-rendered. Organized by the failure each prevents.

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func renderFor(t *testing.T, md string) []byte {
	t.Helper()
	html, err := RenderHTMLOpts(Parse(md), Result{}, RenderOptions{Slug: "t"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return html
}

func saveRound(t *testing.T, dir string, at time.Time, items ...FeedbackItem) {
	t.Helper()
	if _, err := SaveFeedback(dir, FeedbackSet{Slug: "t", Reviewer: "ryan", Items: items}, at); err != nil {
		t.Fatalf("save feedback: %v", err)
	}
}

// TestRemapFeedback_HeadingRename_RebindsMark verifies the core promise: the
// agent rewords a section heading (which changes every anchor under it), and
// the reviewer's open mark follows its element to the new anchor. Failure
// prevented: a plan update silently orphans feedback that was never addressed.
func TestRemapFeedback_HeadingRename_RebindsMark(t *testing.T) {
	dir := t.TempDir()
	oldAnchor := AnchorFor("Rollout", "Ship the CLI first")
	saveRound(t, dir, time.Now(), FeedbackItem{
		Anchor: oldAnchor, Section: "Rollout", Label: "Ship the CLI first",
		Status: FeedbackRequestChange, Note: "daemon must go first",
	})

	// the update: heading reworded, bullet untouched.
	v2 := renderFor(t, "# T\n\n## Rollout Plan\n\n- Ship the CLI first\n- Then the daemon\n")
	entries, err := RemapFeedback(dir, v2, time.Now())
	if err != nil {
		t.Fatalf("remap: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 rebind, got %d (%+v)", len(entries), entries)
	}
	newAnchor := AnchorFor("Rollout Plan", "Ship the CLI first")
	if entries[0].From != oldAnchor || entries[0].To != newAnchor || entries[0].Method != "label-exact" {
		t.Errorf("rebind = %+v, want %s -> %s via label-exact", entries[0], oldAnchor, newAnchor)
	}

	items, err := AssembleReview(dir)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 merged item, got %d", len(items))
	}
	it := items[0]
	if it.Anchor != newAnchor {
		t.Errorf("merged anchor = %s, want remapped %s", it.Anchor, newAnchor)
	}
	if it.RemappedFrom != oldAnchor {
		t.Errorf("RemappedFrom = %q, want %s", it.RemappedFrom, oldAnchor)
	}
	if !it.Open || it.Note != "daemon must go first" {
		t.Errorf("the human's words must survive the move intact: %+v", it)
	}
	if d := FeedbackDigest(items); !strings.Contains(d, newAnchor) || !strings.Contains(d, "remapped from") {
		t.Errorf("digest must show the current anchor and the remap: %q", d)
	}
}

// TestRemapFeedback_DeletedContent_StaysOpenNeverDropped verifies the orphan
// path: when the marked content is gone and nothing matches confidently, the
// item stays OPEN under its original anchor and keeps appearing in the digest.
// Failure prevented: "couldn't remap" quietly becoming "deleted feedback".
func TestRemapFeedback_DeletedContent_StaysOpenNeverDropped(t *testing.T) {
	dir := t.TempDir()
	a := AnchorFor("Rollout", "Ship the CLI first")
	saveRound(t, dir, time.Now(), FeedbackItem{
		Anchor: a, Section: "Rollout", Label: "Ship the CLI first",
		Status: FeedbackFlag, Note: "why CLI first?",
	})

	v2 := renderFor(t, "# T\n\n## Entirely Different\n\n- unrelated content here\n")
	entries, err := RemapFeedback(dir, v2, time.Now())
	if err != nil {
		t.Fatalf("remap: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("nothing matches — a low-confidence rebind is worse than an orphan: %+v", entries)
	}
	items, _ := AssembleReview(dir)
	if len(items) != 1 || !items[0].Open || items[0].Anchor != a {
		t.Fatalf("orphaned item must stay open under its original anchor: %+v", items)
	}
	if d := FeedbackDigest(items); !strings.Contains(d, "why CLI first?") {
		t.Errorf("orphaned feedback must keep surfacing in the digest: %q", d)
	}
}

// TestRemapChain_ResolutionsFollowAcrossUpdates verifies two successive plan
// updates chain remaps (A→B→C) and that a resolution recorded at ANY hop
// closes the item at the final address. Failure prevented: multi-update plans
// splitting one item into unresolvable ghosts.
func TestRemapChain_ResolutionsFollowAcrossUpdates(t *testing.T) {
	dir := t.TempDir()
	raise := time.Now().Add(-time.Hour)
	saveRound(t, dir, raise, FeedbackItem{Anchor: "haaaa0001", Section: "S", Label: "x", Status: FeedbackRequestChange})
	if err := appendRemaps(dir, []RemapEntry{{From: "haaaa0001", To: "hbbbb0002", Method: "label-exact", Score: 1, At: raise}}); err != nil {
		t.Fatalf("remap 1: %v", err)
	}
	if err := appendRemaps(dir, []RemapEntry{{From: "hbbbb0002", To: "hcccc0003", Method: "label-fuzzy", Score: 0.9, At: raise}}); err != nil {
		t.Fatalf("remap 2: %v", err)
	}
	// the agent resolved the item back when it lived at its FIRST address.
	if err := AppendResolution(dir, Resolution{Anchor: "haaaa0001", State: ResolutionAddressed, Note: "done"}, time.Now()); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	items, err := AssembleReview(dir)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	it := items[0]
	if it.Anchor != "hcccc0003" || it.RemappedFrom != "haaaa0001" {
		t.Errorf("chain must resolve to the final anchor: %+v", it)
	}
	if it.Open || it.Resolution == nil || it.Resolution.State != ResolutionAddressed {
		t.Errorf("a resolution at any hop must close the item at the final address: %+v", it)
	}
}

// TestRemapResolver_CycleSafe verifies a malformed chain (A→B, B→A) terminates
// instead of hanging AssembleReview. Failure prevented: one corrupt remaps.json
// hard-locks every digest, render, and await for that plan.
func TestRemapResolver_CycleSafe(t *testing.T) {
	canon := remapResolver([]RemapEntry{{From: "a", To: "b"}, {From: "b", To: "a"}})
	done := make(chan string, 1)
	go func() { done <- canon("a") }()
	select {
	case got := <-done:
		if got != "a" && got != "b" {
			t.Errorf("cycle must settle on a hop, got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remap cycle did not terminate")
	}
}

// TestAppendResolution_ConcurrentWritersAllSurvive verifies the flock around
// the resolutions read-modify-write: N concurrent resolvers (reviewer Accepts
// via the server + agent `feedback resolve` are separate processes) must all
// land. Failure prevented: last-write-wins clobber losing a human's Accept.
func TestAppendResolution_ConcurrentWritersAllSurvive(t *testing.T) {
	dir := t.TempDir()
	const n = 12
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := Resolution{Anchor: fmt.Sprintf("h%08d", i), State: ResolutionAddressed, Note: "c"}
			if err := AppendResolution(dir, r, time.Now()); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("append: %v", err)
	}
	got, err := LoadResolutions(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != n {
		t.Fatalf("lost resolutions under concurrency: got %d, want %d", len(got), n)
	}
}
