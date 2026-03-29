package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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

// runAgentSessionAbort discards a session without uploading to ledger.
// This is a destructive operation — all local session data is permanently deleted.
//
// Two modes:
//   - No args: abort this agent's active recording (requires .recording.json)
//   - With session name: abort any session by name (orphaned, ghost, non-recording)
//
// Confirmation behavior:
//   - Interactive terminal: prompts user with y/N confirmation
//   - Non-interactive (agent/pipe): requires --force flag
//
// Usage:
//
//	ox agent <id> session abort [--force]
//	ox agent <id> session abort <session-name> [--force]
func runAgentSessionAbort(inst *agentinstance.Instance, cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return runAgentSessionAbortByName(inst, cmd, args[0])
	}
	return runAgentSessionAbortActive(inst, cmd)
}

// runAgentSessionAbortActive aborts the calling agent's own active recording.
// This is the original behavior when no session name argument is provided.
func runAgentSessionAbortActive(inst *agentinstance.Instance, cmd *cobra.Command) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	state, err := session.LoadRecordingStateForAgent(projectRoot, inst.AgentID)
	if err != nil {
		return fmt.Errorf("failed to load recording state: %w", err)
	}
	if state == nil {
		return fmt.Errorf("no active session to abort\nRun 'ox agent %s session start' to begin recording", inst.AgentID)
	}

	if err := confirmAbort(inst, cmd); err != nil {
		return err
	}

	sessionName := session.GetSessionName(state.SessionPath)

	// clear .recording.json so future session start works
	if err := session.ClearRecordingStateForAgent(projectRoot, inst.AgentID); err != nil {
		return fmt.Errorf("failed to clear recording state: %w", err)
	}

	// remove entire session cache folder (raw.jsonl, plan.md, etc.)
	// guard against empty path — os.RemoveAll("") would delete cwd
	if state.SessionPath != "" {
		if err := os.RemoveAll(state.SessionPath); err != nil {
			return fmt.Errorf("recording cleared but failed to remove session data at %s: %w", state.SessionPath, err)
		}
	}

	// intentionally do NOT set doctor.SetNeedsDoctorAgent() — user chose to discard

	return emitAbortOutput(inst.AgentID, sessionName)
}

// runAgentSessionAbortByName aborts a session by name. Only allows discarding
// orphan, ghost, or local-only sessions — not uploaded or actively recording ones.
// This handles the gap where orphaned sessions have no clean discard path.
func runAgentSessionAbortByName(inst *agentinstance.Instance, cmd *cobra.Command, nameArg string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	// resolve session name and path in local cache
	sessionName, sessionPath, err := resolveSessionForAbort(projectRoot, nameArg)
	if err != nil {
		return err
	}

	// classify the session to guard against aborting uploaded or recording sessions
	info := buildSessionInfo(sessionName, sessionPath)
	isUploaded := isSessionInLedger(sessionName)
	status := session.ClassifySession(info, isUploaded)

	switch status {
	case session.StatusOrphan, session.StatusGhost, session.StatusLocal:
		// safe to discard
	case session.StatusRecording:
		return fmt.Errorf("session %q is actively recording — use 'ox agent %s session abort' (without a name) to abort your own session", sessionName, inst.AgentID)
	case session.StatusUploaded:
		return fmt.Errorf("session %q is already uploaded to the ledger — use 'ox agent %s session delete %s' to remove it", sessionName, inst.AgentID, sessionName)
	case session.StatusPaused:
		// paused = stopped but not uploaded; safe to discard locally
	case session.StatusCanceled:
		// already canceled — just clean up if folder somehow remains
	default:
		return fmt.Errorf("session %q has unexpected status %q", sessionName, status)
	}

	if err := confirmAbort(inst, cmd); err != nil {
		return err
	}

	// remove .recording.json if present (unconditional — no agent ownership check)
	recPath := filepath.Join(sessionPath, ".recording.json")
	if err := os.Remove(recPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to clear recording state: %w", err)
	}

	// remove entire session folder
	if err := os.RemoveAll(sessionPath); err != nil {
		return fmt.Errorf("failed to remove session data at %s: %w", sessionPath, err)
	}

	return emitAbortOutput(inst.AgentID, sessionName)
}

