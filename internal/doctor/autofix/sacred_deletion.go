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
// scans recent history for any commit deleting more than sacred.MassDeleteThreshold
// files under a sacred prefix.
//
// DETECTION ONLY — it never restores. Per ADR-024 sacred-data deletion needs
// explicit human approval, and the content is always recoverable from history,
// so a hit is surfaced as StatusFound and alerted for a human to recover. This
// is the belt to the commit-time guard's suspenders: it fires even when the
// guard never ran (old binary) or was bypassed (force-push).
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
	var cnt int
	flush := func() {
		if cur != "" && cnt > sacred.MassDeleteThreshold {
			hits = append(hits, wipe{cur, cnt})
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, commitMarker) {
			flush()
			cur = strings.TrimSpace(strings.TrimPrefix(line, commitMarker))
			cnt = 0
			continue
		}
		if sacred.HasPrefix(strings.TrimSpace(line)) {
			cnt++
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
	// Loud alert: a data-loss event is sitting in history right now.
	slog.ErrorContext(ctx, "ALERT: sacred mass-deletion found in ledger history",
		"repo", repoPath,
		"ledger", ledgerPath,
		"wipe_commits", len(hits),
		"sacred_deletions_total", total,
		"sample", sample,
		"threshold", sacred.MassDeleteThreshold)
	return CheckResult{
		Status: StatusFound,
		Repo:   repoPath,
		Summary: fmt.Sprintf("sacred mass-deletion in ledger history: %d commit(s) deleting %d plan/session files (e.g. %s) — recover from history; do NOT auto-delete (ADR-024)",
			len(hits), total, strings.Join(sample, ", ")),
	}
}

// shortSHA abbreviates a commit id for bounded log/summary output.
func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
