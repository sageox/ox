package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/plan"
	"github.com/spf13/cobra"
)

// plan_backfill_test.go covers the cmd/ox layer of `ox plan backfill-titles`:
// dry-run output/no-op contract and the real-run apply+rename+commit
// orchestration. The retitle/rename/collision/idempotency computation itself
// is proven directly against internal/plan.ComputeBackfill/ApplyBackfillMeta
// in internal/plan/backfill_test.go — this file only proves the CLI plumbing
// wires those primitives together correctly, per the test pyramid split
// plan_lifecycle_test.go already documents for this command family.

// writeBackfillPlanFixture hand-writes a plan directory (plan.md, meta.json,
// events.jsonl) directly under ledgerPath/data/plans — the on-disk shape
// runPlanBackfillTitlesOnLedger reads — without going through plan.Save,
// since Save's ledger resolution goes through a package-private resolver
// tests outside internal/plan cannot override. buggyTopic simulates exactly
// what the pre-ox-1tjj.8 planTopic wrote: the first H2's text, not the H1's.
func writeBackfillPlanFixture(t *testing.T, ledgerPath, dirName, raw, buggyTopic string, createdAt time.Time) string {
	t.Helper()
	dir := filepath.Join(ledgerPath, "data", "plans", dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := plan.Meta{Topic: buggyTopic, Slug: plan.Slugify(buggyTopic), CreatedAt: createdAt}
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), metaBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	ev := plan.Event{PlanID: "pln_backfilltest00000001", Kind: plan.EventCreated, Status: plan.PlanStatusDraft, Topic: buggyTopic}
	if err := plan.AppendEvent(context.Background(), dir, ev); err != nil {
		t.Fatalf("seed created event: %v", err)
	}
	return dir
}

// runBackfillForTest wires an isolated cobra.Command (own buffer, own
// context) and calls runPlanBackfillTitlesOnLedger directly against
// ledgerPath — the testable core, bypassing findGitRoot/config resolution.
func runBackfillForTest(t *testing.T, ledgerPath string, dryRun bool) (string, error) {
	t.Helper()
	return runBackfillForTestWithFlags(t, ledgerPath, dryRun, false)
}

// runBackfillForTestWithFlags is runBackfillForTest plus the --allow-diverged
// escape hatch, for the preflight-guard tests that need to exercise it.
func runBackfillForTestWithFlags(t *testing.T, ledgerPath string, dryRun, allowDiverged bool) (string, error) {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := runPlanBackfillTitlesOnLedger(cmd, ledgerPath, dryRun, allowDiverged)
	return out.String(), err
}

const backfillTestRaw = "# Conversation model update — the execution plan\n\n" +
	"## 1. Context — Why Now\n\nframing prose.\n\n## Approach\n\ndo the thing.\n"

func TestRunPlanBackfillTitlesOnLedger_DryRunTouchesNothing(t *testing.T) {
	ledger := t.TempDir()
	dir := writeBackfillPlanFixture(t, ledger, "2026-05-01-1-context-why-now", backfillTestRaw, "1. Context — Why Now", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))

	metaBefore, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}

	out, err := runBackfillForTest(t, ledger, true)
	if err != nil {
		t.Fatalf("runPlanBackfillTitlesOnLedger dry-run: %v", err)
	}

	if !strings.Contains(out, "Dry run — nothing written.") {
		t.Errorf("output missing dry-run banner:\n%s", out)
	}
	if !strings.Contains(out, "total=1 retitled=1 renamed=1 skipped=0") {
		t.Errorf("output missing expected counts:\n%s", out)
	}
	if !strings.Contains(out, "Conversation model update") {
		t.Errorf("output missing the corrected title:\n%s", out)
	}
	if !strings.Contains(out, "retitled + renamed") {
		t.Errorf("output missing the retitled+renamed action:\n%s", out)
	}

	// Nothing on disk changed: same dir, same meta.json bytes, no new dir.
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("original plan dir was removed/renamed during a dry-run: %v", err)
	}
	metaAfter, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(metaBefore) != string(metaAfter) {
		t.Errorf("meta.json changed during a dry-run")
	}
	entries, err := os.ReadDir(filepath.Join(ledger, "data", "plans"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("plans dir has %d entries after dry-run, want 1 (no rename applied)", len(entries))
	}
}

