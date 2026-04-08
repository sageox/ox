package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// runAgentSessionCapturePrior captures prior history from the active coding agent.
//
// Usage: ox agent <id> session capture-prior [--title "..."] [--file <path>] [--session-id <id>]
//
// The command detects the active adapter and delegates to it. Each adapter
// (Claude Code, Codex, etc.) implements its own session discovery and parsing.
//
// If no adapter with capture-prior capability is detected, falls back to
// reading validated JSONL from stdin or --file.
func runAgentSessionCapturePrior(inst *agentinstance.Instance, args []string) error {
	title := parseTitle(args)
	filePath := parseCapturePriorFile(args)
	sessionID := parseSessionID(args)

	projectRoot := mustFindProjectRoot()

	opts := session.CaptureOptions{
		AgentID:         inst.AgentID,
		Title:           title,
		MergeWithActive: session.IsRecordingForAgent(projectRoot, inst.AgentID),
	}

	var result *session.CaptureResult
	var err error

	// try adapter-based capture first (unless explicit --file was given)
	if filePath == "" && !isStdinPiped() {
		result, err = capturePriorViaAdapter(inst, projectRoot, opts, sessionID)
		if err != nil {
			return fmt.Errorf("capture-prior failed: %w", err)
		}
	} else {
		// JSONL input path (stdin or --file)
		var reader *bufio.Reader
		if filePath != "" {
			f, openErr := os.Open(filePath)
			if openErr != nil {
				return fmt.Errorf("open file: %w", openErr)
			}
			defer f.Close()
			reader = bufio.NewReader(f)
		} else {
			reader = bufio.NewReader(os.Stdin)
		}
		result, err = session.CapturePrior(reader, opts)
		if err != nil {
			return fmt.Errorf("capture-prior failed: %w", err)
		}
	}

	return outputCaptureResult(result)
}

// capturePriorViaAdapter detects the active adapter and delegates capture-prior to it.
func capturePriorViaAdapter(inst *agentinstance.Instance, projectRoot string, opts session.CaptureOptions, sessionID string) (*session.CaptureResult, error) {
	// resolve native session ID from agent environment if not provided
	if sessionID == "" {
		if agent := agentx.CurrentAgent(); agent != nil && agent.SupportsSession() {
			sessionID = agent.SessionID(agentx.NewSystemEnvironment())
		}
	}

	// discover adapters and find one with capture-prior capability
	if err := adapters.RegisterExternalAdapters(); err != nil {
		return nil, fmt.Errorf("adapter discovery: %w", err)
	}

	ea, err := findCapturePriorAdapter()
	if err != nil {
		return nil, err
	}

	// delegate to adapter
	captureResult, err := ea.CapturePrior(adapterprotocol.CapturePriorParams{
		SessionID: sessionID,
		RepoRoot:  projectRoot,
		AgentID:   inst.AgentID,
		Title:     opts.Title,
	})
	if err != nil {
		return nil, fmt.Errorf("adapter capture-prior: %w", err)
	}

	if len(captureResult.Entries) == 0 {
		return nil, fmt.Errorf("no entries found in session")
	}

	// convert protocol entries to CapturedHistory and store
	history := session.ConvertProtocolEntriesToHistory(
		captureResult.Entries,
		inst.AgentID,
		captureResult.AgentType,
	)

	return session.CapturePriorFromHistory(history, opts)
}

// findCapturePriorAdapter discovers an adapter that supports capture-prior.
// DetectAdapter already handles full discovery (built-in + external binaries).
func findCapturePriorAdapter() (*adapters.ExternalAdapter, error) {
	adapter, err := adapters.DetectAdapter()
	if err != nil {
		return nil, fmt.Errorf("no adapter detected: %w", err)
	}

	ea, ok := adapter.(*adapters.ExternalAdapter)
	if !ok {
		return nil, fmt.Errorf("detected adapter does not support capture-prior")
	}

	if !ea.HasCapability(adapterprotocol.CapCapturePrior) {
		return nil, fmt.Errorf("adapter %q does not support capture-prior", ea.Name())
	}

	return ea, nil
}

// isStdinPiped returns true if stdin has piped data.
func isStdinPiped() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) == 0
}

// outputCaptureResult formats and prints the capture result.
func outputCaptureResult(result *session.CaptureResult) error {
	output := session.NewCaptureOutput(result)

	if cfg.Review {
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

	jsonOut, err := output.ToJSON()
	if err != nil {
		return fmt.Errorf("format JSON output: %w", err)
	}
	fmt.Println(string(jsonOut))
	return nil
}

// parseSessionID extracts --session-id value from args.
func parseSessionID(args []string) string {
	for i, arg := range args {
		if arg == "--session-id" && i+1 < len(args) {
			return args[i+1]
		}
		if len(arg) > 13 && arg[:13] == "--session-id=" {
			return arg[13:]
		}
	}
	return ""
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
