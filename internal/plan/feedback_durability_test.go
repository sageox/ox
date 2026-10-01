package plan

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func roundFiles(t *testing.T, planDir string) []string {
	t.Helper()
	names, err := roundFileNames(filepath.Join(planDir, feedbackSubdir))
	if err != nil {
		t.Fatalf("list rounds: %v", err)
	}
	return names
}

// TestParseFeedback_RoundID verifies the round ID contract: absent is fine,
// a well-formed ID survives, and anything outside [A-Za-z0-9_-]{8,64} is
// refused. Failure prevented: an ID carrying a path separator steers the
// round filename outside feedback/.
func TestParseFeedback_RoundID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		id   string
		ok   bool
	}{
		{"absent", "", true},
		{"min length", "abcd1234", true},
		{"dashes and underscores", "round_ab-CD_1234", true},
		{"max length", strings.Repeat("a", 64), true},
		{"too short", "abc1234", false},
		{"too long", strings.Repeat("a", 65), false},
		{"slash", "abcd/1234", false},
		{"traversal", "../../etc/x", false},
		{"dot", "abcd.1234", false},
		{"space", "abcd 1234", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, _ := json.Marshal(map[string]any{
				"id":    tt.id,
				"items": []map[string]string{{"anchor": "h1", "status": "flag"}},
			})
			set, err := ParseFeedback(raw)
			if (err == nil) != tt.ok {
				t.Fatalf("ParseFeedback(id=%q) err=%v, want ok=%v", tt.id, err, tt.ok)
			}
			if tt.ok && set.ID != tt.id {
				t.Errorf("ID = %q, want %q", set.ID, tt.id)
			}
			if !tt.ok {
				if _, serr := SaveFeedback(t.TempDir(), FeedbackSet{ID: tt.id, Items: []FeedbackItem{{Anchor: "h1"}}}, time.Now()); serr == nil {
					t.Errorf("SaveFeedback must also refuse id %q", tt.id)
				}
			}
		})
	}
}

// TestSaveFeedback_DuplicateID verifies a second save with the same ID writes
// nothing and returns ErrDuplicateRound with the first round's path, while
// rounds without an ID keep stacking. Failure prevented: a double Submit
// doubles every reviewer mark.
func TestSaveFeedback_DuplicateID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		first     string
		second    string
		wantFiles int
		wantDup   bool
	}{
		{"same id", "submit-0001", "submit-0001", 1, true},
		{"different ids", "submit-0001", "submit-0002", 2, false},
		{"no ids", "", "", 2, false},
		{"id is suffix of another", "x-submit-0001", "submit-0001", 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			items := []FeedbackItem{{Anchor: "h1", Status: FeedbackFlag}}
			first, err := SaveFeedback(dir, FeedbackSet{ID: tt.first, Items: items}, time.Now())
			if err != nil {
				t.Fatalf("first save: %v", err)
			}
			second, err := SaveFeedback(dir, FeedbackSet{ID: tt.second, Items: items}, time.Now().Add(time.Second))
			if tt.wantDup {
				if !errors.Is(err, ErrDuplicateRound) {
					t.Fatalf("second save err = %v, want ErrDuplicateRound", err)
				}
				if second != first {
					t.Errorf("duplicate must return the existing path %q, got %q", first, second)
				}
			} else if err != nil {
				t.Fatalf("second save: %v", err)
			}
			if got := len(roundFiles(t, dir)); got != tt.wantFiles {
				t.Errorf("round files = %d, want %d", got, tt.wantFiles)
			}
			if tt.first != "" && !strings.HasSuffix(first, "-"+tt.first+".json") {
				t.Errorf("round filename %q must embed the id %q", first, tt.first)
			}
		})
	}
}

// TestSaveFeedback_ConcurrentSameID verifies the exists-check and write are
// one critical section: 16 racing saves of one ID leave exactly one round.
// Failure prevented: the review server and an `apply` both pass the check
// before either writes, and the round lands twice.
func TestSaveFeedback_ConcurrentSameID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	saved, dups := 0, 0
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			set := FeedbackSet{ID: "race-round-01", Items: []FeedbackItem{{Anchor: "h1", Status: FeedbackComment}}}
			// distinct timestamps so a lost race could not hide behind a shared filename
			_, err := SaveFeedback(dir, set, time.Now().Add(time.Duration(i)*time.Millisecond))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				saved++
			case errors.Is(err, ErrDuplicateRound):
				dups++
			default:
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("save: %v", err)
	}
	if saved != 1 || dups != n-1 {
		t.Errorf("saved=%d dups=%d, want 1 and %d", saved, dups, n-1)
	}
	if got := len(roundFiles(t, dir)); got != 1 {
		t.Errorf("round files = %d, want exactly 1", got)
	}
}

