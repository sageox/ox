// Package session provides storage and management for agent session recordings.
// This file implements markdown generation from stored sessions.
package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// threshold for collapsible content (number of lines)
	collapsibleThreshold = 10

	// maximum length for inline content before truncation
	maxInlineContentLen = 500
)

// MarkdownGenerator creates markdown sessions from stored sessions.
type MarkdownGenerator struct {
	summary *SummarizeResponse
}

// NewMarkdownGenerator creates a new markdown generator.
func NewMarkdownGenerator() *MarkdownGenerator {
	return &MarkdownGenerator{}
}

// loadSummary attempts to load summary.json from the session directory.
func (g *MarkdownGenerator) loadSummary(sessionPath string) {
	summaryPath := filepath.Join(filepath.Dir(sessionPath), "summary.json")
	data, err := os.ReadFile(summaryPath)
	if err != nil {
		return
	}
	var summary SummarizeResponse
	if json.Unmarshal(data, &summary) == nil {
		g.summary = &summary
	}
}

// Generate creates markdown bytes from a StoredSession.
func (g *MarkdownGenerator) Generate(t *StoredSession) ([]byte, error) {
	if t == nil {
		return nil, fmt.Errorf("session cannot be nil")
	}

	// try to load summary.json for aha moments
	if t.Info.FilePath != "" {
		g.loadSummary(t.Info.FilePath)
	}

	var buf bytes.Buffer

	// write header
	g.writeHeader(&buf, t)

	// write key moments section if available
	g.writeKeyMoments(&buf)

	// write entries
	g.writeEntries(&buf, t.Entries, t.Meta)

	// write footer
	g.writeFooter(&buf, t)

	return buf.Bytes(), nil
}

// GenerateToFile writes the markdown session to a file.
// TODO(server-side): move to server-side for MVP+1; client should not write to ledger directly.
func (g *MarkdownGenerator) GenerateToFile(t *StoredSession, outputPath string) error {
	mdBytes, err := g.Generate(t)
	if err != nil {
		return err
	}

	if err := os.WriteFile(outputPath, mdBytes, 0644); err != nil {
		return fmt.Errorf("write markdown file=%s: %w", outputPath, err)
	}

	return nil
}

// writeHeader writes the metadata header section.
func (g *MarkdownGenerator) writeHeader(buf *bytes.Buffer, t *StoredSession) {
	buf.WriteString("# Agent Session\n\n")

	// metadata table
	buf.WriteString("## Session Information\n\n")
	buf.WriteString("| Field | Value |\n")
	buf.WriteString("|-------|-------|\n")

	// session ID from StoreMeta
	if t.Meta != nil {
		if t.Meta.AgentID != "" {
			fmt.Fprintf(buf, "| **Session ID** | `%s` |\n", t.Meta.AgentID)
		}
		if t.Meta.AgentType != "" {
			fmt.Fprintf(buf, "| **Agent** | %s |\n", t.Meta.AgentType)
		}
		if t.Meta.AgentVersion != "" {
			fmt.Fprintf(buf, "| **Agent Version** | %s |\n", t.Meta.AgentVersion)
		}
		if t.Meta.Model != "" {
			fmt.Fprintf(buf, "| **Model** | %s |\n", t.Meta.Model)
		}
		if t.Meta.Username != "" {
			fmt.Fprintf(buf, "| **User** | %s |\n", t.Meta.Username)
		}
		if !t.Meta.CreatedAt.IsZero() {
			fmt.Fprintf(buf, "| **Started** | %s |\n", t.Meta.CreatedAt.Format(time.RFC3339))
		}
	}

	// file info
	if t.Info.Filename != "" {
		fmt.Fprintf(buf, "| **File** | `%s` |\n", t.Info.Filename)
	}
	if t.Info.Type != "" {
		fmt.Fprintf(buf, "| **Type** | %s |\n", t.Info.Type)
	}

	fmt.Fprintf(buf, "| **Entry Count** | %d |\n", len(t.Entries))

	buf.WriteString("\n---\n\n")
	buf.WriteString("## Conversation\n\n")
}

