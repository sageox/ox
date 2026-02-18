package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/session"
	sessionhtml "github.com/sageox/ox/internal/session/html"
	"github.com/sageox/ox/internal/theme"
)

// generateHTML creates an HTML file from a stored session.
func generateHTML(t *session.StoredSession, outputPath string) error {
	// try to load summary.json from the same directory
	var summary *session.SummarizeResponse
	summaryPath := filepath.Join(filepath.Dir(t.Info.FilePath), "summary.json")
	if data, err := os.ReadFile(summaryPath); err == nil {
		var s session.SummarizeResponse
		if json.Unmarshal(data, &s) == nil {
			summary = &s
		}
	}

	// build template data
	data := buildTemplateData(t, summary)

	// parse and execute template with helper functions
	funcMap := template.FuncMap{
		"add":            func(a, b int) int { return a + b },
		"renderDiffHTML": sessionhtml.RenderDiffHTML,
		"isDiffOutput":   sessionhtml.IsDiffOutput,
	}
	tmpl, err := template.New("session").Funcs(funcMap).Parse(htmlTemplate)
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create output file: %w", err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, data); err != nil {
		return fmt.Errorf("execute template: %w", err)
	}

	return nil
}

// buildTemplateData converts a stored session to template data.
func buildTemplateData(t *session.StoredSession, summary *session.SummarizeResponse) *sessionhtml.TemplateData {
	// get title from summary.json or fall back to buildTitle
	title := buildTitle(t)
	if summary != nil && summary.Title != "" {
		title = summary.Title
	}

	data := &sessionhtml.TemplateData{
		Title:       title,
		BrandColors: sessionhtml.DefaultBrandColors(),
		Styles:      template.CSS(cssRootVars() + embeddedStylesBase),
		Scripts:     template.JS(embeddedScripts),
	}

	// populate summary from summary.json
	if summary != nil {
		data.Summary = &sessionhtml.SummaryView{
			Text:        summary.Summary,
			KeyActions:  summary.KeyActions,
			Outcome:     summary.Outcome,
			TopicsFound: summary.TopicsFound,
			FinalPlan:   summary.FinalPlan,
			Diagrams:    summary.Diagrams,
		}
		// populate SageOx insights
		for _, si := range summary.SageoxInsights {
			data.Summary.SageoxInsights = append(data.Summary.SageoxInsights, sessionhtml.SageoxInsightView{
				Seq:     si.Seq,
				Topic:   si.Topic,
				Insight: si.Insight,
				Impact:  si.Impact,
			})
			data.SageoxInsights = append(data.SageoxInsights, sessionhtml.SageoxInsightView{
				Seq:     si.Seq,
				Topic:   si.Topic,
				Insight: si.Insight,
				Impact:  si.Impact,
			})
		}
	}

	// build aha moments lookup for message highlighting
	type ahaEntry struct {
		index int // 1-based index
		view  *sessionhtml.AhaMomentView
	}
	ahaMomentsBySeq := make(map[int]ahaEntry) // seq -> aha entry
	if summary != nil && len(summary.AhaMoments) > 0 {
		for i, am := range summary.AhaMoments {
			ahaView := &sessionhtml.AhaMomentView{
				Seq:       am.Seq,
				Role:      am.Role,
				Type:      am.Type,
				Highlight: am.Highlight,
				Why:       am.Why,
			}
			ahaMomentsBySeq[am.Seq] = ahaEntry{index: i + 1, view: ahaView}
			data.AhaMoments = append(data.AhaMoments, *ahaView)
		}
	}

	// build metadata view
	if t.Meta != nil {
		data.Metadata = &sessionhtml.MetadataView{
			AgentType:    t.Meta.AgentType,
			AgentVersion: t.Meta.AgentVersion,
			Model:        t.Meta.Model,
			Username:     t.Meta.Username,
			StartedAt:    t.Meta.CreatedAt,
		}
	}

	// extract ended time from footer if available
	if t.Footer != nil {
		if closedAt, ok := t.Footer["closed_at"].(string); ok {
			if parsed, err := time.Parse(time.RFC3339Nano, closedAt); err == nil {
				if data.Metadata != nil {
					data.Metadata.EndedAt = parsed
					// calculate duration if we have start time
					if t.Meta != nil && !t.Meta.CreatedAt.IsZero() {
						duration := parsed.Sub(t.Meta.CreatedAt)
						data.Metadata.Duration = sessionhtml.FormatDuration(duration)
					}
				}
			}
		}
	}

	// determine sender labels from metadata
	userLabel := "User"
	agentLabel := "Assistant"
	if t.Meta != nil {
		if t.Meta.Username != "" {
			userLabel = t.Meta.Username
		}
		if t.Meta.AgentType != "" {
			agentLabel = formatAgentType(t.Meta.AgentType)
		}
	}

	// build messages from entries
	var userMessages, toolCalls int
	for i, entry := range t.Entries {
		msg := buildMessageView(i+1, entry, userLabel, agentLabel)

		// mark aha moments based on seq number and attach full details
		if aha, ok := ahaMomentsBySeq[i+1]; ok {
			msg.IsAhaMoment = true
			msg.AhaMomentID = aha.index
			msg.AhaMoment = aha.view
		}

		data.Messages = append(data.Messages, msg)

		// count for statistics
		switch msg.Type {
		case "user":
			userMessages++
		case "tool":
			toolCalls++
		}
	}

	// fallback: compute duration from entry timestamps when meta times are missing
	if data.Metadata != nil && data.Metadata.Duration == "" && len(data.Messages) > 0 {
		dur, first, last := sessionhtml.ComputeFallbackDuration(data.Messages)
		if dur > 0 {
			data.Metadata.Duration = sessionhtml.FormatDuration(dur)
			if data.Metadata.StartedAt.IsZero() {
				data.Metadata.StartedAt = first
			}
			if data.Metadata.EndedAt.IsZero() {
				data.Metadata.EndedAt = last
			}
		}
	}

	// build statistics
	data.Statistics = &sessionhtml.StatsView{
		TotalMessages: len(t.Entries),
		UserMessages:  userMessages,
		ToolCalls:     toolCalls,
	}
	if data.Metadata != nil && data.Metadata.Duration != "" {
		data.Statistics.Duration = data.Metadata.Duration
	}

	return data
}

