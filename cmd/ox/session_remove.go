package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/pkg/sessionprovenance"

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
		confirmed, confirmErr := cli.ConfirmYesNoRequired(fmt.Sprintf("This will remove %d local session(s). Continue?", len(sessions)), false, false)
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			fmt.Println("Canceled.")
			return nil
		}
	}

	ledgerPath, _ := resolveLedgerPath()
	var removed int
	for _, t := range sessions {
		name := strings.TrimSuffix(t.Filename, ".jsonl")
		if err := preserveLocalDeletionIntent(store.GetSessionPath(name), ledgerPath); err != nil {
			return err
		}
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
				// A draft placeholder is not a removable session (ADR-029) —
				// it belongs to a recording that is very likely still LIVE.
				// Before drafts, a pattern could not match a live session in
				// the ledger at all; now it can, and removing it would delete
				// the running recording's local copy alongside the placeholder.
				// `ox agent session abort` is the command for discarding a
				// live session, and it removes the placeholder itself.
				if ls.Draft {
					slog.Debug("skip_draft_placeholder", "session", name)
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
		confirmed, confirmErr := cli.ConfirmYesNoRequired(prompt, false, false)
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			fmt.Println("Canceled.")
			return nil
		}
	} else if !force && !hasLedger {
		// local-only single match confirmation
		confirmed, confirmErr := cli.ConfirmYesNoRequired(fmt.Sprintf("Remove %s?", matchName(matches[0])), false, false)
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			fmt.Println("Canceled.")
			return nil
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
				return fmt.Errorf("ledger removal pending; local content retained: %w", err)
			}
		}
	}

	// delete from local cache
	var localRemoved int
	for _, m := range matches {
		if !m.isLocal {
			continue
		}
		name := matchName(m)
		if !m.isLedger {
			if err := preserveLocalDeletionIntent(store.GetSessionPath(strings.TrimSuffix(name, ".jsonl")), ledgerPath); err != nil {
				return err
			}
		}
		if err := store.Delete(name); err != nil {
			fmt.Printf("  Warning: failed to remove %s locally: %v\n", name, err)
		} else {
			localRemoved++
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
// Uses pushLedger() for push with pull --rebase retry on conflict.
func batchDeleteSessionsFromLedger(ledgerPath string, sessionNames []string) (int, error) {
	var staged int
	err := gitutil.WithRepoLock(context.Background(), ledgerPath, func() error {
		var paths []string
		// Validate every source before mutating any session, so a malformed receipt
		// cannot turn a batch into an unprotected partial deletion.
		type deletion struct {
			name   string
			source *sessionprovenance.Source
		}
		var selected []deletion
		for _, name := range sessionNames {
			if !sessionprovenance.ValidSessionName(name) {
				return fmt.Errorf("invalid session name")
			}
			dir := filepath.Join(ledgerPath, "sessions", name)
			if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return err
			}
			meta, err := lfs.ReadSessionMeta(dir)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			var source *sessionprovenance.Source
			if meta != nil {
				source = meta.Source
			}
			if source != nil {
				if err := gitutil.CheckSourcePublication(context.Background(), ledgerPath); err != nil {
					return err
				}
				record, err := session.ReadSourceRecord(ledgerPath, source.NativeSessionID)
				if err != nil {
					return err
				}
				if record == nil || record.Generation != source.Generation {
					return fmt.Errorf("missing or conflicting source receipt")
				}
				if len(source.Ranges) == 0 {
					return fmt.Errorf("missing source ranges")
				}
				for _, span := range source.Ranges {
					if span.Start < 0 || span.End <= span.Start {
						return fmt.Errorf("invalid source ranges")
					}
					found := false
					for _, coverage := range record.Coverage {
						if coverage.SessionName == name && coverage.Start == span.Start && coverage.End == span.End {
							found = true
							break
						}
					}
					if !found {
						return fmt.Errorf("source coverage does not identify deleted session")
					}
				}
			}
			selected = append(selected, deletion{name, source})
		}
		for _, item := range selected {
			if item.source != nil {
				for _, span := range item.source.Ranges {
					if err := session.ExcludeNativeSession(ledgerPath, item.source.NativeSessionID, "deleted", span.Start, span.End); err != nil {
						return err
					}
				}
				rel, _ := sessionprovenance.Path(item.source.NativeSessionID)
				if _, err := gitutil.RunGit(context.Background(), ledgerPath, "add", "--sparse", "--", rel); err != nil {
					return err
				}
				paths = append(paths, rel)
			}
			rel := filepath.ToSlash(filepath.Join("sessions", item.name))
			if _, err := gitutil.RunGit(context.Background(), ledgerPath, "rm", "-r", "--force", "--", rel); err != nil {
				return err
			}
			paths = append(paths, rel)
			staged++
		}
		if staged == 0 {
			return fmt.Errorf("no sessions could be staged for removal")
		}
		message := fmt.Sprintf("session: delete %d session(s)", staged)
		if staged == 1 {
			message = "session: delete " + selected[0].name
		}
		_, err := gitutil.CommitLedgerSessionDeletion(context.Background(), ledgerPath, message, sessionNames, paths...)
		return err
	})
	if err != nil {
		return 0, err
	}
	if err := pushLedger(context.Background(), ledgerPath); err != nil {
		return 0, fmt.Errorf("push: %w", err)
	}
	return staged, nil
}
