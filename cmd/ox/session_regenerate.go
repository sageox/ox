package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

var sessionRegenerateCmd = &cobra.Command{
	Use:   "regenerate [session-name]",
	Short: "Regenerate session HTML from raw data",
	Long: `Regenerate the session.html file from raw session data.

Useful when the HTML template has been updated and you want to
refresh existing sessions with the new design.

The session name supports partial matching (e.g. agent ID suffix).

Examples:
  ox session regenerate 2026-01-06T14-32-ryan-OxK3ZN
  ox session regenerate OxK3ZN           # partial match
  ox session regenerate --all            # regenerate all sessions
  ox session regenerate --all --force    # skip confirmation`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSessionRegenerate,
}

func init() {
	sessionCmd.AddCommand(sessionRegenerateCmd)
	sessionRegenerateCmd.Flags().Bool("all", false, "regenerate HTML for all sessions")
	sessionRegenerateCmd.Flags().Bool("force", false, "skip confirmation prompts")
}

func runSessionRegenerate(cmd *cobra.Command, args []string) error {
	regenAll, _ := cmd.Flags().GetBool("all")
	force, _ := cmd.Flags().GetBool("force")

	store, projectRoot, err := newSessionStore()
	if err != nil {
		return err
	}

	if regenAll {
		return regenerateAllSessions(store, projectRoot, force)
	}

	if len(args) == 0 {
		return fmt.Errorf("specify a session name or use --all\nRun 'ox session list' to see available sessions")
	}

	return regenerateSingleSession(store, projectRoot, args[0])
}

func regenerateSingleSession(store *session.Store, projectRoot, name string) error {
	sessionName, err := store.ResolveSessionName(name)
	if err != nil {
		return fmt.Errorf("resolve session name: %w", err)
	}

	storedSession, err := store.ReadSession(sessionName)
	if err != nil {
		return fmt.Errorf("session %q not found\nRun 'ox session list' to see available sessions", name)
	}

	sessionPath := store.GetSessionPath(sessionName)
	if err := regenerateSessionHTML(storedSession, sessionPath); err != nil {
		return err
	}

	// re-upload to LFS and push to ledger if applicable
	if err := syncRegeneratedSession(projectRoot, sessionPath, sessionName); err != nil {
		slog.Warn("ledger sync skipped", "session", sessionName, "error", err)
	}

	cli.PrintSuccess(fmt.Sprintf("Regenerated HTML for %s", sessionName))
	return nil
}

func regenerateAllSessions(store *session.Store, projectRoot string, force bool) error {
	sessions, err := store.ListAllSessions()
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}

	if len(sessions) == 0 {
		fmt.Println("No sessions found.")
		return nil
	}

	if !force {
		if !cli.ConfirmYesNo(fmt.Sprintf("Regenerate HTML for %d session(s)?", len(sessions)), false) {
			fmt.Println("Canceled.")
			return nil
		}
	}

	var regenerated, skipped int
	for _, info := range sessions {
		sessionName := info.SessionName
		if sessionName == "" {
			skipped++
			continue
		}

		storedSession, readErr := store.ReadSession(sessionName)
		if readErr != nil {
			slog.Warn("skipping unreadable session", "session", sessionName, "error", readErr)
			skipped++
			continue
		}

		sessionPath := store.GetSessionPath(sessionName)
		if regenErr := regenerateSessionHTML(storedSession, sessionPath); regenErr != nil {
			slog.Warn("failed to regenerate session", "session", sessionName, "error", regenErr)
			skipped++
			continue
		}

		regenerated++
	}

	// batch ledger sync: single commit+push for all regenerated sessions
	if regenerated > 0 {
		ledgerPath, ledgerErr := resolveLedgerPath()
		if ledgerErr == nil {
			// re-upload LFS for each session, then do a single push
			for _, info := range sessions {
				if info.SessionName == "" {
					continue
				}
				sessionPath := store.GetSessionPath(info.SessionName)
				htmlPath := filepath.Join(sessionPath, "session.html")
				if _, statErr := os.Stat(htmlPath); statErr != nil {
					continue
				}
				if _, lfsErr := uploadSessionLFS(projectRoot, sessionPath); lfsErr != nil {
					slog.Debug("LFS re-upload skipped", "session", info.SessionName, "error", lfsErr)
				}
			}
			if pushErr := commitAndPushLedger(ledgerPath, "batch-regenerate"); pushErr != nil {
				slog.Warn("ledger push skipped", "error", pushErr)
			}
		}
	}

	cli.PrintSuccess(fmt.Sprintf("Regenerated %d session(s)", regenerated))
	if skipped > 0 {
		cli.PrintWarning(fmt.Sprintf("Skipped %d session(s) (unreadable or missing raw data)", skipped))
	}

	return nil
}

// regenerateSessionHTML deletes any existing session.html and generates a new one.
func regenerateSessionHTML(storedSession *session.StoredSession, sessionPath string) error {
	htmlPath := filepath.Join(sessionPath, "session.html")

	// remove existing HTML
	if err := os.Remove(htmlPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove existing HTML: %w", err)
	}

	if err := generateHTML(storedSession, htmlPath); err != nil {
		return fmt.Errorf("generate HTML: %w", err)
	}

	slog.Debug("regenerated session HTML", "path", htmlPath)
	return nil
}

// syncRegeneratedSession re-uploads to LFS and pushes to the ledger for a single session.
func syncRegeneratedSession(projectRoot, sessionPath, sessionName string) error {
	if _, err := uploadSessionLFS(projectRoot, sessionPath); err != nil {
		return fmt.Errorf("LFS upload: %w", err)
	}

	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return fmt.Errorf("resolve ledger: %w", err)
	}

	if err := commitAndPushLedger(ledgerPath, sessionName); err != nil {
		return fmt.Errorf("commit and push: %w", err)
	}

	return nil
}