// buildTitle generates a title from the session.
func buildTitle(t *session.StoredSession) string {
	// try to extract from metadata
	if t.Meta != nil {
		if t.Meta.AgentType != "" {
			return fmt.Sprintf("%s Session", t.Meta.AgentType)
		}
	}

	// fall back to filename-based title
	baseName := strings.TrimSuffix(t.Info.Filename, ".jsonl")
	// extract date portion if it matches our format
	if len(baseName) >= 16 {
		dateStr := baseName[:16]
		if parsed, err := time.Parse("2006-01-02T15-04", dateStr); err == nil {
			return fmt.Sprintf("Session %s", parsed.Format("Jan 2, 2006 15:04"))
		}
	}

	return "Agent Session"
}

// buildMessageView converts a session entry to a message view.
func buildMessageView(id int, entry map[string]any, userLabel, agentLabel string) sessionhtml.MessageView {
	msg := sessionhtml.MessageView{
		ID: id,
	}

	// get entry type
	entryType, _ := entry["type"].(string)
	msg.Type = mapEntryType(entryType)

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
	default:
		msg.SenderLabel = msg.Type
	}

	// get timestamp - check both "timestamp" and "ts" field names
	if ts, ok := entry["timestamp"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			msg.Timestamp = parsed
		} else if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
			msg.Timestamp = parsed
		}
	} else if ts, ok := entry["ts"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			msg.Timestamp = parsed
		} else if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
			msg.Timestamp = parsed
		}
	}

	// check for content at root level first (ox native format)
	if content, ok := entry["content"].(string); ok && content != "" {
		msg.Content = sessionhtml.RenderMarkdown(content)
	}

	// handle tool entries with root-level fields (reference format)
	if entryType == "tool" {
		toolName, _ := entry["tool_name"].(string)
		toolInput, _ := entry["tool_input"].(string)
		toolOutput, _ := entry["tool_output"].(string)
		if toolName != "" {
			msg.Content = template.HTML("<p>Tool: " + template.HTMLEscapeString(toolName) + "</p>")
			msg.ToolCall = &sessionhtml.ToolCallView{
				Name:   toolName,
				Input:  escapeHTML(sessionhtml.StripANSI(toolInput)),
				Output: escapeHTML(sessionhtml.StripANSI(truncateOutput(toolOutput, 10000))),
			}
			msg.ToolCall.Summary = sessionhtml.FormatToolSummary(msg.ToolCall)
		}
	}

	// get content based on entry type from nested data
	if data, ok := entry["data"].(map[string]any); ok {
		switch entryType {
		case "message":
			if content, _ := data["content"].(string); content != "" {
				msg.Content = sessionhtml.RenderMarkdown(content)
			}
			if role, ok := data["role"].(string); ok {
				msg.Type = mapRoleToType(role)
				// update sender label to match the actual role
				switch msg.Type {
				case "user":
					msg.SenderLabel = userLabel
				case "assistant":
					msg.SenderLabel = agentLabel
				}
			}

		case "tool_call":
			toolName, _ := data["tool_name"].(string)
			input, _ := data["input"].(string)
			msg.Type = "tool"
			msg.Content = template.HTML("<p>Tool call: " + template.HTMLEscapeString(toolName) + "</p>")
			msg.ToolCall = &sessionhtml.ToolCallView{
				Name:  toolName,
				Input: escapeHTML(sessionhtml.StripANSI(input)),
			}
			msg.ToolCall.Summary = sessionhtml.FormatToolSummary(msg.ToolCall)

		case "tool_result":
			toolName, _ := data["tool_name"].(string)
			output, _ := data["output"].(string)
			msg.Type = "tool"
			msg.Content = template.HTML("<p>Tool result: " + template.HTMLEscapeString(toolName) + "</p>")
			msg.ToolCall = &sessionhtml.ToolCallView{
				Name:   toolName,
				Output: escapeHTML(sessionhtml.StripANSI(truncateOutput(output, 10000))),
			}
			msg.ToolCall.Summary = sessionhtml.FormatToolSummary(msg.ToolCall)

		default:
			// generic entry - get content from data if not already set
			if msg.Content == "" {
				if content, ok := data["content"].(string); ok {
					msg.Content = sessionhtml.RenderMarkdown(content)
				}
			}
		}
	}

	// reclassify based on content patterns (tool output or system context
	// that was incorrectly tagged as "user" in the session recording)
	rawContent, _ := entry["content"].(string)
	msg.Type = reclassifyByContent(msg.Type, string(msg.Content), rawContent)
	switch msg.Type {
	case "tool":
		msg.SenderLabel = "Tool Call"
	case "system":
		msg.SenderLabel = "System"
	}

	return msg
}

// reclassifyByContent overrides the message type when content patterns indicate
// tool output or system context that was misattributed as a user message.
// rawContent is the original entry content before markdown rendering.
func reclassifyByContent(msgType string, content string, rawContent string) string {
	// only reclassify messages currently typed as "user" -- other types are
	// already correct from structural metadata
	if msgType != "user" {
		return msgType
	}

	// skill prompt expansions contain an ox-hash marker in the raw content
	// (e.g. "<!-- ox-hash: b1e68f3b2727 ver: 0.17.0 -->")
	if strings.Contains(rawContent, "<!-- ox-hash:") {
		return "system"
	}

	// system-reminder blocks injected by the framework
	if strings.Contains(content, "<system-reminder>") || strings.Contains(content, "&lt;system-reminder&gt;") {
		return "system"
	}

	// strip leading HTML tags to reach the raw text prefix (content is
	// rendered markdown so it may start with <p>, etc.)
	trimmed := content
	for strings.HasPrefix(trimmed, "<") {
		idx := strings.Index(trimmed, ">")
		if idx < 0 {
			break
		}
		trimmed = strings.TrimSpace(trimmed[idx+1:])
	}

	// black circle prefix used by Claude Code for tool call display (U+23FA)
	if strings.HasPrefix(trimmed, "\u23fa ") || strings.HasPrefix(trimmed, "⏺ ") {
		return "tool"
	}

	// left bracket used by Claude Code for tool result display (U+23BF)
	if strings.HasPrefix(trimmed, "\u23bf") || strings.HasPrefix(trimmed, "⎿") {
		return "tool"
	}

	return msgType
}

