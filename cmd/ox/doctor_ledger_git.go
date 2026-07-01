package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"log/slog"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/ledger"
)

// Ledger Git Health check slug constants
const (
	CheckSlugLedgerRemoteReachable = "ledger-remote-reachable"
	CheckSlugLedgerBranchStatus    = "ledger-branch-status"
	CheckSlugLedgerCleanWorkdir    = "ledger-clean-workdir"
	CheckSlugLedgerURLAPIMatch     = "ledger-url-api-match"
	CheckSlugLedgerCacheTracked    = "ledger-cache-tracked"
	// CheckSlugLedgerUnmergedPaths detects an in-progress merge/rebase/cherry-pick
	// that has left files in U-state. These wedges silently block every future
	// commit on the ledger (push-summary, doctor auto-commit, session uploads)
	// until cleared. See bd ox-8zd3 for the original incident.
	CheckSlugLedgerUnmergedPaths = "ledger-unmerged-paths"
	// CheckSlugLedgerStuckOperation detects an in-progress merge/rebase/cherry-pick
	// that has NO unresolved conflicts — the blind spot the unmerged-paths check
	// misses. Its worst case is a structurally-incomplete "zombie" rebase-merge
	// dir (only an autostash entry, no head-name/orig-head) left by a process
	// killed mid-rebase: `git rebase --abort` cannot clear it, so every ledger
	// pull fails with "already a rebase-merge directory" until it is quit. See
	// bd ox-j3cl for the original incident.
	CheckSlugLedgerStuckOperation = "ledger-stuck-operation"
)

func init() {
	// ============================================================
	// Ledger Git Health checks
	// ============================================================

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugLedgerRemoteReachable,
		Name:        "Ledger remote connectivity",
		Category:    "Ledger Git Health",
		FixLevel:    FixLevelCheckOnly,
		Description: "Verifies ledger remote is reachable",
		Run:         func(fix bool) checkResult { return checkLedgerRemoteReachable() },
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugLedgerBranchStatus,
		Name:        "Ledger branch status",
		Category:    "Ledger Git Health",
		FixLevel:    FixLevelAuto,
		Description: "Checks if local ledger branch is up-to-date with remote",
		Run:         func(fix bool) checkResult { return checkLedgerBranchStatus(fix) },
	})

	// Register the unmerged-paths check ahead of the clean-workdir check so
	// the wedge — which silently blocks every future commit — surfaces with
	// a higher-priority status before the dirty-workdir check tries (and
	// fails) to auto-commit on top of it. The actual ordering in the
	// doctor run is established in checkLedgerGitHealth (doctor.go); this
	// note documents the intent for anyone touching either site.
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugLedgerUnmergedPaths,
		Name:        "Ledger unmerged paths",
		Category:    "Ledger Git Health",
		FixLevel:    FixLevelAuto,
		Description: "Detects ledger files left in U-state by a stuck merge/rebase/cherry-pick",
		Run:         func(fix bool) checkResult { return checkLedgerUnmergedPaths(fix) },
	})

	// Register the stuck-operation check alongside unmerged-paths: together they
	// cover both wedge shapes — WITH conflicts (unmerged-paths) and WITHOUT
	// conflicts (stuck-operation, incl. the zombie rebase dir that git rebase
	// --abort cannot clear). Ordering in the doctor run is set in
	// checkLedgerGitHealth (doctor.go).
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugLedgerStuckOperation,
		Name:        "Ledger stuck operation",
		Category:    "Ledger Git Health",
		FixLevel:    FixLevelAuto,
		Description: "Detects a stuck merge/rebase/cherry-pick (incl. a corrupt rebase dir) blocking ledger sync",
		Run:         func(fix bool) checkResult { return checkLedgerStuckOperation(fix) },
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugLedgerCleanWorkdir,
		Name:        "Ledger clean workdir",
		Category:    "Ledger Git Health",
		FixLevel:    FixLevelAuto,
		Description: "Checks for uncommitted changes in ledger repository",
		Run:         func(fix bool) checkResult { return checkLedgerCleanWorkdir(fix) },
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugLedgerCacheTracked,
		Name:        "Ledger cache files untracked",
		Category:    "Ledger Git Health",
		FixLevel:    FixLevelAuto,
		Description: "Detects local-only cache files that were accidentally committed to the ledger",
		Run:         func(fix bool) checkResult { return checkLedgerCacheTracked(fix) },
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugLedgerURLAPIMatch,
		Name:        "Ledger remote URL vs API",
		Category:    "Ledger Git Health",
		FixLevel:    FixLevelConfirm,
		Description: "Verifies local ledger remote URL matches the API-authoritative URL",
		Run:         checkLedgerURLAPIMatch,
	})
}

// getLedgerPath returns the ledger path from local config or default.
// Returns empty string if no ledger is configured or found.
func getLedgerPath() string {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return ""
	}

	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err == nil && localCfg != nil && localCfg.Ledger != nil && localCfg.Ledger.Path != "" {
		return localCfg.Ledger.Path
	}

	// try default path
	defaultPath, err := ledger.DefaultPath()
	if err != nil {
		return ""
	}

	if ledger.Exists(defaultPath) {
		return defaultPath
	}

	return ""
}

