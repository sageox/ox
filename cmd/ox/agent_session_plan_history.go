package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/session"
)

// planHistoryEntry represents a single entry in the planning history input.
// Supports both the CLAUDE.md session format and a simpler format.
type planHistoryEntry struct {
	Type      string `json:"type"`                 // user, assistant, system, tool
	Content   string `json:"content"`              // message content
	Timestamp string `json:"ts,omitempty"`         // optional timestamp (RFC3339)
	Seq       int    `json:"seq,omitempty"`        // sequence number
	Source    string `json:"source,omitempty"`     // "planning_history" marker
	IsPlan    bool   `json:"is_plan,omitempty"`    // explicit plan marker
	ToolName  string `json:"tool_name,omitempty"`  // for tool entries
	ToolInput string `json:"tool_input,omitempty"` // for tool entries
}

// planHistoryMeta represents the metadata header in planning history input.
type planHistoryMeta struct {
	SchemaVersion string `json:"schema_version"`
	AgentType     string `json:"agent_type"`
	SessionID     string `json:"session_id"`
	StartedAt     string `json:"started_at"`
}

// planHistoryOutput is the JSON output format for plan-history command.
type planHistoryOutput struct {
	Success      bool     `json:"success"`
	Type         string   `json:"type"` // always "session_plan_history"
	AgentID      string   `json:"agent_id"`
	SessionID    string   `json:"session_id"`
	RawPath      string   `json:"raw_path,omitempty"`
	PlanPath     string   `json:"plan_path,omitempty"`
	SessionPath  string   `json:"session_path,omitempty"`
	EntryCount   int      `json:"entry_count"`
	DiagramCount int      `json:"diagram_count"`
	Diagrams     []string `json:"diagrams,omitempty"`
	PlanExtract  string   `json:"plan_extract,omitempty"` // brief extract of plan
	Message      string   `json:"message,omitempty"`
}

// planHeaderPatterns matches plan section headers in content.
var planHeaderPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^##\s*Final\s*Plan\b`),
	regexp.MustCompile(`(?m)^##\s*Plan\b`),
	regexp.MustCompile(`(?m)^#\s*Final\s*Plan\b`),
	regexp.MustCompile(`(?m)^#\s*Plan\b`),
}