func TestRunPlanBackfillTitlesOnLedger_DryRunNoPlansIsCleanNoOp(t *testing.T) {
	ledger := t.TempDir()
	out, err := runBackfillForTest(t, ledger, true)
	if err != nil {
		t.Fatalf("runPlanBackfillTitlesOnLedger on an empty ledger: %v", err)
	}
	if !strings.Contains(out, "total=0") {
		t.Errorf("output = %q, want total=0", out)
	}
}

// initGitLedger turns ledgerPath into a minimal git repo with one commit —
// the real-run path's git mv/add/commit needs a working git repository.
// Pushing is best-effort (see runPlanBackfillTitlesOnLedger's contract
// mirroring commitPlanToLedger/savePlanArtifacts) and is expected to fail
// here (no remote configured) without failing the command.
func initGitLedger(t *testing.T, ledgerPath string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = ledgerPath
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(ledgerPath, ".gitignore"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "init")
}

// TestRunPlanBackfillTitlesOnLedger_RealRunRenamesAndCommits exercises the
// real (non-dry-run) path end to end against an actual git repo: the plan
// directory is renamed on disk, its meta.json is corrected, and a batch
// commit lands — all without a network push succeeding (best-effort, must
// not fail the command).
func TestRunPlanBackfillTitlesOnLedger_RealRunRenamesAndCommits(t *testing.T) {
	ledger := t.TempDir()
	oldDir := writeBackfillPlanFixture(t, ledger, "2026-05-01-1-context-why-now", backfillTestRaw, "1. Context — Why Now", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	initGitLedger(t, ledger)

	out, err := runBackfillForTest(t, ledger, false)
	if err != nil {
		t.Fatalf("runPlanBackfillTitlesOnLedger real run: %v", err)
	}
	if !strings.Contains(out, "Backfilled: total=1 retitled=1 renamed=1 skipped=0") {
		t.Errorf("output missing expected summary:\n%s", out)
	}

	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Errorf("old plan dir still exists after a real run: stat err=%v", err)
	}
	newDir := filepath.Join(ledger, "data", "plans", "2026-05-01-conversation-model-update-the-execution-plan")
	metaBytes, err := os.ReadFile(filepath.Join(newDir, "meta.json"))
	if err != nil {
		t.Fatalf("renamed plan dir missing meta.json: %v", err)
	}
	var meta plan.Meta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Topic != "Conversation model update — the execution plan" {
		t.Errorf("meta.Topic = %q, want the corrected H1 title", meta.Topic)
	}

	// The rename + edit landed in a real git commit (whether or not the
	// best-effort push to a nonexistent remote succeeded).
	logCmd := exec.Command("git", "log", "--oneline")
	logCmd.Dir = ledger
	logOut, err := logCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, logOut)
	}
	if !strings.Contains(string(logOut), "plan: backfill 1 title") {
		t.Errorf("git log missing the batch commit:\n%s", logOut)
	}
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = ledger
	statusOut, err := statusCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v\n%s", err, statusOut)
	}
	if strings.TrimSpace(string(statusOut)) != "" {
		t.Errorf("working tree not clean after the batch commit:\n%s", statusOut)
	}
}

// TestRunPlanBackfillTitlesOnLedger_RealRunIdempotent proves a second real
// run on an already-backfilled ledger is a clean no-op: no further rename,
// no new commit.
func TestRunPlanBackfillTitlesOnLedger_RealRunIdempotent(t *testing.T) {
	ledger := t.TempDir()
	writeBackfillPlanFixture(t, ledger, "2026-05-01-1-context-why-now", backfillTestRaw, "1. Context — Why Now", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	initGitLedger(t, ledger)

	if _, err := runBackfillForTest(t, ledger, false); err != nil {
		t.Fatalf("first real run: %v", err)
	}
	// exec.Cmd runs exactly once — a fresh Command is needed for each git log
	// call, not a reused one.
	gitLog := func() string {
		t.Helper()
		cmd := exec.Command("git", "log", "--oneline")
		cmd.Dir = ledger
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git log: %v\n%s", err, out)
		}
		return string(out)
	}
	before := gitLog()

	out, err := runBackfillForTest(t, ledger, false)
	if err != nil {
		t.Fatalf("second real run: %v", err)
	}
	if !strings.Contains(out, "Nothing to backfill") {
		t.Errorf("second run output = %q, want the no-op message", out)
	}

	after := gitLog()
	if before != after {
		t.Errorf("a no-op second run created a new commit:\nbefore: %s\nafter:  %s", before, after)
	}
}