// checkLedgerRemoteReachable verifies the ledger remote is reachable.
// Uses git ls-remote with a timeout to check connectivity.
func checkLedgerRemoteReachable() checkResult {
	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck("Ledger remote connectivity", "no ledger found", "")
	}

	if !isGitRepo(ledgerPath) {
		return SkippedCheck("Ledger remote connectivity", "ledger not a git repo", "")
	}

	// check if remote is configured
	remoteCmd := exec.Command("git", "-C", ledgerPath, "remote", "get-url", "origin")
	output, err := remoteCmd.Output()
	if err != nil {
		return SkippedCheck("Ledger remote connectivity", "no origin remote", "")
	}

	remoteURL := strings.TrimSpace(string(output))
	if remoteURL == "" {
		return SkippedCheck("Ledger remote connectivity", "empty remote URL", "")
	}

	// test connectivity with git ls-remote (with timeout)
	lsCmd := exec.Command("git", "-C", ledgerPath, "ls-remote", "--exit-code", "-q", "origin", "HEAD")

	// capture stderr for error classification
	var stderrBuf strings.Builder
	lsCmd.Stderr = &stderrBuf

	done := make(chan error, 1)
	go func() {
		done <- lsCmd.Run()
	}()

	select {
	case err := <-done:
		if err != nil {
			stderrOutput := stderrBuf.String()
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && exitErr.ExitCode() == 128 {
				// distinguish 403 (access denied) from 401 (auth failed)
				stderrLower := strings.ToLower(stderrOutput)
				if strings.Contains(stderrLower, "403") || strings.Contains(stderrLower, "forbidden") {
					return WarningCheck("Ledger remote connectivity", "access denied",
						"You are not a member of this team. Request an invite URL from a team admin.")
				}
				return WarningCheck("Ledger remote connectivity", "auth failed",
					"Check git credentials or SSH keys for ledger remote")
			}
			return WarningCheck("Ledger remote connectivity", "unreachable",
				"Check network connection or verify remote URL is correct")
		}
		return PassedCheck("Ledger remote connectivity", "reachable")
	case <-time.After(10 * time.Second):
		_ = lsCmd.Process.Kill()
		return WarningCheck("Ledger remote connectivity", "timeout",
			"Remote did not respond within 10s - check network or firewall")
	}
}

// checkLedgerBranchStatus checks if local ledger branch is up-to-date with remote.
// Reports if the branch is ahead, behind, or diverged from remote.
// With fix=true, auto-syncs: pushes when ahead, pulls when behind, rebase+push when diverged.
// The ledger is fully ox-managed, so auto-sync is safe.
func checkLedgerBranchStatus(fix bool) checkResult {
	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck("Ledger branch status", "no ledger found", "")
	}

	if !isGitRepo(ledgerPath) {
		return SkippedCheck("Ledger branch status", "ledger not a git repo", "")
	}

	// check if remote is configured
	remoteCmd := exec.Command("git", "-C", ledgerPath, "remote")
	output, err := remoteCmd.Output()
	if err != nil || strings.TrimSpace(string(output)) == "" {
		return SkippedCheck("Ledger branch status", "no remote configured", "")
	}

	// get current branch
	branchCmd := exec.Command("git", "-C", ledgerPath, "rev-parse", "--abbrev-ref", "HEAD")
	branchOutput, err := branchCmd.Output()
	if err != nil {
		return SkippedCheck("Ledger branch status", "failed to get branch", "")
	}
	branch := strings.TrimSpace(string(branchOutput))
	if branch == "HEAD" {
		return WarningCheck("Ledger branch status", "detached HEAD",
			"Ledger is in detached HEAD state - checkout a branch")
	}

	// check if tracking branch exists
	trackingCmd := exec.Command("git", "-C", ledgerPath, "rev-parse", "--abbrev-ref", branch+"@{upstream}")
	if _, err := trackingCmd.Output(); err != nil {
		return InfoCheck("Ledger branch status", "no tracking branch",
			"Run `git -C "+ledgerPath+" push -u origin "+branch+"` to set up tracking")
	}

	// NOTE: We intentionally do NOT fetch here. Read-side git operations (fetch/pull)
	// are handled by the daemon. We use cached data from the last daemon sync,
	// which should be recent enough for diagnostics. CLI handles writes (add/commit/push)
	// during session upload, but that's not relevant for this diagnostic check.

	// count commits ahead/behind (using cached remote tracking data)
	aheadCmd := exec.Command("git", "-C", ledgerPath, "rev-list", "--count", branch+"@{upstream}..HEAD")
	aheadOutput, _ := aheadCmd.Output()
	ahead := strings.TrimSpace(string(aheadOutput))

	behindCmd := exec.Command("git", "-C", ledgerPath, "rev-list", "--count", "HEAD.."+branch+"@{upstream}")
	behindOutput, _ := behindCmd.Output()
	behind := strings.TrimSpace(string(behindOutput))

	aheadCount := 0
	behindCount := 0
	fmt.Sscanf(ahead, "%d", &aheadCount)
	fmt.Sscanf(behind, "%d", &behindCount)

	if aheadCount > 0 && behindCount > 0 {
		if fix {
			return fixLedgerBranchDiverged(ledgerPath, aheadCount, behindCount)
		}
		return WarningCheck("Ledger branch status",
			fmt.Sprintf("diverged: %d ahead, %d behind", aheadCount, behindCount),
			"Run `ox doctor --fix` to reconcile ledger changes")
	}

	if aheadCount > 0 {
		if fix {
			return fixLedgerBranchAhead(ledgerPath, aheadCount)
		}
		return WarningCheck("Ledger branch status",
			fmt.Sprintf("%d commit(s) ahead", aheadCount),
			"Run `ox doctor --fix` to push ledger changes to remote")
	}

	if behindCount > 0 {
		if fix {
			return fixLedgerBranchBehind(ledgerPath, behindCount)
		}
		return WarningCheck("Ledger branch status",
			fmt.Sprintf("%d commit(s) behind", behindCount),
			"Run `ox doctor --fix` to pull latest ledger changes")
	}

	return PassedCheck("Ledger branch status", "up to date")
}