// writeKeyMoments writes the Key Moments section with anchor links.
func (g *MarkdownGenerator) writeKeyMoments(buf *bytes.Buffer) {
	if g.summary == nil || len(g.summary.AhaMoments) == 0 {
		return
	}

	buf.WriteString("## Key Moments ★\n\n")
	buf.WriteString("_Pivotal points of collaborative intelligence_\n\n")

	for i, am := range g.summary.AhaMoments {
		fmt.Fprintf(buf, "%d. **[%s by %s](#msg-%d)**: %q\n",
			i+1, am.Type, am.Role, am.Seq, am.Highlight)
		fmt.Fprintf(buf, "   - _Why:_ %s\n\n", am.Why)
	}

	buf.WriteString("---\n\n")
}

// isAhaMoment checks if an entry is an aha moment.
func (g *MarkdownGenerator) isAhaMoment(seq int) (int, bool) {
	if g.summary == nil {
		return 0, false
	}
	for i, am := range g.summary.AhaMoments {
		if am.Seq == seq {
			return i + 1, true
		}
	}
	return 0, false
}

// writeEntries writes all session entries.
func (g *MarkdownGenerator) writeEntries(buf *bytes.Buffer, entries []map[string]any, meta *StoreMeta) {
	for i, entry := range entries {
		g.writeEntry(buf, i, entry, meta)
	}
}

// writeEntry writes a single entry based on its type.
func (g *MarkdownGenerator) writeEntry(buf *bytes.Buffer, index int, entry map[string]any, meta *StoreMeta) {
	entryType := ExtractEntryType(entry)
	seq := index + 1

	// determine username - use metadata if available
	username := "User"
	if meta != nil && meta.Username != "" {
		username = meta.Username
	}

	// determine agent name - use metadata if available
	agentName := "Assistant"
	if meta != nil && meta.AgentType != "" {
		// format agent name nicely (e.g., "claude-code" -> "Claude Code")
		agentName = formatAgentName(meta.AgentType)
	}

	// entry header with role indicator
	var role string
	switch entryType {
	case "user", "human":
		role = fmt.Sprintf("**%s**", username)
	case "assistant", "ai", "model":
		role = fmt.Sprintf("**%s**", agentName)
	case "system":
		role = "**System**"
	case "tool", "tool_use", "tool_call":
		role = "**Tool Call**"
	case "tool_result", "tool_output":
		role = "**Tool Result**"
	default:
		role = fmt.Sprintf("**%s**", entryType)
	}

	// check if this is an aha moment
	ahaNum, isAha := g.isAhaMoment(seq)
	ahaIndicator := ""
	if isAha {
		ahaIndicator = fmt.Sprintf(" ★ Key Moment #%d", ahaNum)
	}

	// write entry header with anchor ID for navigation
	fmt.Fprintf(buf, "<a id=\"msg-%d\"></a>\n\n", seq)
	fmt.Fprintf(buf, "### %s%s\n\n", role, ahaIndicator)

	// write content based on type
	switch entryType {
	case "tool", "tool_use", "tool_call":
		g.writeToolCall(buf, entry)
	case "tool_result", "tool_output":
		g.writeToolResult(buf, entry)
	default:
		g.writeMessageContent(buf, entry)
	}

	buf.WriteString("\n")
}

// writeMessageContent writes user/assistant/system message content.
func (g *MarkdownGenerator) writeMessageContent(buf *bytes.Buffer, entry map[string]any) {
	content := ExtractContent(entry)
	if content == "" {
		buf.WriteString("_No content_\n")
		return
	}

	// render mermaid diagrams to ASCII if present
	if HasMermaidBlocks(content) {
		content = ProcessMermaidBlocks(content)
	}

	// check if content is long enough to warrant collapsing
	lines := strings.Count(content, "\n") + 1
	if lines > collapsibleThreshold || len(content) > maxInlineContentLen*2 {
		preview := mdGetPreview(content, 200)
		fmt.Fprintf(buf, "%s...\n\n", preview)
		buf.WriteString("<details>\n")
		buf.WriteString("<summary>Show full message</summary>\n\n")
		buf.WriteString("```\n")
		buf.WriteString(content)
		buf.WriteString("\n```\n\n")
		buf.WriteString("</details>\n")
	} else {
		buf.WriteString(content)
		buf.WriteString("\n")
	}
}

