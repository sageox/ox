package plan

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
)

// savePlanWithBuggyTopic writes a plan via Save, but stamps meta.Topic/Slug
// with an explicit (wrong) value rather than letting Save derive it — this is
// how every real corpus plan ended up mistitled: the pre-ox-1tjj.8 cmd/ox
// planTopic computed the first H2's text (Parse only splits on H2, so it
// could never see raw's H1) and handed THAT to Save as meta.Topic. Save
// itself never derives a topic; it only persists what the caller supplies.
func savePlanWithBuggyTopic(t *testing.T, ledger, raw, buggyTopic string, createdAt time.Time) string {
	t.Helper()
	withLedger(t, ledger)
	meta := Meta{Topic: buggyTopic, Slug: Slugify(buggyTopic), CreatedAt: createdAt}
	dir, err := Save("/g", Input{Raw: raw}, sampleResult(), nil, meta)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	return dir
}

// h1FollowedByContextH2 is the exact real-world shape ox-1tjj.8 shipped:
// an H1 real title followed by a "1. Context — Why Now" H2, whose text the
// buggy planTopic grabbed instead.
const h1FollowedByContextH2 = "# Conversation model update — the execution plan\n\n" +
	"## 1. Context — Why Now\n\nframing prose.\n\n## Approach\n\ndo the thing.\n"

func TestComputeBackfill_RenameCorrectsTopicAndPreservesOriginalDatePrefix(t *testing.T) {
	ledger := t.TempDir()
	// deliberately NOT today — proves the rename keeps the plan's ORIGINAL
	// save date, not the date the backfill happens to run on.
	created := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	dir := savePlanWithBuggyTopic(t, ledger, h1FollowedByContextH2, "1. Context — Why Now", created)
	plansDir := filepath.Dir(dir)

	candidates, err := ComputeBackfill(plansDir)
	if err != nil {
		t.Fatalf("ComputeBackfill: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1: %+v", len(candidates), candidates)
	}
	c := candidates[0]

	if !c.Changed() || !c.TopicChanged || !c.SlugChanged {
		t.Fatalf("candidate not flagged as changed: %+v", c)
	}
	wantTopic := "Conversation model update — the execution plan"
	if c.NewTopic != wantTopic {
		t.Errorf("NewTopic = %q, want %q", c.NewTopic, wantTopic)
	}
	if c.OldTopic != "1. Context — Why Now" {
		t.Errorf("OldTopic = %q, want the original buggy topic", c.OldTopic)
	}
	wantSlug := Slugify(wantTopic)
	if c.NewSlug != wantSlug {
		t.Errorf("NewSlug = %q, want %q", c.NewSlug, wantSlug)
	}
	if c.NewDir == "" {
		t.Fatal("NewDir is empty, want a rename")
	}
	wantDirName := "2026-05-01-" + wantSlug
	if got := filepath.Base(c.NewDir); got != wantDirName {
		t.Errorf("NewDir basename = %q, want %q (original date prefix preserved, not today's date)", got, wantDirName)
	}
}

func TestComputeBackfill_TLDRRegression(t *testing.T) {
	ledger := t.TempDir()
	raw := "# ox plan — Team-Context-Enriched Plans\n\n## TL;DR\n\nShip it.\n\n## Design\n\ndetails.\n"
	dir := savePlanWithBuggyTopic(t, ledger, raw, "TL;DR", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	plansDir := filepath.Dir(dir)

	candidates, err := ComputeBackfill(plansDir)
	if err != nil {
		t.Fatalf("ComputeBackfill: %v", err)
	}
	if len(candidates) != 1 || candidates[0].NewTopic != "ox plan — Team-Context-Enriched Plans" {
		t.Fatalf("candidates = %+v, want the corrected H1 topic", candidates)
	}
}

func TestComputeBackfill_AlreadyCorrectPlanIsUnchanged(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)
	// A plan saved by the FIXED code: meta.Topic already matches the H1.
	raw := "# Already Correct Plan\n\n## Approach\n\ndo it.\n"
	meta := Meta{Topic: "Already Correct Plan", Slug: Slugify("Already Correct Plan"), CreatedAt: time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)}
	dir, err := Save("/g", Input{Raw: raw}, sampleResult(), nil, meta)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	plansDir := filepath.Dir(dir)

	candidates, err := ComputeBackfill(plansDir)
	if err != nil {
		t.Fatalf("ComputeBackfill: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Changed() {
		t.Fatalf("candidates = %+v, want exactly 1 unchanged candidate", candidates)
	}
}