// fixLedgerBranchAhead pushes local ledger commits to remote.
func fixLedgerBranchAhead(ledgerPath string, aheadCount int) checkResult {
	// pushLedger refreshes credentials before pushing (same as session upload path)
	if err := pushLedger(context.Background(), ledgerPath); err != nil {
		return FailedCheck("Ledger branch status",
			"push failed",
			fmt.Sprintf("push error: %s", err))
	}
	return PassedCheck("Ledger branch status",
		fmt.Sprintf("pushed %d commit(s)", aheadCount))
}

// fixLedgerBranchBehind pulls remote changes into local ledger.
func fixLedgerBranchBehind(ledgerPath string, behindCount int) checkResult {
	// --autostash: uncommitted local changes must not block the pull
	pullCmd := exec.Command("git", "-C", ledgerPath, "pull", "--rebase", "--autostash")
	output, err := pullCmd.CombinedOutput()
	if err != nil {
		errStr := strings.TrimSpace(string(output))
		// try accept-theirs for data/github/ conflicts
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		resolveErr := gitutil.ResolveRebaseAcceptTheirs(ctx, ledgerPath, ledgerAutoResolvePrefixes)
		cancel()
		if resolveErr != nil {
			slog.Debug("rebase auto-resolve failed", "error", resolveErr)
			// AuditAndAbort: log HEAD SHA, unmerged files, and stash count
			// before discarding rebase state so silent recovery is not
			// invisible. See ox-ooy3 and .claude/rules/daemon-git.md.
			abortCtx, abortCancel := context.WithTimeout(context.Background(), 15*time.Second)
			_ = gitutil.AuditAndAbort(abortCtx, ledgerPath, gitutil.AuditOpRebase, "doctor --fix auto-resolve failed", slog.Default())
			abortCancel()
			return FailedCheck("Ledger branch status",
				"pull --rebase failed (aborted)",
				fmt.Sprintf("Conflict during rebase (aborted to restore clean state): %s", errStr))
		}
		return PassedCheck("Ledger branch status",
			fmt.Sprintf("pulled %d commit(s) (auto-resolved conflicts)", behindCount))
	}
	return PassedCheck("Ledger branch status",
		fmt.Sprintf("pulled %d commit(s)", behindCount))
}

// fixLedgerBranchDiverged reconciles a diverged ledger by rebasing then pushing.
// The daemon proactively rebases diverged branches, so by the time a user runs
// ox doctor --fix the divergence may already be resolved. Re-check before
// attempting the heavier PushWithRetry path.
func fixLedgerBranchDiverged(ledgerPath string, aheadCount, behindCount int) checkResult {
	// re-fetch so we compare against the latest remote state
	fetchCmd := exec.Command("git", "-C", ledgerPath, "fetch", "--quiet")
	if err := fetchCmd.Run(); err != nil {
		slog.Debug("fetch before diverged re-check failed", "error", err)
	}

	// re-check ahead/behind — daemon may have already reconciled
	revCmd := exec.Command("git", "-C", ledgerPath, "rev-list", "--left-right", "--count", "origin/main...HEAD")
	if revOut, err := revCmd.Output(); err == nil {
		parts := strings.Fields(strings.TrimSpace(string(revOut)))
		if len(parts) == 2 {
			behind, ahead := 0, 0
			fmt.Sscanf(parts[0], "%d", &behind)
			fmt.Sscanf(parts[1], "%d", &ahead)

			switch {
			case ahead == 0 && behind == 0:
				return PassedCheck("Ledger branch status", "already reconciled (synced by daemon)")
			case ahead > 0 && behind == 0:
				return fixLedgerBranchAhead(ledgerPath, ahead)
			case ahead == 0 && behind > 0:
				return fixLedgerBranchBehind(ledgerPath, behind)
			}
			// still diverged — update counts and fall through
			aheadCount = ahead
			behindCount = behind
		}
	}

	// pushLedger handles pull --rebase + auto-resolve + push with credential refresh
	if err := pushLedger(context.Background(), ledgerPath); err != nil {
		return FailedCheck("Ledger branch status",
			"reconcile failed",
			fmt.Sprintf("push error: %s", err))
	}
	return PassedCheck("Ledger branch status",
		fmt.Sprintf("reconciled: rebased %d + pushed %d commit(s)", behindCount, aheadCount))
}

