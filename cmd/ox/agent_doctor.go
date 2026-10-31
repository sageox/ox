package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/sageox/ox/internal/doctor"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/session"
)

// Agent Doctor Output Structures for JSON mode

// AgentDoctorOutput is the top-level JSON output for ox agent <id> doctor
type AgentDoctorOutput struct {
	Success            bool                    `json:"success"`
	Type               string                  `json:"type"` // "agent_doctor"
	AgentID            string                  `json:"agent_id"`
	IncompleteSessions []IncompleteSessionInfo `json:"incomplete_sessions,omitempty"`
	// HeldSessions are kept on this machine (session_publishing: manual);
	// CacheOnlySessions wait in the cache for the daemon to reclaim them.
	// Neither is published by doctor (GH #1095, #1077).
	HeldSessions        []string `json:"held_sessions,omitempty"`
	CacheOnlySessions   []string `json:"cache_only_sessions,omitempty"`
	PendingUploads      []string `json:"pending_uploads,omitempty"`
	PendingFinalization []string `json:"pending_finalization,omitempty"`
	RecoverySessions    []string `json:"recovery_sessions,omitempty"`
	BlockedSessions     []string `json:"blocked_sessions,omitempty"`
	RefusedSessions     []string `json:"refused_sessions,omitempty"`
	SessionScanError    string   `json:"session_scan_error,omitempty"`
	StagedCount         int      `json:"staged_count"`
	CommitNeeded        bool     `json:"commit_needed"`
	PushNeeded          bool     `json:"push_needed"`
	NextSteps           []string `json:"next_steps,omitempty"`
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
func runAgentDoctor(w io.Writer, inst *agentinstance.Instance) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	// gather session health info
	output := buildAgentDoctorOutput(inst.AgentID, projectRoot)

	// Inspection must succeed before acknowledging the marker. Recovery and
	// blocked work still need attention even when no summary can be generated.
	gitRoot := findGitRoot()
	if gitRoot != "" && output.Success && len(output.IncompleteSessions) == 0 &&
		len(output.PendingUploads) == 0 && len(output.RecoverySessions) == 0 &&
		len(output.PendingFinalization) == 0 && len(output.BlockedSessions) == 0 && !output.CommitNeeded && !output.PushNeeded {
		_ = doctor.ClearNeedsDoctorAgent(gitRoot)
	}

	// output format selection (priority: review > text > json default)
	if cfg.Review {
		// security audit mode: human summary + JSON
		outputAgentDoctorText(w, output)
		fmt.Fprintln(w)
		fmt.Fprintln(w, "--- Machine Output ---")
		return outputAgentDoctorJSON(w, output)
	}

	if cfg.Text {
		// human-readable text output
		outputAgentDoctorText(w, output)
		return nil
	}

	// default: JSON output
	return outputAgentDoctorJSON(w, output)
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
		output.Success = false
		output.NextSteps = append(output.NextSteps, "ox doctor --fix (ledger not found)")
		return output
	}

	// Share the daemon's eligibility rules, including cache-only work and
	// explicit summary requests. Inspection never recovers or publishes data.
	handler := agentwork.NewSessionFinalizeHandler(slog.New(slog.DiscardHandler))
	handler.SetProjectRoot(projectRoot)
	inspection, scanErr := handler.Inspect(ledgerPath)
	output.RecoverySessions = inspection.Recovery
	output.BlockedSessions = inspection.Blocked
	output.RefusedSessions = inspection.Refused
	output.PendingFinalization = inspection.Automatic
	for _, item := range inspection.Items {
		payload := item.Payload.(*agentwork.SessionFinalizePayload)
		name := filepath.Base(payload.SessionDir)
		if payload.UploadOnly {
			output.PendingUploads = append(output.PendingUploads, name)
			continue
		}
		info := IncompleteSessionInfo{SessionID: name, Path: payload.SessionDir}
		for _, artifact := range payload.Missing {
			switch artifact {
			case ledgerFileSummaryMD:
				info.Missing = append(info.Missing, "summary")
			case "summary.json":
				info.Missing = append(info.Missing, "summary_json")
			case ledgerFileSessionMD:
				info.Missing = append(info.Missing, "session_md")
			}
		}
		info.FinalizeCommands = buildFinalizeCommands(name, agentID, info.Missing, info.Path)
		if containsString(info.Missing, "summary") || containsString(info.Missing, "summary_json") {
			info.SummarizePrompt = buildSummarizePromptForSession(info.Path)
		}
		output.IncompleteSessions = append(output.IncompleteSessions, info)
	}

	unpublished := listUnpublishedCacheSessions(ledgerPath)
	output.HeldSessions = append(inspection.Held, unpublished.Held...)
	slices.Sort(output.HeldSessions)
	output.HeldSessions = slices.Compact(output.HeldSessions)
	output.CacheOnlySessions = slices.DeleteFunc(unpublished.Waiting, func(name string) bool {
		return slices.Contains(output.HeldSessions, name)
	})
	for _, name := range output.HeldSessions {
		output.NextSteps = append(output.NextSteps,
			fmt.Sprintf("Held on this machine: %s. Publish only if the coworker asks: 'ox session upload %s'", name, name))
	}

	// check git status for staged/pending changes
	stagedCount, commitNeeded, pushNeeded, gitErr := checkLedgerGitStatus(ledgerPath)
	output.StagedCount = stagedCount
	output.CommitNeeded = commitNeeded
	output.PushNeeded = pushNeeded
	if err := errors.Join(scanErr, gitErr); err != nil {
		output.Success = false
		output.SessionScanError = err.Error()
	}

	// build next steps based on state, preserving any steps already appended above
	output.NextSteps = append(output.NextSteps, buildNextSteps(output)...)

	return output
}

