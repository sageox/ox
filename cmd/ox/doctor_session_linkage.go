package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/lfs"
)

// doctor_session_linkage.go houses two soft-signal doctor checks for the
// commit↔session linkage system:
//
//  1. checkSessionTrailerRatio surfaces "how many of the last N commits on
//     this branch carry a SageOx-Session: trailer." A low ratio is the
//     smoking gun for GitHub squash-merge stripping trailers, for users
//     committing with --no-verify, or for prepare-commit-msg not being
//     installed. Never fails — just informs.
//
//  2. checkSessionProducedCommitsStaleness scans closed sessions in the
//     ledger sessions/ tree and reports how many SHAs they reference
//     that are no longer reachable in the current branch's history.
//     This is the visible part of D3 (closed-session post-rewrite is
//     intentionally not auto-mutated; staleness is a soft signal so
//     users notice).
//
// Both checks are read-only and intended to run quickly (bounded scan
// windows). They never block any session work.

// trailerScanCommitCount caps how far back the trailer-ratio scan looks.
// 50 is enough to catch a recent regression without scanning thousands of
// commits on a long-lived branch.
const trailerScanCommitCount = 50

// trailerRatioWarnThreshold is the fraction below which we render a warn-
// style soft signal. 0.4 chosen heuristically: a healthy active session
// produces commits with trailers; a 40% floor lets normal post-stop
// activity coexist without alarming.
const trailerRatioWarnThreshold = 0.4

// checkSessionTrailerRatio scans the last N commits and reports the share
// carrying a SageOx-Session: trailer. Soft signal only.
//
// Skip cases (each returns SkippedCheck with a reason — not a failure):
//   - Not inside a git repo
//   - Repo has no commits yet
//   - git log invocation errors
func checkSessionTrailerRatio() checkResult {
	const name = "session trailer coverage"

	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck(name, "not in a git repo", "")
	}

	count, withTrailer, err := scanTrailerRatio(gitRoot, trailerScanCommitCount)
	if err != nil {
		return SkippedCheck(name, fmt.Sprintf("git log failed: %v", err), "")
	}
	if count == 0 {
		return SkippedCheck(name, "no commits in scan window", "")
	}

	ratio := float64(withTrailer) / float64(count)
	msg := fmt.Sprintf("%d/%d recent commits carry SageOx-Session trailer (%.0f%%)",
		withTrailer, count, ratio*100)

	if ratio >= trailerRatioWarnThreshold {
		return PassedCheck(name, msg)
	}
	fix := "If a squash-merge config strips trailers or commits are landing without `ox` running, " +
		"recent commits will not be linkable to their sessions. See docs/specs/session-commit-linkage.md."
	return WarningCheck(name, msg, fix)
}

// scanTrailerRatio returns (totalCommits, withTrailer, err) for the last
// `limit` commits on HEAD. Pure: only reads git state.
func scanTrailerRatio(gitRoot string, limit int) (int, int, error) {
	// %H + %B per commit, separated by NUL to survive newlines in messages
	out, err := runGitLinkage(gitRoot, "log", fmt.Sprintf("-%d", limit), "--format=%H%n%B%x00")
	if err != nil {
		return 0, 0, err
	}
	records := strings.Split(out, "\x00")
	total, with := 0, 0
	for _, rec := range records {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		total++
		if strings.Contains(rec, "SageOx-Session:") {
			with++
		}
	}
	return total, with, nil
}

// checkSessionProducedCommitsStaleness scans closed sessions in the
// ledger and reports how many of their ProducedCommits SHAs are
// unreachable in the project repo's current history. Soft signal only.
func checkSessionProducedCommitsStaleness() checkResult {
	const name = "session ProducedCommits reachability"

	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck(name, "no ledger found", "")
	}
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck(name, "not in a git repo", "")
	}

	sessionsDir := filepath.Join(ledgerPath, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return SkippedCheck(name, "no sessions directory", "")
	}

	var totalSessions, sessionsWithStale, totalStaleSHAs int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionDir := filepath.Join(sessionsDir, e.Name())
		meta, err := lfs.ReadSessionMeta(sessionDir)
		if err != nil || meta == nil || len(meta.ProducedCommits) == 0 {
			continue
		}
		totalSessions++
		stale := countUnreachableSHAs(gitRoot, meta.ProducedCommits)
		if stale > 0 {
			sessionsWithStale++
			totalStaleSHAs += stale
		}
	}

	if totalSessions == 0 {
		return SkippedCheck(name, "no closed sessions with ProducedCommits", "")
	}
	if sessionsWithStale == 0 {
		return PassedCheck(name, fmt.Sprintf("all %d sessions' commits reachable", totalSessions))
	}

	msg := fmt.Sprintf("%d/%d sessions reference unreachable commits (%d SHAs total)",
		sessionsWithStale, totalSessions, totalStaleSHAs)
	fix := "Closed sessions are intentionally NOT mutated by post-rewrite (D3). " +
		"Stale entries are expected after rebasing commit ranges that occurred during prior recordings."
	return WarningCheck(name, msg, fix)
}

// countUnreachableSHAs returns how many SHAs in `shas` are not present in
// the current project repo's object database (git cat-file -e).
func countUnreachableSHAs(gitRoot string, shas []string) int {
	unreachable := 0
	for _, sha := range shas {
		if sha == "" {
			continue
		}
		cmd := exec.Command("git", "-C", gitRoot, "cat-file", "-e", sha)
		if err := cmd.Run(); err != nil {
			unreachable++
		}
	}
	return unreachable
}

// runGitLinkage captures stdout from a git invocation rooted at gitRoot.
// Local to this file to avoid colliding with the test-only runGit helper
// elsewhere in cmd/ox.
func runGitLinkage(gitRoot string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", gitRoot}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
