package plan

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestStableReviewPort_DeterministicPerPlan verifies the port derivation is
// stable (same plan → same port across runs/machines) and range-bounded.
// Failure prevented: a restarted review server binds a different origin, which
// strands the open tab's localStorage marks and kills its reconnect.
func TestStableReviewPort_DeterministicPerPlan(t *testing.T) {
	a := StableReviewPort("2026-07-01-my-plan")
	if b := StableReviewPort("2026-07-01-my-plan"); a != b {
		t.Fatalf("port not deterministic: %d vs %d", a, b)
	}
	if a < reviewPortBase || a >= reviewPortBase+reviewPortSlots {
		t.Fatalf("port %d outside stable range", a)
	}
	if StableReviewPort("2026-07-01-other-plan") == a {
		// not guaranteed distinct, but these two must not collide or the test
		// fixture is useless — pick different names if this ever fires.
		t.Log("two fixture plans hash to the same slot; harmless but rename one")
	}
}

// TestReviewServerState_RoundTripAndPermissions verifies the persisted server
// identity (port + token) survives a process restart and is written 0600 (it
// carries the review token). Failure prevented: a restart mints a fresh token,
// so the surviving tab's queued submits die on 403 forever.
func TestReviewServerState_RoundTripAndPermissions(t *testing.T) {
	ledger := t.TempDir()
	prev := ledgerResolver
	ledgerResolver = func(string) string { return ledger }
	t.Cleanup(func() { ledgerResolver = prev })

	const dirName = "2026-07-01-my-plan"
	if _, ok := LoadReviewServerState("/repo", dirName); ok {
		t.Fatal("no state saved yet — Load must report ok=false")
	}
	want := ReviewServerState{Port: 42711, Token: "cafe0123"}
	if err := SaveReviewServerState("/repo", dirName, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok := LoadReviewServerState("/repo", dirName)
	if !ok || got != want {
		t.Fatalf("round-trip = %+v ok=%v, want %+v", got, ok, want)
	}

	path := reviewServerStatePath("/repo", dirName)
	if !filepath.IsAbs(path) || filepath.Base(filepath.Dir(path)) != "plan-review" {
		t.Fatalf("state must live under the ledger cache plan-review dir, got %s", path)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("state file carries the token — want 0600, got %o", perm)
		}
	}
}

// TestReviewServerState_RejectsPathShapedDirName verifies a dir name can never
// steer the state file outside the cache dir. Failure prevented: a crafted
// slug writing token files to arbitrary paths.
func TestReviewServerState_RejectsPathShapedDirName(t *testing.T) {
	ledger := t.TempDir()
	prev := ledgerResolver
	ledgerResolver = func(string) string { return ledger }
	t.Cleanup(func() { ledgerResolver = prev })

	for _, bad := range []string{"../escape", "a/b", ""} {
		if p := reviewServerStatePath("/repo", bad); p != "" {
			t.Errorf("dir name %q must not resolve to a state path, got %s", bad, p)
		}
	}
}

// TestSave_RemapsOpenFeedbackOnUpdate verifies the WIRING, not just the remap
// unit: re-saving a plan (the real update path — same dated-slug dir, new
// html) re-anchors open feedback automatically. Failure prevented: remap
// exists but nothing calls it, so real plan updates still orphan marks.
func TestSave_RemapsOpenFeedbackOnUpdate(t *testing.T) {
	ledger := t.TempDir()
	prev := ledgerResolver
	ledgerResolver = func(string) string { return ledger }
	t.Cleanup(func() { ledgerResolver = prev })

	created := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	meta := Meta{Topic: "My Plan", Slug: "my-plan", CreatedAt: created}

	v1 := "# T\n\n## Rollout\n\n- Ship the CLI first\n"
	html1, err := RenderHTMLOpts(Parse(v1), Result{}, RenderOptions{Slug: "my-plan"})
	if err != nil {
		t.Fatalf("render v1: %v", err)
	}
	dir, _, err := Save("/repo", Input{Raw: v1}, Result{}, html1, meta)
	if err != nil {
		t.Fatalf("save v1: %v", err)
	}
	saveRound(t, dir, time.Now(), FeedbackItem{
		Anchor: AnchorFor("Rollout", "Ship the CLI first"), Section: "Rollout",
		Label: "Ship the CLI first", Status: FeedbackRequestChange, Note: "daemon first",
	})

	v2 := "# T\n\n## Rollout Plan\n\n- Ship the CLI first\n"
	html2, err := RenderHTMLOpts(Parse(v2), Result{}, RenderOptions{Slug: "my-plan"})
	if err != nil {
		t.Fatalf("render v2: %v", err)
	}
	if _, _, err := Save("/repo", Input{Raw: v2}, Result{}, html2, meta); err != nil {
		t.Fatalf("save v2: %v", err)
	}

	items, err := AssembleReview(dir)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(items) != 1 || items[0].Anchor != AnchorFor("Rollout Plan", "Ship the CLI first") {
		t.Fatalf("re-save must remap the open mark to the updated heading: %+v", items)
	}
	if items[0].RemappedFrom == "" || items[0].Note != "daemon first" {
		t.Fatalf("the human's mark must survive the update with provenance: %+v", items[0])
	}
}
