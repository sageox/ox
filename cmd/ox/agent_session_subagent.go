package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session"
)

// subagentCompleteInput represents JSON input for subagent-complete command.
type subagentCompleteInput struct {
	Summary     string `json:"summary,omitempty"`      // brief description of subagent work
	SessionPath string `json:"session_path,omitempty"` // path to subagent's session
	SessionName string `json:"session_name,omitempty"` // subagent's session folder name
	EntryCount  int    `json:"entry_count,omitempty"`  // number of session entries
	DurationMs  int64  `json:"duration_ms,omitempty"`  // runtime in milliseconds
	Model       string `json:"model,omitempty"`        // LLM model used
	AgentType   string `json:"agent_type,omitempty"`   // coding agent type
}

// subagentCompleteOutput is the JSON output for subagent-complete command.
type subagentCompleteOutput struct {
	Success           bool   `json:"success"`
	Type              string `json:"type"` // "session_subagent_complete"
	SubagentID        string `json:"subagent_id"`
	ParentSessionPath string `json:"parent_session_path"`
	RegisteredAt      string `json:"registered_at"`
	Message           string `json:"message,omitempty"`
}

// runAgentSessionSubagentComplete handles subagent completion reporting.
// Usage: ox agent <id> session subagent-complete --parent <session-path> [--summary "..."]
//
// This command is called by a subagent when it completes its work to register
// its session with the parent session. The parent session aggregates all
// subagent sessions when it stops recording.
//
// Input can be provided via:
//   - --parent flag (required): path to parent session folder
//   - --summary flag: brief description of subagent work
//   - stdin: JSON with additional metadata
//
// Example:
//
//	ox agent Oxb2c3 session subagent-complete --parent /path/to/parent/session
//	echo '{"summary":"analyzed infrastructure"}' | ox agent Oxb2c3 session subagent-complete --parent /path/to/session
func runAgentSessionSubagentComplete(inst *agentinstance.Instance, args []string) error {
	// parse flags
	parentPath := parseFlag(args, "--parent")
	summary := parseFlag(args, "--summary")

	// if no parent path, try to find active recording
	if parentPath == "" {
		projectRoot, err := findProjectRoot()
		if err == nil {
			parentPath = session.FindParentSessionPath(projectRoot)
		}
	}

	if parentPath == "" {
		return fmt.Errorf("parent session path required\nUsage: ox agent %s session subagent-complete --parent <session-path>", inst.AgentID)
	}

	// parse optional JSON input from stdin
	var input subagentCompleteInput
	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) == 0 {
		decoder := json.NewDecoder(os.Stdin)
		if err := decoder.Decode(&input); err == nil {
			// apply stdin values if not overridden by flags
			if summary == "" {
				summary = input.Summary
			}
		}
	}

	// merge input values
	opts := session.SubagentCompleteOptions{
		SubagentID:        inst.AgentID,
		ParentSessionPath: parentPath,
		Summary:           summary,
		SessionPath:       input.SessionPath,
		SessionName:       input.SessionName,
		EntryCount:        input.EntryCount,
		DurationMs:        input.DurationMs,
		Model:             input.Model,
		AgentType:         input.AgentType,
	}

	// apply instance metadata if available
	if opts.Model == "" && inst.Model != "" {
		opts.Model = inst.Model
	}
	if opts.AgentType == "" && inst.AgentType != "" {
		opts.AgentType = inst.AgentType
	}

	// report completion
	if err := session.ReportSubagentComplete(opts); err != nil {
		return fmt.Errorf("failed to report subagent completion: %w", err)
	}

	// get subagent count for feedback
	subagents, _ := session.GetSubagentSessions(parentPath)
	subagentCount := len(subagents)

	// output format selection
	if cfg.Review {
		// security audit mode
		cli.PrintSuccess("Subagent completion registered")
		fmt.Printf("  Subagent: %s\n", inst.AgentID)
		fmt.Printf("  Parent session: %s\n", parentPath)
		fmt.Printf("  Total subagents: %d\n", subagentCount)
		if summary != "" {
			fmt.Printf("  Summary: %s\n", summary)
		}
		fmt.Println()
		fmt.Println("--- Machine Output ---")
		output := subagentCompleteOutput{
			Success:           true,
			Type:              "session_subagent_complete",
			SubagentID:        inst.AgentID,
			ParentSessionPath: parentPath,
			Message:           fmt.Sprintf("Registered as subagent #%d", subagentCount),
		}
		jsonOut, _ := json.MarshalIndent(output, "", "  ")
		fmt.Println(string(jsonOut))
		return nil
	}

	if cfg.Text {
		// human-readable text output
		cli.PrintSuccess("Subagent completion registered")
		fmt.Printf("  Subagent: %s\n", inst.AgentID)
		fmt.Printf("  Parent session: %s\n", parentPath)
		fmt.Printf("  Total subagents: %d\n", subagentCount)
		return nil
	}

	// default: JSON output
	output := subagentCompleteOutput{
		Success:           true,
		Type:              "session_subagent_complete",
		SubagentID:        inst.AgentID,
		ParentSessionPath: parentPath,
		Message:           fmt.Sprintf("Registered as subagent #%d", subagentCount),
	}
	jsonOut, _ := json.MarshalIndent(output, "", "  ")
	fmt.Println(string(jsonOut))
	return nil
}