// checkLedgerUnmergedPaths detects ledger files left in U-state by a stuck
// merge / rebase / cherry-pick. These wedges silently block every future
// commit on the ledger — including push-summary and ox doctor's own
// auto-commit path — until the operation is aborted or the conflicts are
// resolved.
//
// Failure prevented: a 6-day-old wedge in sessions/<name>/ that blocked
// every coworker's push-summary while ox doctor reported "Ledger clean
// workdir: 3 modified" (the wedge was silently lumped into the modified
// counter). See bd ox-8zd3 for the original incident.
func checkLedgerUnmergedPaths(fix bool) checkResult {
	const name = "Ledger unmerged paths"

	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck(name, "no ledger found", "")
	}

	if !isGitRepo(ledgerPath) {
		return SkippedCheck(name, "ledger not a git repo", "")
	}

	statusCmd := exec.Command("git", "-C", ledgerPath, "status", "--porcelain=v1")
	output, err := statusCmd.Output()
	if err != nil {
		return SkippedCheck(name, "status check failed", "")
	}

	unmerged := parseUnmergedPaths(string(output))
	if len(unmerged) == 0 {
		return PassedCheck(name, "no conflicts")
	}

	if !fix {
		return unmergedPathsFailure(name, ledgerPath, unmerged)
	}

	return fixLedgerUnmergedPaths(ledgerPath, unmerged)
}

// unmergedPathsFailure constructs the P0 failure surfaced when unmerged
// paths are present. Extracted so the message shape is unit-testable
// without standing up a real git repo.
func unmergedPathsFailure(name, ledgerPath string, unmerged []unmergedPath) checkResult {
	sample := make([]string, 0, 3)
	for i, u := range unmerged {
		if i >= 3 {
			break
		}
		sample = append(sample, fmt.Sprintf("%s %s", u.Code, u.Path))
	}
	more := ""
	if len(unmerged) > len(sample) {
		more = fmt.Sprintf(" (+%d more)", len(unmerged)-len(sample))
	}
	detail := fmt.Sprintf(
		"%d file(s) in U-state at %s%s:\n       %s\n       Run `ox doctor --fix` to abort the stuck operation and clear the wedge.",
		len(unmerged), ledgerPath, more, strings.Join(sample, "\n       "),
	)
	// CriticalCheck — this silently blocks every future commit (push-summary,
	// session uploads, doctor auto-commit). It must be loud.
	r := CriticalCheck(name, fmt.Sprintf("%d unresolved conflict(s)", len(unmerged)), detail)
	r.slug = CheckSlugLedgerUnmergedPaths
	r.fixLevel = FixLevelAuto
	return r
}

// unmergedPath represents a single `git status --porcelain` entry whose
// XY pair indicates an unmerged path (per git-status(1)).
type unmergedPath struct {
	Code string // e.g. "UU", "AA", "DD"
	Path string
}

// isUnmergedCode reports whether the XY pair from `git status --porcelain`
// indicates an unmerged path. Per git-status(1):
//
//	DD    unmerged, both deleted
//	AU    unmerged, added by us
//	UD    unmerged, deleted by them
//	UA    unmerged, added by them
//	DU    unmerged, deleted by us
//	AA    unmerged, both added
//	UU    unmerged, both modified
//
// Any other XY combination (e.g. " M", "M ", "??", "A ") is NOT unmerged.
func isUnmergedCode(x, y byte) bool {
	switch string([]byte{x, y}) {
	case "DD", "AU", "UD", "UA", "DU", "AA", "UU":
		return true
	}
	return false
}

// parseUnmergedPaths scans `git status --porcelain=v1` output and returns
// the subset of entries that are in U-state. Lines that don't fit the
// porcelain shape are silently skipped — they would also be invisible to
// `git commit` so callers can't act on them anyway.
func parseUnmergedPaths(porcelain string) []unmergedPath {
	if porcelain == "" {
		return nil
	}
	var out []unmergedPath
	for _, raw := range strings.Split(porcelain, "\n") {
		if len(raw) < 4 {
			// "XY <space> <path>" — minimum 4 bytes
			continue
		}
		x, y := raw[0], raw[1]
		if !isUnmergedCode(x, y) {
			continue
		}
		// raw[2] is always a space; the path begins at raw[3].
		// Rename entries (`R  old -> new`) don't appear for unmerged
		// codes per the porcelain spec, so we don't need to split on " -> ".
		path := raw[3:]
		out = append(out, unmergedPath{Code: string([]byte{x, y}), Path: path})
	}
	return out
}