// buildFinalizeCommands returns commands to generate missing artifacts
func buildFinalizeCommands(sessionName, agentID string, missing []string, sessionPath string) []string {
	var commands []string

	for _, artifact := range missing {
		switch artifact {
		case "summary":
			rawPath := filepath.Join(sessionPath, ledgerFileRaw)
			commands = append(commands, fmt.Sprintf("ox agent %s session summarize --file %s", agentID, rawPath))
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
func checkLedgerGitStatus(ledgerPath string) (staged int, commitNeeded bool, pushNeeded bool, err error) {
	// check for uncommitted changes
	output, err := runAgentDoctorGitCommand(ledgerPath, "status", "--porcelain")
	if err != nil {
		return 0, false, false, fmt.Errorf("read ledger git status: %w", err)
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

	// also check if we have local commits not pushed.
	// Skip on shallow clones: rev-list --count truncates at the shallow
	// boundary and may either over- or under-count. We rely on the
	// "[ahead" porcelain check above, which uses upstream-tracking info
	// that's accurate even when local history is truncated.
	if !pushNeeded {
		if state, _ := gitutil.InspectRepo(ledgerPath); !state.Shallow {
			revOutput, err := runAgentDoctorGitCommand(ledgerPath, "rev-list", "--count", "@{upstream}..HEAD")
			if err == nil {
				count := strings.TrimSpace(revOutput)
				if count != "0" && count != "" {
					pushNeeded = true
				}
			}
		}
	}

	return staged, commitNeeded, pushNeeded, nil
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
	for _, name := range output.PendingUploads {
		steps = append(steps, fmt.Sprintf("Retry upload for session %s: ox session upload %s", name, name))
	}
	for _, name := range output.PendingFinalization {
		steps = append(steps, fmt.Sprintf("Session %s needs deterministic finalization by a running daemon with an enabled background worker. Run ox doctor to check availability; enable a worker with ox config set agent_worker auto. No summary generation is needed", name))
	}
	for _, name := range output.RecoverySessions {
		steps = append(steps, fmt.Sprintf("Session %s needs recording recovery; run ox doctor for recovery diagnostics", name))
	}
	for _, name := range output.BlockedSessions {
		steps = append(steps, fmt.Sprintf("Automatic finalization blocked for session %s; inspect its recording state and meta.json", name))
	}
	for _, name := range output.RefusedSessions {
		steps = append(steps, fmt.Sprintf("Publication refused for session %s; resolve the refusal before retrying", name))
	}

	// prioritize incomplete sessions
	if len(output.IncompleteSessions) > 0 {
		for _, sess := range output.IncompleteSessions {
			if containsString(sess.Missing, "summary") {
				steps = append(steps, fmt.Sprintf("Generate summary for session %s", sess.SessionID))
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
func outputAgentDoctorJSON(w io.Writer, output *AgentDoctorOutput) error {
	jsonOut, err := cli.MarshalJSONIndent(output)
	if err != nil {
		return fmt.Errorf("format doctor JSON: %w", err)
	}
	trackContextBytes(int64(len(jsonOut)))
	return cli.WriteJSONBytes(w, jsonOut)
}

// outputAgentDoctorText outputs the doctor results as human-readable text
func outputAgentDoctorText(w io.Writer, output *AgentDoctorOutput) {
	if output.SessionScanError != "" {
		fmt.Fprintf(w, "Session inspection failed: %s\n", output.SessionScanError)
	}
	if output.Success && len(output.NextSteps) == 0 && len(output.IncompleteSessions) == 0 && !output.CommitNeeded && !output.PushNeeded {
		cli.PrintSuccessTo(w, "Session health: all good")
		return
	}

	if len(output.IncompleteSessions) > 0 {
		fmt.Fprintf(w, "Incomplete sessions: %d\n", len(output.IncompleteSessions))
		for _, sess := range output.IncompleteSessions {
			fmt.Fprintf(w, "  %s: missing %s\n", sess.SessionID, strings.Join(sess.Missing, ", "))
			for _, cmd := range sess.FinalizeCommands {
				fmt.Fprintf(w, "    -> %s\n", cmd)
			}
		}
		fmt.Fprintln(w)
	}

	if output.CommitNeeded {
		fmt.Fprintf(w, "Staged files: %d (commit needed)\n", output.StagedCount)
	}
	if output.PushNeeded {
		fmt.Fprintln(w, "Push needed to sync with remote")
	}

	if len(output.NextSteps) > 0 {
		fmt.Fprintln(w, "\nNext steps:")
		for _, step := range output.NextSteps {
			fmt.Fprintf(w, "  - %s\n", step)
		}
	}
}
