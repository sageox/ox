package main

// plan_status_display_test.go covers the cmd/ox layer of Task B's status
// display: `ox plan list`'s STATUS column and `ox plan view`'s status line,
// both backed by plan.PlanInfo.Status (plan.CurrentStatus — folded
// events.jsonl, falling back to meta.json for legacy plans). The underlying
// fold-or-fallback logic and its wiring into PlanInfo are already proven
// directly at the internal/plan package level
// (internal/plan/current_status_test.go); this file proves the CLI plumbing
// actually renders that field, per the test pyramid split
// plan_lifecycle_test.go already documents for this command family.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/plan"
	"github.com/spf13/cobra"
)

// newPlanStatusTestRepo creates an isolated git repo with a minimal
// .sageox/config.json (so config.LoadProjectContext succeeds). It reuses
// newPlanEnrichTestRepo's HOME sandbox + t.Chdir, then ADDITIONALLY
// sandboxes every XDG_*_HOME var: plan.Save/List/Load's ledger resolution
// (ctx.DefaultLedgerPath, a pure function of repo_id + endpoint — NOT of any
// registered LocalConfig ledger override, see store.go's
// defaultLedgerResolver) goes through paths.DataDir(), which prefers
// $XDG_DATA_HOME over $HOME when the env var is set. A real dev machine
// commonly has XDG_DATA_HOME set (e.g. to ~/.local/share) — HOME alone is
// NOT enough sandboxing here, or this test silently writes into the real
// user's ~/.local/share/sageox ledger tree instead of a temp dir.
func newPlanStatusTestRepo(t *testing.T) string {
	t.Helper()
	root := newPlanEnrichTestRepo(t)

	xdgHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdgHome, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(xdgHome, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(xdgHome, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(xdgHome, "state"))

	sageoxDir := filepath.Join(root, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"config_version":"2","repo_id":"plan-status-test-repo"}`
	if err := os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestPlanListView_RealizedEventShowsEvenWithStaleMeta is the CLI-level
// proof of "fold wins": a plan whose events.jsonl has advanced to realized,
// but whose meta.json dual-write mirror is still draft (simulating a missed
// dual-write), must show realized in both `ox plan list`'s STATUS column and
// `ox plan view`'s status line.
func TestPlanListView_RealizedEventShowsEvenWithStaleMeta(t *testing.T) {
	root := newPlanStatusTestRepo(t)

	meta := plan.Meta{Topic: "Realized despite stale meta", CreatedAt: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)}
	dir, err := plan.Save(root, plan.Input{Raw: "# Realized despite stale meta\n"}, plan.Result{}, nil, meta)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	events, err := plan.LoadEvents(dir)
	if err != nil || len(events) != 1 {
		t.Fatalf("LoadEvents after Save: err=%v n=%d", err, len(events))
	}
	// Append a realized event via the low-level primitive (not
	// AppendPlanEvent), simulating a meta.json dual-write miss: events.jsonl
	// advances, meta.json stays draft.
	if err := plan.AppendEvent(context.Background(), dir, plan.Event{
		PlanID: events[0].PlanID, Kind: plan.EventRealized, Status: plan.PlanStatusRealized,
	}); err != nil {
		t.Fatalf("append realized: %v", err)
	}
	if gotMeta, merr := plan.ReadPlanMeta(root, "realized-despite-stale-meta"); merr != nil || gotMeta.Status != plan.PlanStatusDraft {
		t.Fatalf("precondition failed: meta.json Status = %+v (err=%v), want draft (still stale)", gotMeta, merr)
	}

	var listOut bytes.Buffer
	listCmd := &cobra.Command{}
	listCmd.SetOut(&listOut)
	if err := runPlanList(listCmd, false); err != nil {
		t.Fatalf("runPlanList: %v", err)
	}
	if !strings.Contains(listOut.String(), "realized") {
		t.Errorf("ox plan list output missing realized status:\n%s", listOut.String())
	}

	var viewOut bytes.Buffer
	viewCmd := &cobra.Command{}
	viewCmd.SetOut(&viewOut)
	if err := runPlanView(viewCmd, "realized-despite-stale-meta"); err != nil {
		t.Fatalf("runPlanView: %v", err)
	}
	if !strings.Contains(viewOut.String(), "status: realized") {
		t.Errorf("ox plan view output missing 'status: realized' line:\n%s", viewOut.String())
	}
}

// TestPlanListView_LegacyPlanFallsBackToMetaStatus verifies a plan saved
// before the event log existed (meta.json only, no events.jsonl) still shows
// its meta.json status in both `ox plan list` and `ox plan view` — the
// additive fallback this task requires.
func TestPlanListView_LegacyPlanFallsBackToMetaStatus(t *testing.T) {
	root := newPlanStatusTestRepo(t)

	ctx, err := config.LoadProjectContext(root)
	if err != nil || ctx == nil {
		t.Fatalf("LoadProjectContext: %v", err)
	}
	ledgerPath := ctx.DefaultLedgerPath()
	if ledgerPath == "" {
		t.Fatal("DefaultLedgerPath is empty")
	}
	dir := filepath.Join(ledgerPath, "data", "plans", "2026-01-01-legacy-cli-plan")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"topic":"Legacy CLI plan","slug":"legacy-cli-plan","created_at":"2026-01-01T00:00:00Z","status":"approved"}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte("# Legacy CLI plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var listOut bytes.Buffer
	listCmd := &cobra.Command{}
	listCmd.SetOut(&listOut)
	if err := runPlanList(listCmd, false); err != nil {
		t.Fatalf("runPlanList: %v", err)
	}
	if !strings.Contains(listOut.String(), "approved") {
		t.Errorf("ox plan list output missing legacy meta.json status (approved):\n%s", listOut.String())
	}

	var viewOut bytes.Buffer
	viewCmd := &cobra.Command{}
	viewCmd.SetOut(&viewOut)
	if err := runPlanView(viewCmd, "legacy-cli-plan"); err != nil {
		t.Fatalf("runPlanView: %v", err)
	}
	if !strings.Contains(viewOut.String(), "status: approved") {
		t.Errorf("ox plan view output missing 'status: approved' line:\n%s", viewOut.String())
	}
}
