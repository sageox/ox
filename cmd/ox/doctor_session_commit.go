package main

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/gitutil"
)

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugSessionCommit,
		Name:        "staged session commit",
		Category:    "Sessions",
		FixLevel:    FixLevelSuggested,
		Description: "Commits staged session files in the ledger repository",
		Run:         func(fix bool) checkResult { return checkSessionCommit(fix) },
	})
}

// checkSessionCommit checks for staged session files in the ledger and optionally commits them.
// This is a FixLevelSuggested check - it only performs the commit when fix=true.
func checkSessionCommit(fix bool) checkResult {
	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck("staged session commit", "no ledger found", "")
	}

	if !isGitRepo(ledgerPath) {
		return SkippedCheck("staged session commit", "ledger not a git repo", "")
	}

	// check for staged files in sessions/ directory
	stagedFiles, err := getStagedSessionFiles(ledgerPath)
	if err != nil {
		return SkippedCheck("staged session commit", "failed to check staged files", "")
	}

	if len(stagedFiles) == 0 {
		// no staged files - check passes (nothing to do)
		return PassedCheck("staged session commit", "no staged sessions")
	}

	// there are staged session files
	sessionCount := len(stagedFiles)
	sessionIDs := extractSessionIDs(stagedFiles)

	if !fix {
		// report that there are staged files that could be committed
		msg := fmt.Sprintf("%d staged session(s)", sessionCount)
		return WarningCheck("staged session commit", msg,
			fmt.Sprintf("Run `ox doctor --fix` to commit %d staged session file(s)", sessionCount))
	}

	// fix=true: refuse an unmerged index; the whole-index commit below would sweep it in (#1055)
	statusOut, err := exec.Command("git", "-C", ledgerPath, "status", "--porcelain=v1").Output()
	if err != nil {
		return SkippedCheck("staged session commit", "status check failed", "")
	}
	if unmerged := parseUnmergedPaths(string(statusOut)); len(unmerged) > 0 {
		sample := unmerged[0].Path
		if len(unmerged) > 1 {
			sample = fmt.Sprintf("%s (+%d more)", sample, len(unmerged)-1)
		}
		return FailedCheck("staged session commit",
			"unresolved conflicts present, refusing to auto-commit",
			fmt.Sprintf("%d unmerged file(s) at %s, e.g. %s.\n       "+
				"Committing now would bake the conflict markers into the ledger permanently. "+
				"Resolve by hand:\n       "+
				"  cd %s\n       "+
				"  git status                       # inspect the conflict\n       "+
				"  git checkout --ours <file>       # or --theirs, only if that side HAS the file\n       "+
				"  git add <file> && git commit",
				len(unmerged), ledgerPath, sample, ledgerPath))
	}

	// commit only sessions/ through the validated snapshot so markers or invalid meta.json are refused
	commitMsg := buildSessionCommitMessage(sessionIDs)
	committed, err := gitutil.CommitLedgerSnapshot(context.Background(), ledgerPath, commitMsg, "sessions/")
	if err != nil {
		return FailedCheck("staged session commit",
			"refusing to auto-commit",
			fmt.Sprintf("%v\n       "+
				"Fix the named file by hand (remove the markers, or `git -C %s checkout HEAD -- <file>` "+
				"to discard the local change), then `git add` it and rerun `ox doctor --fix`.",
				err, ledgerPath))
	}
	if !committed {
		return PassedCheck("staged session commit", "nothing to commit")
	}

	return PassedCheck("staged session commit",
		fmt.Sprintf("committed %d session(s)", sessionCount))
}

// getStagedSessionFiles returns the list of staged files in the ledger's sessions/ directory.
func getStagedSessionFiles(ledgerPath string) ([]string, error) {
	// git diff --cached --name-only -- sessions/
	cmd := exec.Command("git", "-C", ledgerPath, "diff", "--cached", "--name-only", "--", "sessions/")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --cached: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var files []string
	for _, line := range lines {
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// extractSessionIDs extracts session identifiers from staged file paths.
// Session files have format: sessions/YYYY-MM-DD-HH-MM-username-sessionid.jsonl
func extractSessionIDs(files []string) []string {
	var sessionIDs []string
	seen := make(map[string]bool)

	for _, file := range files {
		base := filepath.Base(file)
		if !strings.HasSuffix(base, ".jsonl") {
			continue
		}

		// extract session ID from filename
		// format: YYYY-MM-DD-HH-MM-username-sessionid.jsonl
		parts := strings.Split(strings.TrimSuffix(base, ".jsonl"), "-")
		if len(parts) >= 7 {
			// session ID is typically the last part (e.g., "Oxa7b3")
			sessionID := parts[len(parts)-1]
			if !seen[sessionID] {
				seen[sessionID] = true
				sessionIDs = append(sessionIDs, sessionID)
			}
		}
	}
	return sessionIDs
}

// buildSessionCommitMessage creates a commit message for session files.
// Reuses the same format as cmd/ox/session_commit.go for consistency.
func buildSessionCommitMessage(sessionIDs []string) string {
	timestamp := time.Now().Format("2006-01-02")

	if len(sessionIDs) == 1 {
		return fmt.Sprintf("Add session %s-%s", timestamp, sessionIDs[0])
	}
	if len(sessionIDs) > 1 {
		return fmt.Sprintf("Add %d sessions %s", len(sessionIDs), timestamp)
	}
	return fmt.Sprintf("Update sessions %s", timestamp)
}
