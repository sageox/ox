package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/plan"
)

// plan_backfill_preflight_test.go covers the two safety guards
// preflightPlanBackfill (plan_backfill_preflight.go) runs before
// runPlanBackfillTitlesOnLedger scans a single plan directory. The pure
// abort/proceed verdicts (VerifyCorpusComplete, VerifyNotDiverged) are
// proven directly in internal/plan/backfill_preflight_test.go; this file
// proves the git-backed counting wires into that verdict correctly, and
// that an abort here actually stops the command before it does anything —
// including in dry-run mode, which is the whole point of Guard 1.

// runGitInDir runs a git command in dir, failing the test on error.
func runGitInDir(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (in %s): %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// initGitLedgerWithOrigin builds a local git repo AND a bare "origin" remote
// pre-loaded with its current content, then fetches it — so origin/main
// resolves locally, which is what both guards compare against. Deliberately
// separate from plan_backfill_test.go's initGitLedger (which configures no
// remote at all): that absence is itself a fixture, exercising the
// "unresolvable origin/main" skip path every guard must degrade to safely.
//
// Forces the branch name to "main" via "git init -b main" regardless of the
// host's init.defaultBranch — both guards hardcode "origin/main" (matching
// production: ledgers are always on main), so a test host defaulting to
// "master" would silently make every case here exercise the skip path
// instead of the divergence/completeness path under test.
func initGitLedgerWithOrigin(t *testing.T, ledgerPath string) (originPath string) {
	t.Helper()
	runGitInDir(t, ledgerPath, "init", "-q", "-b", "main")
	runGitInDir(t, ledgerPath, "config", "user.email", "test@example.com")
	runGitInDir(t, ledgerPath, "config", "user.name", "Test")
	runGitInDir(t, ledgerPath, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(ledgerPath, ".gitignore"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, ledgerPath, "add", "-A")
	runGitInDir(t, ledgerPath, "commit", "-q", "-m", "init")

	originPath = t.TempDir()
	runGitInDir(t, originPath, "init", "-q", "--bare")
	runGitInDir(t, ledgerPath, "remote", "add", "origin", originPath)
	runGitInDir(t, ledgerPath, "push", "-q", "-u", "origin", "main")
	return originPath
}

// TestRunPlanBackfillTitlesOnLedger_NoRemoteSkipsBothGuards proves a ledger
// with no remote at all (the common case for a brand-new/local-only ledger)
// is NOT blocked by either guard — both degrade to a printed skip note and
// the command proceeds exactly as it did before these guards existed.
func TestRunPlanBackfillTitlesOnLedger_NoRemoteSkipsBothGuards(t *testing.T) {
	ledger := t.TempDir()
	writeBackfillPlanFixture(t, ledger, "2026-05-01-1-context-why-now", backfillTestRaw, "1. Context — Why Now", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	initGitLedger(t, ledger) // no remote configured

	out, err := runBackfillForTest(t, ledger, true)
	if err != nil {
		t.Fatalf("runPlanBackfillTitlesOnLedger with no remote configured: %v", err)
	}
	if !strings.Contains(out, "corpus completeness check skipped") {
		t.Errorf("output missing the corpus-completeness skip note:\n%s", out)
	}
	if !strings.Contains(out, "divergence check skipped") {
		t.Errorf("output missing the divergence skip note:\n%s", out)
	}
	if !strings.Contains(out, "Dry run — nothing written.") {
		t.Errorf("preflight skip notes must not block the rest of the command:\n%s", out)
	}
}

// TestRunPlanBackfillTitlesOnLedger_DivergedLedgerAborts proves Guard 2 stops
// a run when the ledger has a local commit origin/main doesn't have yet —
// landing a batch rename on top of that divergence is exactly what turns a
// routine repair into a manual rebase.
func TestRunPlanBackfillTitlesOnLedger_DivergedLedgerAborts(t *testing.T) {
	ledger := t.TempDir()
	writeBackfillPlanFixture(t, ledger, "2026-05-01-1-context-why-now", backfillTestRaw, "1. Context — Why Now", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	initGitLedgerWithOrigin(t, ledger)

	if err := os.WriteFile(filepath.Join(ledger, "unpushed.txt"), []byte("local only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, ledger, "add", "-A")
	runGitInDir(t, ledger, "commit", "-q", "-m", "local-only commit")

	out, err := runBackfillForTest(t, ledger, true)
	if err == nil {
		t.Fatalf("expected the divergence guard to abort, got output:\n%s", out)
	}
	var derr *plan.DivergenceError
	if !errors.As(err, &derr) {
		t.Fatalf("error = %v (%T), want *plan.DivergenceError", err, err)
	}
	if derr.Ahead != 1 || derr.Behind != 0 {
		t.Errorf("DivergenceError = {ahead:%d behind:%d}, want {ahead:1 behind:0}", derr.Ahead, derr.Behind)
	}
	if !strings.Contains(err.Error(), "--allow-diverged") {
		t.Errorf("abort message missing the --allow-diverged remedy: %v", err)
	}
}

// TestRunPlanBackfillTitlesOnLedger_AllowDivergedOverridesAbort proves the
// --allow-diverged escape hatch lets an operator who already knows their
// ledger is intentionally ahead/behind proceed anyway.
func TestRunPlanBackfillTitlesOnLedger_AllowDivergedOverridesAbort(t *testing.T) {
	ledger := t.TempDir()
	writeBackfillPlanFixture(t, ledger, "2026-05-01-1-context-why-now", backfillTestRaw, "1. Context — Why Now", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	initGitLedgerWithOrigin(t, ledger)

	if err := os.WriteFile(filepath.Join(ledger, "unpushed.txt"), []byte("local only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, ledger, "add", "-A")
	runGitInDir(t, ledger, "commit", "-q", "-m", "local-only commit")

	out, err := runBackfillForTestWithFlags(t, ledger, true, true)
	if err != nil {
		t.Fatalf("runPlanBackfillTitlesOnLedger with --allow-diverged: %v", err)
	}
	if !strings.Contains(out, "--allow-diverged was passed") {
		t.Errorf("output missing the allow-diverged proceeding note:\n%s", out)
	}
	if !strings.Contains(out, "Dry run — nothing written.") {
		t.Errorf("the divergence override must not block the rest of the command:\n%s", out)
	}
}

// TestRunPlanBackfillTitlesOnLedger_IncompleteCorpusAbortsInDryRun reproduces
// the exact production incident: a plan directory present in git history
// (and on origin/main) but missing from the working tree, e.g. because
// git sparse-checkout set evicted it. Guard 1 must abort even for --dry-run
// — a dry run's table is only trustworthy if the corpus it scanned was
// complete, and this is the case where it silently wasn't.
func TestRunPlanBackfillTitlesOnLedger_IncompleteCorpusAbortsInDryRun(t *testing.T) {
	ledger := t.TempDir()
	dirA := writeBackfillPlanFixture(t, ledger, "2026-05-01-plan-a", backfillTestRaw, "1. Context — Why Now", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	writeBackfillPlanFixture(t, ledger, "2026-05-02-plan-b", backfillTestRaw, "1. Context — Why Now", time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC))
	initGitLedgerWithOrigin(t, ledger) // both dirs committed + pushed: local == remote == 2

	// Delete ONLY the working-tree copy — content stays in HEAD and on
	// origin/main, matching what a sparse-checkout eviction does (it touches
	// disk, never history).
	if err := os.RemoveAll(dirA); err != nil {
		t.Fatal(err)
	}

	out, err := runBackfillForTest(t, ledger, true) // dry-run
	if err == nil {
		t.Fatalf("expected the corpus-completeness guard to abort a dry run, got output:\n%s", out)
	}
	var cerr *plan.CorpusCompletenessError
	if !errors.As(err, &cerr) {
		t.Fatalf("error = %v (%T), want *plan.CorpusCompletenessError", err, err)
	}
	if cerr.LocalCount != 1 || cerr.RemoteCount != 2 {
		t.Errorf("CorpusCompletenessError = {local:%d remote:%d}, want {local:1 remote:2}", cerr.LocalCount, cerr.RemoteCount)
	}
	if strings.Contains(out, "Dry run — nothing written.") {
		t.Errorf("an aborted preflight must never reach the dry-run summary line:\n%s", out)
	}
}
