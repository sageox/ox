package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/session"
)

// sessionAbortOutput is the JSON output format for session abort.
type sessionAbortOutput struct {
	Success     bool   `json:"success"`
	Type        string `json:"type"`
	AgentID     string `json:"agent_id"`
	SessionName string `json:"session_name,omitempty"`
	Message     string `json:"message"`
}

// runAgentSessionAbort discards the active session without uploading to ledger.
// This is a destructive operation — all local session data is permanently deleted.
// Requires --force flag; agents should confirm with the user before invoking.
//
// Usage: ox agent <id> session abort --force
func runAgentSessionAbort(inst *agentinstance.Instance, args []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	if !session.IsRecording(projectRoot) {
		return fmt.Errorf("no active session to abort\nRun 'ox agent %s session start' to begin recording", inst.AgentID)
	}

	state, err := session.LoadRecordingState(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to load recording state: %w", err)
	}
	if state == nil {
		return fmt.Errorf("no active session to abort")
	}

	// require --force to prevent accidental data loss
	if !hasFlag(args, "--force") {
		return fmt.Errorf("session abort is destructive and cannot be undone\nConfirm with the user first, then run: ox agent %s session abort --force", inst.AgentID)
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
			return fmt.Errorf("failed to remove session data: %w", err)
		}
	}

	// intentionally do NOT set doctor.SetNeedsDoctorAgent() — user chose to discard

	output := sessionAbortOutput{
		Success:     true,
		Type:        "session_abort",
		AgentID:     inst.AgentID,
		SessionName: sessionName,
		Message:     "session aborted and discarded",
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

// hasFlag checks if a flag is present in args.
func hasFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}