// TestComputeBackfill_CollisionSuffixing verifies two plans whose H1s
// collide on the same slug (Slugify truncates/normalizes, so distinct real
// titles CAN legitimately produce the same slug) get -2, -3, ... suffixes
// rather than one silently overwriting the other.
func TestComputeBackfill_CollisionSuffixing(t *testing.T) {
	ledger := t.TempDir()
	created := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	// Both correct to the exact same H1 text (and therefore the exact same
	// slug) but start from different buggy topics so Save gives them distinct
	// starting directories.
	raw := "# Add Login Support\n\n## 1. Context — Why Now\n\nframing.\n\n## Approach\n\ndo it.\n"
	dirA := savePlanWithBuggyTopic(t, ledger, raw, "1. Context — Why Now", created)
	raw2 := "# Add Login Support\n\n## TL;DR\n\nShip it.\n\n## Approach\n\ndo it.\n"
	dirB := savePlanWithBuggyTopic(t, ledger, raw2, "TL;DR", created)
	plansDir := filepath.Dir(dirA)
	if filepath.Dir(dirB) != plansDir {
		t.Fatalf("test setup: dirA/dirB not siblings: %q vs %q", dirA, dirB)
	}

	candidates, err := ComputeBackfill(plansDir)
	if err != nil {
		t.Fatalf("ComputeBackfill: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("got %d candidates, want 2: %+v", len(candidates), candidates)
	}

	// ComputeBackfill processes directory names in sorted order — dirA's
	// buggy slug ("1-context-why-now") sorts before dirB's ("tl-dr"), so dirA
	// claims the clean slug and dirB gets suffixed.
	byOldDir := map[string]BackfillPlan{}
	for _, c := range candidates {
		byOldDir[c.OldDir] = c
	}
	first, second := byOldDir[dirA], byOldDir[dirB]

	wantSlug := Slugify("Add Login Support")
	if first.NewSlug != wantSlug {
		t.Errorf("first candidate NewSlug = %q, want the clean %q", first.NewSlug, wantSlug)
	}
	if second.NewSlug != wantSlug+"-2" {
		t.Errorf("second candidate NewSlug = %q, want the suffixed %q", second.NewSlug, wantSlug+"-2")
	}
	if first.NewDir == second.NewDir {
		t.Fatalf("both candidates resolved to the same NewDir: %q", first.NewDir)
	}
}

// TestComputeBackfill_SkipsUnreadableDirButContinues verifies a single
// corrupt plan dir never aborts the batch: it's reported via SkipReason and
// scanning continues to the next dir.
func TestComputeBackfill_SkipsUnreadableDirButContinues(t *testing.T) {
	ledger := t.TempDir()
	created := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)
	good := savePlanWithBuggyTopic(t, ledger, h1FollowedByContextH2, "1. Context — Why Now", created)
	plansDir := filepath.Dir(good)

	broken := filepath.Join(plansDir, "2026-06-04-broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "meta.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	candidates, err := ComputeBackfill(plansDir)
	if err != nil {
		t.Fatalf("ComputeBackfill must not error on one broken dir: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("got %d candidates, want 2 (1 good + 1 skipped): %+v", len(candidates), candidates)
	}
	var sawSkip, sawGood bool
	for _, c := range candidates {
		switch c.OldDir {
		case broken:
			sawSkip = c.SkipReason != ""
		case good:
			sawGood = c.Changed()
		}
	}
	if !sawSkip {
		t.Error("broken dir was not reported with a SkipReason")
	}
	if !sawGood {
		t.Error("good dir alongside the broken one was not processed")
	}
}

// TestComputeBackfill_DryRunTouchesNothing proves the compute half is
// read-only: file contents and mtimes are byte-identical before and after.
func TestComputeBackfill_DryRunTouchesNothing(t *testing.T) {
	ledger := t.TempDir()
	dir := savePlanWithBuggyTopic(t, ledger, h1FollowedByContextH2, "1. Context — Why Now", time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC))
	plansDir := filepath.Dir(dir)

	metaPath := filepath.Join(dir, planMetaFile)
	before, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(metaPath)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ComputeBackfill(plansDir); err != nil {
		t.Fatalf("ComputeBackfill: %v", err)
	}

	after, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("meta.json content changed after ComputeBackfill (dry-run must never write)")
	}
	if !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Errorf("meta.json mtime changed after ComputeBackfill (dry-run must never write)")
	}
	if _, err := os.Stat(filepath.Join(dir, eventsFile)); err == nil {
		data, _ := os.ReadFile(filepath.Join(dir, eventsFile))
		if strings.Count(string(data), "\n") != 1 {
			t.Errorf("events.jsonl grew during a dry-run compute: %s", data)
		}
	}
}