// fixLedgerUnmergedPaths attempts to clear an unmerged-path wedge by aborting
// the in-progress operation that created it. Aborts are intentionally chosen
// over `git reset` / `checkout --theirs` because they're reversible — they
// only undo the operation in flight, never user-authored commits.
//
// If no in-progress operation can be identified (rare — usually means the
// conflict was staged manually via `git update-index --cacheinfo`), the
// situation is surfaced for human attention rather than guessed at.
func fixLedgerUnmergedPaths(ledgerPath string, unmerged []unmergedPath) checkResult {
	const name = "Ledger unmerged paths"

	op, hint := detectInProgressGitOp(ledgerPath)
	if op == "" {
		// No state markers — likely a manually-staged conflict, OR
		// detectInProgressGitOp could not inspect .git (permission/IO).
		// DO NOT auto-resolve; the right action depends on user intent.
		sample := unmerged[0].Path
		if len(unmerged) > 1 {
			sample = fmt.Sprintf("%s (+%d more)", sample, len(unmerged)-1)
		}
		// Surface the inspection error when present so a permission/IO failure
		// in os.Stat(.git) is visible instead of silently downgraded to
		// "manually-staged conflict."
		prefix := ""
		if hint != "" {
			prefix = fmt.Sprintf("could not inspect .git for in-progress operation (%s).\n       ", hint)
		}
		detail := prefix + fmt.Sprintf(
			"%d unmerged file(s) but no merge/rebase/cherry-pick in progress (%s).\n       "+
				"This usually means the conflict was staged manually. Resolve by hand:\n       "+
				"  cd %s\n       "+
				"  git status                       # inspect the conflict\n       "+
				"  git checkout --ours <file>       # or --theirs, depending on intent\n       "+
				"  git reset HEAD <file>            # if you want to discard the staged conflict",
			len(unmerged), sample, ledgerPath,
		)
		r := FailedCheck(name, "manual resolution required", detail)
		r.slug = CheckSlugLedgerUnmergedPaths
		return r
	}

	// AuditAndAbort: structured pre/post audit so silent recovery from a
	// wedged ledger leaves a trail. See ox-ooy3 and .claude/rules/daemon-git.md.
	abortCtx, abortCancel := context.WithTimeout(context.Background(), 15*time.Second)
	abortErr := gitutil.AuditAndAbort(abortCtx, ledgerPath, gitutil.AuditableOp(op), "doctor --fix unmerged-paths wedge", slog.Default())
	abortCancel()
	if abortErr != nil {
		return FailedCheck(name,
			fmt.Sprintf("git %s --abort failed", op),
			fmt.Sprintf("error: %s\n       %s", abortErr, hint))
	}

	slog.Info("ledger unmerged-paths wedge cleared",
		"op", op, "ledger", ledgerPath, "files", len(unmerged),
	)
	return PassedCheck(name,
		fmt.Sprintf("aborted stuck %s (%d file(s) cleared)", op, len(unmerged)))
}

// detectInProgressGitOp inspects the ledger's .git directory for markers
// of an in-progress merge / rebase / cherry-pick and returns the
// corresponding `git <op> --abort` operation name. Returns "" if no
// marker is present.
//
// The second return value is a human-readable hint used when the abort
// itself fails, to help a human follow up by hand.
func detectInProgressGitOp(ledgerPath string) (op, hint string) {
	gitDir := filepath.Join(ledgerPath, ".git")
	// .git may be a file (gitlink) in worktree-style layouts; we don't
	// support that for ledgers today, so a missing dir means "no op."
	// Distinguish "doesn't exist" (clean no-op) from permission/IO failures
	// (caller surfaces hint so a human can investigate) so we don't silently
	// downgrade an inspection error to "nothing to do."
	if _, err := os.Stat(gitDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ""
		}
		return "", fmt.Sprintf("stat .git: %v", err)
	}

	// rebase-merge / rebase-apply are state directories. They exist for
	// the duration of an interactive or non-interactive rebase respectively.
	if _, err := os.Stat(filepath.Join(gitDir, "rebase-merge")); err == nil {
		return "rebase", "rebase state dir present: .git/rebase-merge/"
	}
	if _, err := os.Stat(filepath.Join(gitDir, "rebase-apply")); err == nil {
		return "rebase", "rebase state dir present: .git/rebase-apply/"
	}

	// MERGE_HEAD / CHERRY_PICK_HEAD / REVERT_HEAD are single-file markers
	// written by git when those operations begin and removed when they
	// complete (or are aborted).
	if _, err := os.Stat(filepath.Join(gitDir, "CHERRY_PICK_HEAD")); err == nil {
		return "cherry-pick", "CHERRY_PICK_HEAD present"
	}
	if _, err := os.Stat(filepath.Join(gitDir, "MERGE_HEAD")); err == nil {
		return "merge", "MERGE_HEAD present"
	}
	// REVERT_HEAD doesn't typically produce U-state conflicts ox cares
	// about, but we include it for completeness — `git revert --abort`
	// is the documented escape hatch.
	if _, err := os.Stat(filepath.Join(gitDir, "REVERT_HEAD")); err == nil {
		return "revert", "REVERT_HEAD present"
	}
	return "", ""
}