// mapEntryType converts session entry types to display types.
func mapEntryType(entryType string) string {
	switch entryType {
	case "user":
		return "user"
	case "assistant", "message":
		return "assistant"
	case "tool_call", "tool_result", "tool":
		return "tool"
	case "system":
		return "system"
	default:
		return "info"
	}
}

// mapRoleToType converts message roles to display types.
func mapRoleToType(role string) string {
	switch role {
	case "user":
		return "user"
	case "assistant":
		return "assistant"
	case "system":
		return "system"
	default:
		return "info"
	}
}

// formatAgentType formats agent type for display (e.g., "claude-code" -> "Claude Code").
func formatAgentType(agentType string) string {
	if agentType == "" {
		return "Assistant"
	}
	// capitalize first letter of each word, replace hyphens with spaces
	words := strings.Split(agentType, "-")
	for i, word := range words {
		if len(word) > 0 {
			words[i] = strings.ToUpper(word[:1]) + word[1:]
		}
	}
	return strings.Join(words, " ")
}

// escapeHTML escapes HTML special characters.
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&#39;")
	return s
}

// truncateOutput truncates long output strings.
func truncateOutput(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}


// cssRootVars generates the :root CSS variables from theme constants.
func cssRootVars() string {
	return `:root {
  --color-primary: ` + theme.HexPrimary + `;
  --color-secondary: ` + theme.HexSecondary + `;
  --color-accent: ` + theme.HexAccent + `;
  --color-text: ` + theme.HexText + `;
  --color-text-dim: ` + theme.HexTextDim + `;
  --color-bg-dark: ` + theme.HexBgDark + `;
  --color-bg-card: ` + theme.HexBgCard + `;
  --color-border: ` + theme.HexBorder + `;
  --color-error: ` + theme.HexError + `;
  --color-info: ` + theme.HexInfo + `;
  --spacing-xs: 0.25rem;
  --spacing-sm: 0.5rem;
  --spacing-md: 1rem;
  --spacing-lg: 1.5rem;
  --spacing-xl: 2rem;
  --font-body: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
  --font-mono: 'SF Mono', Monaco, 'Cascadia Code', Consolas, monospace;
  --border-radius: 0.5rem;
  --border-radius-sm: 0.25rem;
  --border-width: 3px;
  --shadow-sm: 0 1px 2px rgba(0,0,0,0.3);
  --shadow-md: 0 4px 6px rgba(0,0,0,0.4);
  color-scheme: dark light;
}
@media (prefers-color-scheme: light) {
  :root {
    --color-primary: ` + theme.HexLightPrimary + `;
    --color-secondary: ` + theme.HexLightSecondary + `;
    --color-accent: ` + theme.HexLightAccent + `;
    --color-text: ` + theme.HexLightText + `;
    --color-text-dim: ` + theme.HexLightTextDim + `;
    --color-bg-dark: ` + theme.HexLightBgLight + `;
    --color-bg-card: #FFFFFF;
    --color-border: #D0D0D0;
    --color-error: ` + theme.HexLightError + `;
    --color-info: ` + theme.HexLightInfo + `;
    --shadow-sm: 0 1px 2px rgba(0,0,0,0.1);
    --shadow-md: 0 4px 6px rgba(0,0,0,0.15);
  }
}
body.light-theme {
  --color-primary: ` + theme.HexLightPrimary + `;
  --color-secondary: ` + theme.HexLightSecondary + `;
  --color-accent: ` + theme.HexLightAccent + `;
  --color-text: ` + theme.HexLightText + `;
  --color-text-dim: ` + theme.HexLightTextDim + `;
  --color-bg-dark: ` + theme.HexLightBgLight + `;
  --color-bg-card: #FFFFFF;
  --color-border: #D0D0D0;
  --color-error: ` + theme.HexLightError + `;
  --color-info: ` + theme.HexLightInfo + `;
  --shadow-sm: 0 1px 2px rgba(0,0,0,0.1);
  --shadow-md: 0 4px 6px rgba(0,0,0,0.15);
}`
}