// TestBackfill_Idempotent applies a full backfill pass (ApplyBackfillMeta +
// the directory rename the cmd layer would perform via git mv, simulated
// here with os.Rename since this test lives at the internal/plan layer) and
// then re-runs ComputeBackfill: the second pass must report every plan
// unchanged, append no new event, and propose no further rename.
func TestBackfill_Idempotent(t *testing.T) {
	ledger := t.TempDir()
	dir := savePlanWithBuggyTopic(t, ledger, h1FollowedByContextH2, "1. Context — Why Now", time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC))
	plansDir := filepath.Dir(dir)
	ctx := context.Background()

	candidates, err := ComputeBackfill(plansDir)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("ComputeBackfill first pass: %v, %+v", err, candidates)
	}
	c := candidates[0]
	if err := ApplyBackfillMeta(ctx, c.OldDir, c.NewTopic, c.NewSlug); err != nil {
		t.Fatalf("ApplyBackfillMeta: %v", err)
	}
	if c.NewDir != "" {
		if err := os.Rename(c.OldDir, c.NewDir); err != nil {
			t.Fatalf("simulated git mv (os.Rename): %v", err)
		}
	}

	eventsBefore, err := LoadEvents(c.NewDir)
	if err != nil || len(eventsBefore) != 2 { // created (Save) + revised (ApplyBackfillMeta)
		t.Fatalf("LoadEvents after first pass: err=%v n=%d", err, len(eventsBefore))
	}

	second, err := ComputeBackfill(plansDir)
	if err != nil {
		t.Fatalf("ComputeBackfill second pass: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("second pass found %d plans, want 1: %+v", len(second), second)
	}
	if second[0].Changed() {
		t.Fatalf("second pass reports a change on an already-backfilled plan: %+v", second[0])
	}
	if second[0].NewDir != "" {
		t.Errorf("second pass proposes a further rename: %+v", second[0])
	}

	eventsAfter, err := LoadEvents(c.NewDir)
	if err != nil || len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("a no-op second pass must append no event: before=%d after=%d (err=%v)", len(eventsBefore), len(eventsAfter), err)
	}
}

