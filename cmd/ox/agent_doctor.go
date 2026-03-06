package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/doctor"
	"github.com/sageox/ox/internal/session"
)

// Agent Doctor Output Structures for JSON mode

// AgentDoctorOutput is the top-level JSON output for ox agent <id> doctor
type AgentDoctorOutput struct {
	Success            bool                    `json:"success"`
	Type               string                  `json:"type"` // "agent_doctor"
	AgentID            string                  `json:"agent_id"`
	IncompleteSessions []IncompleteSessionInfo `json:"incomplete_sessions,omitempty"`
	StagedCount        int                     `json:"staged_count"`
	CommitNeeded       bool                    `json:"commit_needed"`
	PushNeeded         bool                    `json:"push_needed"`
	NextSteps          []string                `json:"next_steps,omitempty"`
}

// IncompleteSessionInfo describes a session missing artifacts
type IncompleteSessionInfo struct {
	SessionID        string   `json:"session_id"`
	Path             string   `json:"path"`
	Missing          []string `json:"missing"`
	FinalizeCommands []string `json:"finalize_commands"`
	SummarizePrompt  string   `json:"summarize_prompt,omitempty"`
}

// runAgentDoctor checks session health and returns structured info for agents.
// Usage: ox agent <id> doctor [--json]
func runAgentDoctor(inst *agentinstance.Instance) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	// gather session health info
	output := buildAgentDoctorOutput(inst.AgentID, projectRoot)

	// clear .needs-doctor-agent marker on successful completion
	// (agent doctor always succeeds if we reach this point)
	gitRoot := findGitRoot()
	if gitRoot != "" {
		_ = doctor.ClearNeedsDoctorAgent(gitRoot)
	}

	// output format selection (priority: review > text > json default)
	if cfg.Review {
		// security audit mode: human summary + JSON
		outputAgentDoctorText(output)
		fmt.Println()
		fmt.Println("--- Machine Output ---")
		return outputAgentDoctorJSON(output)
	}

	if cfg.Text {
		// human-readable text output
		outputAgentDoctorText(output)
		return nil
	}

	// default: JSON output
	return outputAgentDoctorJSON(output)
}

// buildAgentDoctorOutput gathers session health information for agent consumption
func buildAgentDoctorOutput(agentID, projectRoot string) *AgentDoctorOutput {
	output := &AgentDoctorOutput{
		Success: true,
		Type:    "agent_doctor",
		AgentID: agentID,
	}

	// check ledger path
	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		output.NextSteps = append(output.NextSteps, "ox doctor --fix (ledger not found)")
		return output
	}

	// find incomplete sessions in ledger
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	incompleteSessions := findIncompleteSessions(sessionsDir, agentID)
	output.IncompleteSessions = incompleteSessions

	// check for orphaned/stale recordings
	if session.IsRecordingForAgent(projectRoot, agentID) {
		state, _ := session.LoadRecordingStateForAgent(projectRoot, agentID)
		if state != nil {
			age := state.Duration()
			if age.Hours() > 24 {
				output.NextSteps = append(output.NextSteps,
					fmt.Sprintf("Orphaned session (%s old). Upload: 'ox agent %s session recover', or discard: 'ox agent %s session abort --force'",
						formatDurationHuman(age), agentID, agentID))
			}
		}
	}

	// check git status for staged/pending changes
	stagedCount, commitNeeded, pushNeeded := checkLedgerGitStatus(ledgerPath)
	output.StagedCount = stagedCount
	output.CommitNeeded = commitNeeded
	output.PushNeeded = pushNeeded

	// build next steps based on state, preserving any steps already appended above
	output.NextSteps = append(output.NextSteps, buildNextSteps(output)...)

	return output
}

