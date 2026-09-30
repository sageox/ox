package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/gitutil"
)

// sync_ledger_health.go keeps `ox sync` from printing "Ledger: synced" when the
// ledger did not sync. The daemon's ledger pull returns nil for every skip —
// backoff, a held lock, a pull already in flight — so a successful IPC round
// trip says nothing about the ledger itself. The observed failure: "Ledger:
// synced" on a ledger 19 ahead / 3561 behind, suspended in backoff, with a
// wedged rebase and a stale index.lock. After the round trip we look at the
// ledger and the daemon's own ledger-scoped issues and report what is true.

// ledgerSyncStatusNotSynced is the ledger status when the daemon accepted the
// sync request but the ledger is observably not in sync with origin.
const ledgerSyncStatusNotSynced = "not_synced"

// ledgerSyncFix is the one command that diagnoses every blocker listed below.
const ledgerSyncFix = "run `ox doctor`"

// blockingLedgerIssueTypes are daemon issues that mean the ledger pull did not
// land. The daemon clears each of them on a successful ledger pull
// (internal/daemon/sync.go), so one still present after a forced sync is
// current, not stale.
var blockingLedgerIssueTypes = map[string]bool{
	daemon.IssueTypeSyncBackoff:          true,
	daemon.IssueTypeGitLock:              true,
	daemon.IssueTypeDiverged:             true,
	daemon.IssueTypeMergeConflict:        true,
	daemon.IssueTypeRebaseStuck:          true,
	daemon.IssueTypeRepoIntegrity:        true,
	daemon.IssueTypeSessionConflictWedge: true,
}

// ledgerSyncFacts are the observations classifyLedgerSync decides from, split
// out so every blocker shape is table-testable without a daemon or a remote.
type ledgerSyncFacts struct {
	Issues           []daemon.DaemonIssue // all daemon issues; filtered to Repo=="ledger"
	RebaseInProgress bool
	StaleLocks       []string
	Ahead, Behind    int
	UpstreamKnown    bool
}

// classifyLedgerSync returns the reasons the ledger is not synced, or nil when
// nothing observable contradicts "synced".
//
// Ahead-only is deliberately not a blocker: sync is a pull, and unpushed local
// commits are pushed by the commit paths, not by this command. Behind is,
// because a pull that landed leaves nothing behind the tracking ref.
func classifyLedgerSync(f ledgerSyncFacts) []string {
	var reasons []string
	seen := map[string]bool{}
	for _, issue := range f.Issues {
		if issue.Repo != "ledger" || !blockingLedgerIssueTypes[issue.Type] {
			continue
		}
		summary := strings.TrimSpace(issue.Summary)
		if summary == "" {
			summary = issue.Type
		}
		if !seen[summary] {
			seen[summary] = true
			reasons = append(reasons, summary)
		}
	}
	if f.RebaseInProgress {
		reasons = append(reasons, "wedged rebase in progress")
	}
	if len(f.StaleLocks) > 0 {
		reasons = append(reasons, "stale git lock: "+strings.Join(f.StaleLocks, ", "))
	}
	if f.UpstreamKnown && f.Behind > 0 {
		reasons = append(reasons, fmt.Sprintf("ahead %d / behind %d", f.Ahead, f.Behind))
	}
	return reasons
}

// ledgerNotSyncedError renders the reasons into the one line the text output
// and the JSON error field share.
func ledgerNotSyncedError(reasons []string) string {
	return strings.Join(reasons, "; ") + "; " + ledgerSyncFix
}

// ledgerSyncLockAge is how old a ledger lock must be before sync reports it.
// The daemon may still be finishing its own git work when the IPC returns, so
// a fresh lock is expected; gitutil.StaleLockAge is the same bound the daemon
// uses before treating a lock as abandoned.
const ledgerSyncLockAge = gitutil.StaleLockAge

// staleLedgerLocks returns the ledger's git lock files older than
// ledgerSyncLockAge.
func staleLedgerLocks(ledgerPath string, now time.Time) []string {
	gitDir := filepath.Join(ledgerPath, ".git")
	var stale []string
	for _, lock := range gitutil.HasLockFiles(gitDir) {
		info, err := os.Stat(filepath.Join(gitDir, lock))
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) >= ledgerSyncLockAge {
			stale = append(stale, lock)
		}
	}
	return stale
}

// gatherLedgerSyncFacts reads the daemon's ledger issues and the ledger's
// on-disk state. A missing daemon status or ledger path yields empty facts:
// "could not look" is not evidence of a problem, and the IPC already
// succeeded.
func gatherLedgerSyncFacts(status *daemon.StatusData) ledgerSyncFacts {
	var f ledgerSyncFacts
	if status == nil {
		return f
	}
	f.Issues = status.Issues
	path := status.LedgerPath
	if path == "" {
		return f
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		return f
	}
	f.RebaseInProgress = gitutil.IsRebaseInProgress(path)
	f.StaleLocks = staleLedgerLocks(path, time.Now())
	f.Ahead, f.Behind, f.UpstreamKnown = gitAheadBehind(path)
	return f
}

// ledgerStatusForSync is indirected so tests never probe a real daemon.
var ledgerStatusForSync = func() *daemon.StatusData {
	// Same client the sync itself used, so the status is for this repo's
	// daemon, not whichever daemon TryConnect happens to reach.
	status, err := daemon.NewClientForCurrentRepoWithTimeout(5 * time.Second).Status()
	if err != nil {
		return nil
	}
	return status
}
