package plan

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
)

// withLedger points the package ledger resolver at a temp ledger for the
// duration of a test, restoring the production resolver afterward.
func withLedger(t *testing.T, ledger string) {
	t.Helper()
	prev := ledgerResolver
	ledgerResolver = func(string) string { return ledger }
	t.Cleanup(func() { ledgerResolver = prev })
}

func sampleResult() Result {
	return Result{
		Annotations: []Annotation{
			{Section: "Storage", Kind: BadgeDeterministic, Type: BadgeCollision, Why: "touches open PR #42", Files: []string{"store.go"}},
		},
		Context: []ContextItem{
			{Kind: "adr", Title: "ADR-016", Ref: "docs/adr/016.md", Score: 0.9},
		},
		Signals: SignalSummary{Collisions: 1, PriorArt: 0, ExpertRoutes: 1, Material: true},
	}
}

func TestSaveListLoad_RoundTrip(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)

	created := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	in := Input{Path: "PLAN.md", Raw: "# Plan capture to ledger\n\nDo the thing."}
	res := sampleResult()
	meta := Meta{
		Topic:     "Plan capture to ledger",
		Authors:   []string{"Person A"},
		CreatedAt: created,
	}

	dir, _, err := Save("/fake/git/root", in, res, nil, meta)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// dir is the dated-slug folder under data/plans
	wantDir := filepath.Join(ledger, "data", "plans", "2026-06-03-plan-capture-to-ledger")
	if dir != wantDir {
		t.Fatalf("Save dir = %q, want %q", dir, wantDir)
	}

	// plan.md, annotations.json, meta.json are PLAIN git (not LFS pointers).
	for _, f := range []string{"plan.md", "annotations.json", "meta.json"} {
		p := filepath.Join(dir, f)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s to exist: %v", f, err)
		}
		if lfs.IsPointerFile(p) {
			t.Errorf("%s must be plain git, got an LFS pointer", f)
		}
	}

	// no plan.html when html == nil
	if _, err := os.Stat(filepath.Join(dir, "plan.html")); !os.IsNotExist(err) {
		t.Errorf("plan.html should not exist when html is nil, stat err=%v", err)
	}

	// List sees exactly one plan with the expected fields.
	plans, err := List("/fake/git/root")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("List returned %d plans, want 1", len(plans))
	}
	got := plans[0]
	if got.Slug != "plan-capture-to-ledger" {
		t.Errorf("slug = %q, want plan-capture-to-ledger", got.Slug)
	}
	if got.Topic != "Plan capture to ledger" {
		t.Errorf("topic = %q", got.Topic)
	}
	if got.HasHTML {
		t.Errorf("HasHTML = true, want false")
	}
	if !got.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, created)
	}

	// Load round-trips the markdown and the Result.
	planMD, loadedRes, info, err := Load("/fake/git/root", "plan-capture-to-ledger")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if planMD != in.Raw {
		t.Errorf("Load markdown = %q, want %q", planMD, in.Raw)
	}
	if loadedRes.Signals != res.Signals {
		t.Errorf("Load signals = %+v, want %+v", loadedRes.Signals, res.Signals)
	}
	if len(loadedRes.Annotations) != 1 || loadedRes.Annotations[0].Type != BadgeCollision {
		t.Errorf("Load annotations = %+v", loadedRes.Annotations)
	}
	if info.Slug != "plan-capture-to-ledger" {
		t.Errorf("Load info slug = %q", info.Slug)
	}

	// SchemaVersion is stamped on both artifacts on write so a future reader
	// can detect/migrate an older layout. Without this, version-bumping the
	// on-disk shape later would be a silent guess.
	if loadedRes.SchemaVersion != SchemaVersion {
		t.Errorf("annotations.json schema_version = %q, want %q", loadedRes.SchemaVersion, SchemaVersion)
	}
	metaBytes, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var gotMeta Meta
	if err := json.Unmarshal(metaBytes, &gotMeta); err != nil {
		t.Fatal(err)
	}
	if gotMeta.SchemaVersion != SchemaVersion {
		t.Errorf("meta.json schema_version = %q, want %q", gotMeta.SchemaVersion, SchemaVersion)
	}

	// The dropped approval fields must not reappear in the serialized meta.
	// Their presence would imply an approval flow that does not exist.
	if strings.Contains(string(metaBytes), "approved") || strings.Contains(string(metaBytes), "reviewed_by") {
		t.Errorf("meta.json must not carry approval fields: %s", metaBytes)
	}
}

