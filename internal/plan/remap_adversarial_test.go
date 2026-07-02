package plan

// Adversarial suite for the feedback remap subsystem. Review marks are sacred
// data: these tests try to LOSE, MISATTACH, or BRICK them — corrupt state,
// ambiguous content, hostile anchors, racing writers, and a deterministic
// model battery driving random op sequences against an obviously-correct
// oracle. Each test names the class of bug it prevents.

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- A. Corrupt / malformed on-disk state -----------------------------------

// TestAssembleReview_CorruptRemaps_DegradesNeverBricks: a torn or garbage
// remaps.json must NOT take down the merge — remaps are derived data; the
// human's rounds are the sacred part. Degradation contract: marks come back at
// their ORIGINAL anchors (orphaned-but-visible), never an error. Failure
// prevented: one corrupt derived file 500s every render, digest, and await for
// the plan — feedback intact on disk but unreachable everywhere.
func TestAssembleReview_CorruptRemaps_DegradesNeverBricks(t *testing.T) {
	dir := t.TempDir()
	saveRound(t, dir, time.Now(), FeedbackItem{
		Anchor: "haaaa0001", Section: "S", Label: "keep me", Status: FeedbackFlag, Note: "sacred words",
	})
	fbDir := filepath.Join(dir, feedbackSubdir)
	for _, garbage := range []string{"", "{", `{"not":"an array"}`, "\x00\x01\x02", `[{"from":1}]`} {
		if err := os.WriteFile(filepath.Join(fbDir, remapsFile), []byte(garbage), 0o644); err != nil {
			t.Fatal(err)
		}
		items, err := AssembleReview(dir)
		if err != nil {
			t.Fatalf("corrupt remaps (%q) must degrade, not error: %v", garbage, err)
		}
		if len(items) != 1 || items[0].Anchor != "haaaa0001" || items[0].Note != "sacred words" || !items[0].Open {
			t.Fatalf("corrupt remaps (%q): mark must survive at its original anchor: %+v", garbage, items)
		}
	}
}

