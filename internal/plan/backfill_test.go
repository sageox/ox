package plan

import (
	"context"
	"os"
	"path/filepath"
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