// checkLedgerStuckOperation detects an in-progress merge / rebase / cherry-pick
// that has left NO unresolved conflicts — the blind spot checkLedgerUnmergedPaths
// (gated on U-state files) misses. The worst case is a structurally-incomplete
// "zombie" rebase-merge directory (only an autostash entry, no head-name /
// orig-head) left by a process killed mid-rebase: git thinks a rebase is in
// progress, `git rebase --abort` cannot clear it (nothing to reset to), and
// every ledger pull fails with "already a rebase-merge directory" — the exact
// production wedge in bd ox-j3cl.
//
// With fix=true, a rebase is cleared via gitutil.AbortOrClearRebase (abort, then
// `--quit` for a zombie dir); a merge/cherry-pick/revert via the audited abort.
// A fresh rebase (younger than StaleRebaseThreshold) is left alone — it's almost
// always a live daemon pull --rebase or a human mid-operation.
func checkLedgerStuckOperation(fix bool) checkResult {
	const name = "Ledger stuck operation"

	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck(name, "no ledger found", "")
	}
	if !isGitRepo(ledgerPath) {
		return SkippedCheck(name, "ledger not a git repo", "")
	}

	op, hint := detectInProgressGitOp(ledgerPath)
	if op == "" {
		return PassedCheck(name, "no operation in progress")
	}

	// A fresh rebase is likely in flight (daemon pull --rebase or a human) —
	// leave it alone. Only a stale one is a wedge. Non-rebase markers
	// (MERGE_HEAD, CHERRY_PICK_HEAD) never appear during normal ox operation on
	// the ledger, so treat their presence as a wedge regardless of age.
	if op == "rebase" {
		if age, ok := gitutil.RebaseAge(ledgerPath); ok && age < gitutil.StaleRebaseThreshold {
			return PassedCheck(name, fmt.Sprintf("rebase in progress (%s, fresh)", age.Round(time.Second)))
		}
	}

	if !fix {
		return stuckOperationFailure(name, ledgerPath, op, hint)
	}
	return fixLedgerStuckOperation(ledgerPath, op, hint)
}

// stuckOperationFailure constructs the P0 surfaced when a stuck operation with
// no unresolved conflicts is blocking ledger sync. Extracted so the message
// shape is unit-testable without standing up a real git repo (mirrors
// unmergedPathsFailure).
func stuckOperationFailure(name, ledgerPath, op, hint string) checkResult {
	detail := fmt.Sprintf(
		"a %s is in progress at %s with no unresolved conflicts (%s).\n       "+
			"This wedge blocks every ledger pull. Run `ox doctor --fix` to clear it.",
		op, ledgerPath, hint,
	)
	// CriticalCheck — a stuck operation silently blocks all ledger sync (pull)
	// and every future commit; it must be loud.
	r := CriticalCheck(name, fmt.Sprintf("stuck %s blocking ledger sync", op), detail)
	r.slug = CheckSlugLedgerStuckOperation
	r.fixLevel = FixLevelAuto
	return r
}

// fixLedgerStuckOperation clears a stuck operation. A rebase goes through
// gitutil.AbortOrClearRebase (reversible `git rebase --abort`, escalating to
// `git rebase --quit` for a structurally-incomplete zombie dir that abort
// cannot clear). A merge/cherry-pick/revert clears via the audited abort — its
// state is always intact enough to abort. Extracted for direct unit testing.
func fixLedgerStuckOperation(ledgerPath, op, hint string) checkResult {
	const name = "Ledger stuck operation"

	var clearErr error
	if op == "rebase" {
		clearErr = gitutil.AbortOrClearRebase(context.Background(), ledgerPath, "doctor --fix stuck rebase", slog.Default())
	} else {
		abortCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		clearErr = gitutil.AuditAndAbort(abortCtx, ledgerPath, gitutil.AuditableOp(op), "doctor --fix stuck "+op, slog.Default())
		cancel()
	}
	if clearErr != nil {
		r := FailedCheck(name,
			fmt.Sprintf("could not clear stuck %s", op),
			fmt.Sprintf("error: %s\n       %s", clearErr, hint))
		r.slug = CheckSlugLedgerStuckOperation
		return r
	}

	slog.Info("ledger stuck-operation wedge cleared", "op", op, "ledger", ledgerPath)
	return PassedCheck(name, fmt.Sprintf("cleared stuck %s", op))
}

