package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plan_commit_test.go covers commitPlanBackfillToLedger's failure routing:
// a failure at or before `git commit` must be distinguishable from a
// push-only failure (see errBackfillCommitFailed in plan_commit.go), since
// the caller (runPlanBackfillTitlesOnLedger) treats the two very
// differently — one is a hard command failure, the other a best-effort
// warning. Reuses initGitLedger from plan_backfill_test.go (same package).

// TestCommitPlanBackfillToLedger_PreCommitGitFailureIsErrBackfillCommitFailed
// forces `git mv` to fail (source path was never created) — a stand-in for
// any pre-commit git failure — and verifies the returned error satisfies
// errors.Is(err, errBackfillCommitFailed).
func TestCommitPlanBackfillToLedger_PreCommitGitFailureIsErrBackfillCommitFailed(t *testing.T) {
	ledger := t.TempDir()
	initGitLedger(t, ledger)

	bogusOld := filepath.Join(ledger, "data", "plans", "2026-01-01-does-not-exist")
	bogusNew := filepath.Join(ledger, "data", "plans", "2026-01-01-renamed")

	err := commitPlanBackfillToLedger(ledger, [][2]string{{bogusOld, bogusNew}}, nil, nil)
	if err == nil {
		t.Fatal("expected an error: git mv on a path that was never created")
	}
	if !errors.Is(err, errBackfillCommitFailed) {
		t.Errorf("error = %v, want errors.Is(err, errBackfillCommitFailed)", err)
	}

	// Nothing must have been committed — HEAD is still just the init commit.
	out := runGitInDir(t, ledger, "log", "--oneline")
	if strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Errorf("expected only the init commit after a pre-commit failure, got:\n%s", out)
	}
}

// TestCommitPlanBackfillToLedger_PushOnlyFailureIsNotErrBackfillCommitFailed
// is the negative case finding 3 explicitly requires: a real, valid commit
// (mv/add/commit all succeed) followed by a push failure (no remote
// configured) must return an error that does NOT match
// errBackfillCommitFailed, and the commit must have actually landed.
func TestCommitPlanBackfillToLedger_PushOnlyFailureIsNotErrBackfillCommitFailed(t *testing.T) {
	ledger := t.TempDir()
	initGitLedger(t, ledger)

	dir := filepath.Join(ledger, "data", "plans", "2026-01-01-real")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"topic":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	err := commitPlanBackfillToLedger(ledger, nil, []string{dir}, nil)
	if err == nil {
		t.Fatal("expected a push failure: no remote is configured for this ledger")
	}
	if errors.Is(err, errBackfillCommitFailed) {
		t.Errorf("push-only failure incorrectly matched errBackfillCommitFailed: %v", err)
	}

	out := runGitInDir(t, ledger, "log", "--oneline")
	if !strings.Contains(out, "plan: backfill 1 title") {
		t.Errorf("commit did not land locally despite this being a push-only failure:\n%s", out)
	}
	status := runGitInDir(t, ledger, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Errorf("working tree not clean after a successful commit (push-only failure): %q", status)
	}
}