// buildSessionInfo constructs a SessionInfo from a session folder on disk.
// Reads .recording.json if present; checks raw.jsonl for data presence.
func buildSessionInfo(sessionName, sessionPath string) session.SessionInfo {
	info := session.SessionInfo{
		SessionName: sessionName,
	}

	// check for .recording.json
	recPath := filepath.Join(sessionPath, ".recording.json")
	if data, err := os.ReadFile(recPath); err == nil {
		var state session.RecordingState
		if jsonErr := json.Unmarshal(data, &state); jsonErr == nil {
			info.Recording = true
			info.AgentID = state.AgentID
			info.EntryCount = state.EntryCount
			info.ParentPID = state.ParentPID
			info.CreatedAt = state.StartedAt
		}
	}

	// check for raw.jsonl data
	info.HasRawData = session.RawJSONLHasData(sessionPath)

	return info
}

// isSessionInLedger checks if a session exists in the ledger (uploaded).
func isSessionInLedger(sessionName string) bool {
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return false
	}
	ledgerSessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	info, statErr := os.Stat(ledgerSessionDir)
	return statErr == nil && info.IsDir()
}

// resolveSessionForAbort finds a session by name across all known session locations.
// Supports partial name resolution (e.g., agent ID suffix like "OxKMZN").
// Returns the resolved session name and full path to the session folder.
//
// Searches the same three locations as 'ox session list':
//  1. XDG cache (active/recent recordings)
//  2. Ledger sessions directory (uploaded sessions)
//  3. Ledger cache (in-progress sessions before upload)
func resolveSessionForAbort(projectRoot, nameArg string) (string, string, error) {
	// build list of stores to search, in priority order
	var stores []*session.Store

	// 1. XDG cache (primary location for active recordings)
	repoID := getRepoIDOrDefault(projectRoot)
	if contextPath := session.GetContextPath(repoID); contextPath != "" {
		if s, err := session.NewStore(contextPath); err == nil {
			stores = append(stores, s)
		}
	}

	// 2. Ledger sessions + 3. Ledger cache
	if ledgerPath, err := resolveLedgerPath(); err == nil {
		if s, err := session.NewStore(ledgerPath); err == nil {
			stores = append(stores, s)
		}
		ledgerCachePath := filepath.Join(ledgerPath, ".sageox", "cache")
		if s, err := session.NewStore(ledgerCachePath); err == nil {
			stores = append(stores, s)
		}
	}

	for _, s := range stores {
		resolved, resolveErr := s.ResolveSessionName(nameArg)
		if resolveErr != nil {
			// ambiguous match: surface the error immediately
			return "", "", resolveErr
		}
		sessionPath := s.GetSessionPath(resolved)
		if info, statErr := os.Stat(sessionPath); statErr == nil && info.IsDir() {
			return resolved, sessionPath, nil
		}
	}

	return "", "", fmt.Errorf("session not found: %s\nRun 'ox session list' to see available sessions", nameArg)
}

// confirmAbort checks --force flag or prompts for interactive confirmation.
// Returns nil if confirmed, error if denied or missing --force.
func confirmAbort(inst *agentinstance.Instance, cmd *cobra.Command) error {
	forceFlag := cmd.Flag("force") != nil && cmd.Flag("force").Value.String() == "true"
	if forceFlag {
		return nil
	}
	if cli.IsInteractive() {
		if !cli.ConfirmYesNo("Abort and discard session? This cannot be undone", false) {
			fmt.Println("Canceled.")
			return fmt.Errorf("canceled")
		}
		return nil
	}
	return fmt.Errorf("session abort is destructive and cannot be undone\nPass --force to confirm: ox agent %s session abort --force", inst.AgentID)
}

// emitAbortOutput writes the JSON (and optional text) output for a successful abort.
func emitAbortOutput(agentID, sessionName string) error {
	output := sessionAbortOutput{
		Success:     true,
		Type:        "session_abort",
		AgentID:     agentID,
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
