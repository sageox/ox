package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
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
			_ = exec.Command("git", "-C", ledgerPath, "rebase", "--abort").Run()
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

// checkLedgerCleanWorkdir checks for uncommitted changes in the ledger repository.
// Reports if there are staged, unstaged, or untracked files.
// With fix=true, auto-commits all changes. The ledger is fully ox-managed.
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
