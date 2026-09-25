package main

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/repotools"
	"github.com/spf13/cobra"
)

// TODO(ajit,ryan): Discuss whether this command should exist at all.
//
// CORE PRINCIPLE: Git is an IMPLEMENTATION DETAIL.
// Users should never need to know that ledger/team-context are git repos.
// We're currently leaking too much git semantics onto users:
//   - "commit" is git jargon
//   - "sync" implies git push/pull
//   - "pending sessions" suggests uncommitted git state
//
// Current workflow has friction:
//  1. ox agent <id> session stop  → ends recording, uploads to ledger (can fail)
//  2. ox session commit           → manual fallback if upload failed
//  3. ox sync                     → push to remote
//
// Problems:
//   - Users shouldn't have to remember to run "ox session commit"
//   - If session stop fails to upload, sessions silently accumulate locally
//   - No visibility into "pending" sessions that need commit
//   - Exposing "commit" and "sync" teaches users git internals they don't need
//
// Options to discuss:
//
//	a) Remove this command entirely - session stop handles everything
//	b) Make daemon auto-sync in background (no user action needed)
//	c) Rename to user-friendly terms (e.g., "ox session retry-upload")
//	d) Hide all sync mechanics - just show "session saved" or "upload pending"
//
// The goal: users say "ox session stop" and sessions magically appear for their team.
// All git operations (commit, push, retry) should be invisible plumbing.
var sessionCommitCmd = &cobra.Command{
	Use:   "commit",
	Short: "Commit pending sessions",
	Long: `Commit pending session changes to the ledger.

This commits any sessions that haven't been committed yet.
Use 'ox sync' to also push to remote.`,
	RunE: runSessionCommit,
}

func init() {
	sessionCommitCmd.Flags().StringP("message", "m", "", "Custom commit message")
}

func runSessionCommit(cmd *cobra.Command, args []string) error {
	// find project root using helper
	projectRoot, err := requireProjectRoot()
	if err != nil {
		return err
	}

	// check for git
	if err := repotools.RequireVCS(repotools.VCSGit); err != nil {
		return fmt.Errorf("git required: %w", err)
	}

	// find pending session changes
	sessionsDir := filepath.Join(projectRoot, ".sageox", "sessions")
	if _, err := os.Stat(sessionsDir); os.IsNotExist(err) {
		fmt.Println(cli.StyleDim.Render("No sessions directory found."))
		return nil
	}

	// check for uncommitted changes in sessions directory
	gitCmd := exec.Command("git", "-C", projectRoot, "status", "--porcelain", sessionsDir)
	output, err := gitCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check git status: %w", err)
	}

	if len(strings.TrimSpace(string(output))) == 0 {
		fmt.Println(cli.StyleDim.Render("No pending session changes to commit."))
		return nil
	}

	// parse changes for commit message
	changes := strings.TrimSpace(string(output))
	lines := strings.Split(changes, "\n")
	newCount := 0
	modifiedCount := 0
	var sessionIDs []string

	for _, line := range lines {
		if len(line) < 3 {
			continue
		}
		status := line[:2]
		filename := strings.TrimSpace(line[3:])

		// extract session ID from filename (format: YYYY-MM-DD-HH-MM-username-sessionid.jsonl)
		base := filepath.Base(filename)
		if strings.HasSuffix(base, ".jsonl") {
			parts := strings.Split(strings.TrimSuffix(base, ".jsonl"), "-")
			if len(parts) >= 7 {
				// session ID is typically the last part (e.g., "Oxa7b3")
				sessionID := parts[len(parts)-1]
				if !contains(sessionIDs, sessionID) {
					sessionIDs = append(sessionIDs, sessionID)
				}
			}
		}

		if strings.Contains(status, "?") || strings.Contains(status, "A") {
			newCount++
		} else if strings.Contains(status, "M") {
			modifiedCount++
		}
	}

	// refuse before `git add`, which would mark a conflict resolved with its markers (#1055)
	if unmerged := parseUnmergedPaths(string(output)); len(unmerged) > 0 {
		return fmt.Errorf("refusing to commit: %d unresolved conflict(s) in %s, e.g. %s; resolve them by hand (git status, then git checkout --ours/--theirs <file>) and rerun",
			len(unmerged), sessionsDir, unmerged[0].Path)
	}
	// a conflict already `git add`ed by hand is stage-0 but still carries its markers
	marked, err := sessionFileWithConflictMarkers(projectRoot, string(output))
	if err != nil {
		return fmt.Errorf("failed to check session files for conflict markers: %w", err)
	}
	if marked != "" {
		return fmt.Errorf("refusing to commit: %s still contains conflict markers; remove them by hand and rerun", marked)
	}

	// stage session files
	addCmd := exec.Command("git", "-C", projectRoot, "add", sessionsDir)
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("failed to stage sessions: %w", err)
	}

	// build commit message
	customMessage, _ := cmd.Flags().GetString("message")
	var commitMsg string

	if customMessage != "" {
		commitMsg = customMessage
	} else {
		timestamp := time.Now().UTC().Format("2006-01-02T15-04")
		if len(sessionIDs) == 1 {
			commitMsg = fmt.Sprintf("Add session %s-%s", timestamp, sessionIDs[0])
		} else if len(sessionIDs) > 1 {
			commitMsg = fmt.Sprintf("Add %d sessions %s", len(sessionIDs), timestamp)
		} else {
			commitMsg = fmt.Sprintf("Update sessions %s", timestamp)
		}
	}

	// commit only the sessions dir, so the user's other staged files stay staged
	commitCmd := exec.Command("git", "-C", projectRoot, "commit", "-m", commitMsg, "--", sessionsDir)
	commitOutput, err := commitCmd.CombinedOutput()
	if err != nil {
		// check if nothing to commit
		if strings.Contains(string(commitOutput), "nothing to commit") {
			fmt.Println(cli.StyleDim.Render("No changes to commit (already committed)."))
			return nil
		}
		return fmt.Errorf("failed to commit: %s", string(commitOutput))
	}

	// print status
	cli.PrintSuccess(fmt.Sprintf("Committed sessions: %s", commitMsg))

	if newCount > 0 || modifiedCount > 0 {
		var parts []string
		if newCount > 0 {
			parts = append(parts, fmt.Sprintf("%d new", newCount))
		}
		if modifiedCount > 0 {
			parts = append(parts, fmt.Sprintf("%d modified", modifiedCount))
		}
		fmt.Printf("  %s\n", cli.StyleDim.Render(strings.Join(parts, ", ")))
	}

	fmt.Println()
	cli.PrintHint("Run 'ox sync' to push changes to remote")

	return nil
}

// contains checks if a string slice contains a value
// sessionFileWithConflictMarkers returns the first changed session file, from
// `git status --porcelain` output, whose working-tree content has conflict markers.
func sessionFileWithConflictMarkers(projectRoot, porcelain string) (string, error) {
	for _, line := range strings.Split(porcelain, "\n") {
		if len(line) < 4 || strings.ContainsRune(line[:2], 'D') {
			continue
		}
		rel := line[3:]
		if i := strings.Index(rel, " -> "); i >= 0 {
			rel = rel[i+len(" -> "):]
		}
		// untracked directories are listed once, so walk them
		var marked string
		err := filepath.WalkDir(filepath.Join(projectRoot, rel), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || marked != "" {
				return err
			}
			has, err := gitutil.HasConflictMarkers(path)
			if has {
				marked = path
			}
			return err
		})
		if err != nil || marked != "" {
			return marked, err
		}
	}
	return "", nil
}

func contains(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}