// TestSave_AlwaysWritesRetrievablePlainHTML pins the GH #810 invariant: a
// successful Save leaves plan.html RETRIEVABLE, at EVERY size — including above
// htmlLFSThreshold, which is exactly where the bug lived.
//
// Failure prevented: Save wrote a large render as an LFS pointer WITHOUT
// uploading the blob, so the content named by the pointer existed nowhere — lost,
// and the poisoned pointer wedged the ledger push. Save is the no-network layer
// now, so it writes plain at every size; pointerizing a large render is the CLI
// caller's job (DehydrateHTML), only AFTER a confirmed upload.
//
// The old test asserted the LARGE case produced a pointer with the right OID —
// which the bug produced too. That is test theater: it passed while the content
// was being discarded. This asserts the bytes come back.
func TestSave_AlwaysWritesRetrievablePlainHTML(t *testing.T) {
	tests := []struct {
		name string
		size int
	}{
		{"small render stays plain", 1024},
		{"large render crosses the old LFS threshold", htmlLFSThreshold + 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ledger := t.TempDir()
			withLedger(t, ledger)

			// distinctive payload so a silently-discarded render can't masquerade
			// as an empty-but-present file.
			html := bytes.Repeat([]byte("PRESERVE-ME "), tc.size/12+1)[:tc.size]
			in := Input{Raw: "# H\n"}
			meta := Meta{Topic: "Size gate", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}

			dir, _, err := Save("/g", in, sampleResult(), html, meta)
			if err != nil {
				t.Fatalf("Save: %v", err)
			}

			htmlPath := filepath.Join(dir, "plan.html")
			if lfs.IsPointerFile(htmlPath) {
				t.Fatalf("Save wrote plan.html as an LFS pointer (size=%d); it must write plain — "+
					"pointerizing without an upload is GH #810", tc.size)
			}
			got, err := os.ReadFile(htmlPath)
			if err != nil {
				t.Fatalf("read plan.html: %v", err)
			}
			// StampHTMLMeta may rewrite tag-bearing HTML; this payload has no tags,
			// so the bytes must round-trip exactly.
			if !bytes.Equal(got, html) {
				t.Fatalf("plan.html not retrievable: got %d bytes, want %d", len(got), len(html))
			}

			plans, err := List("/g")
			if err != nil || len(plans) != 1 {
				t.Fatalf("List: %v (n=%d)", err, len(plans))
			}
			if !plans[0].HasHTML {
				t.Errorf("HasHTML = false, want true")
			}
		})
	}
}

func TestList_FailOpen(t *testing.T) {
	t.Run("no ledger configured", func(t *testing.T) {
		withLedger(t, "")
		plans, err := List("/g")
		if err != nil {
			t.Fatalf("List err = %v, want nil (fail-open)", err)
		}
		if plans != nil {
			t.Errorf("List = %v, want nil", plans)
		}
	})

	t.Run("ledger exists but no plans dir", func(t *testing.T) {
		withLedger(t, t.TempDir())
		plans, err := List("/g")
		if err != nil {
			t.Fatalf("List err = %v, want nil", err)
		}
		if len(plans) != 0 {
			t.Errorf("List = %v, want empty", plans)
		}
	})

	t.Run("skips malformed plan dir", func(t *testing.T) {
		ledger := t.TempDir()
		withLedger(t, ledger)
		// a dir with no meta.json must be skipped, not error.
		bad := filepath.Join(ledger, "data", "plans", "2026-01-01-broken")
		if err := os.MkdirAll(bad, 0o755); err != nil {
			t.Fatal(err)
		}
		plans, err := List("/g")
		if err != nil {
			t.Fatalf("List err = %v", err)
		}
		if len(plans) != 0 {
			t.Errorf("List = %v, want empty (malformed skipped)", plans)
		}
	})
}

func TestSave_NoLedgerErrors(t *testing.T) {
	withLedger(t, "")
	_, _, err := Save("/g", Input{Raw: "# x"}, sampleResult(), nil, Meta{Topic: "x"})
	if err == nil {
		t.Fatal("Save with no ledger should error so the porcelain path can treat it as 'nothing to save'")
	}
}

