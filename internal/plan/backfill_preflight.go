package plan

// backfill_preflight.go holds the two GIT-FREE verdict functions
// `ox plan backfill-titles` checks before it scans or touches a single plan
// directory. Both guards close the same failure mode: a ledger whose
// working tree silently disagrees with origin/main (an incomplete
// sparse-checkout cone, or an unpushed/unpulled branch) would otherwise let
// ComputeBackfill run against a fraction of the real corpus and print a
// CLEAN summary — "nothing to backfill" looks identical whether nothing
// needed fixing or the run barely saw anything. The git plumbing that
// gathers the counts these functions consume (git ls-tree, git rev-list)
// lives in cmd/ox/plan_backfill_preflight.go, mirroring the compute/apply
// split ComputeBackfill/ApplyBackfillMeta already establish in this file.

import (
	"fmt"
	"os"
)

// CountPlanDirs counts plan directories actually present in the working
// tree at plansDir — Guard 1's local half. A plansDir that doesn't exist yet
// (a fresh ledger that has never saved a plan) is 0 dirs, not an error,
// mirroring ComputeBackfill's own os.IsNotExist handling.
func CountPlanDirs(plansDir string) (int, error) {
	entries, err := os.ReadDir(plansDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read plans dir=%s: %w", plansDir, err)
	}
	count := 0
	for _, e := range entries {
		if e.IsDir() {
			count++
		}
	}
	return count, nil
}

// CorpusCompletenessError means the working tree has fewer plan directories
// than origin/main does. The real incident this guards against: until the
// ledger's sparse-checkout cone included data/plans, git sparse-checkout set
// deleted plan directories from disk roughly 60s after every save (see
// baseSparseDirs' doc comment in internal/ledger/ledger.go), leaving ledgers
// with dozens of plans on origin/main and none on disk. A backfill run
// against that tree scans almost nothing, reworks nothing, and reports
// success — indistinguishable from "everything was already correct".
type CorpusCompletenessError struct {
	LocalCount  int
	RemoteCount int
}

func (e *CorpusCompletenessError) Error() string {
	return fmt.Sprintf(
		"plan backfill aborted: working tree has %d plan dir(s) under data/plans but origin/main has %d — the sparse-checkout cone is likely missing data/plans; run `ox doctor --fix`, then `git pull`, then retry",
		e.LocalCount, e.RemoteCount)
}

// VerifyCorpusComplete is Guard 1's pure verdict, computed from counts the
// cmd/ox layer gathers (local dir count, remote tree count via git ls-tree).
// local < remote is a hard abort (CorpusCompletenessError) — proceeding
// would silently rework a subset of the real corpus. local > remote is
// legitimate (plans saved locally that haven't been pushed yet) and is
// reported via note rather than blocked. local == remote is silent.
//
// remoteKnown=false means origin/main could not be resolved at all — no
// remote configured, a repo that's never been fetched, or (in tests) not a
// git repository yet. That must never hard-fail an otherwise-valid local
// run: a freshly cloned or intentionally offline ledger doing a purely
// local repair is a normal, supported case, not a reason to block backfill
// entirely just because there's nothing to compare against.
func VerifyCorpusComplete(localCount, remoteCount int, remoteKnown bool) (note string, err error) {
	if !remoteKnown {
		return "corpus completeness check skipped: origin/main unresolvable (no remote configured, or never fetched)", nil
	}
	switch {
	case localCount < remoteCount:
		return "", &CorpusCompletenessError{LocalCount: localCount, RemoteCount: remoteCount}
	case localCount > remoteCount:
		return fmt.Sprintf("working tree has %d plan dir(s) not yet on origin/main (%d there) — proceeding (unpushed local saves)", localCount, remoteCount), nil
	default:
		return "", nil
	}
}

// DivergenceError means the ledger has local commits origin/main doesn't
// have (ahead), commits from origin/main it hasn't pulled (behind), or
// both. Landing a mass directory rename on top of that divergence turns a
// routine repair into a manual rebase — worth stopping for, unless the
// operator explicitly says the divergence is expected (--allow-diverged).
type DivergenceError struct {
	Ahead  int
	Behind int
}

func (e *DivergenceError) Error() string {
	return fmt.Sprintf(
		"plan backfill aborted: ledger diverged from origin/main (ahead %d, behind %d) — reconcile first (`ox doctor --fix`, or pull/push), or pass --allow-diverged if this divergence is expected",
		e.Ahead, e.Behind)
}

// VerifyNotDiverged is Guard 2's pure verdict. allowDiverged is the
// --allow-diverged escape hatch for an operator who already knows their
// ledger is intentionally ahead/behind. remoteKnown mirrors
// VerifyCorpusComplete: an unresolvable origin/main skips the guard rather
// than failing a valid local-only run.
func VerifyNotDiverged(ahead, behind int, remoteKnown, allowDiverged bool) (note string, err error) {
	if !remoteKnown {
		return "divergence check skipped: origin/main unresolvable (no remote configured, or never fetched)", nil
	}
	if ahead == 0 && behind == 0 {
		return "", nil
	}
	if !allowDiverged {
		return "", &DivergenceError{Ahead: ahead, Behind: behind}
	}
	return fmt.Sprintf("ledger diverged from origin/main (ahead %d, behind %d) — proceeding: --allow-diverged was passed", ahead, behind), nil
}