// writeToolCall writes tool call details in compact command format.
func (g *MarkdownGenerator) writeToolCall(buf *bytes.Buffer, entry map[string]any) {
	toolName := mdExtractToolName(entry)
	toolInput := mdExtractToolInput(entry)

	cmd := mdFormatToolCompact(toolName, toolInput)
	fmt.Fprintf(buf, "`>_ %s`\n", cmd)
}

// mdFormatToolCompact renders a tool call as a compact command string.
func mdFormatToolCompact(name, input string) string {
	if name == "" {
		return "Tool"
	}

	var data map[string]any
	hasJSON := input != "" && json.Unmarshal([]byte(input), &data) == nil

	switch strings.ToLower(name) {
	case "bash":
		if hasJSON {
			if command, ok := data["command"].(string); ok {
				return fmt.Sprintf("Bash  %s", command)
			}
		}
	case "read":
		if hasJSON {
			if fp, ok := data["file_path"].(string); ok {
				return fmt.Sprintf("Read  %s", mdShortenPath(fp))
			}
		}
	case "edit", "multiedit":
		if hasJSON {
			if fp, ok := data["file_path"].(string); ok {
				return fmt.Sprintf("%s  %s", name, mdShortenPath(fp))
			}
		}
	case "write":
		if hasJSON {
			if fp, ok := data["file_path"].(string); ok {
				return fmt.Sprintf("Write  %s", mdShortenPath(fp))
			}
		}
	case "grep":
		if hasJSON {
			pattern, _ := data["pattern"].(string)
			path, _ := data["path"].(string)
			parts := name
			if pattern != "" {
				parts += fmt.Sprintf(" \"%s\"", pattern)
			}
			if path != "" {
				parts += fmt.Sprintf(" %s", mdShortenPath(path))
			}
			return parts
		}
	case "glob":
		if hasJSON {
			if pattern, ok := data["pattern"].(string); ok {
				return fmt.Sprintf("Glob  %s", pattern)
			}
		}
	}

	// fallback: tool name + first string value
	if hasJSON {
		for _, v := range data {
			if s, ok := v.(string); ok && s != "" {
				if len(s) > 80 {
					s = s[:80] + "..."
				}
				return fmt.Sprintf("%s  %s", name, s)
			}
		}
	}

	return name
}

// mdShortenPath strips home directory prefixes for readability.
func mdShortenPath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		if (part == "Users" || part == "home") && i+2 < len(parts) {
			return strings.Join(parts[i+2:], "/")
		}
	}
	return p
}

// writeToolResult writes tool result/output.
func (g *MarkdownGenerator) writeToolResult(buf *bytes.Buffer, entry map[string]any) {
	toolName := mdExtractToolName(entry)
	toolOutput := mdExtractToolOutput(entry)

	if toolName != "" {
		fmt.Fprintf(buf, "**Tool:** `%s`\n\n", toolName)
	}

	if toolOutput != "" {
		lines := strings.Count(toolOutput, "\n") + 1
		if lines > collapsibleThreshold || len(toolOutput) > maxInlineContentLen {
			// show brief preview
			preview := mdGetPreview(toolOutput, 100)
			fmt.Fprintf(buf, "_Output preview:_ `%s`\n\n", preview)

			buf.WriteString("<details>\n")
			buf.WriteString("<summary>Full output</summary>\n\n")
			buf.WriteString("```\n")
			buf.WriteString(toolOutput)
			buf.WriteString("\n```\n\n")
			buf.WriteString("</details>\n")
		} else {
			buf.WriteString("**Output:**\n")
			buf.WriteString("```\n")
			buf.WriteString(toolOutput)
			buf.WriteString("\n```\n")
		}
	} else {
		// fallback to content field
		content := ExtractContent(entry)
		if content != "" {
			g.writeMessageContent(buf, entry)
		} else {
			buf.WriteString("_No output_\n")
		}
	}
}