func TestLoad_NotFound(t *testing.T) {
	withLedger(t, t.TempDir())
	_, _, _, err := Load("/g", "does-not-exist")
	if err == nil {
		t.Fatal("Load of missing slug should error")
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"basic phrase", "Plan capture to ledger", "plan-capture-to-ledger"},
		{"two words", "One Two", "one-two"},
		{"trims whitespace", "  Trim   Me  ", "trim-me"},
		{"strips punctuation", "A/B: C.d, E!", "a-b-c-d-e"},
		{"single word", "Single", "single"},
		{"empty input", "", "untitled-plan"},
		{"punctuation only", "!!!", "untitled-plan"},
		{"preserves case-folded compounds", "CamelCase Words", "camelcase-words"},
		// slugWordCap raised 4 -> 6 (ox-1tjj.8): a title with 5-6 words no
		// longer gets silently truncated.
		{"six-word cap keeps all six", "One Two Three Four Five Six", "one-two-three-four-five-six"},
		{"five words, under the new cap", "Five Six Seven Eight Nine", "five-six-seven-eight-nine"},
		{"seven words still caps at six", "One Two Three Four Five Six Seven", "one-two-three-four-five-six"},
		// trailing-stopword trim: a cap (or a title that just ends on a
		// function word) landing on "the"/"for"/etc. reads as a cut-off
		// mid-sentence, not a title.
		{"trims a single trailing stopword", "My Cool Plan For", "my-cool-plan"},
		{"trims a run of trailing stopwords", "Fix The Bug In The", "fix-the-bug"},
		{"cap truncation landing on a stopword gets trimmed", "One Two Three Four Five The Six", "one-two-three-four-five"},
		{"cap truncation NOT landing on a stopword keeps all six", "Add support for the new thing", "add-support-for-the-new-thing"},
		{"all-stopword input falls back", "The Of For", "untitled-plan"},
		// interior stopwords are NEVER stripped — only a trailing run.
		{"preserves interior stopwords", "Fix the bug in the parser", "fix-the-bug-in-the-parser"},
		{"conversation model update em-dash the execution plan", "Conversation model update — the execution plan", "conversation-model-update-the-execution-plan"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Slugify(tc.in); got != tc.want {
				t.Errorf("Slugify(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSave_DefaultsSlugAndTimestamp(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)

	// empty Slug and zero CreatedAt should be derived/defaulted.
	dir, _, err := Save("/g", Input{Raw: "# x"}, sampleResult(), nil, Meta{Topic: "Derive My Slug"})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if base := filepath.Base(dir); !strings.HasSuffix(base, "-derive-my-slug") {
		t.Errorf("dir base = %q, want suffix -derive-my-slug", base)
	}

	// meta.json carries a non-zero created_at and the derived slug.
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Slug != "derive-my-slug" {
		t.Errorf("meta.Slug = %q", meta.Slug)
	}
	if meta.CreatedAt.IsZero() {
		t.Errorf("meta.CreatedAt is zero, want defaulted")
	}
}

// --- Provenance, status, collaboration, and read-merge lifecycle ---

// TestSave_StampsProvenanceStatusAndCollab verifies a saved plan persists its
// forward link (provenance), collaboration signals, and defaults Status=draft.
// Failure prevented: plans saved with no committed link back to the producing
// session/agent, defeating the whole feature.
func TestSave_StampsProvenanceStatusAndCollab(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)

	in := Input{Path: "PLAN.md", Raw: "# Link plans to sessions\n\nbody"}
	meta := Meta{
		Topic:     "Link plans to sessions",
		CreatedAt: time.Date(2026, 6, 4, 9, 0, 0, 0, time.UTC),
		Provenance: &Provenance{
			SessionName: "2026-06-04-ryan-Oxab12", AgentID: "Oxab12",
			RepoID: "repo-1", AgentType: "claude-code", Model: "opus",
			AuthorName: "Person A", SessionOutcome: SessionOutcomeActive,
		},
		Collaboration: &CollabSignals{UserPrompts: 5, AgentQuestions: 3, ToolCalls: 12, DurationSeconds: 600},
	}

	if _, _, err := Save("/g", in, sampleResult(), nil, meta); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := ReadPlanMeta("/g", "link-plans-to-sessions")
	if err != nil {
		t.Fatalf("ReadPlanMeta: %v", err)
	}
	if got.Status != PlanStatusDraft {
		t.Errorf("Status = %q, want draft", got.Status)
	}
	if got.Provenance == nil || got.Provenance.AgentID != "Oxab12" || got.Provenance.SessionName != "2026-06-04-ryan-Oxab12" {
		t.Errorf("provenance not persisted: %+v", got.Provenance)
	}
	if got.Collaboration == nil || got.Collaboration.UserPrompts != 5 || got.Collaboration.DurationSeconds != 600 {
		t.Errorf("collaboration not persisted: %+v", got.Collaboration)
	}
}

// TestSave_ReadMergePreservesLifecycle verifies a re-save preserves a
// manually-set Status, the system-reconciled SessionOutcome, and the original
// CreatedAt while refreshing the rest.
// Failure prevented: the skill's full save (or any re-enrich) silently resets a
// plan marked "implemented" back to "draft", or loses the session outcome.
func TestSave_ReadMergePreservesLifecycle(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)

	orig := time.Date(2026, 6, 4, 8, 0, 0, 0, time.UTC)
	in := Input{Path: "P.md", Raw: "# My plan\n\nv1"}
	if _, _, err := Save("/g", in, sampleResult(), nil, Meta{Topic: "My plan", CreatedAt: orig}); err != nil {
		t.Fatalf("Save v1: %v", err)
	}

	// human marks implemented; stop reconciles the session outcome.
	if err := SetStatus("/g", "my-plan", PlanStatusImplemented); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if err := ReconcileSessionOutcome("/g", "my-plan", "ses_abc", SessionOutcomeStopped); err != nil {
		t.Fatalf("ReconcileSessionOutcome: %v", err)
	}

	// re-save (e.g. skill's full save) with a later CreatedAt and fresh result.
	if _, _, err := Save("/g", Input{Path: "P.md", Raw: "# My plan\n\nv2"}, sampleResult(), nil,
		Meta{Topic: "My plan", CreatedAt: orig.Add(2 * time.Hour)}); err != nil {
		t.Fatalf("Save v2: %v", err)
	}

	got, err := ReadPlanMeta("/g", "my-plan")
	if err != nil {
		t.Fatalf("ReadPlanMeta: %v", err)
	}
	if got.Status != PlanStatusImplemented {
		t.Errorf("Status = %q, want implemented preserved across re-save", got.Status)
	}
	if got.Provenance == nil || got.Provenance.SessionOutcome != SessionOutcomeStopped {
		t.Errorf("SessionOutcome not preserved: %+v", got.Provenance)
	}
	if got.Provenance.SessionID != "ses_abc" {
		t.Errorf("SessionID backfill lost on re-save: %+v", got.Provenance)
	}
	if !got.CreatedAt.Equal(orig) {
		t.Errorf("CreatedAt = %v, want original %v preserved", got.CreatedAt, orig)
	}
	// markdown still refreshed to v2.
	md, _, _, _ := Load("/g", "my-plan")
	if !strings.Contains(md, "v2") {
		t.Errorf("plan.md not refreshed on re-save: %q", md)
	}
}

// TestReconcileSessionOutcome_BackfillsSessionID verifies stop-time backfill of
// the canonical ses_ id alongside the outcome.
// Failure prevented: the forward link never gets its ses_ id (only the
// save-time session name), so a UI keyed on ses_ can't join.
func TestReconcileSessionOutcome_BackfillsSessionID(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)
	// "Topic Alpha", not "Topic A" — a trailing single-letter "A" would be
	// trimmed as the stopword "a" (Slugify's trailing-stopword trim), which is
	// incidental to what this test actually exercises (SessionID backfill).
	if _, _, err := Save("/g", Input{Raw: "# Topic Alpha\n"}, sampleResult(), nil, Meta{Topic: "Topic Alpha", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := ReconcileSessionOutcome("/g", "topic-alpha", "ses_7", SessionOutcomeAborted); err != nil {
		t.Fatalf("ReconcileSessionOutcome: %v", err)
	}
	got, _ := ReadPlanMeta("/g", "topic-alpha")
	if got.Provenance == nil || got.Provenance.SessionID != "ses_7" || got.Provenance.SessionOutcome != SessionOutcomeAborted {
		t.Errorf("backfill failed: %+v", got.Provenance)
	}
}

// TestMutatePlanMeta_MissingFileNoOp verifies the mutator gets nil for a
// missing meta.json and a nil return writes nothing.
func TestMutatePlanMeta_MissingFileNoOp(t *testing.T) {
	dir := t.TempDir()
	called := false
	err := MutatePlanMeta(context.Background(), dir, func(m *Meta) (*Meta, error) {
		called = true
		if m != nil {
			t.Errorf("expected nil meta for missing file, got %+v", m)
		}
		return nil, nil
	})
	if err != nil {
		t.Fatalf("MutatePlanMeta: %v", err)
	}
	if !called {
		t.Error("mutator not called")
	}
	if _, err := os.Stat(filepath.Join(dir, "meta.json")); !os.IsNotExist(err) {
		t.Error("meta.json should not be created on nil return")
	}
}

// TestReadPlanMeta_LegacyNoProvenance verifies a v1 plan dir (no provenance /
// status) loads cleanly with empty status — readers treat it as draft.
// Failure prevented: the new fields break reading plans saved before them.
func TestReadPlanMeta_LegacyNoProvenance(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)
	dir := filepath.Join(ledger, "data", "plans", "2026-01-01-legacy-plan")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"topic":"Legacy plan","slug":"legacy-plan","created_at":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPlanMeta("/g", "legacy-plan")
	if err != nil {
		t.Fatalf("ReadPlanMeta legacy: %v", err)
	}
	if got.Status != "" || got.Provenance != nil || got.Collaboration != nil {
		t.Errorf("legacy meta should have empty new fields, got %+v", got)
	}
}