// findIncompleteSessions scans the sessions directory for incomplete sessions
func findIncompleteSessions(sessionsDir, agentID string) []IncompleteSessionInfo {
	var incomplete []IncompleteSessionInfo

	// list session folders
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return incomplete
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// skip legacy subdirs
		name := entry.Name()
		if name == "raw" || name == "events" {
			continue
		}

		sessionPath := filepath.Join(sessionsDir, name)
		missing := checkSessionCompleteness(sessionPath)

		if len(missing) > 0 {
			info := IncompleteSessionInfo{
				SessionID:        name,
				Path:             sessionPath,
				Missing:          missing,
				FinalizeCommands: buildFinalizeCommands(name, agentID, missing, sessionPath),
			}

			// if summary is missing, build a prompt for the agent
			if containsString(missing, "summary") || containsString(missing, "summary_json") {
				info.SummarizePrompt = buildSummarizePromptForSession(sessionPath)
			}

			incomplete = append(incomplete, info)
		}
	}

	return incomplete
}

// checkSessionCompleteness returns list of missing artifacts for a session folder
func checkSessionCompleteness(sessionPath string) []string {
	var missing []string

	// expected files in a complete session
	expectedFiles := map[string]string{
		ledgerFileRaw:       "raw",
		ledgerFileEvents:    "events",
		ledgerFileHTML:      "html",
		ledgerFileSummaryMD: "summary",
		ledgerFileSessionMD: "session_md",
		"summary.json":      "summary_json",
	}

	// raw.jsonl is required - if missing, the session is invalid
	rawPath := filepath.Join(sessionPath, ledgerFileRaw)
	if _, err := os.Stat(rawPath); os.IsNotExist(err) {
		// no raw file means no session data to process
		return nil
	}

	// check each expected file
	for filename, key := range expectedFiles {
		path := filepath.Join(sessionPath, filename)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			// skip raw since we already checked it above
			if key != "raw" {
				missing = append(missing, key)
			}
		}
	}

	return missing
}

// buildFinalizeCommands returns commands to generate missing artifacts
func buildFinalizeCommands(sessionName, agentID string, missing []string, sessionPath string) []string {
	var commands []string

	for _, artifact := range missing {
		switch artifact {
		case "html":
			rawPath := filepath.Join(sessionPath, ledgerFileRaw)
			commands = append(commands, fmt.Sprintf("ox session export --input %s", rawPath))
		case "summary":
			rawPath := filepath.Join(sessionPath, ledgerFileRaw)
			commands = append(commands, fmt.Sprintf("ox agent %s session summarize --file %s", agentID, rawPath))
		case "events":
			commands = append(commands, fmt.Sprintf("# %s missing for %s (regenerate by re-processing raw)", ledgerFileEvents, sessionName))
		case "session_md":
			rawPath := filepath.Join(sessionPath, ledgerFileRaw)
			commands = append(commands, fmt.Sprintf("ox session export --markdown --input %s", rawPath))
		case "summary_json":
			commands = append(commands, fmt.Sprintf("# summary.json missing for %s (run ox agent doctor to regenerate)", sessionName))
		}
	}

	return commands
}

// buildSummarizePromptForSession reads the session and builds a summary prompt
func buildSummarizePromptForSession(sessionPath string) string {
	rawPath := filepath.Join(sessionPath, ledgerFileRaw)

	// read the session file
	stored, err := session.ReadSessionFromPath(rawPath)
	if err != nil {
		return fmt.Sprintf("# Error reading session: %v", err)
	}

	// convert stored entries to session.Entry format for prompt building
	entries := convertStoredMapEntries(stored.Entries)

	// use the standard prompt builder
	return session.BuildSummaryPrompt(entries, rawPath, "")
}

// convertStoredMapEntries converts stored map entries to session.Entry
func convertStoredMapEntries(stored []map[string]any) []session.Entry {
	entries := make([]session.Entry, 0, len(stored))
	for _, entry := range stored {
		e := session.Entry{}
		if t, ok := entry["type"].(string); ok {
			e.Type = session.EntryType(t)
		}
		if c, ok := entry["content"].(string); ok {
			e.Content = c
		}
		if tn, ok := entry["tool_name"].(string); ok {
			e.ToolName = tn
		}
		if ti, ok := entry["tool_input"].(string); ok {
			e.ToolInput = ti
		}
		entries = append(entries, e)
	}
	return entries
}

