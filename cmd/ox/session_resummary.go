package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

var sessionResummaryCmd = &cobra.Command{
	Use:    "resummary <raw.jsonl>",
	Short:  "Generate prompt for re-summarizing a session",
	Hidden: true, // hidden command for agent use
	Long: `Generate a prompt that can be used to re-summarize a session.

This command reads a raw.jsonl file and outputs a prompt suitable for
pasting into an AI agent to regenerate the summary.json file.

The prompt instructs the agent to analyze the session and produce
a structured summary including:
- Title
- Summary paragraph
- Key actions
- Outcome
- Topics found
- Aha moments (pivotal insights)
- Mermaid diagrams

Examples:
  ox session resummary /path/to/raw.jsonl
  ox session resummary /path/to/raw.jsonl | pbcopy  # copy to clipboard`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionResummary,
}

func init() {
	sessionCmd.AddCommand(sessionResummaryCmd)
	sessionResummaryCmd.Flags().Bool("compact", false, "output compact session (less context)")
}

func runSessionResummary(cmd *cobra.Command, args []string) error {
	inputPath := args[0]
	compact, _ := cmd.Flags().GetBool("compact")

	// read and parse the raw.jsonl file
	file, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	var entries []map[string]any
	scanner := bufio.NewScanner(file)
	// increase buffer for large lines
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue // skip malformed lines
		}
		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	// build the prompt
	prompt := buildResummaryPrompt(entries, inputPath, compact)

	// output the prompt
	fmt.Println(prompt)

	// show instructions
	outputPath := filepath.Join(filepath.Dir(inputPath), "summary.json")
	cli.PrintHint(fmt.Sprintf("Paste this prompt into an agent to regenerate %s", outputPath))

	return nil
}

func buildResummaryPrompt(entries []map[string]any, inputPath string, compact bool) string {
	var sb strings.Builder

	sb.WriteString("# Re-summarize Session\n\n")
	sb.WriteString("Analyze the following session and generate a summary.json file.\n\n")

	// use shared prompt guidelines
	sb.WriteString(session.SummaryPromptGuidelines)
	sb.WriteString("\n")

	sb.WriteString("## Session to Analyze\n\n")
	fmt.Fprintf(&sb, "File: `%s`\n\n", inputPath)

	// output the session content
	sb.WriteString("```\n")
	for i, entry := range entries {
		seq := i + 1

		// skip header/footer entries
		if entryType, ok := entry["type"].(string); ok {
			if entryType == "header" || entryType == "footer" {
				continue
			}
		}
		if _, ok := entry["_meta"]; ok {
			continue
		}

		// filter out read-only tool noise
		if isResummaryNoiseEntry(entry) {
			continue
		}

		// extract message info
		msgType := getEntryType(entry)
		content := getEntryContent(entry)

		if compact && len(content) > 500 {
			content = content[:500] + "..."
		}

		if content != "" {
			fmt.Fprintf(&sb, "[%d] %s: %s\n\n", seq, msgType, content)
		}
	}
	sb.WriteString("```\n\n")

	sb.WriteString("## Instructions\n\n")
	sb.WriteString("1. Read through the entire session\n")
	sb.WriteString("2. Identify the main goal and what was accomplished\n")
	sb.WriteString("3. Find the pivotal aha moments (questions, insights, decisions)\n")
	sb.WriteString("4. Generate the summary.json with all required fields\n")
	sb.WriteString("5. Save to: `" + filepath.Join(filepath.Dir(inputPath), "summary.json") + "`\n")

	return sb.String()
}

func getEntryType(entry map[string]any) string {
	// check type at root level
	if t, ok := entry["type"].(string); ok {
		switch t {
		case "user":
			return "USER"
		case "assistant":
			return "ASSISTANT"
		case "tool":
			if name, ok := entry["tool_name"].(string); ok {
				return fmt.Sprintf("TOOL[%s]", name)
			}
			return "TOOL"
		case "message":
			// check nested role
			if data, ok := entry["data"].(map[string]any); ok {
				if role, ok := data["role"].(string); ok {
					return strings.ToUpper(role)
				}
			}
			return "MESSAGE"
		}
		return strings.ToUpper(t)
	}
	return "UNKNOWN"
}

func getEntryContent(entry map[string]any) string {
	// check content at root level
	if content, ok := entry["content"].(string); ok && content != "" {
		return content
	}

	// check for tool output/input
	if output, ok := entry["tool_output"].(string); ok && output != "" {
		return fmt.Sprintf("Output: %s", truncateForPrompt(output, 200))
	}
	if input, ok := entry["tool_input"].(string); ok && input != "" {
		return fmt.Sprintf("Input: %s", truncateForPrompt(input, 200))
	}

	// check nested data.content
	if data, ok := entry["data"].(map[string]any); ok {
		if content, ok := data["content"].(string); ok && content != "" {
			return content
		}
		if output, ok := data["output"].(string); ok && output != "" {
			return fmt.Sprintf("Output: %s", truncateForPrompt(output, 200))
		}
		if input, ok := data["input"].(string); ok && input != "" {
			return fmt.Sprintf("Input: %s", truncateForPrompt(input, 200))
		}
	}

	return ""
}

func truncateForPrompt(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// isResummaryNoiseEntry returns true for tool entries that are read-only
// exploration (read, glob, grep) and should be filtered from resummary prompts.
func isResummaryNoiseEntry(entry map[string]any) bool {
	entryType, _ := entry["type"].(string)
	if entryType != "tool" {
		return false
	}

	toolName, _ := entry["tool_name"].(string)
	toolLower := strings.ToLower(toolName)

	// read-only tools are noise unless they failed
	readOnlyTools := map[string]bool{
		"read": true, "glob": true, "grep": true,
		"webfetch": true, "websearch": true,
	}
	if readOnlyTools[toolLower] {
		// keep if output indicates an error
		output, _ := entry["tool_output"].(string)
		if output == "" {
			output, _ = entry["content"].(string)
		}
		if session.IsNoiseCommand(output) {
			return true
		}
		// simple heuristic: if no error keywords, it's noise
		outputLower := strings.ToLower(output)
		for _, errWord := range []string{"error:", "failed", "fatal:", "panic:", "not found"} {
			if strings.Contains(outputLower, errWord) {
				return false // keep errors
			}
		}
		return true
	}

	// noise bash commands
	if toolLower == "bash" || toolLower == "execute" {
		input, _ := entry["tool_input"].(string)
		if session.IsNoiseCommand(input) {
			return true
		}
	}

	return false
}
