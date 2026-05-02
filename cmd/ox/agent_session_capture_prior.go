package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// runAgentSessionCapturePrior captures prior history from the active coding agent.
//
// Usage: ox agent <id> session capture-prior [--title "..."] [--file <path>] [--session-id <id>] [--adapter <name>]
//
// The command detects the active adapter and delegates to it. Each adapter
// (Claude Code, Codex, etc.) implements its own session discovery and parsing.
// An explicit --adapter <name> overrides auto-detection — useful when multiple
// adapters detect as present (e.g. both aider and claude-code installed).
//
// If no adapter with capture-prior capability is detected, falls back to
// reading validated JSONL from stdin or --file.
func runAgentSessionCapturePrior(inst *agentinstance.Instance, args []string) error {
	title := parseTitle(args)
	filePath := parseCapturePriorFile(args)
	sessionID := parseSessionID(args)
	adapterName := parseAdapter(args)

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
		result, err = capturePriorViaAdapter(inst, projectRoot, opts, sessionID, adapterName)
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
// When adapterName is non-empty, that adapter is used explicitly instead of
// auto-detection, overriding the usual Detect() race.
func capturePriorViaAdapter(inst *agentinstance.Instance, projectRoot string, opts session.CaptureOptions, sessionID, adapterName string) (*session.CaptureResult, error) {
	// resolve native session ID from agent environment if not provided
	if sessionID == "" {
		if agent := agentx.CurrentAgent(); agent != nil && agent.SupportsSession() {
			sessionID = agent.SessionID(agentx.NewSystemEnvironment())
		}
	}

	ea, err := findCapturePriorAdapter(adapterName)
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
//
// When adapterName is non-empty, that adapter is looked up by name (canonical
// or alias) and its capability is verified. An explicit name is authoritative
// and does not run Detect() — the caller has declared intent.
//
// When adapterName is empty, selection is deterministic:
//  1. Ensure external adapters are discovered (one-time registry scan).
//  2. Filter the registry to adapters that *have* the capture-prior
//     capability (cheap: uses cached Info response).
//  3. Run Detect() only on capable adapters, in alphabetical order, and
//     return the first that detects.
//
// This avoids the previous bug where DetectAdapter() iterated the registry in
// non-deterministic map order and returned any adapter that detected, even
// when it lacked the capability (e.g. aider winning the race over claude-code
// when both had Detect()=true). It also avoids shelling out to every
// adapter's detect binary — only adapters that could possibly serve the
// request are asked.
func findCapturePriorAdapter(adapterName string) (*adapters.ExternalAdapter, error) {
	if adapterName != "" {
		return findCapturePriorAdapterByName(adapterName)
	}

	if err := adapters.RegisterExternalAdapters(); err != nil {
		return nil, fmt.Errorf("adapter discovery: %w", err)
	}

	// filter by capability first (cheap: uses cached Info response)
	names := adapters.ListAdapters()
	sort.Strings(names)

	var capable []*adapters.ExternalAdapter
	for _, name := range names {
		a, err := adapters.GetAdapter(name)
		if err != nil {
			continue
		}
		ea, ok := a.(*adapters.ExternalAdapter)
		if !ok {
			continue
		}
		if ea.HasCapability(adapterprotocol.CapCapturePrior) {
			capable = append(capable, ea)
		}
	}

	if len(capable) == 0 {
		return nil, fmt.Errorf("no installed adapter supports capture-prior")
	}

	// detect only on capable adapters (expensive: shells out to detect subcommand)
	var tried []string
	for _, ea := range capable {
		if ea.Detect() {
			return ea, nil
		}
		tried = append(tried, ea.Name())
	}

	return nil, fmt.Errorf("no capture-prior adapter detected in current environment (tried: %s)",
		strings.Join(tried, ", "))
}

// findCapturePriorAdapterByName looks up an adapter by canonical name or alias
// and verifies it supports capture-prior.
func findCapturePriorAdapterByName(name string) (*adapters.ExternalAdapter, error) {
	adapter, err := adapters.GetAdapter(name)
	if err != nil {
		return nil, fmt.Errorf("adapter %q not found: %w", name, err)
	}
	ea, ok := adapter.(*adapters.ExternalAdapter)
	if !ok {
		return nil, fmt.Errorf("adapter %q does not support capture-prior", name)
	}
	if !ea.HasCapability(adapterprotocol.CapCapturePrior) {
		return nil, fmt.Errorf("adapter %q does not support capture-prior", name)
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

// parseAdapter extracts --adapter value from args. Used to explicitly select
// an adapter by canonical name (e.g. "claude-code") or alias (e.g. "claude"),
// overriding the default capability-filtered detection.
func parseAdapter(args []string) string {
	for i, arg := range args {
		if arg == "--adapter" && i+1 < len(args) {
			return args[i+1]
		}
		if len(arg) > 10 && arg[:10] == "--adapter=" {
			return arg[10:]
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
