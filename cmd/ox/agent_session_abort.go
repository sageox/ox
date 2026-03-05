package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

// sessionAbortOutput is the JSON output format for session abort.
type sessionAbortOutput struct {
	Success     bool   `json:"success"`
	Type        string `json:"type"`
	AgentID     string `json:"agent_id"`
	SessionName string `json:"session_name,omitempty"`
	Message     string `json:"message"`
	Guidance    string `json:"guidance,omitempty"`
}

// runAgentSessionAbort discards the active session without uploading to ledger.
// This is a destructive operation — all local session data is permanently deleted.
//
// Confirmation behavior:
//   - Interactive terminal: prompts user with y/N confirmation
//   - Non-interactive (agent/pipe): requires --force flag
//
// Usage: ox agent <id> session abort [--force]
func runAgentSessionAbort(inst *agentinstance.Instance, cmd *cobra.Command) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	state, err := session.LoadRecordingState(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to load recording state: %w", err)
	}
	if state == nil {
		return fmt.Errorf("no active session to abort\nRun 'ox agent %s session start' to begin recording", inst.AgentID)
	}

	// confirmation: interactive terminal prompts, non-interactive requires --force
	forceFlag := cmd.Flag("force") != nil && cmd.Flag("force").Value.String() == "true"
	if !forceFlag {
		if cli.IsInteractive() {
			if !cli.ConfirmYesNo("Abort and discard active session? This cannot be undone", false) {
				fmt.Println("Canceled.")
				return nil
			}
		} else {
			return fmt.Errorf("session abort is destructive and cannot be undone\nPass --force to confirm: ox agent %s session abort --force", inst.AgentID)
		}
	}

	sessionName := session.GetSessionName(state.SessionPath)

	// clear .recording.json so future session start works
	if err := session.ClearRecordingState(projectRoot); err != nil {
		return fmt.Errorf("failed to clear recording state: %w", err)
	}

	// remove entire session cache folder (raw.jsonl, events.jsonl, plan.md, etc.)
	// guard against empty path — os.RemoveAll("") would delete cwd
	if state.SessionPath != "" {
		if err := os.RemoveAll(state.SessionPath); err != nil {
			return fmt.Errorf("recording cleared but failed to remove session data at %s: %w", state.SessionPath, err)
		}
	}

	// intentionally do NOT set doctor.SetNeedsDoctorAgent() — user chose to discard

	output := sessionAbortOutput{
		Success:     true,
		Type:        "session_abort",
		AgentID:     inst.AgentID,
		SessionName: sessionName,
		Message:     "session aborted and discarded",
		Guidance:    "Session aborted and discarded. No further action needed. Continue with your current task.",
	}

	if cfg.Text || cfg.Review {
		fmt.Printf("Session %q aborted and discarded.\n", sessionName)
		if cfg.Review {
			fmt.Println()
			fmt.Println("--- Machine Output ---")
		} else {
			return nil
		}
	}

	jsonOut, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("format abort JSON: %w", err)
	}
	fmt.Println(string(jsonOut))
	return nil
}

