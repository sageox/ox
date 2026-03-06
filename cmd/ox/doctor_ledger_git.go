package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/ledger"
)

// Ledger Git Health check slug constants
const (
	CheckSlugLedgerRemoteReachable = "ledger-remote-reachable"
	CheckSlugLedgerBranchStatus    = "ledger-branch-status"
	CheckSlugLedgerCleanWorkdir    = "ledger-clean-workdir"
	CheckSlugLedgerRemoteURLMatch  = "ledger-remote-url-match"
	CheckSlugLedgerURLAPIMatch     = "ledger-url-api-match"
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
		Slug:        CheckSlugLedgerRemoteURLMatch,
		Name:        "Ledger remote URL match",
		Category:    "Ledger Git Health",
		FixLevel:    FixLevelAuto,
		Description: "Validates ledger remote credentials match current login",
		Run:         func(fix bool) checkResult { return checkLedgerRemoteURLMatch(fix) },
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
	pushCmd := exec.Command("git", "-C", ledgerPath, "push")
	output, err := pushCmd.CombinedOutput()
	if err != nil {
		errStr := strings.TrimSpace(string(output))
		return FailedCheck("Ledger branch status",
			"push failed",
			fmt.Sprintf("git push error: %s", errStr))
	}
	return PassedCheck("Ledger branch status",
		fmt.Sprintf("pushed %d commit(s)", aheadCount))
}

// fixLedgerBranchBehind pulls remote changes into local ledger.
func fixLedgerBranchBehind(ledgerPath string, behindCount int) checkResult {
	pullCmd := exec.Command("git", "-C", ledgerPath, "pull", "--rebase")
	output, err := pullCmd.CombinedOutput()
	if err != nil {
		errStr := strings.TrimSpace(string(output))
		// abort rebase to leave ledger in a clean state
		_ = exec.Command("git", "-C", ledgerPath, "rebase", "--abort").Run()
		return FailedCheck("Ledger branch status",
			"pull --rebase failed (aborted)",
			fmt.Sprintf("Conflict during rebase (aborted to restore clean state): %s", errStr))
	}
	return PassedCheck("Ledger branch status",
		fmt.Sprintf("pulled %d commit(s)", behindCount))
}