// runAgentSessionPlanHistory captures a planning session from prior history.
// Usage: ox agent <id> session plan-history [--title "..."] [--file <path>]
//
// Reads JSONL planning history from stdin or --file:
//
//	{"_meta":{"schema_version":"1","agent_type":"claude-code","session_id":"manual","started_at":"<ISO8601>"}}
//	{"ts":"<ISO8601>","type":"user","content":"<prompt>","seq":1,"source":"planning_history"}
//	{"ts":"<ISO8601>","type":"assistant","content":"<response>","seq":2,"source":"planning_history"}
//	...
func runAgentSessionPlanHistory(inst *agentinstance.Instance, args []string) error {
	// parse optional arguments
	title := parseTitle(args)
	filePath := parsePlanHistoryFile(args)

	// read entries from file or stdin
	entries, meta, err := readPlanHistoryEntries(filePath)
	if err != nil {
		return fmt.Errorf("failed to read plan history: %w", err)
	}

	if len(entries) == 0 {
		return fmt.Errorf("no entries provided\nProvide entries via --file or stdin as JSONL")
	}

	// extract plan content from entries
	planContent := extractPlanFromEntries(entries)
	if planContent == "" {
		// fallback: use last assistant message as plan
		planContent = extractLastAssistantMessage(entries)
	}

	// extract mermaid diagrams from all entries
	diagrams := extractDiagramsFromPlanEntries(entries)

	// create store and session folder
	store, _, err := newSessionStore()
	if err != nil {
		return fmt.Errorf("failed to access session store: %w", err)
	}

	planHistoryRoot, _ := findProjectRoot()
	planHistoryEndpoint := endpoint.GetForProject(planHistoryRoot)
	username := getAuthenticatedUsername(planHistoryEndpoint)
	if username == "" {
		username = "anonymous"
	}

	sessionName := session.GenerateSessionName(inst.AgentID, username)
	sessionPath := filepath.Join(store.BasePath(), sessionName)

	// create session directory
	if err := os.MkdirAll(sessionPath, 0755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}

	// write raw.jsonl with planning entries
	rawPath := filepath.Join(sessionPath, "raw.jsonl")
	if err := writePlanHistoryRaw(rawPath, entries, meta, inst.AgentID); err != nil {
		return fmt.Errorf("write raw session: %w", err)
	}

	// write plan.md if we found plan content
	var planPath string
	if planContent != "" {
		planPath = filepath.Join(sessionPath, "plan.md")
		if err := os.WriteFile(planPath, []byte(planContent), 0644); err != nil {
			return fmt.Errorf("write plan file: %w", err)
		}
	}

	// write session metadata with planning session type
	metaPath := filepath.Join(sessionPath, "session.json")
	sessionMeta := map[string]any{
		"session_type": "planning",
		"agent_id":     inst.AgentID,
		"created_at":   time.Now().Format(time.RFC3339),
		"title":        title,
		"entry_count":  len(entries),
	}
	if len(diagrams) > 0 {
		sessionMeta["diagram_count"] = len(diagrams)
	}
	metaJSON, _ := json.MarshalIndent(sessionMeta, "", "  ")
	if err := os.WriteFile(metaPath, metaJSON, 0644); err != nil {
		// non-fatal, continue
		fmt.Fprintf(os.Stderr, "warning: could not write session metadata: %v\n", err)
	}

	// prepare plan extract for output (first 200 chars)
	planExtract := ""
	if planContent != "" {
		planExtract = truncateString(planContent, 200)
	}

	// output format selection
	output := planHistoryOutput{
		Success:      true,
		Type:         "session_plan_history",
		AgentID:      inst.AgentID,
		SessionID:    sessionName,
		RawPath:      rawPath,
		PlanPath:     planPath,
		SessionPath:  sessionPath,
		EntryCount:   len(entries),
		DiagramCount: len(diagrams),
		Diagrams:     diagrams,
		PlanExtract:  planExtract,
	}

	if cfg.Review {
		// security audit mode: human summary + JSON
		if title != "" {
			cli.PrintSuccess(fmt.Sprintf("Planning session captured: %q", title))
		} else {
			cli.PrintSuccess("Planning session captured")
		}
		fmt.Printf("  Session: %s\n", sessionName)
		fmt.Printf("  Entries: %d\n", len(entries))
		fmt.Printf("  Diagrams: %d\n", len(diagrams))
		if planPath != "" {
			fmt.Printf("  Plan: %s\n", planPath)
		}
		fmt.Printf("  Raw: %s\n", rawPath)
		fmt.Println()
		fmt.Println("--- Machine Output ---")
		jsonOut, _ := json.MarshalIndent(output, "", "  ")
		fmt.Println(string(jsonOut))
		return nil
	}

	if cfg.Text {
		// human-readable text output
		if title != "" {
			cli.PrintSuccess(fmt.Sprintf("Planning session captured: %q", title))
		} else {
			cli.PrintSuccess("Planning session captured")
		}
		fmt.Printf("  Session: %s\n", sessionName)
		fmt.Printf("  Entries: %d\n", len(entries))
		fmt.Printf("  Diagrams: %d\n", len(diagrams))
		if planPath != "" {
			fmt.Printf("  Plan: %s\n", planPath)
		}
		fmt.Printf("  Raw: %s\n", rawPath)
		return nil
	}

	// default: JSON output
	jsonOut, _ := json.MarshalIndent(output, "", "  ")
	fmt.Println(string(jsonOut))
	return nil
}

// parsePlanHistoryFile extracts --file value from args.
func parsePlanHistoryFile(args []string) string {
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

// readPlanHistoryEntries reads JSONL entries from file or stdin.
func readPlanHistoryEntries(filePath string) ([]planHistoryEntry, *planHistoryMeta, error) {
	var reader *bufio.Scanner

	if filePath != "" {
		f, err := os.Open(filePath)
		if err != nil {
			return nil, nil, fmt.Errorf("open file: %w", err)
		}
		defer f.Close()
		reader = bufio.NewScanner(f)
	} else {
		// check if stdin has data
		stat, err := os.Stdin.Stat()
		if err != nil {
			return nil, nil, fmt.Errorf("check stdin: %w", err)
		}
		if (stat.Mode() & os.ModeCharDevice) != 0 {
			return nil, nil, fmt.Errorf("no input piped to stdin and no --file specified")
		}
		reader = bufio.NewScanner(os.Stdin)
	}

	// increase buffer for large entries
	reader.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var entries []planHistoryEntry
	var meta *planHistoryMeta

	for reader.Scan() {
		line := reader.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		// try to parse as metadata line first
		var metaLine struct {
			Meta *planHistoryMeta `json:"_meta"`
		}
		if err := json.Unmarshal([]byte(line), &metaLine); err == nil && metaLine.Meta != nil {
			meta = metaLine.Meta
			continue
		}

		// parse as entry
		var entry planHistoryEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			// skip invalid lines
			continue
		}

		// validate entry has required fields
		if entry.Type == "" || entry.Content == "" {
			continue
		}

		entries = append(entries, entry)
	}

	if err := reader.Err(); err != nil {
		return nil, nil, fmt.Errorf("read input: %w", err)
	}

	return entries, meta, nil
}

