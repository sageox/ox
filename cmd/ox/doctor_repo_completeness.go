package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/gitutil"
)

// checkRepoCompleteness reports whether the working repo is shallow,
// partial, or both — clone modes that silently break ox subsystems:
//
//   - Shallow clones (CI default for actions/checkout) truncate the
//     commit graph. rev-list / merge-base produce wrong answers; ox
//     status displays "—" instead of misleading divergence numbers
//     after Step 1, but the underlying cause should be surfaced here.
//   - Partial clones (--filter=blob:none, common on 2026 CI providers
//     like Buildkite, Depot, Blacksmith, Namespace) keep the commit
//     graph but fault into the network on blob/tree reads. Codedb's
//     IndexLocalRepo walks blobs and will trigger lazy fetch.
//
// Policy: warn-only in CI / non-TTY (auto-unshallow is bandwidth-hostile
// and may break the CI checkout); in interactive TTY, surface the same
// finding so the user can re-clone or `git fetch --unshallow` themselves.
// We deliberately do NOT auto-fix — both shallow and partial are often
// intentional and the cost of an unsolicited unshallow is high.
func checkRepoCompleteness(_ bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Repo completeness", "not in git repo", "")
	}

	state, err := gitutil.InspectRepo(gitRoot)
	if err != nil {
		return WarningCheck("Repo completeness", "detection failed", err.Error())
	}
	if !state.Incomplete() {
		return PassedCheck("Repo completeness", "full history available")
	}

	var remediation []string
	if state.Shallow {
		remediation = append(remediation,
			"shallow clone — divergence/ancestry checks return undefined",
			"  remediation (caller's choice): git fetch --unshallow")
	}
	if state.Partial {
		remediation = append(remediation,
			"partial clone — blob/tree reads may fault into network",
			"  remediation (caller's choice): git clone without --filter, or",
			"  `git config --unset-all remote.origin.promisor && git fetch --refetch`")
	}

	envHint := ""
	if isCIEnvironment() || !cli.IsInteractive() {
		envHint = " (CI/non-TTY: informational only)"
	}

	return WarningCheck("Repo completeness",
		state.Reason+envHint,
		strings.Join(remediation, "\n"))
}

// isCIEnvironment reports whether the process is running under a known CI
// runner. Used to pick warn-only output instead of an interactive fix.
// Conservative: only matches signals that are unambiguously CI.
func isCIEnvironment() bool {
	for _, k := range []string{"CI", "GITHUB_ACTIONS", "GITLAB_CI", "BUILDKITE", "CIRCLECI"} {
		if v := os.Getenv(k); v == "true" || v == "1" {
			return true
		}
	}
	return false
}

// checkGitAlternates verifies that any `.git/objects/info/alternates`
// entries resolve to readable object stores, and surfaces the situation
// to the user. Native git CLI handles alternates correctly, but go-git v6
// — used by codedb's IndexLocalRepo — silently fails on alternates (both
// relative and absolute paths). See
// TestPlainOpenTolerant_AlternatesUpstreamLimitation.
//
// The check is informational: ox does not auto-rewrite alternates. The
// remediation surfaces `git repack -a -d --local` (the standard
// "dissociate" pattern, which copies alternated objects into the local
// store), or re-clone without --shared/--reference.
func checkGitAlternates(_ bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Git alternates", "not in git repo", "")
	}

	commonDir, err := commonGitDir(gitRoot)
	if err != nil {
		return WarningCheck("Git alternates", "rev-parse failed", err.Error())
	}

	altsPath := filepath.Join(commonDir, "objects", "info", "alternates")
	data, err := os.ReadFile(altsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PassedCheck("Git alternates", "no alternates configured")
		}
		return WarningCheck("Git alternates", "read failed", err.Error())
	}

	var entries, unreachable []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entries = append(entries, line)

		resolved := line
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(commonDir, "objects", line)
		}
		if _, statErr := os.Stat(resolved); statErr != nil {
			unreachable = append(unreachable, fmt.Sprintf("%s → %s (%v)", line, resolved, statErr))
		}
	}

	if len(entries) == 0 {
		return PassedCheck("Git alternates", "alternates file empty")
	}

	detail := strings.Builder{}
	fmt.Fprintf(&detail, "%d alternates entry(ies) configured. Native git handles this correctly, but go-git (used by codedb indexing) does NOT honor alternates and will fail object lookups.\n", len(entries))
	for _, e := range entries {
		detail.WriteString("  ")
		detail.WriteString(e)
		detail.WriteString("\n")
	}
	detail.WriteString("\nRemediation options:\n")
	detail.WriteString("  • Dissociate (copy objects into this repo, keep clone): git repack -a -d --local\n")
	detail.WriteString("  • Re-clone without --shared / --reference\n")
	detail.WriteString("  • Affected ox subsystems: codedb indexing (internal/codedb/index/IndexLocalRepo).")

	if len(unreachable) > 0 {
		return FailedCheck("Git alternates",
			fmt.Sprintf("%d unreachable alternate(s)", len(unreachable)),
			detail.String()+"\n\nUnreachable:\n  "+strings.Join(unreachable, "\n  "))
	}
	return WarningCheck("Git alternates",
		fmt.Sprintf("%d alternate(s) configured", len(entries)),
		detail.String())
}

// commonGitDir returns the resolved common git dir for repoPath using
// `git rev-parse --git-common-dir`. Worktree-safe — a linked worktree
// returns the main repo's git dir, where shallow + alternates live.
func commonGitDir(repoPath string) (string, error) {
	out, err := gitutil.RunGit(context.Background(), repoPath, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if !filepath.IsAbs(out) {
		return filepath.Join(repoPath, out), nil
	}
	return out, nil
}
