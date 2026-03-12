package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session"
)

// runAgentSessionCapturePrior captures prior history from JSONL input.
//
// Usage: ox agent <id> session capture-prior [--title "..."] [--file <path>]
//
// Reads validated JSONL history from stdin or --file:
//
//	{"_meta":{"schema_version":"1","source":"agent_reconstruction","agent_id":"Oxa7b3",...}}
//	{"seq":1,"type":"user","content":"<prompt>","ts":"<ISO8601>","source":"planning_history"}
//	{"seq":2,"type":"assistant","content":"<response>","ts":"<ISO8601>","source":"planning_history"}
//	...
//
// The command:
//  1. Validates input against the history schema
//  2. Applies secret redaction
//  3. Stores in the session ledger
//  4. Returns JSON with the storage path
func runAgentSessionCapturePrior(inst *agentinstance.Instance, args []string) error {
	// parse optional arguments
	title := parseTitle(args)
	filePath := parseCapturePriorFile(args)

	// determine input source
	var reader *bufio.Reader
	if filePath != "" {
		f, err := os.Open(filePath)
		if err != nil {
			return fmt.Errorf("open file: %w", err)
		}
		defer f.Close()
		reader = bufio.NewReader(f)
	} else {
		// check if stdin has data
		stat, err := os.Stdin.Stat()
		if err != nil {
			return fmt.Errorf("check stdin: %w", err)
		}
		if (stat.Mode() & os.ModeCharDevice) != 0 {
			return fmt.Errorf("no input piped to stdin and no --file specified\nUsage: cat history.jsonl | ox agent %s session capture-prior", inst.AgentID)
		}
		reader = bufio.NewReader(os.Stdin)
	}

	// capture using the session package
	opts := session.CaptureOptions{
		AgentID:         inst.AgentID,
		Title:           title,
		MergeWithActive: session.IsRecordingForAgent(mustFindProjectRoot(), inst.AgentID),
	}

	result, err := session.CapturePrior(reader, opts)
	if err != nil {
		return fmt.Errorf("capture-prior failed: %w", err)
	}

	// build output
	output := session.NewCaptureOutput(result)

	// output format selection (priority: review > text > json default)
	if cfg.Review {
		// security audit mode: human summary + JSON
		if result.Title != "" {
			cli.PrintSuccess(fmt.Sprintf("Prior history captured: %q", result.Title))
		} else {
			cli.PrintSuccess("Prior history captured")
		}
		fmt.Printf("  Path: %s\n", result.Path)
		fmt.Printf("  Entries: %d\n", result.EntryCount)
		if result.SecretsRedacted > 0 {
			fmt.Printf("  Secrets redacted: %d\n", result.SecretsRedacted)
		}
		if result.TimeRange != nil {
			fmt.Printf("  Time range: %s to %s\n",
				result.TimeRange.Earliest.Format("15:04:05"),
				result.TimeRange.Latest.Format("15:04:05"))
		}
		fmt.Println()
		fmt.Println("--- Machine Output ---")
		jsonOut, _ := output.ToJSON()
		fmt.Println(string(jsonOut))
		return nil
	}

	if cfg.Text {
		// human-readable text output
		if result.Title != "" {
			cli.PrintSuccess(fmt.Sprintf("Prior history captured: %q", result.Title))
		} else {
			cli.PrintSuccess("Prior history captured")
		}
		fmt.Printf("  Path: %s\n", result.Path)
		fmt.Printf("  Entries: %d\n", result.EntryCount)
		if result.SecretsRedacted > 0 {
			fmt.Printf("  Secrets redacted: %d\n", result.SecretsRedacted)
		}
		return nil
	}

	// default: JSON output
	jsonOut, err := output.ToJSON()
	if err != nil {
		return fmt.Errorf("format JSON output: %w", err)
	}
	fmt.Println(string(jsonOut))
	return nil
}

// parseCapturePriorFile extracts --file value from args.
func parseCapturePriorFile(args []string) string {
	for i, arg := range args {
		if arg == "--file" && i+1 < len(args) {
			return args[i+1]
		}
		if len(arg) > 7 && arg[:7] == "--file=" {
			return arg[7:]
		}
	}
	return ""
}

// mustFindProjectRoot returns project root or empty string.
func mustFindProjectRoot() string {
	root, err := findProjectRoot()
	if err != nil {
		return ""
	}
	return root
}

// capturePriorOutput is the JSON output format for capture-prior command.
// Deprecated: use session.CaptureOutput instead.
type capturePriorOutput struct {
	Success         bool                      `json:"success"`
	Type            string                    `json:"type"` // "session_capture_prior"
	AgentID         string                    `json:"agent_id"`
	Path            string                    `json:"path"`
	SessionName     string                    `json:"session_name,omitempty"`
	EntryCount      int                       `json:"entry_count"`
	SecretsRedacted int                       `json:"secrets_redacted,omitempty"`
	TimeRange       *session.HistoryTimeRange `json:"time_range,omitempty"`
	Title           string                    `json:"title,omitempty"`
	Message         string                    `json:"message,omitempty"`
}

// formatCapturePriorOutput creates JSON output from capture result.
func formatCapturePriorOutput(result *session.CaptureResult) ([]byte, error) {
	output := capturePriorOutput{
		Success:         true,
		Type:            "session_capture_prior",
		AgentID:         result.AgentID,
		Path:            result.Path,
		SessionName:     result.SessionName,
		EntryCount:      result.EntryCount,
		SecretsRedacted: result.SecretsRedacted,
		TimeRange:       result.TimeRange,
		Title:           result.Title,
	}
	return json.MarshalIndent(output, "", "  ")
}