// TestRemapFeedback_CorruptRemaps_SelfHeals: a later plan update must not be
// blocked forever by a corrupt remaps.json — the remapper resets the derived
// file (loudly) and records the new rebind. Failure prevented: one bad byte
// permanently disables remapping for a plan, so every subsequent update
// orphans marks that were rebindable.
func TestRemapFeedback_CorruptRemaps_SelfHeals(t *testing.T) {
	dir := t.TempDir()
	saveRound(t, dir, time.Now(), FeedbackItem{
		Anchor: AnchorFor("Rollout", "Ship the CLI first"), Section: "Rollout",
		Label: "Ship the CLI first", Status: FeedbackRequestChange, Note: "daemon first",
	})
	fbDir := filepath.Join(dir, feedbackSubdir)
	if err := os.WriteFile(filepath.Join(fbDir, remapsFile), []byte("{garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	v2 := renderFor(t, "# T\n\n## Rollout Plan\n\n- Ship the CLI first\n")
	entries, err := RemapFeedback(dir, v2, time.Now())
	if err != nil {
		t.Fatalf("remap over corrupt derived state must self-heal: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want the rebind recorded after self-heal, got %+v", entries)
	}
	items, err := AssembleReview(dir)
	if err != nil || len(items) != 1 || items[0].Anchor != AnchorFor("Rollout Plan", "Ship the CLI first") {
		t.Fatalf("post-heal merge must show the rebound mark: %+v (%v)", items, err)
	}
}

// TestAssembleReview_MalformedRoundAmongGood_OthersSurvive: one torn round
// file (a crash mid-write) must not hide the other rounds. Failure prevented:
// a single bad file silently blanks the whole review.
func TestAssembleReview_MalformedRoundAmongGood_OthersSurvive(t *testing.T) {
	dir := t.TempDir()
	saveRound(t, dir, time.Now(), FeedbackItem{Anchor: "haaaa0001", Label: "good", Status: FeedbackComment, Note: "kept"})
	if err := os.WriteFile(filepath.Join(dir, feedbackSubdir, "round-19990101-000000.000000000-dead.json"), []byte("{torn"), 0o644); err != nil {
		t.Fatal(err)
	}
	items, err := AssembleReview(dir)
	if err != nil || len(items) != 1 || items[0].Note != "kept" {
		t.Fatalf("good rounds must survive a torn sibling: %+v (%v)", items, err)
	}
}

// --- B. Ambiguity + threshold boundaries ------------------------------------

// TestBestRebind_AmbiguousExactDuplicates_PrefersSectionThenRefuses: two
// sections carry IDENTICAL text; a mark whose label matches both must rebind
// to the stored-section copy when that disambiguates, and REFUSE when it
// can't. Failure prevented: a human's comment silently reattaches to the wrong
// section's identical bullet — misattached sacred data reads as review-approved
// content that was never reviewed.
func TestBestRebind_AmbiguousExactDuplicates_PrefersSectionThenRefuses(t *testing.T) {
	md := "# T\n\n## Alpha\n\n- TBD budget\n\n## Beta\n\n- TBD budget\n"
	targets, err := extractReviewTargets(renderFor(t, md))
	if err != nil {
		t.Fatal(err)
	}
	// stored section still exists → must pick Alpha's copy, not Beta's.
	it := MergedItem{FeedbackItem: FeedbackItem{Anchor: "hdead0001", Section: "Alpha", Label: "TBD budget"}, Open: true}
	e, ok := bestRebind(it, targets)
	if !ok || e.To != AnchorFor("Alpha", "TBD budget") {
		t.Fatalf("same-section exact duplicate must win: ok=%v %+v", ok, e)
	}
	// stored section is gone and BOTH surviving copies are exact matches →
	// refusing is the only safe move.
	it.Section = "Gamma (deleted)"
	if e, ok := bestRebind(it, targets); ok {
		t.Fatalf("ambiguous exact duplicates must refuse to rebind, got %+v", e)
	}
}

// TestBestRebind_FuzzyThresholds_RefuseBelowMinAndOnTightMargin: fuzzy rebinds
// just under the similarity floor, or with a too-close runner-up, must refuse.
// Failure prevented: threshold drift quietly converts "orphaned but visible"
// into "attached to the wrong element".
func TestBestRebind_FuzzyThresholds_RefuseBelowMinAndOnTightMargin(t *testing.T) {
	// below the floor: one shared token out of many.
	weak := []reviewTarget{{Anchor: "hweak0001", Section: "S", Label: "entirely different words here", Norm: "entirely different words here"}}
	it := MergedItem{FeedbackItem: FeedbackItem{Anchor: "hdead0002", Section: "S", Label: "ship the daemon first"}, Open: true}
	if e, ok := bestRebind(it, weak); ok {
		t.Fatalf("below-floor similarity must refuse, got %+v", e)
	}
	// two near-identical candidates: best-vs-second margin is ~0 → refuse.
	twins := []reviewTarget{
		{Anchor: "htwin0001", Section: "A", Label: "ship the daemon first now", Norm: "ship the daemon first now"},
		{Anchor: "htwin0002", Section: "B", Label: "ship the daemon first soon", Norm: "ship the daemon first soon"},
	}
	it = MergedItem{FeedbackItem: FeedbackItem{Anchor: "hdead0003", Section: "C", Label: "ship the daemon first"}, Open: true}
	if e, ok := bestRebind(it, twins); ok {
		t.Fatalf("tight-margin twins must refuse to rebind, got %+v", e)
	}
}

// TestBestRebind_EmptyLabel_NeverRebinds: an item with no label carries no
// matchable content — any rebind would be a guess. Failure prevented: blank
// marks vacuuming up arbitrary anchors.
func TestBestRebind_EmptyLabel_NeverRebinds(t *testing.T) {
	targets := []reviewTarget{{Anchor: "hx", Section: "S", Label: "content", Norm: "content"}}
	it := MergedItem{FeedbackItem: FeedbackItem{Anchor: "hdead0004", Label: "   "}, Open: true}
	if e, ok := bestRebind(it, targets); ok {
		t.Fatalf("empty label must never rebind, got %+v", e)
	}
}

// --- C. Hostile inputs -------------------------------------------------------

// TestHostileAnchors_NeverEscapeNeverPanic: anchors arrive from the browser
// (and from feedback JSON a human can hand-edit). Path-shaped, huge, empty,
// and control-character anchors must flow through parse→merge→digest without
// panicking, and must be rejected wherever an anchor gates a write
// (resolutions). Failure prevented: crafted feedback steering writes or
// crashing every reader of the plan.
func TestHostileAnchors_NeverEscapeNeverPanic(t *testing.T) {
	dir := t.TempDir()
	hostile := []string{"../../etc/cron.d/x", `a\b`, strings.Repeat("h", 64<<10), "", "h\x00nul", "h🔥🔥"}
	items := make([]FeedbackItem, 0, len(hostile))
	for _, a := range hostile {
		items = append(items, FeedbackItem{Anchor: a, Label: "l", Status: FeedbackComment, Note: "n"})
	}
	saveRound(t, dir, time.Now(), items...)

	merged, err := AssembleReview(dir)
	if err != nil {
		t.Fatalf("hostile anchors must not break the merge: %v", err)
	}
	if len(merged) != len(hostile) {
		t.Fatalf("every mark survives, hostile or not: got %d want %d", len(merged), len(hostile))
	}
	_ = FeedbackDigest(merged) // must not panic

	// anchors that gate a WRITE stay validated.
	for _, a := range []string{"../../etc/cron.d/x", `a\b`, ""} {
		if err := AppendResolution(dir, Resolution{Anchor: a, State: ResolutionAddressed}, time.Now()); err == nil {
			t.Errorf("path-shaped anchor %q must be rejected by resolve", a)
		}
	}
	// and the remapper must ignore them without panicking (nothing rebindable).
	if _, err := RemapFeedback(dir, renderFor(t, "# T\n\n## S\n\n- x\n"), time.Now()); err != nil {
		t.Fatalf("remap over hostile anchors: %v", err)
	}
}

// TestRemapEntries_HostileChains_SettleSafely: crafted remaps.json shapes —
// self-loops, cycles, unknown froms, duplicate froms, long chains — must
// settle to a stable anchor and keep every mark visible. Chronological-fold
// semantics: a later duplicate-From entry must NOT re-route a mark that
// already moved (the path-dependence bug the model battery caught). Failure
// prevented: derived state weaponized into hangs, vanishing marks, or
// retroactive re-routing of already-moved marks.
func TestRemapEntries_HostileChains_SettleSafely(t *testing.T) {
	dir := t.TempDir()
	saveRound(t, dir, time.Now(), FeedbackItem{Anchor: "ha", Label: "l", Status: FeedbackFlag, Note: "words"})
	entries := []RemapEntry{
		{From: "ha", To: "ha"},                         // self-loop: ignored
		{From: "zz", To: "yy"},                         // unknown from: inert
		{From: "ha", To: "hb"}, {From: "ha", To: "hc"}, // mark moves ha→hb; the later ha→hc must not drag it to hc
		{From: "hx", To: "hy"}, {From: "hy", To: "hx"}, // 2-cycle elsewhere: inert, must not hang
	}
	for i := 0; i < 40; i++ { // long chain hc000 → … → hc040, unrelated to the mark
		entries = append(entries, RemapEntry{From: fmt.Sprintf("hc%03d", i), To: fmt.Sprintf("hc%03d", i+1)})
	}
	if err := appendRemaps(dir, entries); err != nil {
		t.Fatal(err)
	}
	done := make(chan []MergedItem, 1)
	go func() { m, _ := AssembleReview(dir); done <- m }()
	select {
	case merged := <-done:
		if len(merged) != 1 || merged[0].Anchor != "hb" || merged[0].Note != "words" || merged[0].RemappedFrom != "ha" {
			t.Fatalf("hostile chain must settle at the first legitimate move with the mark intact: %+v", merged)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hostile remap chain hung the merge")
	}
}

// --- D. Interleavings --------------------------------------------------------

// TestResolveVsRemap_RaceConvergesClosed: the agent resolves an item (at its
// old anchor) while a plan save remaps that anchor. Whichever order wins, the
// merged item must end CLOSED at the final anchor — equivalent to a legal
// serial order. Failure prevented: a remap racing a resolve forking one item
// into an open ghost + an orphaned resolution.
func TestResolveVsRemap_RaceConvergesClosed(t *testing.T) {
	for i := 0; i < 20; i++ {
		dir := t.TempDir()
		raise := time.Now().Add(-time.Hour)
		saveRound(t, dir, raise, FeedbackItem{Anchor: "haaaa0001", Section: "S", Label: "x", Status: FeedbackRequestChange})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = AppendResolution(dir, Resolution{Anchor: "haaaa0001", State: ResolutionAddressed, Note: "done"}, time.Now())
		}()
		go func() {
			defer wg.Done()
			_ = appendRemaps(dir, []RemapEntry{{From: "haaaa0001", To: "hbbbb0002", Method: "label-exact", Score: 1, At: time.Now()}})
		}()
		wg.Wait()
		items, err := AssembleReview(dir)
		if err != nil || len(items) != 1 {
			t.Fatalf("merge after race: %+v (%v)", items, err)
		}
		if items[0].Anchor != "hbbbb0002" || items[0].Open {
			t.Fatalf("race must converge closed at the final anchor: %+v", items[0])
		}
	}
}

// TestMultiReviewer_RemapConvergence_AllVoicesSurvive: two reviewers marked
// the old anchor, a third already marked the new one directly. After the
// remap, all three marks live at the new anchor, one per reviewer. Failure
// prevented: convergence collapsing distinct reviewers into one (the exact
// multi-user regression #687 guarded against, now across a remap).
func TestMultiReviewer_RemapConvergence_AllVoicesSurvive(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Add(-time.Hour)
	old, now := "haaaa0001", "hbbbb0002"
	saveRound(t, dir, at, FeedbackItem{Anchor: old, Label: "x", Status: FeedbackRequestChange, Reviewer: "alice", Note: "a-says"})
	saveRound(t, dir, at.Add(time.Minute), FeedbackItem{Anchor: old, Label: "x", Status: FeedbackFlag, Reviewer: "bob", Note: "b-says"})
	saveRound(t, dir, at.Add(2*time.Minute), FeedbackItem{Anchor: now, Label: "x", Status: FeedbackComment, Reviewer: "carol", Note: "c-says"})
	if err := appendRemaps(dir, []RemapEntry{{From: old, To: now, Method: "label-exact", Score: 1, At: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	items, err := AssembleReview(dir)
	if err != nil {
		t.Fatal(err)
	}
	notes := map[string]string{}
	for _, it := range items {
		if it.Anchor != now {
			t.Fatalf("all marks must converge on the new anchor: %+v", it)
		}
		notes[it.Reviewer] = it.Note
	}
	if len(items) != 3 || notes["alice"] != "a-says" || notes["bob"] != "b-says" || notes["carol"] != "c-says" {
		t.Fatalf("every reviewer's words must survive convergence: %+v", items)
	}
}

// --- E. Metamorphic ----------------------------------------------------------

// TestRemapFeedback_Idempotent_SecondRunIsNoOp: remapping the same render
// twice must record nothing new — the first run moved every mark it could.
// Failure prevented: each save appending duplicate remap entries until the
// chain resolver chokes.
func TestRemapFeedback_Idempotent_SecondRunIsNoOp(t *testing.T) {
	dir := t.TempDir()
	saveRound(t, dir, time.Now(), FeedbackItem{
		Anchor: AnchorFor("Rollout", "Ship the CLI first"), Section: "Rollout",
		Label: "Ship the CLI first", Status: FeedbackRequestChange,
	})
	v2 := renderFor(t, "# T\n\n## Rollout Plan\n\n- Ship the CLI first\n")
	first, err := RemapFeedback(dir, v2, time.Now())
	if err != nil || len(first) != 1 {
		t.Fatalf("first remap: %+v (%v)", first, err)
	}
	second, err := RemapFeedback(dir, v2, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("second remap over the same render must be a no-op, got %+v", second)
	}
}

// --- F. Model battery --------------------------------------------------------

// TestFeedbackMerge_ModelBattery drives random op sequences (rounds from
// several reviewers, resolutions, remap hops) against the real files and an
// obviously-correct oracle that RECOMPUTES the expected view from the raw
// event logs after EVERY op — no incremental bookkeeping to share bugs with
// the implementation. Deterministic seeds — a failure names its seed and
// replays. Failure prevented: the class of merge bugs no example test
// enumerates — lost marks, reviewer collapse, resolutions keyed to stale
// hops, wrong open-state after re-raise-across-remap. (Its first run caught
// the chain-walk path-dependence bug that became the fold resolver.)
func TestFeedbackMerge_ModelBattery(t *testing.T) {
	anchors := []string{"ha1", "ha2", "ha3", "ha4", "ha5"}
	reviewers := []string{"alice", "bob", ""}
	statuses := []FeedbackStatus{FeedbackApprove, FeedbackRequestChange, FeedbackFlag, FeedbackComment}

	type key struct{ anchor, reviewer string }
	type rawRound struct {
		at    time.Time
		rev   string
		items []FeedbackItem
	}

	for seed := int64(1); seed <= 40; seed++ {
		rng := rand.New(rand.NewSource(seed))
		dir := t.TempDir()

		// raw event logs — the oracle's only state.
		var rounds []rawRound
		var resolutions []Resolution
		var moves []RemapEntry

		// fold is the SPEC of anchor identity: chronological moves, an entry
		// applies only when it departs the anchor's current position.
		fold := func(a string) string {
			cur := a
			for _, e := range moves {
				if e.From == cur && e.To != "" && e.To != cur {
					cur = e.To
				}
			}
			return cur
		}

		now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
		for op := 0; op < 30; op++ {
			now = now.Add(time.Minute)
			switch rng.Intn(3) {
			case 0: // a review round (1-2 items), possibly at a stale anchor
				n := 1 + rng.Intn(2)
				rev := reviewers[rng.Intn(len(reviewers))]
				items := make([]FeedbackItem, 0, n)
				for i := 0; i < n; i++ {
					items = append(items, FeedbackItem{
						Anchor: anchors[rng.Intn(len(anchors))], Reviewer: rev,
						Status: statuses[rng.Intn(len(statuses))],
						Label:  fmt.Sprintf("label-%d", rng.Intn(3)), Note: fmt.Sprintf("note-%d-%d", seed, op),
					})
				}
				if _, err := SaveFeedback(dir, FeedbackSet{Slug: "t", Reviewer: rev, Items: items}, now); err != nil {
					t.Fatalf("seed %d op %d save: %v", seed, op, err)
				}
				rounds = append(rounds, rawRound{at: now, rev: rev, items: items})
			case 1: // a resolution at a random (possibly stale) anchor
				r := Resolution{Anchor: anchors[rng.Intn(len(anchors))], State: ResolutionAddressed, At: now}
				if err := AppendResolution(dir, r, now); err != nil {
					t.Fatalf("seed %d op %d resolve: %v", seed, op, err)
				}
				resolutions = append(resolutions, r)
			case 2: // a remap hop between two distinct anchors
				from, to := anchors[rng.Intn(len(anchors))], anchors[rng.Intn(len(anchors))]
				if from == to {
					continue
				}
				e := RemapEntry{From: from, To: to, Method: "label-exact", Score: 1, At: now}
				if err := appendRemaps(dir, []RemapEntry{e}); err != nil {
					t.Fatalf("seed %d op %d remap: %v", seed, op, err)
				}
				moves = append(moves, e)
			}

			// ORACLE: recompute the expected view from the raw logs.
			type mark struct {
				it     FeedbackItem
				raised time.Time
			}
			latest := map[key]mark{}
			for _, r := range rounds {
				for _, it := range r.items {
					c := it
					c.Anchor = fold(it.Anchor)
					latest[key{c.Anchor, r.rev}] = mark{it: c, raised: r.at} // rounds are chronological: later wins
				}
			}
			resAt := map[string]Resolution{}
			for _, r := range resolutions {
				ca := fold(r.Anchor)
				if cur, ok := resAt[ca]; !ok || r.At.After(cur.At) {
					resAt[ca] = r
				}
			}

			// INVARIANTS after every op: real merge ≡ oracle.
			merged, err := AssembleReview(dir)
			if err != nil {
				t.Fatalf("seed %d op %d assemble: %v", seed, op, err)
			}
			got := map[key]MergedItem{}
			for _, it := range merged {
				k := key{it.Anchor, it.Reviewer}
				if _, dup := got[k]; dup {
					t.Fatalf("seed %d op %d: duplicate merged key %+v", seed, op, k)
				}
				got[k] = it
			}
			if len(got) != len(latest) {
				t.Fatalf("seed %d op %d: conservation broken: real=%d oracle=%d\nreal=%+v", seed, op, len(got), len(latest), merged)
			}
			for k, m := range latest {
				g, ok := got[k]
				if !ok {
					t.Fatalf("seed %d op %d: mark %+v LOST", seed, op, k)
				}
				if g.Note != m.it.Note || g.Status != m.it.Status {
					t.Fatalf("seed %d op %d: human words mutated: got %+v want %+v", seed, op, g.FeedbackItem, m.it)
				}
				wantOpen := true
				if r, ok := resAt[k.anchor]; ok {
					wantOpen = m.raised.After(r.At)
				}
				if g.Open != wantOpen {
					t.Fatalf("seed %d op %d: open-state law broken for %+v: got %v want %v", seed, op, k, g.Open, wantOpen)
				}
			}
		}
	}
}