// writeFooter writes the closing section.
func (g *MarkdownGenerator) writeFooter(buf *bytes.Buffer, t *StoredSession) {
	buf.WriteString("\n---\n\n")

	// footer metadata if available
	if t.Footer != nil {
		if closedAt, ok := t.Footer["closed_at"].(string); ok {
			if endTime, err := time.Parse(time.RFC3339Nano, closedAt); err == nil {
				fmt.Fprintf(buf, "_Session closed: %s_\n", endTime.Format(time.RFC3339))
			}
		}
	}

	buf.WriteString("\n---\n")
}




// mdExtractToolName gets the tool name from an entry.
func mdExtractToolName(entry map[string]any) string {
	if name, ok := entry["tool_name"].(string); ok {
		return name
	}

	if data, ok := entry["data"].(map[string]any); ok {
		if name, ok := data["tool_name"].(string); ok {
			return name
		}
		if name, ok := data["name"].(string); ok {
			return name
		}
	}

	return ""
}

// mdExtractToolInput gets tool input from an entry.
func mdExtractToolInput(entry map[string]any) string {
	return mdExtractToolField(entry, "tool_input", "input", "parameters")
}

// mdExtractToolOutput gets tool output from an entry.
func mdExtractToolOutput(entry map[string]any) string {
	return mdExtractToolField(entry, "tool_output", "output", "result")
}

// mdExtractToolField tries multiple field names to find a tool field value.
func mdExtractToolField(entry map[string]any, fieldNames ...string) string {
	// try direct fields first
	for _, name := range fieldNames {
		if val, ok := entry[name]; ok {
			return mdFormatFieldValue(val)
		}
	}

	// try nested data fields
	if data, ok := entry["data"].(map[string]any); ok {
		for _, name := range fieldNames {
			if val, ok := data[name]; ok {
				return mdFormatFieldValue(val)
			}
		}
	}

	return ""
}

// mdFormatFieldValue converts a value to a displayable string.
func mdFormatFieldValue(val any) string {
	switch v := val.(type) {
	case string:
		return v
	case map[string]any, []any:
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// mdExtractEndTime gets the session end time from footer or file info.
func mdExtractEndTime(t *StoredSession) time.Time {
	if t.Footer != nil {
		if closedAt, ok := t.Footer["closed_at"].(string); ok {
			if endTime, err := time.Parse(time.RFC3339Nano, closedAt); err == nil {
				return endTime
			}
		}
	}

	// fall back to file mod time
	if !t.Info.ModTime.IsZero() {
		return t.Info.ModTime
	}

	return time.Time{}
}

// mdCountEntryTypes counts entries by their type.
func mdCountEntryTypes(entries []map[string]any) map[string]int {
	counts := make(map[string]int)
	for _, entry := range entries {
		entryType := ExtractEntryType(entry)
		// normalize similar types
		switch entryType {
		case "user", "human":
			counts["user"]++
		case "assistant", "ai", "model":
			counts["assistant"]++
		case "system":
			counts["system"]++
		case "tool", "tool_use", "tool_call":
			counts["tool_call"]++
		case "tool_result", "tool_output":
			counts["tool_result"]++
		default:
			counts[entryType]++
		}
	}
	return counts
}

// formatAgentName converts agent type to display name (e.g., "claude-code" -> "Claude Code").
func formatAgentName(agentType string) string {
	// handle common agent types
	switch strings.ToLower(agentType) {
	case "claude-code", "claudecode":
		return "Claude Code"
	case "claude", "anthropic":
		return "Claude"
	case "cursor":
		return "Cursor"
	case "copilot", "github-copilot":
		return "GitHub Copilot"
	default:
		// convert kebab-case or snake_case to Title Case
		name := strings.ReplaceAll(agentType, "-", " ")
		name = strings.ReplaceAll(name, "_", " ")
		return strings.Title(name) //nolint:staticcheck // Title is fine for display names
	}
}

// mdGetPreview returns a truncated preview of content.
func mdGetPreview(content string, maxLen int) string {
	// normalize whitespace
	preview := strings.Join(strings.Fields(content), " ")
	if len(preview) <= maxLen {
		return preview
	}

	// truncate at word boundary
	truncated := preview[:maxLen]
	if lastSpace := strings.LastIndex(truncated, " "); lastSpace > maxLen/2 {
		truncated = truncated[:lastSpace]
	}

	return truncated
}