// TestSaveFeedback_AtomicLeavesNoTempFiles verifies round writes go through
// temp+rename and clean up after themselves. Failure prevented: leftover
// partial files that a later load would misread.
func TestSaveFeedback_AtomicLeavesNoTempFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for i, id := range []string{"", "atomic-0001", ""} {
		if _, err := SaveFeedback(dir, FeedbackSet{ID: id, Items: []FeedbackItem{{Anchor: "h1"}}}, time.Now().Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, feedbackSubdir))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), roundPrefix) {
			t.Errorf("unexpected non-round file left behind: %s", e.Name())
		}
	}
	if len(entries) != 3 {
		t.Errorf("entries = %d, want 3 rounds", len(entries))
	}
}

// TestCorruptFeedbackRounds_ReportsTornRound verifies a torn round is
// surfaced by CorruptFeedbackRounds while the load keeps every intact round,
// and that non-round files (remaps.json, resolutions.json) are never flagged.
// Failure prevented: a reviewer's round silently vanishes from the review.
func TestCorruptFeedbackRounds_ReportsTornRound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := SaveFeedback(dir, FeedbackSet{Items: []FeedbackItem{{Anchor: "h1", Note: "kept"}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	fb := filepath.Join(dir, feedbackSubdir)
	torn := filepath.Join(fb, "round-20260101-000000.000000000-deadbeef.json")
	for path, body := range map[string]string{
		torn:                               `{"items":[{"anch`,
		filepath.Join(fb, remapsFile):      `[]`,
		filepath.Join(fb, resolutionsFile): `[]`,
	} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	sets, err := LoadAllFeedback(dir)
	if err != nil {
		t.Fatalf("load must not fail on a torn round: %v", err)
	}
	if len(sets) != 1 || sets[0].Items[0].Note != "kept" {
		t.Errorf("intact round must survive, got %+v", sets)
	}
	corrupt, err := CorruptFeedbackRounds(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(corrupt) != 1 || corrupt[0] != torn {
		t.Errorf("corrupt = %v, want [%s]", corrupt, torn)
	}

	clean, err := CorruptFeedbackRounds(t.TempDir())
	if err != nil || len(clean) != 0 {
		t.Errorf("no feedback dir: corrupt=%v err=%v, want none", clean, err)
	}
}

// TestLoadAllFeedback_SameIDAcrossMachinesCollapses verifies two rounds with
// one ID (saved on two clones before the ledger synced, so no lock could see
// both) load as one. Failure prevented: a re-applied export double-counts
// marks after a rebase brings both files together.
func TestLoadAllFeedback_SameIDAcrossMachinesCollapses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	machineB := t.TempDir()
	set := FeedbackSet{ID: "shared-round-1", Items: []FeedbackItem{{Anchor: "h1"}}}
	if _, err := SaveFeedback(dir, set, time.Now()); err != nil {
		t.Fatal(err)
	}
	other, err := SaveFeedback(machineB, set, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, feedbackSubdir, filepath.Base(other)), b, 0o644); err != nil {
		t.Fatal(err)
	}
	sets, err := LoadAllFeedback(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 {
		t.Errorf("same-id rounds must collapse to one, got %d", len(sets))
	}
}

// TestContentRoundID_Deterministic verifies the derived ID depends only on
// reviewer + items, and is a valid round ID.
func TestContentRoundID_Deterministic(t *testing.T) {
	t.Parallel()
	base := FeedbackSet{Reviewer: "person-a", Items: []FeedbackItem{{Anchor: "h1", Status: FeedbackFlag, Note: "n"}}}
	tests := []struct {
		name string
		set  FeedbackSet
		same bool
	}{
		{"identical", base, true},
		{"different slug and time", FeedbackSet{Slug: "other", CreatedAt: time.Now(), Reviewer: base.Reviewer, Items: base.Items}, true},
		{"different note", FeedbackSet{Reviewer: base.Reviewer, Items: []FeedbackItem{{Anchor: "h1", Status: FeedbackFlag, Note: "m"}}}, false},
		{"different reviewer", FeedbackSet{Reviewer: "person-b", Items: base.Items}, false},
	}
	want, err := ContentRoundID(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRoundID(want); err != nil {
		t.Fatalf("derived id must be valid: %v", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ContentRoundID(tt.set)
			if err != nil {
				t.Fatal(err)
			}
			if (got == want) != tt.same {
				t.Errorf("ContentRoundID = %q vs base %q, want same=%v", got, want, tt.same)
			}
		})
	}
}

// TestLoadResolutions_LegacyAndPerEntry covers the read-side merge of the
// legacy resolutions.json array with the per-entry directory.
func TestLoadResolutions_LegacyAndPerEntry(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	r1 := Resolution{Anchor: "h1", State: ResolutionAddressed, Commit: "abc", At: t0}
	r2 := Resolution{Anchor: "h2", State: ResolutionWontfix, Note: "no", At: t0.Add(time.Minute)}
	r3 := Resolution{Anchor: "h1", State: ResolutionVerified, At: t0.Add(2 * time.Minute)}

	tests := []struct {
		name   string
		legacy []Resolution
		dir    []Resolution
		want   []Resolution
	}{
		{"none", nil, nil, nil},
		{"legacy only", []Resolution{r1, r2}, nil, []Resolution{r1, r2}},
		{"dir only", nil, []Resolution{r3, r1}, []Resolution{r1, r3}},
		{"mixed orders by time", []Resolution{r1, r3}, []Resolution{r2}, []Resolution{r1, r2, r3}},
		{"duplicate across both kept once", []Resolution{r1, r2}, []Resolution{r2, r3}, []Resolution{r1, r2, r3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if tt.legacy != nil {
				if err := os.MkdirAll(filepath.Join(dir, feedbackSubdir), 0o755); err != nil {
					t.Fatal(err)
				}
				b, _ := json.Marshal(tt.legacy)
				if err := os.WriteFile(filepath.Join(dir, feedbackSubdir, resolutionsFile), b, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			for _, r := range tt.dir {
				if err := AppendResolution(dir, r, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			got, err := LoadResolutions(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d resolutions %+v, want %d", len(got), got, len(tt.want))
			}
			for i := range got {
				if !containsResolution([]Resolution{tt.want[i]}, got[i]) {
					t.Errorf("[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestAppendResolution_NeverRewritesSharedFile simulates two machines each
// resolving an item, then a sync that unions their files (what a rebase of
// distinct paths does). Failure prevented: the old shared resolutions.json
// conflicted on rebase and was auto-resolved to one side, dropping the other
// machine's disposition.
func TestAppendResolution_NeverRewritesSharedFile(t *testing.T) {
	t.Parallel()
	machineA, machineB := t.TempDir(), t.TempDir()
	legacy := []byte(`[{"anchor":"h0","state":"addressed","at":"2026-08-01T00:00:00Z"}]`)
	for _, m := range []string{machineA, machineB} {
		if err := os.MkdirAll(filepath.Join(m, feedbackSubdir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(m, feedbackSubdir, resolutionsFile), legacy, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := AppendResolution(machineA, Resolution{Anchor: "h1", State: ResolutionAddressed}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := AppendResolution(machineB, Resolution{Anchor: "h2", State: ResolutionWontfix}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{machineA, machineB} {
		b, err := os.ReadFile(filepath.Join(m, feedbackSubdir, resolutionsFile))
		if err != nil || string(b) != string(legacy) {
			t.Fatalf("legacy resolutions.json must never be rewritten (err=%v): %s", err, b)
		}
	}
	// "sync": copy B's per-entry files into A; distinct names never collide.
	bDir := filepath.Join(machineB, feedbackSubdir, resolutionsSubdir)
	entries, err := os.ReadDir(bDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		dst := filepath.Join(machineA, feedbackSubdir, resolutionsSubdir, e.Name())
		if _, err := os.Stat(dst); err == nil {
			t.Fatalf("per-entry filename collided across machines: %s", e.Name())
		}
		b, err := os.ReadFile(filepath.Join(bDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := LoadResolutions(machineA)
	if err != nil {
		t.Fatal(err)
	}
	anchors := map[string]bool{}
	for _, r := range got {
		anchors[r.Anchor] = true
	}
	for _, a := range []string{"h0", "h1", "h2"} {
		if !anchors[a] {
			t.Errorf("resolution %s lost after sync; have %+v", a, got)
		}
	}
}

// TestLoadResolutions_CorruptEntrySkipped verifies one torn per-entry file
// does not hide the rest.
func TestLoadResolutions_CorruptEntrySkipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := AppendResolution(dir, Resolution{Anchor: "h1", State: ResolutionAddressed}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, feedbackSubdir, resolutionsSubdir, "20260101-000000.000000000-bad00000.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadResolutions(dir)
	if err != nil || len(got) != 1 || got[0].Anchor != "h1" {
		t.Fatalf("got %+v (%v), want the intact entry", got, err)
	}
}