// checkLedgerCleanWorkdir checks for uncommitted changes in the ledger repository.
// Reports if there are staged, unstaged, or untracked files.
// With fix=true, auto-commits all changes. The ledger is fully ox-managed.
//
// Unmerged paths (U-state from a stuck merge/rebase/cherry-pick) are
// intentionally skipped here — they're surfaced separately by
// checkLedgerUnmergedPaths, which runs first. Folding them into the
// "modified" counter (as this function used to) let a 6-day-old wedge
// hide behind a benign "3 modified" line.
func checkLedgerCleanWorkdir(fix bool) checkResult {
	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck("Ledger clean workdir", "no ledger found", "")
	}

	if !isGitRepo(ledgerPath) {
		return SkippedCheck("Ledger clean workdir", "ledger not a git repo", "")
	}

	// skip if a rebase is in progress — committing during a broken rebase
	// sweeps conflict debris into a generic commit, making things worse
	if gitutil.IsRebaseInProgress(ledgerPath) {
		return SkippedCheck("Ledger clean workdir", "rebase in progress",
			"Resolve or abort the rebase first: git -C <ledger> rebase --abort")
	}

	// check for any uncommitted changes
	statusCmd := exec.Command("git", "-C", ledgerPath, "status", "--porcelain")
	output, err := statusCmd.Output()
	if err != nil {
		return SkippedCheck("Ledger clean workdir", "status check failed", "")
	}

	if len(output) == 0 {
		return PassedCheck("Ledger clean workdir", "clean")
	}

	// count different types of changes
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	staged := 0
	unstaged := 0
	untracked := 0

	for _, line := range lines {
		if len(line) < 2 {
			continue
		}

		// git status --porcelain format: XY filename
		// X = index status, Y = work tree status
		indexStatus := line[0]
		workTreeStatus := line[1]

		// Skip unmerged entries — they're counted by checkLedgerUnmergedPaths.
		// Without this guard the wedge gets double-counted as "N modified"
		// and the more actionable unmerged-paths failure gets buried.
		if isUnmergedCode(indexStatus, workTreeStatus) {
			continue
		}

		if indexStatus == '?' && workTreeStatus == '?' {
			untracked++
		} else {
			if indexStatus != ' ' && indexStatus != '?' {
				staged++
			}
			if workTreeStatus != ' ' && workTreeStatus != '?' {
				unstaged++
			}
		}
	}

	// build status message
	var parts []string
	if staged > 0 {
		parts = append(parts, fmt.Sprintf("%d staged", staged))
	}
	if unstaged > 0 {
		parts = append(parts, fmt.Sprintf("%d modified", unstaged))
	}
	if untracked > 0 {
		parts = append(parts, fmt.Sprintf("%d untracked", untracked))
	}

	msg := strings.Join(parts, ", ")
	total := staged + unstaged + untracked

	if total > 0 {
		if fix {
			return fixLedgerDirtyWorkdir(ledgerPath, total)
		}
		return WarningCheck("Ledger clean workdir", msg,
			"Run `ox doctor --fix` to commit and sync ledger changes")
	}

	return PassedCheck("Ledger clean workdir", "clean")
}

// fixLedgerDirtyWorkdir stages and commits all changes in the ledger.
func fixLedgerDirtyWorkdir(ledgerPath string, fileCount int) checkResult {
	// ensure .gitignore exists and untrack any cache files BEFORE staging.
	// without this, git add -A will commit local-only files like sync-state.json.
	gitserver.EnsureGitignoreBeforeCommit(ledgerPath)

	// stage all changes
	// --sparse: ledger repos use sparse-checkout
	addCmd := exec.Command("git", "-C", ledgerPath, "add", "--sparse", "-A")
	if output, err := addCmd.CombinedOutput(); err != nil {
		return FailedCheck("Ledger clean workdir",
			"staging failed",
			fmt.Sprintf("git add error: %s", strings.TrimSpace(string(output))))
	}

	// commit
	commitCmd := exec.Command("git", "-C", ledgerPath, "commit", "-m", "ox doctor: auto-commit ledger changes")
	if output, err := commitCmd.CombinedOutput(); err != nil {
		errStr := strings.TrimSpace(string(output))
		// "nothing to commit" is fine (race with session auto-stage)
		if strings.Contains(errStr, "nothing to commit") {
			return PassedCheck("Ledger clean workdir", "clean (already committed)")
		}
		return FailedCheck("Ledger clean workdir",
			"commit failed",
			fmt.Sprintf("git commit error: %s", errStr))
	}

	return PassedCheck("Ledger clean workdir",
		fmt.Sprintf("committed %d file(s)", fileCount))
}

// checkLedgerCacheTracked detects .sageox/cache/ files that are tracked by git in the ledger.
// Cache files are local-only machine state (e.g., sync-state.json) and must never be committed.
// This can happen when commits occurred before .sageox/.gitignore was in place.
// With fix=true, untracks them via `git rm --cached` (preserves local files).
func checkLedgerCacheTracked(fix bool) checkResult {
	const name = "Ledger cache files untracked"

	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck(name, "no ledger found", "")
	}

	if !isGitRepo(ledgerPath) {
		return SkippedCheck(name, "ledger not a git repo", "")
	}

	if !gitserver.CacheFilesTracked(ledgerPath) {
		return PassedCheck(name, "no cache files tracked")
	}

	if !fix {
		return WarningCheck(name,
			"local-only cache files are tracked in ledger git history",
			"run `ox doctor --fix` to untrack (local files preserved)")
	}

	// untrack cache files and ensure gitignore is in place
	gitserver.EnsureGitignoreBeforeCommit(ledgerPath)

	// check if untracking created staged changes that need committing
	statusCmd := exec.Command("git", "-C", ledgerPath, "diff", "--cached", "--name-only")
	if out, err := statusCmd.Output(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
		commitCmd := exec.Command("git", "-C", ledgerPath, "commit", "-m", "chore: untrack local-only cache files")
		if commitOut, err := commitCmd.CombinedOutput(); err != nil {
			errStr := strings.TrimSpace(string(commitOut))
			if !strings.Contains(errStr, "nothing to commit") {
				return FailedCheck(name, "commit failed after untracking",
					fmt.Sprintf("git commit error: %s", errStr))
			}
		}
	}

	return PassedCheck(name, "untracked cache files from ledger")
}