// embeddedStylesBase contains the CSS for the HTML viewer (without :root).
// Colors reference CSS variables defined by cssRootVars().
const embeddedStylesBase = `
* { margin: 0; padding: 0; box-sizing: border-box; }
body { font-family: var(--font-body); color: var(--color-text); background: var(--color-bg-dark); line-height: 1.6; container-type: inline-size; }
body.embed .page-header { display: none; }
body.embed .page-footer { display: none; }
body.embed .messages-container { padding-top: var(--spacing-md); }
.page-header { background: var(--color-bg-card); border-bottom: 2px solid var(--color-primary); padding: var(--spacing-lg); box-shadow: var(--shadow-md); }
.header-content { max-width: 1200px; margin: 0 auto; display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: var(--spacing-md); }
.brand { display: flex; align-items: center; gap: var(--spacing-md); }
.logo-large { width: 48px; height: 48px; }
.brand-text h1 { font-size: 1.5rem; color: var(--color-primary); margin: 0; }
.brand-text .subtitle { font-size: 0.875rem; color: var(--color-text-dim); margin: 0; }
.header-meta { flex: 1; min-width: 300px; }
.session-title { font-size: 1.125rem; color: var(--color-text); margin-bottom: var(--spacing-sm); }
.meta-grid { display: flex; flex-wrap: wrap; gap: var(--spacing-md) var(--spacing-lg); font-size: 0.875rem; }
.meta-item { display: flex; gap: var(--spacing-xs); }
.meta-label { color: var(--color-text-dim); }
.meta-value { color: var(--color-text); font-family: var(--font-mono); }
.stats-bar { max-width: 1200px; margin: var(--spacing-md) auto 0; display: flex; flex-wrap: wrap; gap: var(--spacing-lg); padding-top: var(--spacing-md); border-top: 1px solid rgba(255,255,255,0.1); }
.stat-item { text-align: center; }
.stat-value { display: block; font-size: 1.125rem; font-weight: 600; color: var(--color-secondary); }
.stat-label { font-size: 0.875rem; color: var(--color-text-dim); text-transform: uppercase; letter-spacing: 0.05em; }

.messages-container { max-width: 1200px; margin: 0 auto; padding: var(--spacing-xl) var(--spacing-lg); display: flex; flex-direction: column; gap: var(--spacing-lg); }
.message { background: var(--color-bg-card); border-radius: var(--border-radius); border-left: var(--border-width) solid var(--color-text-dim); padding: var(--spacing-lg); box-shadow: var(--shadow-sm); }
.message:hover { box-shadow: var(--shadow-md); }
.message-user { border-left-color: var(--color-secondary); }
.message-assistant { border-left-color: var(--color-primary); }
.message-tool { border-left-color: var(--color-info); }
.message-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: var(--spacing-md); flex-wrap: wrap; gap: var(--spacing-sm); }
.message-type { padding: var(--spacing-xs) var(--spacing-sm); background: var(--color-bg-dark); border-radius: var(--border-radius-sm); font-size: 0.875rem; font-weight: 600; text-transform: uppercase; letter-spacing: 0.05em; }
.message-type-user { color: var(--color-secondary); }
.message-type-assistant { color: var(--color-primary); }
.message-type-tool { color: var(--color-info); }
.message-type-system { color: var(--color-secondary); }
.message-type-info { color: var(--color-text-dim); }
.message-content { color: var(--color-text); line-height: 1.7; white-space: pre-wrap; }
.tool-details { margin-top: var(--spacing-lg); background: var(--color-bg-dark); border-radius: var(--border-radius); overflow: hidden; }
.tool-summary { padding: var(--spacing-md); cursor: pointer; font-weight: 600; color: var(--color-info); background: rgba(127,167,200,0.1); display: flex; align-items: center; gap: var(--spacing-sm); }
.tool-summary:hover { background: rgba(127,167,200,0.2); }
.tool-icon { width: 16px; height: 16px; max-width: 16px; max-height: 16px; flex-shrink: 0; transition: transform 0.2s; }
details[open] .tool-icon { transform: rotate(90deg); }
.tool-io { padding: var(--spacing-md); }
.tool-section { margin-bottom: var(--spacing-md); }
.tool-section:last-child { margin-bottom: 0; }
.tool-section-title { font-size: 0.875rem; font-weight: 600; text-transform: uppercase; letter-spacing: 0.05em; color: var(--color-text-dim); margin-bottom: var(--spacing-sm); }
pre { background: #0D0F0E; border-radius: var(--border-radius-sm); padding: var(--spacing-md); overflow-x: auto; margin: 0; border: 1px solid rgba(255,255,255,0.1); font-family: var(--font-mono); font-size: 0.9em; line-height: 1.5; color: var(--color-text); white-space: pre-wrap; word-wrap: break-word; }
.page-footer { background: var(--color-bg-card); border-top: 2px solid var(--color-primary); padding: var(--spacing-md) var(--spacing-lg); margin-top: var(--spacing-xl); }
.footer-content { max-width: 1200px; margin: 0 auto; display: flex; align-items: center; justify-content: space-between; font-size: 0.875rem; color: var(--color-text-dim); }
.footer-brand { display: flex; align-items: center; gap: var(--spacing-sm); color: var(--color-primary); text-decoration: none; font-weight: 600; }
.footer-brand:hover { color: var(--color-secondary); }
.logo-small { width: 24px; height: 24px; }
/* session summary section */
.session-summary { max-width: 1200px; margin: var(--spacing-lg) auto; padding: 0 var(--spacing-lg); }
.summary-header { display: flex; align-items: center; gap: var(--spacing-md); margin-bottom: var(--spacing-md); }
.summary-title { font-size: 1.5rem; font-weight: 600; color: var(--color-text); margin: 0; }
.outcome-badge { padding: var(--spacing-xs) var(--spacing-sm); border-radius: var(--border-radius-sm); font-size: 0.75rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.05em; }
.outcome-success { background: rgba(133,178,129,0.2); color: var(--color-primary); border: 1px solid var(--color-primary); }
.outcome-partial { background: rgba(200,162,120,0.2); color: var(--color-secondary); border: 1px solid var(--color-secondary); }
.outcome-failed { background: rgba(200,80,80,0.2); color: var(--color-error); border: 1px solid var(--color-error); }
.summary-text { background: var(--color-bg-card); border-radius: var(--border-radius); padding: var(--spacing-lg); border-left: var(--border-width) solid var(--color-primary); color: var(--color-text); line-height: 1.7; margin-bottom: var(--spacing-lg); }
.key-actions { background: var(--color-bg-card); border-radius: var(--border-radius); padding: var(--spacing-lg); margin-bottom: var(--spacing-lg); }
.key-actions-title { font-size: 0.875rem; font-weight: 600; text-transform: uppercase; letter-spacing: 0.05em; color: var(--color-secondary); margin: 0 0 var(--spacing-md) 0; }
.key-actions-list { list-style: none; margin: 0; padding: 0; }
.key-actions-list li { position: relative; padding-left: var(--spacing-lg); padding-bottom: var(--spacing-sm); color: var(--color-text); line-height: 1.6; }
.key-actions-list li::before { content: '●'; position: absolute; left: 0; color: var(--color-secondary); }
.topics-bar { display: flex; flex-wrap: wrap; gap: var(--spacing-sm); margin-bottom: var(--spacing-lg); }
.topic-tag { padding: var(--spacing-xs) var(--spacing-md); background: var(--color-bg-card); border: 1px solid var(--color-border); border-radius: 999px; font-size: 0.875rem; color: var(--color-text-dim); }

/* key moments navigation bar */
.key-moments-nav { max-width: 1200px; margin: 0 auto var(--spacing-lg); padding: var(--spacing-md) var(--spacing-lg); display: flex; align-items: center; gap: var(--spacing-sm); flex-wrap: wrap; }
.nav-label { font-size: 0.875rem; color: var(--color-secondary); font-weight: 600; }
.moment-jump { background: var(--color-bg-card); border: 1px solid var(--color-border); color: var(--color-text); padding: var(--spacing-xs) var(--spacing-sm); border-radius: var(--border-radius-sm); cursor: pointer; font-size: 0.875rem; transition: all 0.2s; }
.moment-jump:hover { background: var(--color-secondary); color: var(--color-bg-dark); border-color: var(--color-secondary); }
.moment-ffwd { background: var(--color-secondary); border: none; color: var(--color-bg-dark); padding: var(--spacing-xs) var(--spacing-sm); border-radius: var(--border-radius-sm); cursor: pointer; font-size: 1rem; transition: transform 0.2s; }
.moment-ffwd:hover { transform: scale(1.1); }

/* inline aha moment card */
.aha-inline-card { margin-top: var(--spacing-md); padding: var(--spacing-md); background: rgba(200,162,120,0.1); border-radius: var(--border-radius-sm); border-left: 3px solid var(--color-secondary); }
.aha-inline-header { margin-bottom: var(--spacing-xs); }
.aha-inline-type { font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.05em; color: var(--color-secondary); font-weight: 600; }
.aha-inline-highlight { color: var(--color-text); font-style: italic; font-size: 1rem; margin-bottom: var(--spacing-xs); }
.aha-inline-why { font-size: 0.875rem; color: var(--color-text-dim); }

/* shortcuts modal */
.shortcuts-modal { position: fixed; inset: 0; background: rgba(0,0,0,0.7); display: flex; align-items: center; justify-content: center; z-index: 200; backdrop-filter: blur(4px); }
.shortcuts-content { background: var(--color-bg-card); border-radius: var(--border-radius); padding: var(--spacing-xl); max-width: 400px; border: 1px solid var(--color-border); }
.shortcuts-content h3 { color: var(--color-primary); margin-bottom: var(--spacing-md); font-size: 1.125rem; }
.shortcuts-content ul { list-style: none; margin-bottom: var(--spacing-md); }
.shortcuts-content li { display: flex; align-items: center; gap: var(--spacing-md); padding: var(--spacing-sm) 0; color: var(--color-text); }
.shortcuts-content kbd { background: var(--color-bg-dark); padding: var(--spacing-xs) var(--spacing-sm); border-radius: var(--border-radius-sm); font-family: var(--font-mono); font-size: 0.875rem; color: var(--color-secondary); border: 1px solid var(--color-border); min-width: 28px; text-align: center; }
.shortcuts-close { text-align: center; color: var(--color-text-dim); font-size: 0.875rem; }

/* message aha highlight */
.message-aha { border-left-color: var(--color-secondary); position: relative; }
.message-aha-user { border-left-width: 5px; background: linear-gradient(90deg, rgba(200,162,120,0.08) 0%, var(--color-bg-card) 100%); }
.aha-indicator { display: inline-flex; align-items: center; gap: var(--spacing-xs); background: var(--color-secondary); color: var(--color-bg-dark); padding: var(--spacing-xs) var(--spacing-sm); border-radius: var(--border-radius-sm); font-size: 0.75rem; font-weight: 600; text-transform: uppercase; letter-spacing: 0.05em; margin-left: var(--spacing-sm); cursor: help; }
.aha-ffwd { background: none; border: none; color: var(--color-secondary); cursor: pointer; font-size: 1rem; padding: var(--spacing-xs); margin-left: var(--spacing-xs); transition: transform 0.2s; vertical-align: middle; }
.aha-ffwd:hover { transform: scale(1.2); }

/* collapsible message styling */
.message-collapsible { width: 100%; }
.message-collapsible > summary { list-style: none; display: flex; justify-content: space-between; align-items: center; padding: 0.75rem 1rem; cursor: pointer; }
.message-collapsible > summary::-webkit-details-marker { display: none; }
.message-collapsible > summary::marker { display: none; }
.message-collapsible > summary::before { content: '▶'; font-size: 0.7em; margin-right: 0.5rem; transition: transform 0.2s; color: var(--color-text-dim); }
.message-collapsible[open] > summary::before { transform: rotate(90deg); }

/* markdown content styling */
.message-content h1 { font-size: 1.5rem; color: var(--color-primary); margin: var(--spacing-lg) 0 var(--spacing-md); border-bottom: 1px solid var(--color-border); padding-bottom: var(--spacing-sm); }
.message-content h2 { font-size: 1.25rem; color: var(--color-text); margin: var(--spacing-lg) 0 var(--spacing-md); }
.message-content h3 { font-size: 1.1rem; color: var(--color-text); margin: var(--spacing-md) 0 var(--spacing-sm); }
.message-content ul, .message-content ol { margin: var(--spacing-sm) 0; padding-left: var(--spacing-xl); }
.message-content li { margin: var(--spacing-xs) 0; }
.message-content table { border-collapse: collapse; width: 100%; margin: var(--spacing-md) 0; font-size: 0.9rem; }
.message-content th, .message-content td { border: 1px solid var(--color-border); padding: var(--spacing-sm) var(--spacing-md); text-align: left; }
.message-content th { background: var(--color-bg-dark); color: var(--color-secondary); font-weight: 600; }
.message-content tr:nth-child(even) { background: rgba(255,255,255,0.02); }
.message-content code { background: var(--color-bg-dark); padding: 0.1em 0.3em; border-radius: var(--border-radius-sm); font-family: var(--font-mono); font-size: 0.9em; }
.message-content pre { background: var(--color-bg-dark); padding: var(--spacing-md); border-radius: var(--border-radius-sm); overflow-x: auto; margin: var(--spacing-md) 0; }
.message-content pre code { background: none; padding: 0; }
.message-content blockquote { border-left: 3px solid var(--color-secondary); padding-left: var(--spacing-md); margin: var(--spacing-md) 0; color: var(--color-text-dim); font-style: italic; }
.mermaid-container { background: var(--color-bg-dark); border-radius: var(--border-radius); padding: var(--spacing-md); margin: var(--spacing-md) 0; overflow-x: auto; }
.mermaid-container svg { max-width: 100%; height: auto; }
.mermaid-error { color: var(--color-error); }

/* diff coloring in tool output */
.tool-output .diff-add { color: #85b881; }
.tool-output .diff-remove { color: #e07070; }
.tool-output .diff-header { color: #7fa7c8; font-weight: 600; }

/* SageOx insights section */
.sageox-insights { max-width: 1200px; margin: 0 auto var(--spacing-lg); padding: 0 var(--spacing-lg); }
.sageox-insights-header { display: flex; align-items: center; gap: var(--spacing-md); margin-bottom: var(--spacing-md); }
.sageox-insights-title { font-size: 1.125rem; font-weight: 600; color: var(--color-primary); margin: 0; }
.sageox-insights-badge { background: linear-gradient(135deg, var(--color-primary) 0%, var(--color-accent) 100%); color: white; padding: var(--spacing-xs) var(--spacing-sm); border-radius: var(--border-radius-sm); font-size: 0.75rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.05em; }
.sageox-insight-card { background: var(--color-bg-card); border-radius: var(--border-radius); padding: var(--spacing-md); margin-bottom: var(--spacing-sm); border-left: 3px solid var(--color-primary); cursor: pointer; transition: all 0.2s; }
.sageox-insight-card:hover { box-shadow: var(--shadow-md); transform: translateX(2px); }
.sageox-insight-topic { font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.05em; color: var(--color-primary); font-weight: 600; margin-bottom: var(--spacing-xs); }
.sageox-insight-text { color: var(--color-text); margin-bottom: var(--spacing-xs); }
.sageox-insight-impact { font-size: 0.875rem; color: var(--color-text-dim); font-style: italic; }

@container (max-width: 768px) {
  .header-content { flex-direction: column; align-items: flex-start; }
  .message { padding: var(--spacing-md); }
  .footer-content { flex-direction: column; gap: var(--spacing-sm); }
  .key-moments { margin: var(--spacing-md); }
}
@media (max-width: 768px) {
  .header-content { flex-direction: column; align-items: flex-start; }
  .message { padding: var(--spacing-md); }
  .footer-content { flex-direction: column; gap: var(--spacing-sm); }
  .key-moments { margin: var(--spacing-md); }
}
`

