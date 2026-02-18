// Package html provides HTML session viewer generation.
// It converts stored sessions into self-contained HTML files
// for viewing agent conversation history in a browser.
package html

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	gmhtml "github.com/yuin/goldmark/renderer/html"
)

// md is a shared goldmark instance for converting message content to HTML.
// Raw HTML in markdown is stripped by default for XSS safety.
// HardWraps converts single newlines to <br> for plain-text readability.
var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(gmhtml.WithHardWraps()),
)

// ansiEscapeRe matches ANSI escape sequences (CSI sequences like colors/bold,
// OSC sequences, and simple two-byte escapes). These leak into session content
// from CLI tool output (e.g., ox status with colored output).
var ansiEscapeRe = regexp.MustCompile(`\x1b(?:\[[0-9;]*[a-zA-Z]|\][^\x07]*\x07|[\(\)][AB012])`)

// StripANSI removes ANSI escape sequences from text.
func StripANSI(s string) string {
	return ansiEscapeRe.ReplaceAllString(s, "")
}

// regex patterns for stripping internal tags from message content
var (
	reCommandMessage    = regexp.MustCompile(`(?s)<command-message>.*?</command-message>`)
	reCommandName       = regexp.MustCompile(`<command-name>(.*?)</command-name>`)
	reSystemReminder    = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)
	reSystemInstruction  = regexp.MustCompile(`(?s)<system_instruction>.*?</system_instruction>`)
	reSystemInstructHyp  = regexp.MustCompile(`(?s)<system-instruction>.*?</system-instruction>`)
	reLocalCommandStdout = regexp.MustCompile(`(?s)<local-command-stdout>.*?</local-command-stdout>`)
	reLocalCommandCaveat = regexp.MustCompile(`(?s)<local-command-caveat>.*?</local-command-caveat>`)
)

// Generator creates HTML session viewers from stored sessions.
type Generator struct {
	tmpl *template.Template
}

// NewGenerator creates a generator with embedded templates.
// The template is parsed once and reused for multiple Generate calls.
func NewGenerator() (*Generator, error) {
	tmpl, err := template.New("session").Parse(templateHTML)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}

	return &Generator{tmpl: tmpl}, nil
}

// Generate creates HTML bytes from a StoredSession.
func (g *Generator) Generate(t *session.StoredSession) ([]byte, error) {
	if t == nil {
		return nil, fmt.Errorf("session cannot be nil")
	}

	data := convertToTemplateData(t)

	var buf bytes.Buffer
	if err := g.tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}

	return buf.Bytes(), nil
}

// GenerateToFile writes HTML to a file.
// TODO(server-side): move to server-side for MVP+1; client should not write to ledger directly.
func (g *Generator) GenerateToFile(t *session.StoredSession, outputPath string) error {
	htmlBytes, err := g.Generate(t)
	if err != nil {
		return err
	}

	if err := os.WriteFile(outputPath, htmlBytes, 0644); err != nil {
		return fmt.Errorf("write html file=%s: %w", outputPath, err)
	}

	return nil
}

// GenerateWithSummary creates HTML bytes from a StoredSession with a summary.
func (g *Generator) GenerateWithSummary(t *session.StoredSession, summary *SummaryView) ([]byte, error) {
	if t == nil {
		return nil, fmt.Errorf("session cannot be nil")
	}

	data := convertToTemplateData(t)
	data.Summary = summary

	var buf bytes.Buffer
	if err := g.tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}

	return buf.Bytes(), nil
}

// GenerateToFileWithSummary writes HTML to a file with a summary section.
// TODO(server-side): move to server-side for MVP+1; client should not write to ledger directly.
func (g *Generator) GenerateToFileWithSummary(t *session.StoredSession, summary *SummaryView, outputPath string) error {
	htmlBytes, err := g.GenerateWithSummary(t, summary)
	if err != nil {
		return err
	}

	if err := os.WriteFile(outputPath, htmlBytes, 0644); err != nil {
		return fmt.Errorf("write html file=%s: %w", outputPath, err)
	}

	return nil
}