// extractPlanFromEntries scans entries for plan content.
// Priority:
//  1. Entry with is_plan: true
//  2. Entry with ## Plan or ## Final Plan header
func extractPlanFromEntries(entries []planHistoryEntry) string {
	// first pass: look for explicit is_plan marker
	for _, entry := range entries {
		if entry.IsPlan && entry.Content != "" {
			return entry.Content
		}
	}

	// second pass: look for plan headers in content (reverse to get last one)
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if entry.Type != "assistant" {
			continue
		}

		for _, pattern := range planHeaderPatterns {
			if pattern.MatchString(entry.Content) {
				// found a plan section - extract from header to end or next major section
				return extractPlanSection(entry.Content, pattern)
			}
		}
	}

	return ""
}

// extractPlanSection extracts the plan section starting from the matched header.
func extractPlanSection(content string, pattern *regexp.Regexp) string {
	loc := pattern.FindStringIndex(content)
	if loc == nil {
		return content
	}

	// extract from the header position onwards
	section := content[loc[0]:]

	// optionally trim at the next major section (# or ##) that isn't part of the plan
	// for now, return the whole section from the plan header
	return strings.TrimSpace(section)
}

// extractLastAssistantMessage returns the last assistant message as fallback plan.
func extractLastAssistantMessage(entries []planHistoryEntry) string {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type == "assistant" && entries[i].Content != "" {
			return entries[i].Content
		}
	}
	return ""
}

// extractDiagramsFromPlanEntries scans all entries for mermaid diagrams.
func extractDiagramsFromPlanEntries(entries []planHistoryEntry) []string {
	seen := make(map[string]bool)
	var diagrams []string

	for _, entry := range entries {
		for _, d := range session.ExtractMermaidBlocks(entry.Content) {
			if !seen[d] {
				seen[d] = true
				diagrams = append(diagrams, d)
			}
		}
	}

	return diagrams
}

// writePlanHistoryRaw writes entries to raw.jsonl with proper header.
func writePlanHistoryRaw(path string, entries []planHistoryEntry, meta *planHistoryMeta, agentID string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	encoder := json.NewEncoder(f)

	// write header
	header := map[string]any{
		"type": "header",
		"metadata": map[string]any{
			"version":      "1.0",
			"created_at":   time.Now().Format(time.RFC3339),
			"agent_id":     agentID,
			"session_type": "planning",
		},
	}
	if meta != nil {
		if meta.AgentType != "" {
			header["metadata"].(map[string]any)["agent_type"] = meta.AgentType
		}
		if meta.StartedAt != "" {
			header["metadata"].(map[string]any)["started_at"] = meta.StartedAt
		}
	}
	if err := encoder.Encode(header); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	// write entries
	for i, entry := range entries {
		ts := time.Now()
		if entry.Timestamp != "" {
			if parsed, err := time.Parse(time.RFC3339, entry.Timestamp); err == nil {
				ts = parsed
			}
		}

		data := map[string]any{
			"type":      entry.Type,
			"content":   entry.Content,
			"timestamp": ts.Format(time.RFC3339),
			"seq":       i,
		}
		if entry.Source != "" {
			data["source"] = entry.Source
		}
		if entry.IsPlan {
			data["is_plan"] = true
		}
		if entry.ToolName != "" {
			data["tool_name"] = entry.ToolName
		}
		if entry.ToolInput != "" {
			data["tool_input"] = entry.ToolInput
		}

		if err := encoder.Encode(data); err != nil {
			return fmt.Errorf("write entry %d: %w", i, err)
		}
	}

	// write footer
	footer := map[string]any{
		"type":        "footer",
		"closed_at":   time.Now().Format(time.RFC3339),
		"entry_count": len(entries),
	}
	if err := encoder.Encode(footer); err != nil {
		return fmt.Errorf("write footer: %w", err)
	}

	return f.Sync()
}

// truncateString truncates a string to maxLen chars, adding ellipsis if truncated.
func truncateString(s string, maxLen int) string {
	// remove newlines for extract
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)

	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