// embeddedScripts contains the JavaScript for the HTML viewer.
const embeddedScripts = `
// initialize mermaid (guard against blocked CDN)
if (typeof mermaid !== 'undefined') {
  mermaid.initialize({ startOnLoad: false, theme: 'dark' });
}

// render mermaid diagrams in pre-rendered markdown
document.addEventListener('DOMContentLoaded', () => {
  if (typeof mermaid === 'undefined') return;
  document.querySelectorAll('.message-content pre code.language-mermaid, .message-content .language-mermaid').forEach((mermaidEl, i) => {
    const code = mermaidEl.textContent;
    const msg = mermaidEl.closest('.message');
    const id = 'mermaid-' + (msg ? msg.id : 'g') + '-' + i;
    const container = document.createElement('div');
    container.className = 'mermaid-container';
    mermaidEl.parentElement.replaceWith(container);
    mermaid.render(id, code).then(result => {
      container.innerHTML = result.svg;
    }).catch(() => {
      container.innerHTML = '<pre class="mermaid-error">' + code + '</pre>';
    });
  });
});

// smooth scroll to message when clicking stats
document.querySelectorAll('.stat-item').forEach(item => {
  item.style.cursor = 'pointer';
  item.addEventListener('click', () => {
    const messages = document.getElementById('messages');
    if (messages) {
      messages.scrollIntoView({ behavior: 'smooth' });
    }
  });
});

// navigate to specific message by ID
function navigateToMessage(seq) {
  const msg = document.getElementById('msg-' + seq);
  if (msg) {
    msg.scrollIntoView({ behavior: 'smooth', block: 'center' });
    msg.focus();
    // highlight briefly
    msg.style.transition = 'box-shadow 0.3s ease';
    msg.style.boxShadow = '0 0 0 3px var(--color-secondary), 0 4px 20px rgba(200,162,120,0.4)';
    setTimeout(() => {
      msg.style.boxShadow = '';
    }, 2000);
  }
}

// keyboard navigation
const ahaMoments = document.querySelectorAll('.message-aha');
let currentAhaIndex = -1;

document.addEventListener('keydown', (e) => {
  // skip if user is typing in an input
  if (e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA') return;

  if (e.key === 'j' || e.key === 'ArrowDown') {
    navigateMessage(1);
  } else if (e.key === 'k' || e.key === 'ArrowUp') {
    navigateMessage(-1);
  } else if (e.key === 'Enter' || e.key === ' ') {
    e.preventDefault();
    toggleCurrentDetails();
  } else if (e.key === 'a' || e.key === 'A') {
    // jump to next aha moment
    navigateAhaMoment(e.shiftKey ? -1 : 1);
  } else if (e.key === '?' || e.key === '/') {
    // show keyboard shortcuts
    showShortcuts();
  }
});

let currentMessageIndex = -1;
const messages = document.querySelectorAll('.message');

function navigateMessage(direction) {
  const newIndex = Math.max(0, Math.min(messages.length - 1, currentMessageIndex + direction));
  if (newIndex !== currentMessageIndex) {
    currentMessageIndex = newIndex;
    messages[currentMessageIndex].scrollIntoView({ behavior: 'smooth', block: 'center' });
    messages[currentMessageIndex].focus();
  }
}

function navigateAhaMoment(direction) {
  if (ahaMoments.length === 0) return;
  currentAhaIndex = (currentAhaIndex + direction + ahaMoments.length) % ahaMoments.length;
  if (currentAhaIndex < 0) currentAhaIndex = ahaMoments.length - 1;
  ahaMoments[currentAhaIndex].scrollIntoView({ behavior: 'smooth', block: 'center' });
  ahaMoments[currentAhaIndex].focus();
  // update message index to match
  currentMessageIndex = Array.from(messages).indexOf(ahaMoments[currentAhaIndex]);
}

function toggleCurrentDetails() {
  if (currentMessageIndex >= 0) {
    const details = messages[currentMessageIndex].querySelector('details');
    if (details) {
      details.open = !details.open;
    }
  }
}

// iframe height communication
if (window !== window.parent) {
  function postHeight() {
    window.parent.postMessage(
      { type: 'sageox-session-resize', height: document.documentElement.scrollHeight },
      '*'
    );
  }
  window.addEventListener('load', postHeight);
  window.addEventListener('resize', postHeight);
  new MutationObserver(postHeight).observe(document.body, { childList: true, subtree: true, attributes: true });
}

function showShortcuts() {
  const existing = document.querySelector('.shortcuts-modal');
  if (existing) { existing.remove(); return; }
  const modal = document.createElement('div');
  modal.className = 'shortcuts-modal';
  modal.innerHTML = '<div class="shortcuts-content"><h3>Keyboard Shortcuts</h3><ul>' +
    '<li><kbd>j</kbd> / <kbd>↓</kbd> Next message</li>' +
    '<li><kbd>k</kbd> / <kbd>↑</kbd> Previous message</li>' +
    '<li><kbd>a</kbd> Next key moment</li>' +
    '<li><kbd>Shift+a</kbd> Previous key moment</li>' +
    '<li><kbd>Enter</kbd> / <kbd>Space</kbd> Toggle tool details</li>' +
    '<li><kbd>?</kbd> Toggle this help</li>' +
    '</ul><p class="shortcuts-close">Press any key to close</p></div>';
  document.body.appendChild(modal);
  setTimeout(() => {
    document.addEventListener('keydown', function closeModal() {
      modal.remove();
      document.removeEventListener('keydown', closeModal);
    }, { once: true });
  }, 100);
}
`

