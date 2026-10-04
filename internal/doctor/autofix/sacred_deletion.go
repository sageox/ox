package autofix

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/sacred"
)

// sacredDeletionScanDepth bounds how far back the periodic detector looks. The
// commit-time guard (cmd/ox assertNoSacredMassDeletion) prevents NEW wipes; this
// catches one that still landed — via an older binary with no guard, a
// force-push, or any path that bypassed the guard — so it only needs to cover
// recent history: a wipe is loud the moment it lands, and lingering ones are
// caught on the first pass after upgrade.
const sacredDeletionScanDepth = 200

// commitMarker prefixes each commit's log line so the per-commit deletion groups
// can't be split by a blank line or an odd path. \x1e (ASCII record separator)
// is a legal argv byte (unlike NUL) and never appears in a ledger path.
const commitMarker = "\x1e"

// checkLedgerSacredDeletion is the daemon's periodic deep check for the
// data-loss class the 2026-08-25 Ox Dot wipe belongs to: a single commit that
// deleted every saved plan + session. It resolves the workspace's ledger and
// scans recent history for any commit that removed more than
// sacred.DetectorEntityThreshold whole plans/sessions.
//
// DETECTION ONLY — it never restores. Per ADR-024 sacred-data deletion needs
// explicit human approval, so a hit is surfaced as StatusFound for review; a
// commit message is not that approval. This is the belt to the commit-time
// guard's suspenders: it fires even when the guard never ran (old binary) or
// was bypassed (force-push).
//
// Counts entities REMOVED, not sacred files touched. Deleting artifacts from
// inside a session — a sweep of stale `.rej` files, say — loses no session and
// is not reported, which the previous file count could not express.
func checkLedgerSacredDeletion(ctx context.Context, repoPath string) CheckResult {
	if repoPath == "" {
		return CheckResult{Status: StatusClean}
	}
	pctx, err := config.LoadProjectContext(repoPath)
	if err != nil || pctx == nil {
		// uninitialized workspace or no ledger configured — nothing to scan
		return CheckResult{Status: StatusClean, Repo: repoPath}
	}
	ledgerPath := pctx.DefaultLedgerPath()
	if ledgerPath == "" {
		return CheckResult{Status: StatusClean, Repo: repoPath}
	}
	return scanLedgerSacredDeletions(ctx, ledgerPath, repoPath)
}

// scanLedgerSacredDeletions is the side-effect-free core, split out so tests can
// drive it against a real repo without standing up a ProjectContext.
func scanLedgerSacredDeletions(ctx context.Context, ledgerPath, repoPath string) CheckResult {
	// One bounded log walk: for each commit, its deleted paths under the sacred
	// prefixes, grouped by a marker-prefixed header line.
	args := append([]string{
		"log",
		fmt.Sprintf("-n%d", sacredDeletionScanDepth),
		"--no-merges",
		"--diff-filter=D",
		"--pretty=format:" + commitMarker + "%H",
		"--name-only",
		"--",
	}, sacred.Prefixes...)
	out, err := gitutil.RunGit(ctx, ledgerPath, args...)
	if err != nil {
		return CheckResult{
			Status:  StatusError,
			Repo:    repoPath,
			Summary: fmt.Sprintf("sacred-deletion scan: git log failed: %v", err),
		}
	}

	type wipe struct {
		commit string
		count  int
	}
	var hits []wipe
	var cur string
	var touched []string
	flush := func() {
		if cur == "" {
			return
		}
		// Entities merely TOUCHED by a deletion are only candidates: removing
		// `sessions/X/meta.json.rej` leaves session X intact. Confirm each
		// candidate is actually absent from this commit's tree before counting
		// it, so artifact sweeps no longer register as wipes.
		candidates := sacred.Entities(touched)
		if len(candidates) > sacred.DetectorEntityThreshold {
			if removed := removedEntities(ctx, ledgerPath, cur, candidates); len(removed) > sacred.DetectorEntityThreshold {
				// An entity path that vanished is not proof a plan or session was
				// LOST. A rename takes the old path away and puts an equivalent one
				// back, which is exactly what `ox plan backfill` does when it
				// retitles plans: on this repository's own ledger, commit abee78b3
				// ("plan: backfill 26 title(s)") reported 5 deletions while the plan
				// population went 27 -> 27. Nothing was lost, and the alert fired on
				// every scan for two months afterwards.
				//
				// Git's own rename detection cannot close this: the backfill rewrote
				// meta.json in the same commit that moved it, so the pair falls below
				// the similarity threshold and git reports an unpaired delete.
				//
				// The population count is the honest question — "are there fewer
				// plans and sessions than before?" — and it is the one a human means
				// by "a wipe." A reorganization keeps the count; a wipe reduces it.
				// See DetectorEntityThreshold's note on why a detector that cries
				// wolf is worse than no detector.
				if lost, ok := netEntitiesLost(ctx, ledgerPath, cur); ok && lost <= sacred.DetectorEntityThreshold {
					return
				}
				hits = append(hits, wipe{cur, len(removed)})
			}
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, commitMarker) {
			flush()
			cur = strings.TrimSpace(strings.TrimPrefix(line, commitMarker))
			touched = touched[:0]
			continue
		}
		if p := strings.TrimSpace(line); sacred.HasPrefix(p) {
			touched = append(touched, p)
		}
	}
	flush()

	if len(hits) == 0 {
		return CheckResult{Status: StatusClean, Repo: repoPath}
	}

	total := 0
	sample := make([]string, 0, 3)
	for _, h := range hits {
		total += h.count
		if len(sample) < 3 {
			sample = append(sample, fmt.Sprintf("%s(%d)", shortSHA(h.commit), h.count))
		}
	}
	// Keep every threshold hit visible; commit messages do not prove consent.
	slog.ErrorContext(ctx, "ALERT: plan/session deletions found in ledger history",
		"repo", repoPath,
		"ledger", ledgerPath,
		"wipe_commits", len(hits),
		"sacred_deletions_total", total,
		"sample", sample,
		"threshold", sacred.DetectorEntityThreshold)
	return CheckResult{
		Status: StatusFound,
		Repo:   repoPath,
		Summary: fmt.Sprintf("plan/session deletion history: %d commit(s) removing %d whole plans/sessions (e.g. %s) — verify intent before recovery; do NOT auto-delete (ADR-024)",
			len(hits), total, strings.Join(sample, ", ")),
	}
}