// convertToTemplateData transforms a StoredSession to TemplateData.
func convertToTemplateData(t *session.StoredSession) *TemplateData {
	data := &TemplateData{
		Title:       generateTitle(t),
		BrandColors: DefaultBrandColors(),
		Styles:      template.CSS(stylesCSS),
		Scripts:     template.JS(viewerJS),
		Messages:    make([]MessageView, 0, len(t.Entries)),
	}

	// extract metadata
	data.Metadata = extractMetadata(t)

	// determine sender labels from metadata
	userLabel := "User"
	agentLabel := "Assistant"
	if t.Meta != nil {
		if t.Meta.Username != "" {
			userLabel = t.Meta.Username
		}
		if t.Meta.AgentType != "" {
			agentLabel = formatAgentName(t.Meta.AgentType)
		}
	}

	// convert entries to message views, skipping empty ones
	var userCount, toolCount int
	for i, entry := range t.Entries {
		msg := convertEntry(i, entry, userLabel, agentLabel)

		// skip entries with no meaningful content and no tool call
		if msg.Content == "" && msg.ToolCall == nil {
			continue
		}

		data.Messages = append(data.Messages, msg)

		// count message types
		switch msg.Type {
		case "user":
			userCount++
		case "tool", "tool_use", "tool_result":
			toolCount++
		}
	}

	// calculate statistics
	data.Statistics = &StatsView{
		TotalMessages: len(data.Messages),
		UserMessages:  userCount,
		ToolCalls:     toolCount,
		Duration:      data.Metadata.Duration,
	}

	return data
}

// generateTitle creates a title from the session info.
func generateTitle(t *session.StoredSession) string {
	if t.Meta != nil && t.Meta.AgentType != "" {
		return fmt.Sprintf("%s Session", t.Meta.AgentType)
	}
	if t.Info.Filename != "" {
		// strip extension and clean up filename
		name := strings.TrimSuffix(t.Info.Filename, ".jsonl")
		return name
	}
	return "Agent Session"
}

// extractMetadata pulls metadata from the stored session.
func extractMetadata(t *session.StoredSession) *MetadataView {
	meta := &MetadataView{}

	if t.Meta != nil {
		meta.AgentType = t.Meta.AgentType
		meta.AgentVersion = t.Meta.AgentVersion
		meta.Model = t.Meta.Model
		meta.Username = t.Meta.Username
		meta.StartedAt = t.Meta.CreatedAt
	}

	// try to get end time from footer
	if t.Footer != nil {
		if closedAt, ok := t.Footer["closed_at"].(string); ok {
			if endTime, err := time.Parse(time.RFC3339Nano, closedAt); err == nil {
				meta.EndedAt = endTime
			}
		}
	}

	// calculate duration if we have both times
	if !meta.StartedAt.IsZero() && !meta.EndedAt.IsZero() {
		meta.Duration = FormatDuration(meta.EndedAt.Sub(meta.StartedAt))
	} else if !meta.StartedAt.IsZero() {
		// use file mod time as fallback
		if !t.Info.ModTime.IsZero() {
			meta.Duration = FormatDuration(t.Info.ModTime.Sub(meta.StartedAt))
		}
	}

	return meta
}

// convertEntry transforms a raw entry map into a MessageView.
func convertEntry(index int, entry map[string]any, userLabel, agentLabel string) MessageView {
	msg := MessageView{
		ID:        index,
		Type:      "unknown",
		Timestamp: time.Now(),
	}

	// extract type
	if t, ok := entry["type"].(string); ok {
		msg.Type = normalizeMessageType(t)
	}

	// set sender label based on type
	switch msg.Type {
	case "user":
		msg.SenderLabel = userLabel
	case "assistant":
		msg.SenderLabel = agentLabel
	case "system":
		msg.SenderLabel = "System"
	case "tool":
		msg.SenderLabel = "Tool Call"
	case "tool_result":
		msg.SenderLabel = "Tool Result"
	default:
		msg.SenderLabel = msg.Type
	}

	// extract timestamp
	if ts, ok := entry["timestamp"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			msg.Timestamp = parsed
		}
	}

	// extract content, strip internal tags, and render markdown.
	// goldmark strips raw HTML by default, providing XSS safety.
	raw := cleanMessageContent(extractContent(entry))
	if strings.TrimSpace(raw) == "" {
		msg.Content = ""
	} else {
		msg.Content = RenderMarkdown(raw)
	}

	// extract tool call info if present
	msg.ToolCall = extractToolCall(entry)

	// populate tool call summary
	if msg.ToolCall != nil {
		msg.ToolCall.Summary = FormatToolSummary(msg.ToolCall)
	}

	return msg
}

// normalizeMessageType maps various type strings to display-friendly names.
func normalizeMessageType(t string) string {
	switch strings.ToLower(t) {
	case "user", "human":
		return "user"
	case "assistant", "ai", "model":
		return "assistant"
	case "system":
		return "system"
	case "tool", "tool_use", "tool_call":
		return "tool"
	case "tool_result", "tool_output":
		return "tool_result"
	default:
		return t
	}
}