// checkLedgerGitStatus checks git status for the ledger repo
func checkLedgerGitStatus(ledgerPath string) (staged int, commitNeeded bool, pushNeeded bool) {
	// check for uncommitted changes
	output, err := runAgentDoctorGitCommand(ledgerPath, "status", "--porcelain")
	if err != nil {
		return 0, false, false
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		// staged files have status in first column (not space or ?)
		if len(line) >= 2 && line[0] != ' ' && line[0] != '?' {
			staged++
		}
		// any changes mean commit needed
		commitNeeded = true
	}

	// check if ahead of remote (push needed)
	branchOutput, err := runAgentDoctorGitCommand(ledgerPath, "status", "-b", "--porcelain")
	if err == nil && strings.Contains(branchOutput, "[ahead") {
		pushNeeded = true
	}

	// also check if we have local commits not pushed
	if !pushNeeded {
		revOutput, err := runAgentDoctorGitCommand(ledgerPath, "rev-list", "--count", "@{upstream}..HEAD")
		if err == nil {
			count := strings.TrimSpace(revOutput)
			if count != "0" && count != "" {
				pushNeeded = true
			}
		}
	}

	return staged, commitNeeded, pushNeeded
}

// runAgentDoctorGitCommand runs a git command and returns output
func runAgentDoctorGitCommand(repoPath string, args ...string) (string, error) {
	fullArgs := append([]string{"-C", repoPath}, args...)
	cmd := exec.Command("git", fullArgs...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// buildNextSteps returns recommended actions based on doctor findings
func buildNextSteps(output *AgentDoctorOutput) []string {
	var steps []string

	// prioritize incomplete sessions
	if len(output.IncompleteSessions) > 0 {
		for _, sess := range output.IncompleteSessions {
			if containsString(sess.Missing, "summary") {
				steps = append(steps, fmt.Sprintf("Generate summary for session %s", sess.SessionID))
			}
			if containsString(sess.Missing, "html") {
				steps = append(steps, fmt.Sprintf("Generate HTML for session %s", sess.SessionID))
			}
			if containsString(sess.Missing, "summary_json") {
				steps = append(steps, fmt.Sprintf("Generate summary JSON for session %s", sess.SessionID))
			}
		}
	}

	// then git operations
	if output.CommitNeeded {
		steps = append(steps, "ox session commit")
	}
	if output.PushNeeded {
		steps = append(steps, "git push (in ledger)")
	}

	return steps
}

// outputAgentDoctorJSON outputs the doctor results as JSON
func outputAgentDoctorJSON(output *AgentDoctorOutput) error {
	jsonOut, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("format doctor JSON: %w", err)
	}
	trackContextBytes(int64(len(jsonOut)))
	fmt.Println(string(jsonOut))
	return nil
}

// outputAgentDoctorText outputs the doctor results as human-readable text
func outputAgentDoctorText(output *AgentDoctorOutput) {
	if len(output.IncompleteSessions) == 0 && !output.CommitNeeded && !output.PushNeeded {
		cli.PrintSuccess("Session health: all good")
		return
	}

	if len(output.IncompleteSessions) > 0 {
		fmt.Printf("Incomplete sessions: %d\n", len(output.IncompleteSessions))
		for _, sess := range output.IncompleteSessions {
			fmt.Printf("  %s: missing %s\n", sess.SessionID, strings.Join(sess.Missing, ", "))
			for _, cmd := range sess.FinalizeCommands {
				fmt.Printf("    -> %s\n", cmd)
			}
		}
		fmt.Println()
	}

	if output.CommitNeeded {
		fmt.Printf("Staged files: %d (commit needed)\n", output.StagedCount)
	}
	if output.PushNeeded {
		fmt.Println("Push needed to sync with remote")
	}

	if len(output.NextSteps) > 0 {
		fmt.Println("\nNext steps:")
		for _, step := range output.NextSteps {
			fmt.Printf("  - %s\n", step)
		}
	}
}