// removedEntities returns the subset of candidates that no longer exist in
// treeish. One `git ls-tree` lists the survivors in a single call; anything
// not listed is gone.
//
// Only called for commits whose candidate count already exceeds the threshold,
// so the common commit costs no extra git invocation. On error it returns
// candidates unchanged — a scan that cannot confirm survival must not silence
// a potential wipe (fail-loud, matching the check's detection-only contract).
func removedEntities(ctx context.Context, ledgerPath, treeish string, candidates []string) []string {
	args := append([]string{"ls-tree", "-d", "--name-only", treeish, "--"}, candidates...)
	out, err := gitutil.RunGit(ctx, ledgerPath, args...)
	if err != nil {
		return candidates
	}
	survived := make(map[string]bool, len(candidates))
	for _, line := range strings.Split(out, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			survived[p] = true
		}
	}
	removed := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if !survived[c] {
			removed = append(removed, c)
		}
	}
	return removed
}

// netEntitiesLost reports how many whole plans/sessions the commit removed on
// net — the population before it minus the population after.
//
// ok is false when either tree cannot be read (a root that does not exist yet
// is normal in a young ledger). A caller that cannot get an answer must NOT
// treat that as "nothing was lost": the second return exists so an unreadable
// tree falls through to reporting rather than silently suppressing an alert.
func netEntitiesLost(ctx context.Context, ledgerPath, commit string) (int, bool) {
	before, okBefore := entityPopulation(ctx, ledgerPath, commit+"^")
	after, okAfter := entityPopulation(ctx, ledgerPath, commit)
	if !okBefore || !okAfter {
		return 0, false
	}
	return before - after, true
}

// entityPopulation counts the whole plans and sessions present in one tree.
func entityPopulation(ctx context.Context, ledgerPath, treeish string) (int, bool) {
	total := 0
	for _, prefix := range sacred.Prefixes {
		root := strings.TrimSuffix(prefix, "/")
		// `<treeish>:<root>` descends INTO the root. Passing the root as a
		// pathspec instead (`ls-tree -d <treeish> -- data/plans`) returns the
		// directory entry itself — one line, before and after — so every wipe
		// measured as zero net loss and was silently suppressed. The
		// real-deletion test caught that; without it this "fix" would have
		// turned a noisy detector into a blind one.
		out, err := gitutil.RunGit(ctx, ledgerPath, "ls-tree", "-d", "--name-only", treeish+":"+root)
		if err != nil {
			// A missing root is an empty population, not a failure: `sessions/`
			// does not exist until the first session lands. Distinguishing that
			// from a broken tree is why this returns a bool rather than -1.
			if strings.Contains(err.Error(), "Not a valid object name") ||
				strings.Contains(err.Error(), "does not exist") ||
				strings.Contains(err.Error(), "exists on disk, but not in") {
				continue
			}
			return 0, false
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.TrimSpace(line) != "" {
				total++
			}
		}
	}
	return total, true
}

// shortSHA abbreviates a commit id for bounded log/summary output.
func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