// subagentListOutput is the JSON output for listing subagent sessions.
type subagentListOutput struct {
	Success   bool                       `json:"success"`
	Type      string                     `json:"type"` // "session_subagent_list"
	AgentID   string                     `json:"agent_id"`
	Count     int                        `json:"count"`
	Subagents []*session.SubagentSummary `json:"subagents,omitempty"`
	Summary   *session.SubagentSummary   `json:"summary,omitempty"`
}

// runAgentSessionSubagentList lists subagent sessions for the current session.
// Usage: ox agent <id> session subagent-list
func runAgentSessionSubagentList(inst *agentinstance.Instance) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}

	// load current recording state
	state, err := session.LoadRecordingStateForAgent(projectRoot, inst.AgentID)
	if err != nil {
		return fmt.Errorf("failed to load recording state: %w", err)
	}
	if state == nil {
		return fmt.Errorf("no active recording\nRun 'ox agent %s session start' first", inst.AgentID)
	}

	// get subagent sessions
	subagents, err := session.GetSubagentSessions(state.SessionPath)
	if err != nil {
		return fmt.Errorf("failed to get subagent sessions: %w", err)
	}

	summary := session.SummarizeSubagents(subagents)

	// output format selection
	if cfg.Review {
		cli.PrintSuccess(fmt.Sprintf("Found %d subagent session(s)", summary.Count))
		if summary.Count > 0 {
			fmt.Printf("  Total entries: %d\n", summary.TotalEntries)
			fmt.Printf("  Total duration: %dms\n", summary.TotalDurationMs)
			fmt.Println("\n  Subagents:")
			for i, sub := range subagents {
				fmt.Printf("    %d. %s", i+1, sub.SubagentID)
				if sub.Summary != "" {
					fmt.Printf(" - %s", sub.Summary)
				}
				fmt.Println()
			}
		}
		fmt.Println()
		fmt.Println("--- Machine Output ---")
	}

	if cfg.Text {
		if summary.Count == 0 {
			fmt.Println("No subagent sessions found.")
			return nil
		}
		fmt.Printf("Subagent sessions (%d):\n", summary.Count)
		for i, sub := range subagents {
			fmt.Printf("  %d. %s", i+1, sub.SubagentID)
			if sub.Summary != "" {
				fmt.Printf(" - %s", sub.Summary)
			}
			fmt.Println()
		}
		return nil
	}

	// default: JSON output
	output := map[string]interface{}{
		"success":   true,
		"type":      "session_subagent_list",
		"agent_id":  inst.AgentID,
		"count":     summary.Count,
		"subagents": subagents,
		"summary":   summary,
	}
	jsonOut, _ := json.MarshalIndent(output, "", "  ")
	fmt.Println(string(jsonOut))
	return nil
}

// parseFlag extracts a flag value from args (--flag value or --flag=value).
func parseFlag(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
		prefix := flag + "="
		if len(arg) > len(prefix) && arg[:len(prefix)] == prefix {
			return arg[len(prefix):]
		}
	}
	return ""
}
