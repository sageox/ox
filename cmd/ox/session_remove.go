package main

import (
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

var sessionRemoveCmd = &cobra.Command{
	Use:   "remove <filename>",
	Short: "Remove a session",
	Long: `Remove a session from the local cache or ledger.

The filename can be a full filename or a partial match.
Use 'ox session list' to see available sessions.

Searches both local cache and ledger for matching sessions.
Ledger removal requires --force or interactive confirmation
since it affects all coworkers.

Examples:
  ox session remove 2026-01-05T10-30-ryan-Oxa7b3.jsonl
  ox session remove Oxa7b3    # partial match by agent ID
  ox session remove --all     # remove all local sessions (with confirmation)`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSessionRemove,
}

// Future: add session editing capability
// This could be implemented as:
// 1. Cloud feature via SageOx dashboard for collaborative editing
// 2. Local 'ox dashboard' server at localhost:1729 with browser UI
// 3. Export to markdown for manual editing, then re-import
// For now, users can manually edit JSONL files if needed.

func init() {
	sessionCmd.AddCommand(sessionRemoveCmd)
	sessionRemoveCmd.Flags().Bool("all", false, "Remove all local sessions (requires confirmation)")
	sessionRemoveCmd.Flags().Bool("force", false, "Skip confirmation prompts")
}

// sessionMatch tracks where a matching session was found.
type sessionMatch struct {
	info       session.SessionInfo
	isLocal    bool
	isLedger   bool
	ledgerPath string
}

func runSessionRemove(cmd *cobra.Command, args []string) error {
	removeAll, _ := cmd.Flags().GetBool("all")
	force, _ := cmd.Flags().GetBool("force")

	store, _, err := newSessionStore()
	if err != nil {
		return err
	}

	if removeAll {
		return removeAllSessions(store, force)
	}

	if len(args) == 0 {
		return fmt.Errorf("please specify a session filename or use --all\nRun 'ox session list' to see available sessions")
	}

	return removeSessionByPattern(store, args[0], force)
}

// removeAllSessions removes all local sessions with confirmation.
// Does not touch ledger sessions — use pattern matching for that.
func removeAllSessions(store *session.Store, force bool) error {
	sessions, err := store.ListSessions()
	if err != nil {
		return fmt.Errorf("failed to list sessions: %w", err)
	}

	if len(sessions) == 0 {
		fmt.Println("No sessions to remove.")
		return nil
	}

	// confirm unless force flag is set
	if !force {
		if !cli.ConfirmYesNo(fmt.Sprintf("This will remove %d local session(s). Continue?", len(sessions)), false) {
			fmt.Println("Canceled.")
			return nil
		}
	}

	var removed int
	for _, t := range sessions {
		if err := store.Delete(t.Filename); err != nil {
			fmt.Printf("  Warning: failed to remove %s: %v\n", t.Filename, err)
		} else {
			removed++
		}
	}

	cli.PrintSuccess(fmt.Sprintf("Removed %d session(s)", removed))
	return nil
}

// removeSessionByPattern removes sessions matching the pattern from local cache and/or ledger.
func removeSessionByPattern(store *session.Store, pattern string, force bool) error {
	// search local sessions
	localSessions, err := store.ListAllSessions()
	if err != nil {
		return fmt.Errorf("failed to list sessions: %w", err)
	}

	// build matches map keyed by session name
	matchMap := make(map[string]*sessionMatch)
	for _, t := range localSessions {
		name := t.SessionName
		if name == "" {
			name = t.Filename
		}
		if strings.Contains(name, pattern) {
			matchMap[name] = &sessionMatch{
				info:    t,
				isLocal: true,
			}
		}
	}

	// search ledger sessions
	ledgerPath, ledgerErr := resolveLedgerPath()
	if ledgerErr == nil {
		ledgerStore, storeErr := session.NewStore(ledgerPath)
		if storeErr == nil {
			ledgerSessions, listErr := ledgerStore.ListAllSessions()
			if listErr != nil {
				slog.Debug("list_ledger_sessions", "err", listErr)
			}
			for _, ls := range ledgerSessions {
				name := ls.SessionName
				if name == "" {
					name = ls.Filename
				}
				if !strings.Contains(name, pattern) {
					continue
				}
				if existing, ok := matchMap[name]; ok {
					existing.isLedger = true
					existing.ledgerPath = ledgerPath
				} else {
					matchMap[name] = &sessionMatch{
						info:       ls,
						isLedger:   true,
						ledgerPath: ledgerPath,
					}
				}
			}
		} else {
			slog.Debug("skip_ledger_search", "err", storeErr)
		}
	} else {
		slog.Debug("ledger_unavailable", "err", ledgerErr)
	}

	// convert to slice
	var matches []*sessionMatch
	for _, m := range matchMap {
		matches = append(matches, m)
	}

	if len(matches) == 0 {
		return fmt.Errorf("no sessions found matching %q\nRun 'ox session list' to see available sessions", pattern)
	}

	// separate local-only and ledger matches for different confirmation flows
	hasLedger := false
	for _, m := range matches {
		if m.isLedger {
			hasLedger = true
			break
		}
	}

	// if multiple matches, show them and ask user to be more specific
	if len(matches) > 1 && !force {
		fmt.Printf("Multiple sessions match %q:\n", pattern)
		for _, m := range matches {
			name := matchName(m)
			location := locationLabel(m)
			fmt.Printf("  %s  %s\n", name, cli.StyleDim.Render(location))
		}
		fmt.Println("\nPlease provide a more specific pattern or use --force to remove all matches.")
		return nil
	}

	// always show what will be removed when using --force with multiple matches
	if force && len(matches) > 1 {
		fmt.Printf("Removing %d sessions matching %q:\n", len(matches), pattern)
		for _, m := range matches {
			fmt.Printf("  %s  %s\n", matchName(m), cli.StyleDim.Render(locationLabel(m)))
		}
	}

	// ledger deletions always require explicit confirmation (--force or interactive)
	if hasLedger && !force {
		var ledgerNames []string
		for _, m := range matches {
			if m.isLedger {
				ledgerNames = append(ledgerNames, matchName(m))
			}
		}
		prompt := fmt.Sprintf("Remove %s from ledger? This affects all coworkers and cannot be undone", strings.Join(ledgerNames, ", "))
		if !cli.ConfirmYesNo(prompt, false) {
			fmt.Println("Canceled.")
			return nil
		}
	} else if !force && !hasLedger {
		// local-only single match confirmation
		if !cli.ConfirmYesNo(fmt.Sprintf("Remove %s?", matchName(matches[0])), false) {
			fmt.Println("Canceled.")
			return nil
		}
	}

	// delete from local cache
	var localRemoved int
	for _, m := range matches {
		if !m.isLocal {
			continue
		}
		name := matchName(m)
		if err := store.Delete(name); err != nil {
			fmt.Printf("  Warning: failed to remove %s locally: %v\n", name, err)
		} else {
			localRemoved++
		}
	}

	// batch-delete from ledger: collect all session names, single commit + push
	var ledgerRemoved int
	if hasLedger {
		var ledgerSessionNames []string
		var ledgerBase string
		for _, m := range matches {
			if m.isLedger {
				ledgerSessionNames = append(ledgerSessionNames, matchName(m))
				ledgerBase = m.ledgerPath
			}
		}
		if len(ledgerSessionNames) > 0 {
			removed, err := batchDeleteSessionsFromLedger(ledgerBase, ledgerSessionNames)
			ledgerRemoved = removed
			if err != nil {
				fmt.Printf("  Warning: ledger removal error: %v\n", err)
			}
		}
	}

	// print summary
	total := localRemoved + ledgerRemoved
	if total == 0 {
		return fmt.Errorf("failed to remove any sessions")
	}

	if len(matches) == 1 {
		location := removedLocationLabel(localRemoved > 0, ledgerRemoved > 0)
		cli.PrintSuccess(fmt.Sprintf("Removed %s %s", matchName(matches[0]), location))
	} else {
		var parts []string
		if localRemoved > 0 {
			parts = append(parts, fmt.Sprintf("%d local", localRemoved))
		}
		if ledgerRemoved > 0 {
			parts = append(parts, fmt.Sprintf("%d from ledger", ledgerRemoved))
		}
		cli.PrintSuccess(fmt.Sprintf("Removed %s", strings.Join(parts, ", ")))
	}

	return nil
}

// matchName returns the display name for a session match.
func matchName(m *sessionMatch) string {
	if m.info.SessionName != "" {
		return m.info.SessionName
	}
	return m.info.Filename
}

// locationLabel returns a human-readable label for where a session exists.
func locationLabel(m *sessionMatch) string {
	if m.isLocal && m.isLedger {
		return "(local + ledger)"
	}
	if m.isLedger {
		return "(ledger)"
	}
	return "(local)"
}

// removedLocationLabel returns a label for the removal summary.
func removedLocationLabel(local, ledger bool) string {
	if local && ledger {
		return "(local + ledger)"
	}
	if ledger {
		return "(ledger)"
	}
	return "(local)"
}

// batchDeleteSessionsFromLedger removes multiple sessions from the ledger in a
// single git commit + push. Returns the count of successfully staged removals.
// On push failure, resets the local commit to prevent orphaned commits from
// leaking into future git operations.
func batchDeleteSessionsFromLedger(ledgerPath string, sessionNames []string) (int, error) {
	var staged int
	for _, name := range sessionNames {
		gitRm := exec.Command("git", "rm", "-r", "--force", filepath.Join("sessions", name))
		gitRm.Dir = ledgerPath
		if out, err := gitRm.CombinedOutput(); err != nil {
			slog.Warn("git_rm_session", "session", name, "err", fmt.Sprintf("%s: %v", string(out), err))
			continue
		}
		staged++
	}

	if staged == 0 {
		return 0, fmt.Errorf("no sessions could be staged for removal")
	}

	// single commit for all removals
	commitMsg := fmt.Sprintf("session: delete %d session(s)", staged)
	if staged == 1 {
		commitMsg = fmt.Sprintf("session: delete %s", sessionNames[0])
	}
	gitCommit := exec.Command("git", "commit", "-m", commitMsg)
	gitCommit.Dir = ledgerPath
	if out, err := gitCommit.CombinedOutput(); err != nil {
		return 0, fmt.Errorf("git commit: %s: %w", string(out), err)
	}

	// single push — on failure, reset to prevent orphaned commit
	gitPush := exec.Command("git", "push")
	gitPush.Dir = ledgerPath
	if out, err := gitPush.CombinedOutput(); err != nil {
		// reset the commit so it doesn't leak into future pushes
		gitReset := exec.Command("git", "reset", "HEAD~1")
		gitReset.Dir = ledgerPath
		if resetOut, resetErr := gitReset.CombinedOutput(); resetErr != nil {
			slog.Warn("git_reset_after_push_fail", "err", fmt.Sprintf("%s: %v", string(resetOut), resetErr))
		}
		return 0, fmt.Errorf("git push: %s: %w", string(out), err)
	}

	return staged, nil
}
