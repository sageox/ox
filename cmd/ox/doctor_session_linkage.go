package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/lfs"
)

// doctor_session_linkage.go houses soft-signal doctor checks for the
// commit↔session↔PR linkage system:
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
//  3. checkPRAttributionCoverage catches a miss on the one linkage hop that
//     has no automated enforcement at all: commit-level SageOx attribution
//     is written by a deterministic git hook (hooks_commit_msg.go), but
//     PR-body attribution exists only as guidance an AI coworker is
//     expected to remember and apply on every `gh pr create`/`gh pr edit`
//     (internal/prime/attribution.go). A long session with context
//     compaction can lose track of that instruction; this check is the
//     backstop that catches it before merge, when the PR body becomes the
//     permanent squash-merge record. See ox-5r5v.
//
// All three checks are read-only and intended to run quickly (bounded scan
// windows / single gh call). None ever block any session work.

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
	fix := "Informational only: historical commits cannot be updated safely. New commits created while a session is recording receive trailers automatically. " +
		"If new commits are missing trailers, check the commit hook and squash-merge configuration. See docs/specs/session-commit-linkage.md."
	return WarningCheck(name, msg, fix)
}

// scanTrailerRatio returns (totalCommits, withTrailer, err) for the last
// `limit` commits on HEAD. Pure: only reads git state.
func scanTrailerRatio(gitRoot string, limit int) (int, int, error) {
	// %P + %B per commit, separated by NUL to survive newlines in messages.
	// Merge commits are excluded: they commonly predate session recording and
	// cannot be retrofitted without rewriting shared history.
	out, err := runGitLinkage(gitRoot, "log", fmt.Sprintf("-%d", limit), "--format=%P%x1f%B%x00")
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
		parts := strings.SplitN(rec, "\x1f", 2)
		if len(parts) != 2 || len(strings.Fields(parts[0])) > 1 {
			continue
		}
		total++
		if strings.Contains(parts[1], "SageOx-Session:") {
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

// checkPRAttributionCoverage reports whether the open PR for the current
// branch (if any) is missing the SageOx PR-body attribution trailer despite
// having commits that carry SageOx commit attribution. Soft signal only —
// see the package doc comment above for why this is the sole backstop for
// the PR-body attribution hop.
//
// Skip cases (each returns SkippedCheck — not a failure): not in a git
// repo, project not initialized, attribution not configured, gh
// unavailable, no current branch, or no open PR for it.
func checkPRAttributionCoverage() checkResult {
	const name = "PR SageOx attribution coverage"

	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck(name, "not in a git repo", "")
	}
	if !config.IsInitialized(gitRoot) {
		return SkippedCheck(name, "project not initialized", "")
	}
	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		return SkippedCheck(name, "could not load project config", "")
	}
	attr := resolveProjectAttribution(cfg)
	if attr.Commit == "" || attr.PR == "" {
		return SkippedCheck(name, "SageOx attribution not configured for this project", "")
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return SkippedCheck(name, "gh CLI not available", "")
	}

	branch, err := runGitLinkage(gitRoot, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return SkippedCheck(name, "could not determine current branch", "")
	}
	branch = strings.TrimSpace(branch)
	if branch == "" || branch == "HEAD" {
		return SkippedCheck(name, "detached HEAD", "")
	}

	pr, err := lookupOpenPRForBranch(gitRoot, branch)
	if err != nil || pr == nil {
		return SkippedCheck(name, "no open PR for current branch", "")
	}

	hasSageOxCommit, err := branchHasSageOxCommitTrailer(gitRoot, pr.BaseRefName, attr.Commit)
	if err != nil {
		return SkippedCheck(name, fmt.Sprintf("git log failed: %v", err), "")
	}
	if !hasSageOxCommit {
		return PassedCheck(name, "no commits on this branch carry SageOx attribution")
	}

	if strings.Contains(pr.Body, attr.PR) || strings.Contains(pr.Body, "SageOx-Session:") {
		return PassedCheck(name, fmt.Sprintf("PR #%d body carries SageOx attribution", pr.Number))
	}

	msg := fmt.Sprintf("PR #%d has SageOx-attributed commits but its body is missing the trailer", pr.Number)
	fix := fmt.Sprintf("Add %q to the end of the PR body before merge — it becomes the permanent record on squash-merge: gh pr edit %d --body-file <file>",
		attr.PR, pr.Number)
	return WarningCheck(name, msg, fix)
}

// prAttributionInfo is the subset of `gh pr view`/`gh pr list` fields
// needed by checkPRAttributionCoverage.
type prAttributionInfo struct {
	Number      int    `json:"number"`
	Body        string `json:"body"`
	BaseRefName string `json:"baseRefName"`
}

// lookupOpenPRForBranch shells out to gh to find the open PR (if any) whose
// head is branch. Mirrors prURLForBranch's (hooks_pre_push.go) invocation
// style. Bounded by ghTimeout. Returns nil on any failure or no match.
func lookupOpenPRForBranch(gitRoot, branch string) (*prAttributionInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "list",
		"--head", branch, "--state", "open", "--json", "number,body,baseRefName")
	cmd.Dir = gitRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var prs []prAttributionInfo
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, err
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return &prs[0], nil
}

// branchHasSageOxCommitTrailer reports whether any commit ahead of the PR's
// base branch carries commitTrailer (the project's resolved SageOx commit
// attribution string). Falls back to a local base-ref range when the
// remote-tracking ref isn't available (e.g. base branch never fetched).
func branchHasSageOxCommitTrailer(gitRoot, baseRef, commitTrailer string) (bool, error) {
	out, err := runGitLinkage(gitRoot, "log", "origin/"+baseRef+"..HEAD", "--format=%B")
	if err != nil {
		out, err = runGitLinkage(gitRoot, "log", baseRef+"..HEAD", "--format=%B")
		if err != nil {
			return false, err
		}
	}
	return strings.Contains(out, commitTrailer), nil
}