// TestAppendBackfillRevisedEvent_UnreadableEventsNeverMintsNewID is the
// finding-5 regression: a plan dir with real prior history whose
// events.jsonl becomes unreadable (here, a malformed trailing line) must
// fail the append rather than treat "unreadable" the same as "no prior
// events" — that used to mint a fresh pln_ id and record a SECOND `created`
// event on top of the plan's real history, forking its identity.
func TestAppendBackfillRevisedEvent_UnreadableEventsNeverMintsNewID(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	seedID := "pln_original0000000000001"
	if err := AppendEvent(ctx, dir, Event{PlanID: seedID, Kind: EventCreated, Status: PlanStatusDraft, Topic: "Old Topic"}); err != nil {
		t.Fatalf("seed created event: %v", err)
	}
	eventsPath := filepath.Join(dir, eventsFile)
	before, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeLines := strings.Count(strings.TrimRight(string(before), "\n"), "\n") + 1

	// Corrupt the log with one malformed line — LoadEvents fails to parse
	// it, exactly the "momentarily unreadable" scenario the fix targets.
	f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("not valid json\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	gotID, err := appendBackfillRevisedEvent(ctx, dir, "New Topic")
	if err == nil {
		t.Fatalf("appendBackfillRevisedEvent succeeded despite unreadable events.jsonl; minted id=%q", gotID)
	}

	after, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	afterLines := strings.Count(strings.TrimRight(string(after), "\n"), "\n") + 1
	// beforeLines (1 seed line) + 1 injected corrupt line = 2. A buggy
	// implementation appends a THIRD line (the minted `created` event) here.
	if afterLines != beforeLines+1 {
		t.Errorf("events.jsonl grew from %d to %d lines on a failed append (want unchanged at %d): %s",
			beforeLines, afterLines, beforeLines+1, after)
	}
	if strings.Contains(string(after), "New Topic") {
		t.Errorf("a revised/created event carrying the new topic was appended despite the load failure: %s", after)
	}
}

// TestAppendBackfillRevisedEvent_EmptyEventsStillMints is the companion
// positive case: a plan dir with a genuinely EMPTY (not corrupt, not
// missing-and-erroring) events.jsonl is the legacy pre-event-log plan the
// minting path exists for, and must still mint — the finding-5 fix only
// changes the ERROR case, not this one.
func TestAppendBackfillRevisedEvent_EmptyEventsStillMints(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	// No events.jsonl at all: LoadEvents returns (nil, nil), matching a
	// legacy plan saved before the event-log foundation shipped.

	gotID, err := appendBackfillRevisedEvent(ctx, dir, "New Topic")
	if err != nil {
		t.Fatalf("appendBackfillRevisedEvent on a legacy (no events.jsonl) plan: %v", err)
	}
	if gotID == "" {
		t.Fatal("no id minted for a legacy plan with no prior events")
	}

	events, err := LoadEvents(dir)
	if err != nil || len(events) != 1 {
		t.Fatalf("LoadEvents after mint: err=%v n=%d", err, len(events))
	}
	if events[0].Kind != EventCreated {
		t.Errorf("Kind = %q, want %q for the minted event", events[0].Kind, EventCreated)
	}
	if events[0].PlanID != gotID {
		t.Errorf("recorded PlanID = %q, want the returned %q", events[0].PlanID, gotID)
	}
}

// --- produced_plans rewrite ---

func writeTestSessionMeta(t *testing.T, sessionsDir, name string, producedPlans []string) string {
	t.Helper()
	dir := filepath.Join(sessionsDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := lfs.NewSessionMeta(name, "Person A", "Ox0001", "claude-code", time.Now().UTC()).
		ProducedPlans(producedPlans).
		Build()
	if err := lfs.WriteSessionMetaOnly(dir, meta); err != nil {
		t.Fatalf("write session meta: %v", err)
	}
	return dir
}

func TestComputeProducedPlansRewrite_FindsStaleSlug(t *testing.T) {
	sessionsDir := t.TempDir()
	writeTestSessionMeta(t, sessionsDir, "2026-06-01-a-Ox0001", []string{"1-context-why-now", "unrelated-plan"})
	writeTestSessionMeta(t, sessionsDir, "2026-06-02-b-Ox0002", []string{"unrelated-plan"})

	renamed := map[string]string{"1-context-why-now": "conversation-model-update-execution-plan"}
	rewrites, err := ComputeProducedPlansRewrite(sessionsDir, renamed)
	if err != nil {
		t.Fatalf("ComputeProducedPlansRewrite: %v", err)
	}
	if len(rewrites) != 1 {
		t.Fatalf("got %d rewrites, want 1 (only the session naming the stale slug): %+v", len(rewrites), rewrites)
	}
	if got := rewrites[0].OldSlugs; len(got) != 1 || got[0] != "1-context-why-now" {
		t.Errorf("OldSlugs = %v, want [1-context-why-now]", got)
	}
}

// --- BackfillRenameMap ---

// TestBackfillRenameMap covers the produced_plans rewrite map derivation in
// isolation from ComputeBackfill/ApplyBackfillMeta: given a hand-built set
// of candidates, which old slugs make it into the map and which get
// excluded.
func TestBackfillRenameMap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		candidates    []BackfillPlan
		wantRenamed   map[string]string
		wantAmbiguous map[string][]string
	}{
		{
			name: "slug change is included",
			candidates: []BackfillPlan{
				{OldSlug: "old-a", NewSlug: "new-a", SlugChanged: true},
			},
			wantRenamed:   map[string]string{"old-a": "new-a"},
			wantAmbiguous: map[string][]string{},
		},
		{
			name: "topic-only change (no slug change) is excluded",
			candidates: []BackfillPlan{
				{OldSlug: "same-slug", NewSlug: "same-slug", SlugChanged: false, TopicChanged: true},
			},
			wantRenamed:   map[string]string{},
			wantAmbiguous: map[string][]string{},
		},
		{
			name: "a skipped (unreadable) candidate is excluded even if fields look like a slug change",
			candidates: []BackfillPlan{
				{OldSlug: "old-a", NewSlug: "new-a", SlugChanged: true, SkipReason: "unreadable meta.json: boom"},
			},
			wantRenamed:   map[string]string{},
			wantAmbiguous: map[string][]string{},
		},
		{
			name: "same old slug, same new slug from two dirs is not ambiguous",
			candidates: []BackfillPlan{
				{OldDir: "/plans/2026-05-01-dup", OldSlug: "dup", NewSlug: "target", SlugChanged: true},
				{OldDir: "/plans/2026-06-01-dup", OldSlug: "dup", NewSlug: "target", SlugChanged: true},
			},
			wantRenamed:   map[string]string{"dup": "target"},
			wantAmbiguous: map[string][]string{},
		},
		{
			// The exact scenario finding 2 describes: two plans on different
			// dates share a bare old slug (ComputeBackfill's collision
			// suffixing dedupes on the full dir name INCLUDING the date
			// prefix, so this is a legitimate, expected input — not
			// corruption) but derive different new slugs. produced_plans
			// stores only the bare slug, so there is no way to tell which
			// session meant which plan: the old slug must be dropped from
			// the rewrite map entirely rather than guessed.
			name: "same old slug, different new slugs is ambiguous and excluded",
			candidates: []BackfillPlan{
				{OldDir: "/plans/2026-05-01-dup", OldSlug: "dup", NewSlug: "target-a", SlugChanged: true},
				{OldDir: "/plans/2026-06-01-dup", OldSlug: "dup", NewSlug: "target-b", SlugChanged: true},
				{OldDir: "/plans/2026-07-01-clean", OldSlug: "clean", NewSlug: "clean-target", SlugChanged: true},
			},
			wantRenamed:   map[string]string{"clean": "clean-target"},
			wantAmbiguous: map[string][]string{"dup": {"target-a", "target-b"}},
		},
		{
			// Three-way collision: the third candidate must still be folded
			// into the same ambiguous entry, not treated as a fresh conflict.
			name: "three candidates sharing an old slug all land in one ambiguous entry",
			candidates: []BackfillPlan{
				{OldDir: "/plans/2026-05-01-dup", OldSlug: "dup", NewSlug: "target-a", SlugChanged: true},
				{OldDir: "/plans/2026-06-01-dup", OldSlug: "dup", NewSlug: "target-b", SlugChanged: true},
				{OldDir: "/plans/2026-07-01-dup", OldSlug: "dup", NewSlug: "target-c", SlugChanged: true},
			},
			wantRenamed:   map[string]string{},
			wantAmbiguous: map[string][]string{"dup": {"target-a", "target-b", "target-c"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotRenamed, gotAmbiguous := BackfillRenameMap(tt.candidates)

			if len(gotRenamed) != len(tt.wantRenamed) {
				t.Fatalf("renamed = %v, want %v", gotRenamed, tt.wantRenamed)
			}
			for k, v := range tt.wantRenamed {
				if gotRenamed[k] != v {
					t.Errorf("renamed[%q] = %q, want %q", k, gotRenamed[k], v)
				}
			}

			if len(gotAmbiguous) != len(tt.wantAmbiguous) {
				t.Fatalf("ambiguous = %v, want %v", gotAmbiguous, tt.wantAmbiguous)
			}
			for k, want := range tt.wantAmbiguous {
				got := append([]string(nil), gotAmbiguous[k]...)
				sort.Strings(got)
				wantSorted := append([]string(nil), want...)
				sort.Strings(wantSorted)
				if len(got) != len(wantSorted) {
					t.Errorf("ambiguous[%q] = %v, want %v", k, got, wantSorted)
					continue
				}
				for i := range got {
					if got[i] != wantSorted[i] {
						t.Errorf("ambiguous[%q] = %v, want %v", k, got, wantSorted)
						break
					}
				}
			}
		})
	}
}

func TestComputeProducedPlansRewrite_EmptyRenamedIsNoOp(t *testing.T) {
	sessionsDir := t.TempDir()
	writeTestSessionMeta(t, sessionsDir, "2026-06-01-a-Ox0001", []string{"some-slug"})
	rewrites, err := ComputeProducedPlansRewrite(sessionsDir, nil)
	if err != nil {
		t.Fatalf("ComputeProducedPlansRewrite: %v", err)
	}
	if len(rewrites) != 0 {
		t.Errorf("rewrites = %+v, want none when renamed is empty", rewrites)
	}
}

func TestApplyProducedPlansRewrite_RewritesInPlace(t *testing.T) {
	sessionsDir := t.TempDir()
	dir := writeTestSessionMeta(t, sessionsDir, "2026-06-01-a-Ox0001", []string{"1-context-why-now", "unrelated-plan"})

	renamed := map[string]string{"1-context-why-now": "conversation-model-update-execution-plan"}
	if err := ApplyProducedPlansRewrite(context.Background(), dir, renamed); err != nil {
		t.Fatalf("ApplyProducedPlansRewrite: %v", err)
	}

	got, err := lfs.ReadSessionMeta(dir)
	if err != nil {
		t.Fatalf("ReadSessionMeta: %v", err)
	}
	want := []string{"conversation-model-update-execution-plan", "unrelated-plan"}
	if len(got.ProducedPlans) != len(want) || got.ProducedPlans[0] != want[0] || got.ProducedPlans[1] != want[1] {
		t.Errorf("ProducedPlans = %v, want %v", got.ProducedPlans, want)
	}
}

func TestApplyProducedPlansRewrite_NoMatchIsNoOp(t *testing.T) {
	sessionsDir := t.TempDir()
	dir := writeTestSessionMeta(t, sessionsDir, "2026-06-01-a-Ox0001", []string{"unrelated-plan"})
	metaPath := filepath.Join(dir, "meta.json")
	before, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}

	renamed := map[string]string{"1-context-why-now": "conversation-model-update-execution-plan"}
	if err := ApplyProducedPlansRewrite(context.Background(), dir, renamed); err != nil {
		t.Fatalf("ApplyProducedPlansRewrite: %v", err)
	}

	after, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("meta.json rewritten despite no matching produced_plans entry")
	}
}
