package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

// sessionDeleteOutput is the JSON output format for session delete.
type sessionDeleteOutput struct {
	Status      string `json:"status"`
	SessionName string `json:"session_name"`
	AgentID     string `json:"agent_id"`
	LocalDelete bool   `json:"local_deleted"`
	LedgerDelete bool  `json:"ledger_deleted"`
	Warning     string `json:"warning,omitempty"`
}

// runAgentSessionDelete deletes a completed session from the local cache and ledger.
// Unlike abort (which discards an active recording), delete removes a finished session.
//
// Confirmation behavior:
//   - Interactive terminal: prompts user with y/N confirmation
//   - Non-interactive (agent/pipe): requires --force flag
//
// Usage: ox agent <id> session delete <session-name> [--force]
func runAgentSessionDelete(inst *agentinstance.Instance, cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("session name required\nUsage: ox agent %s session delete <session-name>", inst.AgentID)
	}

	sessionName := args[0]

	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	// build session store to verify the session exists locally
	repoID := getRepoIDOrDefault(projectRoot)
	contextPath := session.GetContextPath(repoID)

	var store *session.Store
	var localExists bool
	if contextPath != "" {
		store, err = session.NewStore(contextPath)
		if err == nil {
			sessionPath := store.GetSessionPath(sessionName)
			if info, statErr := os.Stat(sessionPath); statErr == nil && info.IsDir() {
				localExists = true
			}
		}
	}

	// check ledger for the session
	ledgerPath, ledgerErr := resolveLedgerPath()
	var ledgerSessionDir string
	var ledgerExists bool
	if ledgerErr == nil {
		ledgerSessionDir = filepath.Join(ledgerPath, "sessions", sessionName)
		if info, statErr := os.Stat(ledgerSessionDir); statErr == nil && info.IsDir() {
			ledgerExists = true
		}
	}

	if !localExists && !ledgerExists {
		return fmt.Errorf("session not found: %s\nRun 'ox session list' to see available sessions", sessionName)
	}

	// confirmation: interactive terminal prompts, non-interactive requires --force
	forceFlag := cmd.Flag("force") != nil && cmd.Flag("force").Value.String() == "true"
	if !forceFlag {
		if cli.IsInteractive() {
			if !cli.ConfirmYesNo(fmt.Sprintf("Delete session %q? This cannot be undone", sessionName), false) {
				fmt.Println("Canceled.")
				return nil
			}
		} else {
			return fmt.Errorf("session delete is destructive and cannot be undone\nPass --force to confirm: ox agent %s session delete %s --force", inst.AgentID, sessionName)
		}
	}

	var localDeleted, ledgerDeleted bool
	var warning string

	// delete from local cache
	if localExists && store != nil {
		if deleteErr := store.DeleteSession(sessionName); deleteErr != nil {
			slog.Warn("failed to delete session from local cache", "session", sessionName, "error", deleteErr)
			warning = fmt.Sprintf("local delete failed: %v", deleteErr)
		} else {
			localDeleted = true
		}
	}

	// delete from ledger (git rm, commit, push)
	if ledgerExists {
		if deleteErr := deleteSessionFromLedger(ledgerPath, sessionName, ledgerSessionDir); deleteErr != nil {
			slog.Warn("failed to delete session from ledger", "session", sessionName, "error", deleteErr)
			if warning != "" {
				warning += "; "
			}
			warning += fmt.Sprintf("ledger delete failed: %v", deleteErr)
		} else {
			ledgerDeleted = true
		}
	}

	output := sessionDeleteOutput{
		Status:       "deleted",
		SessionName:  sessionName,
		AgentID:      inst.AgentID,
		LocalDelete:  localDeleted,
		LedgerDelete: ledgerDeleted,
		Warning:      warning,
	}

	if cfg.Text || cfg.Review {
		if localDeleted && ledgerDeleted {
			fmt.Printf("Session %q deleted from local cache and ledger.\n", sessionName)
		} else if localDeleted {
			fmt.Printf("Session %q deleted from local cache.\n", sessionName)
		} else if ledgerDeleted {
			fmt.Printf("Session %q deleted from ledger.\n", sessionName)
		}
		if warning != "" {
			fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
		}
		if cfg.Review {
			fmt.Println()
			fmt.Println("--- Machine Output ---")
		} else {
			return nil
		}
	}

	jsonOut, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("format delete JSON: %w", err)
	}
	fmt.Println(string(jsonOut))
	return nil
}

// deleteSessionFromLedger removes the session folder from the ledger git repo,
// commits the removal, and pushes. NEVER uses --force push.
func deleteSessionFromLedger(ledgerPath, sessionName, sessionDir string) error {
	// git rm -r the session folder
	gitRm := exec.Command("git", "rm", "-r", "--force", filepath.Join("sessions", sessionName))
	gitRm.Dir = ledgerPath
	if out, err := gitRm.CombinedOutput(); err != nil {
		return fmt.Errorf("git rm: %s: %w", string(out), err)
	}

	// commit
	commitMsg := fmt.Sprintf("session: delete %s", sessionName)
	gitCommit := exec.Command("git", "commit", "-m", commitMsg)
	gitCommit.Dir = ledgerPath
	if out, err := gitCommit.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %s: %w", string(out), err)
	}

	// push — no --force: ledger history must never be rewritten
	gitPush := exec.Command("git", "push")
	gitPush.Dir = ledgerPath
	if out, err := gitPush.CombinedOutput(); err != nil {
		return fmt.Errorf("git push: %s: %w", string(out), err)
	}

	return nil
}