// htmlTemplate is the HTML template for the viewer.
const htmlTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Title}} - SageOx Session</title>
    <base target="_blank">
    <meta name="color-scheme" content="dark light">
    <style>{{.Styles}}</style>
    <script src="https://cdn.jsdelivr.net/npm/mermaid/dist/mermaid.min.js"></script>
</head>
<body>
    <script>(function(){var p=new URLSearchParams(window.location.search);if(p.get('embed')==='1')document.body.classList.add('embed');if(p.get('theme')==='light')document.body.classList.add('light-theme');})()</script>
    <header class="page-header">
        <div class="header-content">
            <div class="brand">
                <img class="logo-large" src="https://avatars.githubusercontent.com/u/224450799?s=96" alt="SageOx Logo" width="48" height="48">
                <div class="brand-text">
                    <h1>SageOx</h1>
                    <p class="subtitle">Agent Session</p>
                </div>
            </div>
            <div class="header-meta">
                <h2 class="session-title">{{.Title}}</h2>
                {{if .Metadata}}
                <div class="meta-grid">
                    {{if .Metadata.AgentType}}<div class="meta-item"><span class="meta-label">Agent:</span> <span class="meta-value">{{.Metadata.AgentType}}</span></div>{{end}}
                    {{if .Metadata.Model}}<div class="meta-item"><span class="meta-label">Model:</span> <span class="meta-value">{{.Metadata.Model}}</span></div>{{end}}
                    {{if .Metadata.Username}}<div class="meta-item"><span class="meta-label">User:</span> <span class="meta-value">{{.Metadata.Username}}</span></div>{{end}}
                    {{if not .Metadata.StartedAt.IsZero}}<div class="meta-item"><span class="meta-label">Started:</span> <time class="meta-value">{{.Metadata.StartedAt.Format "2006-01-02 15:04:05"}}</time></div>{{end}}
                    {{if not .Metadata.EndedAt.IsZero}}<div class="meta-item"><span class="meta-label">Ended:</span> <time class="meta-value">{{.Metadata.EndedAt.Format "2006-01-02 15:04:05"}}</time></div>{{end}}
                    {{if .Metadata.Duration}}<div class="meta-item"><span class="meta-label">Duration:</span> <span class="meta-value">{{.Metadata.Duration}}</span></div>{{end}}
                </div>
                {{end}}
            </div>
        </div>
        {{if .Statistics}}
        <div class="stats-bar">
            <div class="stat-item">
                <span class="stat-value">{{.Statistics.TotalMessages}}</span>
                <span class="stat-label">Total Messages</span>
            </div>
            <div class="stat-item">
                <span class="stat-value">{{.Statistics.UserMessages}}</span>
                <span class="stat-label">User Messages</span>
            </div>
            <div class="stat-item">
                <span class="stat-value">{{.Statistics.ToolCalls}}</span>
                <span class="stat-label">Tool Calls</span>
            </div>
            {{if .Statistics.Duration}}
            <div class="stat-item">
                <span class="stat-value">{{.Statistics.Duration}}</span>
                <span class="stat-label">Session Duration</span>
            </div>
            {{end}}
        </div>
        {{end}}
    </header>

    {{if .Summary}}
    <section class="session-summary">
        <div class="summary-header">
            <h2 class="summary-title">Session Summary</h2>
            {{if .Summary.Outcome}}<span class="outcome-badge outcome-{{.Summary.Outcome}}">{{.Summary.Outcome}}</span>{{end}}
        </div>
        {{if .Summary.Text}}
        <div class="summary-text">{{.Summary.Text}}</div>
        {{end}}
        {{if .Summary.KeyActions}}
        <div class="key-actions">
            <h3 class="key-actions-title">KEY ACTIONS</h3>
            <ul class="key-actions-list">
                {{range .Summary.KeyActions}}
                <li>{{.}}</li>
                {{end}}
            </ul>
        </div>
        {{end}}
        {{if .Summary.TopicsFound}}
        <div class="topics-bar">
            {{range .Summary.TopicsFound}}
            <span class="topic-tag">{{.}}</span>
            {{end}}
        </div>
        {{end}}
    </section>
    {{end}}

    {{if .SageoxInsights}}
    <section class="sageox-insights">
        <div class="sageox-insights-header">
            <h3 class="sageox-insights-title">🦬 SageOx Value</h3>
            <span class="sageox-insights-badge">{{len .SageoxInsights}} Insight{{if gt (len .SageoxInsights) 1}}s{{end}}</span>
        </div>
        {{range .SageoxInsights}}
        <div class="sageox-insight-card" onclick="navigateToMessage({{.Seq}})">
            <div class="sageox-insight-topic">{{.Topic}}</div>
            <div class="sageox-insight-text">{{.Insight}}</div>
            {{if .Impact}}<div class="sageox-insight-impact">→ {{.Impact}}</div>{{end}}
        </div>
        {{end}}
    </section>
    {{end}}

    {{if .AhaMoments}}
    <nav class="key-moments-nav" id="key-moments">
        <span class="nav-label">★ Key Moments:</span>
        {{range $i, $m := .AhaMoments}}
        <button class="moment-jump" onclick="navigateToMessage({{$m.Seq}})" title="{{$m.Type}}: {{$m.Highlight}}">
            #{{add $i 1}}
        </button>
        {{end}}
        <button class="moment-ffwd" onclick="navigateAhaMoment(1)" title="Jump to next key moment (press 'a')">
            ⏩
        </button>
    </nav>
    {{end}}

    <main id="messages" class="messages-container">
        {{range .Messages}}
        <article class="message message-{{.Type}}{{if .IsAhaMoment}} message-aha{{if .AhaMoment}}{{if eq .AhaMoment.Role "user"}} message-aha-user{{end}}{{end}}{{end}}" id="msg-{{.ID}}" data-type="{{.Type}}" tabindex="0">
            {{if or (eq .Type "tool") (eq .Type "system") (eq .Type "info")}}
            <details class="message-collapsible">
                <summary class="message-header">
                    <span class="message-type message-type-{{.Type}}">{{.SenderLabel}}</span>
                    {{if .IsAhaMoment}}<span class="aha-indicator" title="{{if .AhaMoment}}{{.AhaMoment.Type}}: {{.AhaMoment.Why}}{{end}}">Key Moment #{{.AhaMomentID}}</span><button class="aha-ffwd" onclick="navigateAhaMoment(1)" title="Jump to next key moment (press 'a')">⏩</button>{{end}}
                </summary>
                <div class="message-content">{{.Content}}</div>
                {{if .AhaMoment}}
                <div class="aha-inline-card">
                    <div class="aha-inline-header">
                        <span class="aha-inline-type">{{.AhaMoment.Type}}{{if eq .AhaMoment.Role "user"}} by Human{{else}} by AI{{end}}</span>
                    </div>
                    <div class="aha-inline-why">{{.AhaMoment.Why}}</div>
                </div>
                {{end}}
                {{if .ToolCall}}
                <details class="tool-details">
                    <summary class="tool-summary">
                        <svg class="tool-icon" width="16" height="16" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg">
                            <path d="M6 3L11 8L6 13" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                        <span class="tool-name">{{if .ToolCall.Summary}}{{.ToolCall.Summary}}{{else}}{{.ToolCall.Name}}{{end}}</span>
                    </summary>
                    <div class="tool-io">
                        {{if .ToolCall.Input}}
                        <div class="tool-section">
                            <h4 class="tool-section-title">Input</h4>
                            <pre class="tool-input">{{.ToolCall.Input}}</pre>
                        </div>
                        {{end}}
                        {{if .ToolCall.Output}}
                        <div class="tool-section">
                            <h4 class="tool-section-title">Output</h4>
                            {{if isDiffOutput .ToolCall.Output}}<pre class="tool-output">{{renderDiffHTML .ToolCall.Output}}</pre>{{else}}<pre class="tool-output">{{.ToolCall.Output}}</pre>{{end}}
                        </div>
                        {{end}}
                    </div>
                </details>
                {{end}}
            </details>
            {{else}}
            <div class="message-header">
                <span class="message-type message-type-{{.Type}}">{{.SenderLabel}}</span>
                {{if .IsAhaMoment}}<span class="aha-indicator" title="{{if .AhaMoment}}{{.AhaMoment.Type}}: {{.AhaMoment.Why}}{{end}}">Key Moment #{{.AhaMomentID}}</span><button class="aha-ffwd" onclick="navigateAhaMoment(1)" title="Jump to next key moment (press 'a')">⏩</button>{{end}}
            </div>
            <div class="message-content">{{.Content}}</div>
            {{if .AhaMoment}}
            <div class="aha-inline-card">
                <div class="aha-inline-header">
                    <span class="aha-inline-type">{{.AhaMoment.Type}}{{if eq .AhaMoment.Role "user"}} by Human{{else}} by AI{{end}}</span>
                </div>
                <div class="aha-inline-why">{{.AhaMoment.Why}}</div>
            </div>
            {{end}}
            {{if .ToolCall}}
            <details class="tool-details">
                <summary class="tool-summary">
                    <svg class="tool-icon" width="16" height="16" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg">
                        <path d="M6 3L11 8L6 13" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                    </svg>
                    <span class="tool-name">{{if .ToolCall.Summary}}{{.ToolCall.Summary}}{{else}}{{.ToolCall.Name}}{{end}}</span>
                </summary>
                <div class="tool-io">
                    {{if .ToolCall.Input}}
                    <div class="tool-section">
                        <h4 class="tool-section-title">Input</h4>
                        <pre class="tool-input">{{.ToolCall.Input}}</pre>
                    </div>
                    {{end}}
                    {{if .ToolCall.Output}}
                    <div class="tool-section">
                        <h4 class="tool-section-title">Output</h4>
                        {{if isDiffOutput .ToolCall.Output}}<pre class="tool-output">{{renderDiffHTML .ToolCall.Output}}</pre>{{else}}<pre class="tool-output">{{.ToolCall.Output}}</pre>{{end}}
                    </div>
                    {{end}}
                </div>
            </details>
            {{end}}
            {{end}}
        </article>
        {{end}}
    </main>

    <footer class="page-footer">
        <div class="footer-content">
            <a href="https://sageox.ai/" class="footer-brand" target="_blank" rel="noopener noreferrer">
                <img class="logo-small" src="https://avatars.githubusercontent.com/u/224450799?s=48" alt="SageOx" width="24" height="24">
                <span>Powered by SageOx</span>
            </a>
        </div>
    </footer>

    <script>{{.Scripts}}</script>
</body>
</html>
`