// formatAgentName converts agent type to display name (e.g., "claude-code" -> "Claude Code").
func formatAgentName(agentType string) string {
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

// extractContent pulls the content from an entry in various formats.
func extractContent(entry map[string]any) string {
	// try direct content field
	if content, ok := entry["content"].(string); ok {
		return content
	}

	// try nested data.content
	if data, ok := entry["data"].(map[string]any); ok {
		if content, ok := data["content"].(string); ok {
			return content
		}
		// try data.message
		if message, ok := data["message"].(string); ok {
			return message
		}
	}

	// try message field
	if message, ok := entry["message"].(string); ok {
		return message
	}

	// try text field
	if text, ok := entry["text"].(string); ok {
		return text
	}

	return ""
}

// extractToolCall pulls tool call information if present.
func extractToolCall(entry map[string]any) *ToolCallView {
	// check if this is a tool-related entry
	entryType, _ := entry["type"].(string)
	if !strings.Contains(strings.ToLower(entryType), "tool") {
		return nil
	}

	tool := &ToolCallView{}

	// try tool_name field
	if name, ok := entry["tool_name"].(string); ok {
		tool.Name = name
	}

	// try nested data.tool_name
	if data, ok := entry["data"].(map[string]any); ok {
		if name, ok := data["tool_name"].(string); ok {
			tool.Name = name
		}
		if name, ok := data["name"].(string); ok && tool.Name == "" {
			tool.Name = name
		}
	}

	// extract input
	tool.Input = extractToolField(entry, "tool_input", "input", "parameters")

	// extract output
	tool.Output = extractToolField(entry, "tool_output", "output", "result")

	// only return if we have meaningful data
	if tool.Name == "" && tool.Input == "" && tool.Output == "" {
		return nil
	}

	return tool
}

// extractToolField tries multiple field names to find tool input/output.
func extractToolField(entry map[string]any, fieldNames ...string) string {
	// try direct fields first
	for _, name := range fieldNames {
		if val, ok := entry[name]; ok {
			return formatValue(val)
		}
	}

	// try nested data fields
	if data, ok := entry["data"].(map[string]any); ok {
		for _, name := range fieldNames {
			if val, ok := data[name]; ok {
				return formatValue(val)
			}
		}
	}

	return ""
}

// formatValue converts any value to a displayable string.
func formatValue(val any) string {
	switch v := val.(type) {
	case string:
		return v
	case map[string]any, []any:
		// pretty-print JSON for complex values
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// cleanMessageContent strips internal XML tags from message content.
// This is a backward-compat safety net for sessions recorded before the adapter-layer
// classification fix (see adapters/claude_code.go:classifyUserContent). New sessions
// have correct entry types from the adapter, but pre-fix raw.jsonl files may still
// contain these tags in user-attributed entries.
//
// Tags stripped:
//   - <command-message>...</command-message> blocks removed entirely
//   - <command-name>/foo</command-name> replaced with "/foo"
//   - <system-reminder>...</system-reminder> blocks removed entirely
//   - <system_instruction>...</system_instruction> blocks removed entirely
//   - <system-instruction>...</system-instruction> blocks removed entirely
//   - <local-command-stdout>...</local-command-stdout> blocks removed entirely
//   - <local-command-caveat>...</local-command-caveat> blocks removed entirely
func cleanMessageContent(text string) string {
	text = reCommandMessage.ReplaceAllString(text, "")
	text = reCommandName.ReplaceAllStringFunc(text, func(match string) string {
		subs := reCommandName.FindStringSubmatch(match)
		if len(subs) > 1 {
			return subs[1]
		}
		return ""
	})
	text = reSystemReminder.ReplaceAllString(text, "")
	text = reSystemInstruction.ReplaceAllString(text, "")
	text = reSystemInstructHyp.ReplaceAllString(text, "")
	text = reLocalCommandStdout.ReplaceAllString(text, "")
	text = reLocalCommandCaveat.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

// RenderMarkdown converts markdown text to template.HTML via goldmark.
// Goldmark's default behavior strips raw HTML tags for XSS safety,
// so <script> and similar tags are silently removed from output.
// HardWraps mode converts single newlines to <br> for plain-text readability.
// Returns empty HTML for empty input.
func RenderMarkdown(text string) template.HTML {
	if text == "" {
		return ""
	}

	// strip ANSI escape codes that leak from CLI tool output
	text = StripANSI(text)

	var buf bytes.Buffer
	if err := md.Convert([]byte(text), &buf); err != nil {
		// fallback: HTML-escape and wrap in <pre>
		return template.HTML("<pre>" + template.HTMLEscapeString(text) + "</pre>")
	}
	return template.HTML(buf.String())
}

// FormatToolSummary creates a compact summary for tool calls.
// Examples: "Edit(file.go) -- +5 / -3 lines", "Read(config.yaml)", "Bash"
func FormatToolSummary(tool *ToolCallView) string {
	if tool == nil || tool.Name == "" {
		return ""
	}

	filePath := ExtractFilePathFromInput(tool.Input)
	if filePath != "" {
		base := filepath.Base(filePath)
		// count diff lines if output contains diff markers
		added, removed := CountDiffLines(tool.Output)
		if added > 0 || removed > 0 {
			return fmt.Sprintf("%s(%s) — +%d / -%d lines", tool.Name, base, added, removed)
		}
		return fmt.Sprintf("%s(%s)", tool.Name, base)
	}

	return tool.Name
}

// ExtractFilePathFromInput parses tool input (JSON or raw) to find a file_path field.
func ExtractFilePathFromInput(input string) string {
	if input == "" {
		return ""
	}

	// try to parse as JSON and extract file_path
	var data map[string]any
	if json.Unmarshal([]byte(input), &data) == nil {
		if fp, ok := data["file_path"].(string); ok && fp != "" {
			return fp
		}
		// also check "path" as a fallback
		if fp, ok := data["path"].(string); ok && fp != "" {
			return fp
		}
	}

	return ""
}

// CountDiffLines counts added and removed lines in a unified diff output.
// Lines starting with "+" (not "+++") are counted as added.
// Lines starting with "-" (not "---") are counted as removed.
func CountDiffLines(output string) (added, removed int) {
	if output == "" {
		return 0, 0
	}

	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			removed++
		}
	}
	return
}

// RenderDiffHTML wraps diff lines in colored spans for HTML display.
// Lines are HTML-escaped first, then wrapped:
//   - Lines starting with "+" (not "+++") get class="diff-add"
//   - Lines starting with "-" (not "---") get class="diff-remove"
//   - Lines starting with "@@" get class="diff-header"
//
// Returns raw HTML safe for template rendering.
func RenderDiffHTML(output string) template.HTML {
	if output == "" {
		return ""
	}

	lines := strings.Split(output, "\n")
	var buf strings.Builder
	buf.Grow(len(output) * 2) // rough estimate for HTML overhead

	for i, line := range lines {
		escaped := template.HTMLEscapeString(line)

		switch {
		case strings.HasPrefix(line, "@@"):
			buf.WriteString(`<span class="diff-header">`)
			buf.WriteString(escaped)
			buf.WriteString("</span>")
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			buf.WriteString(`<span class="diff-add">`)
			buf.WriteString(escaped)
			buf.WriteString("</span>")
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			buf.WriteString(`<span class="diff-remove">`)
			buf.WriteString(escaped)
			buf.WriteString("</span>")
		default:
			buf.WriteString(escaped)
		}

		if i < len(lines)-1 {
			buf.WriteByte('\n')
		}
	}

	return template.HTML(buf.String())
}

// IsDiffOutput detects whether the output string looks like a unified diff.
// Checks for common diff markers: lines starting with @@, +++, or ---.
func IsDiffOutput(output string) bool {
	if output == "" {
		return false
	}

	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "@@") ||
			strings.HasPrefix(line, "+++") ||
			strings.HasPrefix(line, "---") {
			return true
		}
		// also detect simple +/- diff blocks (Edit tool output often has these)
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			// need at least a few diff-like lines to avoid false positives
			added, removed := CountDiffLines(output)
			return added > 0 || removed > 0
		}
	}
	return false
}

// FormatDuration creates a human-readable duration string.
func FormatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		mins := int(d.Minutes())
		secs := int(d.Seconds()) % 60
		if secs > 0 {
			return fmt.Sprintf("%dm %ds", mins, secs)
		}
		return fmt.Sprintf("%dm", mins)
	}

	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if mins > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dh", hours)
}

// ComputeFallbackDuration derives duration from message timestamps when
// metadata times are missing. Returns zero values if no valid timestamps found.
func ComputeFallbackDuration(messages []MessageView) (duration time.Duration, first, last time.Time) {
	for _, m := range messages {
		if m.Timestamp.IsZero() {
			continue
		}
		if first.IsZero() || m.Timestamp.Before(first) {
			first = m.Timestamp
		}
		if last.IsZero() || m.Timestamp.After(last) {
			last = m.Timestamp
		}
	}
	if !first.IsZero() && !last.IsZero() && last.After(first) {
		duration = last.Sub(first)
	}
	return
}