// checkLedgerURLAPIMatch compares the local ledger's git remote URL path against
// the authoritative URL from the API. This catches cases where the ledger was
// cloned with an old or incorrect URL that still authenticates but points to the
// wrong repository.
func checkLedgerURLAPIMatch(fix bool) checkResult {
	const checkName = "Ledger remote URL vs API"

	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck(checkName, "no ledger found", "")
	}

	if !isGitRepo(ledgerPath) {
		return SkippedCheck(checkName, "ledger not a git repo", "")
	}

	// get local remote URL
	localCmd := exec.Command("git", "-C", ledgerPath, "remote", "get-url", "origin")
	localOutput, err := localCmd.Output()
	if err != nil {
		return SkippedCheck(checkName, "no origin remote", "")
	}
	localURL := strings.TrimSpace(string(localOutput))

	// get repo_id from project config
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck(checkName, "not in git repo", "")
	}

	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil || cfg.RepoID == "" {
		return SkippedCheck(checkName, "no repo_id configured", "")
	}

	// create API client with auth
	projectEndpoint := endpoint.GetForProject(gitRoot)
	client := api.NewRepoClientForProject(gitRoot)
	if token, tokenErr := auth.GetTokenForEndpoint(projectEndpoint); tokenErr == nil && token != nil && token.AccessToken != "" {
		client.WithAuthToken(token.AccessToken)
	}

	// call API for authoritative ledger URL
	ledgerStatus, apiErr := client.GetLedgerStatus(cfg.RepoID)
	if apiErr != nil {
		// don't fail doctor for network issues
		return SkippedCheck(checkName, "API unavailable", "")
	}
	if ledgerStatus == nil || ledgerStatus.RepoURL == "" {
		return SkippedCheck(checkName, "no API URL available", "")
	}

	// strip credentials from both URLs for comparison
	localStripped := stripURLCredentials(localURL)
	apiStripped := stripURLCredentials(ledgerStatus.RepoURL)

	if localStripped == apiStripped {
		return PassedCheck(checkName, "URLs match")
	}

	// URLs differ
	if !fix {
		return FailedCheck(checkName, "URL mismatch",
			fmt.Sprintf("Local:    %s\n       Expected: %s\n       Run `ox doctor --fix` to update",
				localStripped, apiStripped))
	}

	if applyResult := applyCorrectedLedgerURL(ledgerPath, ledgerStatus.RepoURL, checkName); applyResult != nil {
		return *applyResult
	}

	// verify connectivity with a timeout. Authentication will use the
	// freshly-installed helper, so this also proves the helper is wired
	// correctly end-to-end.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	verifyCmd := exec.CommandContext(ctx, "git", "-C", ledgerPath, "ls-remote", "--heads", "origin")
	if verifyErr := verifyCmd.Run(); verifyErr != nil {
		if ctx.Err() != nil {
			return WarningCheck(checkName, "URL updated (verification timed out)",
				"Remote URL was updated but connectivity check timed out after 5s")
		}
		return WarningCheck(checkName, "URL updated but verification failed",
			"Remote URL was updated but could not verify connectivity. If you haven't logged in, run `ox login`.")
	}
	return PassedCheck(checkName, "URL updated, helper installed, and verified")
}

// applyCorrectedLedgerURL writes the API-provided ledger URL to origin in
// its bare form (no userinfo) and installs the ox credential helper for the
// resulting host. Returns nil on success; on failure returns a non-nil
// pointer to the checkResult the caller should propagate.
//
// Per ox-eeqi this is the load-bearing invariant: the corrected origin URL
// MUST NOT carry an embedded oauth2:TOKEN. The PAT lives in the credential
// helper; embedding it here would re-create the leak the redesign was
// supposed to eliminate. Extracted so the invariant is unit-testable
// without standing up a fake API client.
func applyCorrectedLedgerURL(ledgerPath, apiURL, checkName string) *checkResult {
	parsed, parseErr := url.Parse(apiURL)
	if parseErr != nil {
		// In --fix mode this means the remediation did NOT happen — the
		// origin URL is still stale. Returning a Warning here would let
		// `ox doctor --fix` look successful; surface it as a real failure.
		r := FailedCheck(checkName, "cannot fix (invalid API URL)", parseErr.Error())
		return &r
	}
	parsed.User = nil
	bareURL := parsed.String()

	setCmd := exec.Command("git", "-C", ledgerPath, "remote", "set-url", "origin", bareURL)
	if output, setErr := setCmd.CombinedOutput(); setErr != nil {
		safeOutput := stripURLCredentials(strings.TrimSpace(string(output)))
		r := FailedCheck(checkName, "set-url failed",
			fmt.Sprintf("git remote set-url error: %s", safeOutput))
		return &r
	}

	// Install (or refresh) the credential helper for the new host. The new
	// URL may live on a different host than the old one; even when it
	// doesn't, MigrateLedgerCredentials is idempotent. Same primitive
	// checkLedgerEmbeddedCreds uses, so behavior stays consistent across
	// the two fix paths.
	if _, migErr := gitserver.MigrateLedgerCredentials(ledgerPath, gitserver.DefaultHelperCommand()); migErr != nil {
		r := FailedCheck(checkName, "URL updated but helper install failed",
			fmt.Sprintf("MigrateLedgerCredentials: %v", migErr))
		return &r
	}
	return nil
}

// stripURLCredentials removes userinfo (credentials) from a URL for safe comparison.
// Returns the original string if parsing fails.
func stripURLCredentials(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	parsed.User = nil
	return parsed.String()
}
