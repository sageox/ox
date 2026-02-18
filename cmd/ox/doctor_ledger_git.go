package main

import (
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"

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
		FixLevel:    FixLevelCheckOnly,
		Description: "Checks if local ledger branch is up-to-date with remote",
		Run:         func(fix bool) checkResult { return checkLedgerBranchStatus() },
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugLedgerCleanWorkdir,
		Name:        "Ledger clean workdir",
		Category:    "Ledger Git Health",
		FixLevel:    FixLevelCheckOnly,
		Description: "Checks for uncommitted changes in ledger repository",
		Run:         func(fix bool) checkResult { return checkLedgerCleanWorkdir() },
	})

	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugLedgerRemoteURLMatch,
		Name:        "Ledger remote URL match",
		Category:    "Ledger Git Health",
		FixLevel:    FixLevelAuto,
		Description: "Validates ledger remote credentials match current login",
		Run:         func(fix bool) checkResult { return checkLedgerRemoteURLMatch(fix) },
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
func checkLedgerBranchStatus() checkResult {
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
		return WarningCheck("Ledger branch status",
			fmt.Sprintf("diverged: %d ahead, %d behind", aheadCount, behindCount),
			"Run `ox sync` to reconcile ledger changes")
	}

	if aheadCount > 0 {
		return WarningCheck("Ledger branch status",
			fmt.Sprintf("%d commit(s) ahead", aheadCount),
			"Run `ox sync` to push ledger changes to remote")
	}

	if behindCount > 0 {
		return WarningCheck("Ledger branch status",
			fmt.Sprintf("%d commit(s) behind", behindCount),
			"Run `ox sync` to pull latest ledger changes")
	}

	return PassedCheck("Ledger branch status", "up to date")
}

// checkLedgerCleanWorkdir checks for uncommitted changes in the ledger repository.
// Reports if there are staged, unstaged, or untracked files.
func checkLedgerCleanWorkdir() checkResult {
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
		return WarningCheck("Ledger clean workdir", msg,
			"Run `ox sync` to commit and sync ledger changes")
	}

	return PassedCheck("Ledger clean workdir", "clean")
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