// fixLedgerBranchDiverged reconciles a diverged ledger by rebasing then pushing.
func fixLedgerBranchDiverged(ledgerPath string, aheadCount, behindCount int) checkResult {
	// pull --rebase first to linearize history
	pullCmd := exec.Command("git", "-C", ledgerPath, "pull", "--rebase")
	pullOutput, err := pullCmd.CombinedOutput()
	if err != nil {
		errStr := strings.TrimSpace(string(pullOutput))
		// abort rebase to leave ledger in a clean state
		_ = exec.Command("git", "-C", ledgerPath, "rebase", "--abort").Run()
		return FailedCheck("Ledger branch status",
			"rebase failed during reconcile (aborted)",
			fmt.Sprintf("Conflict during rebase (aborted to restore clean state): %s", errStr))
	}

	// then push
	pushCmd := exec.Command("git", "-C", ledgerPath, "push")
	pushOutput, err := pushCmd.CombinedOutput()
	if err != nil {
		errStr := strings.TrimSpace(string(pushOutput))
		return FailedCheck("Ledger branch status",
			"push failed after rebase",
			fmt.Sprintf("git push error: %s", errStr))
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
	// stage all changes
	addCmd := exec.Command("git", "-C", ledgerPath, "add", "-A")
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

// checkLedgerRemoteURLMatch detects stale PATs in the ledger's git remote URL.
// Compares the embedded PAT against the currently stored credentials.
// With fix=true, updates the remote URL with the current PAT.
func checkLedgerRemoteURLMatch(fix bool) checkResult {
	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck("Ledger remote URL match", "no ledger found", "")
	}

	if !isGitRepo(ledgerPath) {
		return SkippedCheck("Ledger remote URL match", "ledger not a git repo", "")
	}

	// get local origin URL and extract embedded PAT
	localCmd := exec.Command("git", "-C", ledgerPath, "remote", "get-url", "origin")
	localOutput, err := localCmd.Output()
	if err != nil {
		return SkippedCheck("Ledger remote URL match", "no origin remote", "")
	}
	localURL := strings.TrimSpace(string(localOutput))

	// extract the PAT embedded in the remote URL
	embeddedPAT, _ := extractPATFromURL(localURL)

	// SSH URLs — credentials managed externally
	if strings.Contains(localURL, "@") && !strings.Contains(localURL, "://") {
		return PassedCheck("Ledger remote URL match", "SSH remote (credentials managed externally)")
	}

	// load current credentials from store
	gitRoot := findGitRoot()
	ep := endpoint.GetForProject(gitRoot)
	if ep == "" {
		return SkippedCheck("Ledger remote URL match", "no endpoint configured", "")
	}

	creds, err := gitserver.LoadCredentialsForEndpoint(ep)
	if err != nil || creds == nil || creds.Token == "" {
		if embeddedPAT == "" {
			return PassedCheck("Ledger remote URL match", "no credentials to check")
		}
		return SkippedCheck("Ledger remote URL match", "no stored credentials (run ox login)", "")
	}

	// check if credentials are expired
	if !creds.ExpiresAt.IsZero() && creds.ExpiresAt.Before(time.Now()) {
		return SkippedCheck("Ledger remote URL match", "credentials expired (run ox login)", "")
	}

	// compare PATs — both stale and missing PATs need repair
	if embeddedPAT == creds.Token {
		return PassedCheck("Ledger remote URL match", "credentials current")
	}

	// credentials need repair: either stale PAT or bare URL missing credentials
	sanitizedURL := gitserver.SanitizeRemoteURL(localURL)

	if fix {
		return fixLedgerStalePAT(ledgerPath, ep)
	}

	if embeddedPAT == "" {
		return WarningCheck("Ledger remote URL match",
			fmt.Sprintf("missing credentials in remote: %s", sanitizedURL),
			"Remote has no embedded credentials. Run `ox doctor` to auto-fix.")
	}

	return WarningCheck("Ledger remote URL match",
		fmt.Sprintf("stale credentials in remote: %s", sanitizedURL),
		"Remote uses credentials from a different login. Run `ox doctor` to auto-fix.")
}

// extractPATFromURL parses a git URL and returns the embedded PAT and the full URL.
// Returns ("", url) for SSH URLs, bare URLs, or non-oauth2 auth.
func extractPATFromURL(rawURL string) (pat string, fullURL string) {
	// SSH URLs don't have embedded PATs
	if strings.Contains(rawURL, "@") && !strings.Contains(rawURL, "://") {
		return "", rawURL
	}

	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.User == nil {
		return "", rawURL
	}

	// only handle oauth2-style auth (ox-managed)
	if parsed.User.Username() != "oauth2" {
		return "", rawURL
	}

	password, hasPassword := parsed.User.Password()
	if !hasPassword || password == "" {
		return "", rawURL
	}

	return password, rawURL
}

// fixLedgerStalePAT updates the ledger remote URL with the current PAT.
func fixLedgerStalePAT(ledgerPath, ep string) checkResult {
	err := gitserver.RefreshRemoteCredentials(ledgerPath, ep)
	if err != nil {
		return FailedCheck("Ledger remote URL match",
			"failed to update remote credentials",
			fmt.Sprintf("Error: %v", err))
	}

	return PassedCheck("Ledger remote URL match", "credentials updated")
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

	// fix: build correct URL with current PAT embedded
	ep := endpoint.GetForProject(gitRoot)
	creds, credErr := gitserver.LoadCredentialsForEndpoint(ep)
	if credErr != nil || creds == nil || creds.Token == "" {
		return WarningCheck(checkName, "cannot fix (no credentials)",
			"Run `ox login` first, then `ox doctor --fix`")
	}

	parsed, parseErr := url.Parse(ledgerStatus.RepoURL)
	if parseErr != nil {
		return WarningCheck(checkName, "cannot fix (invalid API URL)", parseErr.Error())
	}
	parsed.User = url.UserPassword("oauth2", creds.Token)
	correctURL := parsed.String()

	// update the remote URL
	setCmd := exec.Command("git", "-C", ledgerPath, "remote", "set-url", "origin", correctURL)
	if output, setErr := setCmd.CombinedOutput(); setErr != nil {
		// sanitize output — git may echo the URL with embedded credentials
		safeOutput := stripURLCredentials(strings.TrimSpace(string(output)))
		return FailedCheck(checkName, "set-url failed",
			fmt.Sprintf("git remote set-url error: %s", safeOutput))
	}

	// verify connectivity with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	verifyCmd := exec.CommandContext(ctx, "git", "-C", ledgerPath, "ls-remote", "--heads", "origin")
	if verifyErr := verifyCmd.Run(); verifyErr != nil {
		if ctx.Err() != nil {
			return WarningCheck(checkName, "URL updated (verification timed out)",
				"Remote URL was updated but connectivity check timed out after 5s")
		}
		return WarningCheck(checkName, "URL updated but verification failed",
			"Remote URL was updated but could not verify connectivity")
	}
	return PassedCheck(checkName, "URL updated and verified")
}
