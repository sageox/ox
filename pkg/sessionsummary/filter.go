package sessionsummary

import "strings"

// readOnlyTools are tools that only read/search without modifying state.
// Successful calls to these are low-value noise for summarization.
var readOnlyTools = map[string]bool{
	"read":      true,
	"Read":      true,
	"glob":      true,
	"Glob":      true,
	"grep":      true,
	"Grep":      true,
	"WebFetch":  true,
	"WebSearch": true,
	"webfetch":  true,
	"websearch": true,
}

// noiseCommands are bash commands that are always filtered out (unless they fail).
var noiseCommands = []string{
	"ls", "pwd", "cd", "clear",
	"cat", "head", "tail", "less", "more",
	"echo", "printf",
	"which", "whereis", "type",
	"env", "export",
}

// FilterForSummarization removes low-value tool entries from session data
// before sending to the LLM summarizer. Keeps raw.jsonl complete for
// auditability while reducing noise in the summarization input.
//
// Filtered out (when successful):
//   - Read-only tools: Read, Glob, Grep, WebFetch, WebSearch
//   - Bash commands that are noise: ls, pwd, cat, head, tail, etc.
//
// Always kept:
//   - All user and assistant messages (the human-AI dialog)
//   - System messages
//   - Write/Edit tool calls (actual changes)
//   - Failed tool calls (important for understanding debugging)
//   - Bash commands that modify state
func FilterForSummarization(entries []Entry) []Entry {
	if len(entries) == 0 {
		return entries
	}

	filtered := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.Type != EntryTypeTool {
			filtered = append(filtered, e)
			continue
		}

		// keep tool entries that errored (useful for understanding debugging)
		if hasToolError(e) {
			filtered = append(filtered, e)
			continue
		}

		// filter read-only tools
		if readOnlyTools[e.ToolName] {
			continue
		}

		// filter noise bash/Bash commands
		toolLower := strings.ToLower(e.ToolName)
		if toolLower == "bash" || toolLower == "execute" {
			if IsNoiseCommand(e.ToolInput) {
				continue
			}
		}

		filtered = append(filtered, e)
	}

	return filtered
}

// IsNoiseCommand checks if a command is noise (low-value unless it fails).
func IsNoiseCommand(cmd string) bool {
	cmdLower := strings.ToLower(strings.TrimSpace(cmd))
	for _, pattern := range noiseCommands {
		if strings.HasPrefix(cmdLower, pattern+" ") || cmdLower == pattern {
			return true
		}
	}
	return false
}

// hasToolError checks if a tool entry indicates a failure.
func hasToolError(e Entry) bool {
	output := e.ToolOutput
	if output == "" {
		output = e.Content
	}
	if output == "" {
		return false
	}
	return detectError(output)
}

// detectError checks content for error indicators.
func detectError(content string) bool {
	contentLower := strings.ToLower(content)
	for _, pattern := range []string{
		"error:", "failed", "fatal:", "panic:", "exception",
	} {
		if strings.Contains(contentLower, pattern) {
			return true
		}
	}
	if strings.Contains(contentLower, "exit code") && !strings.Contains(contentLower, "exit code 0") {
		return true
	}
	return false
}
