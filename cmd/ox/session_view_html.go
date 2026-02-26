package main

import (
	"errors"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/session"
	sessionhtml "github.com/sageox/ox/internal/session/html"
	"github.com/sageox/ox/internal/theme"
)

var (
	viewHTMLRecordingBanner = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#000000")).
		Background(cli.ColorWarning).
		Padding(0, 1)
)

// viewAsHTML renders a session as HTML and opens in browser.
func viewAsHTML(_ *session.Store, storedSession *session.StoredSession, projectRoot string) error {
	// check if recording is in progress
	isRecording := false
	if projectRoot != "" {
		isRecording = session.IsRecording(projectRoot)
	}

	// determine HTML path (session.html in the session directory)
	htmlPath := filepath.Join(filepath.Dir(storedSession.Info.FilePath), "session.html")

	// check if HTML exists and is up-to-date
	needsGeneration := false
	htmlInfo, err := os.Stat(htmlPath)
	if os.IsNotExist(err) {
		needsGeneration = true
	} else if err == nil {
		// HTML exists - check if it's stale (JSONL is newer)
		jsonlInfo, jsonlErr := os.Stat(storedSession.Info.FilePath)
		if jsonlErr == nil && jsonlInfo.ModTime().After(htmlInfo.ModTime()) {
			needsGeneration = true
			fmt.Println(cli.StyleDim.Render("  HTML is stale, regenerating..."))
		}
	}

	if needsGeneration {
		// show recording warning if applicable
		if isRecording {
			fmt.Println(viewHTMLRecordingBanner.Render(" RECORDING IN PROGRESS "))
			fmt.Println(cli.StyleDim.Render("  HTML may be incomplete. Final version generated when recording stops."))
			fmt.Println()
		}

		fmt.Println(cli.StyleDim.Render("  Generating HTML viewer..."))

		if err := viewHTMLGenerate(storedSession, htmlPath); err != nil {
			return fmt.Errorf("generate HTML: %w", err)
		}

		cli.PrintSuccess(fmt.Sprintf("Generated: %s", cli.StyleFile.Render(htmlPath)))
	}

	// open in browser
	if err := cli.OpenInBrowser("file://" + htmlPath); err != nil {
		if errors.Is(err, cli.ErrHeadless) {
			cli.PrintSuccess(fmt.Sprintf("Generated: %s", cli.StyleFile.Render(htmlPath)))
			fmt.Println()
			cli.PrintWarning("Cannot open browser in this environment (SSH/headless)")
			fmt.Println()
			cli.PrintHint("To view online, upload the session first: ox session stop")
			cli.PrintHint("Or copy the HTML file to a machine with a browser:")
			cli.PrintHint(fmt.Sprintf("  scp %s <local-machine>:", htmlPath))
			return nil
		}
		return fmt.Errorf("open browser: %w", err)
	}

	cli.PrintSuccess(fmt.Sprintf("Opened in browser: %s", cli.StyleFile.Render(filepath.Base(htmlPath))))
	return nil
}

// viewAsWeb opens a session in the web viewer.
// Requires the session to be pushed to the ledger (meta.json committed).
// Takes only a session name — does not need full session content.
func viewAsWeb(sessionName string, projectRoot string) error {
	if sessionName == "" {
		return fmt.Errorf("no session name provided\n\nUse --html to view a local session file")
	}

	if projectRoot == "" {
		return fmt.Errorf("not in a project directory\n\nUse --html to view locally")
	}

	cfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil || cfg.RepoID == "" || cfg.GetEndpoint() == "" {
		return fmt.Errorf("project not configured for web viewing (missing repo ID or endpoint)\n\nUse --html to view locally")
	}

	// verify the session's meta.json exists in the ledger
	ledgerPath, ledgerErr := resolveLedgerPath()
	if ledgerErr != nil {
		return fmt.Errorf("ledger not available: %w\n\nUse --html to view locally", ledgerErr)
	}
	metaPath := filepath.Join(ledgerPath, "sessions", sessionName, "meta.json")
	if _, err := os.Stat(metaPath); os.IsNotExist(err) {
		return fmt.Errorf("session %q has not been pushed to the ledger yet\n\nRun 'ox session stop' to finalize, or use --html to view locally", sessionName)
	}

	ep := endpoint.NormalizeEndpoint(cfg.GetEndpoint())
	url := fmt.Sprintf("%s/repo/%s/sessions/%s/view", ep, cfg.RepoID, sessionName)

	if err := cli.OpenInBrowser(url); err != nil {
		if errors.Is(err, cli.ErrHeadless) {
			fmt.Printf("View session at: %s\n", url)
			return nil
		}
		fmt.Printf("%s Could not open browser. Visit: %s\n", cli.StyleWarning.Render("!"), url)
		return nil
	}

	fmt.Printf("Opening %s\n", url)
	return nil
}

// viewHTMLGenerate creates an HTML file from a stored session.
// Delegates to generateHTML which has the full chapter/grouping logic.
func viewHTMLGenerate(t *session.StoredSession, outputPath string) error {
	return generateHTML(t, outputPath)
}

// viewHTMLBuildTemplateData converts a stored session to template data.
func viewHTMLBuildTemplateData(t *session.StoredSession) *sessionhtml.TemplateData {
	data := &sessionhtml.TemplateData{
		Title:       viewHTMLBuildTitle(t),
		BrandColors: sessionhtml.DefaultBrandColors(),
		Styles:      template.CSS(viewHTMLCSSRootVars() + viewHTMLEmbeddedStylesBase),
		Scripts:     template.JS(viewHTMLEmbeddedScripts),
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
				}
			}
		}
	}

	// derive sender labels from metadata
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
	var userMessages int
	for i, entry := range t.Entries {
		msg := viewHTMLBuildMessageView(i+1, entry, userLabel, agentLabel)
		data.Messages = append(data.Messages, msg)

		if msg.Type == "user" {
			userMessages++
		}
	}

	// build statistics
	data.Statistics = &sessionhtml.StatsView{
		TotalMessages: len(t.Entries),
		UserMessages:  userMessages,
	}

	return data
}

// viewHTMLBuildTitle generates a title from the session.
func viewHTMLBuildTitle(t *session.StoredSession) string {
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

// viewHTMLBuildMessageView converts a session entry to a message view.
func viewHTMLBuildMessageView(id int, entry map[string]any, userLabel, agentLabel string) sessionhtml.MessageView {
	msg := sessionhtml.MessageView{
		ID: id,
	}

	// get entry type
	entryType, _ := entry["type"].(string)
	msg.Type = viewHTMLMapEntryType(entryType)

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

	// get content based on entry type
	if data, ok := entry["data"].(map[string]any); ok {
		switch entryType {
		case "message":
			content, _ := data["content"].(string)
			msg.Content = sessionhtml.RenderMarkdown(content)
			if role, ok := data["role"].(string); ok {
				msg.Type = viewHTMLMapRoleToType(role)
			}

		case "tool_call":
			toolName, _ := data["tool_name"].(string)
			input, _ := data["input"].(string)
			msg.Type = "tool"
			msg.Content = template.HTML("<p>Tool call: " + template.HTMLEscapeString(toolName) + "</p>")
			msg.ToolCall = &sessionhtml.ToolCallView{
				Name:  toolName,
				Input: viewHTMLEscapeHTML(input),
			}
			msg.ToolCall.Summary = sessionhtml.FormatToolSummary(msg.ToolCall)

		case "tool_result":
			toolName, _ := data["tool_name"].(string)
			output, _ := data["output"].(string)
			msg.Type = "tool"
			msg.Content = template.HTML("<p>Tool result: " + template.HTMLEscapeString(toolName) + "</p>")
			msg.ToolCall = &sessionhtml.ToolCallView{
				Name:   toolName,
				Output: viewHTMLEscapeHTML(viewHTMLTruncateOutput(output, 10000)),
			}
			msg.ToolCall.Summary = sessionhtml.FormatToolSummary(msg.ToolCall)

		default:
			// generic entry
			if content, ok := data["content"].(string); ok {
				msg.Content = sessionhtml.RenderMarkdown(content)
			}
		}
	}

	// fall back to root-level content (imported JSONL / ox native format)
	if msg.Content == "" {
		if content, ok := entry["content"].(string); ok && content != "" {
			msg.Content = sessionhtml.RenderMarkdown(content)
		}
	}

	// reclassify based on content patterns (tool output or system context
	// that was incorrectly tagged as "user" in the session recording)
	// extract raw content from either root level or nested data
	rawContent, _ := entry["content"].(string)
	if rawContent == "" {
		if data, ok := entry["data"].(map[string]any); ok {
			rawContent, _ = data["content"].(string)
		}
	}
	msg.Type = reclassifyByContent(msg.Type, string(msg.Content), rawContent)

	// set sender label based on final type
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

	return msg
}

// viewHTMLMapEntryType converts session entry types to display types.
func viewHTMLMapEntryType(entryType string) string {
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

// viewHTMLMapRoleToType converts message roles to display types.
func viewHTMLMapRoleToType(role string) string {
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

// viewHTMLEscapeHTML escapes HTML special characters.
func viewHTMLEscapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&#39;")
	return s
}

// viewHTMLTruncateOutput truncates long output strings.
func viewHTMLTruncateOutput(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}


// viewHTMLCSSRootVars generates the :root CSS variables from theme constants.
func viewHTMLCSSRootVars() string {
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

// viewHTMLEmbeddedStylesBase contains the CSS for the HTML viewer (without :root).
const viewHTMLEmbeddedStylesBase = `
* {
  box-sizing: border-box;
  margin: 0;
  padding: 0;
}

body {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, sans-serif;
  background: var(--color-bg-dark);
  color: var(--color-text);
  line-height: 1.6;
  min-height: 100vh;
  container-type: inline-size;
}

body.embed .page-header { display: none; }
body.embed .page-footer { display: none; }
body.embed .messages-container { padding-top: 1rem; }

.page-header {
  background: linear-gradient(135deg, var(--color-bg-card) 0%, var(--color-bg-dark) 100%);
  border-bottom: 1px solid var(--color-border);
  padding: 2rem;
}

.header-content {
  max-width: 1200px;
  margin: 0 auto;
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 2rem;
  flex-wrap: wrap;
}

.brand {
  display: flex;
  align-items: center;
  gap: 1rem;
}

.logo-large {
  width: 48px;
  height: 48px;
}

.brand-text h1 {
  font-size: 1.5rem;
  font-weight: 600;
  color: var(--color-primary);
}

.brand-text .subtitle {
  font-size: 0.875rem;
  color: var(--color-text-dim);
}

.header-meta {
  text-align: right;
}

.session-title {
  font-size: 1.25rem;
  font-weight: 500;
  margin-bottom: 0.5rem;
}

.meta-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 0.5rem;
  font-size: 0.875rem;
}

.meta-item {
  color: var(--color-text-dim);
}

.meta-label {
  color: var(--color-text-dim);
}

.meta-value {
  color: var(--color-text);
}

.stats-bar {
  max-width: 1200px;
  margin: 1.5rem auto 0;
  display: flex;
  gap: 2rem;
  padding-top: 1rem;
  border-top: 1px solid var(--color-border);
}

.stat-item {
  text-align: center;
}

.stat-value {
  display: block;
  font-size: 1.5rem;
  font-weight: 600;
  color: var(--color-secondary);
}

.stat-label {
  font-size: 0.75rem;
  color: var(--color-text-dim);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

.messages-container {
  max-width: 1200px;
  margin: 0 auto;
  padding: 2rem;
}

.message {
  background: var(--color-bg-card);
  border: 1px solid var(--color-border);
  border-radius: 8px;
  margin-bottom: 1rem;
  overflow: hidden;
}

.message-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 0.75rem 1rem;
  background: rgba(0, 0, 0, 0.2);
  border-bottom: 1px solid var(--color-border);
}

.message-type {
  font-size: 0.75rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  padding: 0.25rem 0.5rem;
  border-radius: 4px;
}

.message-type-user {
  background: rgba(127, 167, 200, 0.2);
  color: var(--color-info);
}

.message-type-assistant {
  background: rgba(122, 143, 120, 0.2);
  color: var(--color-primary);
}

.message-type-system {
  background: rgba(224, 165, 106, 0.2);
  color: var(--color-secondary);
}

.message-type-tool {
  background: rgba(143, 168, 136, 0.2);
  color: var(--color-accent);
}

.message-type-info {
  background: rgba(126, 135, 132, 0.2);
  color: var(--color-text-dim);
}

.message-header time {
  font-size: 0.75rem;
  color: var(--color-text-dim);
}

.message-content {
  padding: 1rem;
  white-space: pre-wrap;
  word-break: break-word;
}

.tool-details {
  border-top: 1px solid var(--color-border);
}

.tool-summary {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.75rem 1rem;
  cursor: pointer;
  background: rgba(0, 0, 0, 0.1);
  user-select: none;
}

.tool-summary:hover {
  background: rgba(0, 0, 0, 0.2);
}

.tool-icon {
  width: 16px;
  height: 16px;
  max-width: 16px;
  max-height: 16px;
  flex-shrink: 0;
  color: var(--color-accent);
  transition: transform 0.2s;
}

details[open] .tool-icon {
  transform: rotate(90deg);
}

.tool-name {
  font-weight: 500;
  color: var(--color-accent);
}

.tool-io {
  padding: 1rem;
  background: rgba(0, 0, 0, 0.15);
}

.tool-section {
  margin-bottom: 1rem;
}

.tool-section:last-child {
  margin-bottom: 0;
}

.tool-section-title {
  font-size: 0.75rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--color-text-dim);
  margin-bottom: 0.5rem;
}

.tool-input,
.tool-output {
  background: var(--color-bg-dark);
  border: 1px solid var(--color-border);
  border-radius: 4px;
  padding: 0.75rem;
  font-family: 'SF Mono', Monaco, 'Cascadia Code', monospace;
  font-size: 0.8125rem;
  overflow-x: auto;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 400px;
  overflow-y: auto;
}

/* diff coloring in tool output */
.tool-output .diff-add { color: #85b881; }
.tool-output .diff-remove { color: #e07070; }
.tool-output .diff-header { color: #7fa7c8; font-weight: 600; }

/* collapsible message styling */
.message-collapsible { width: 100%; }
.message-collapsible > summary { list-style: none; display: flex; justify-content: space-between; align-items: center; padding: 0.75rem 1rem; cursor: pointer; }
.message-collapsible > summary::-webkit-details-marker { display: none; }
.message-collapsible > summary::marker { display: none; }
.message-collapsible > summary::before { content: '▶'; font-size: 0.7em; margin-right: 0.5rem; transition: transform 0.2s; color: var(--color-text-dim); }
.message-collapsible[open] > summary::before { transform: rotate(90deg); }

/* Mermaid diagram styling */
.mermaid {
  background: var(--color-bg-dark);
  border: 1px solid var(--color-border);
  border-radius: 8px;
  padding: 1.5rem;
  margin: 1rem 0;
  overflow-x: auto;
  text-align: center;
}

.mermaid svg {
  max-width: 100%;
  height: auto;
}

.page-footer {
  background: var(--color-bg-card);
  border-top: 1px solid var(--color-border);
  padding: 1.5rem 2rem;
  margin-top: 2rem;
}

.footer-content {
  max-width: 1200px;
  margin: 0 auto;
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.footer-brand {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  color: var(--color-text-dim);
  text-decoration: none;
  transition: color 0.2s;
}

.footer-brand:hover {
  color: var(--color-primary);
}

.logo-small {
  width: 24px;
  height: 24px;
}

.footer-meta {
  font-size: 0.75rem;
  color: var(--color-text-dim);
}

@container (max-width: 768px) {
  .header-content { flex-direction: column; }
  .header-meta { text-align: left; }
  .stats-bar { flex-wrap: wrap; gap: 1rem; }
  .stat-item { flex: 1 0 calc(50% - 0.5rem); }
  .footer-content { flex-direction: column; gap: 0.5rem; }
  .message-content { padding: 0.75rem; }
}

@media (max-width: 768px) {
  .header-content {
    flex-direction: column;
  }

  .header-meta {
    text-align: left;
  }

  .stats-bar {
    flex-wrap: wrap;
    gap: 1rem;
  }

  .stat-item {
    flex: 1 0 calc(50% - 0.5rem);
  }
}
`

// viewHTMLEmbeddedScripts contains the JavaScript for the HTML viewer.
const viewHTMLEmbeddedScripts = `
// initialize Mermaid diagrams
if (typeof mermaid !== 'undefined') {
  mermaid.initialize({
    startOnLoad: true,
    theme: 'dark',
    themeVariables: {
      primaryColor: getComputedStyle(document.documentElement).getPropertyValue('--color-primary').trim() || '#7a8f78',
      primaryTextColor: getComputedStyle(document.documentElement).getPropertyValue('--color-text').trim() || '#d4d4c8',
      primaryBorderColor: getComputedStyle(document.documentElement).getPropertyValue('--color-border').trim() || '#3a3e3a',
      lineColor: getComputedStyle(document.documentElement).getPropertyValue('--color-text-dim').trim() || '#7e8784',
      secondaryColor: getComputedStyle(document.documentElement).getPropertyValue('--color-secondary').trim() || '#e0a56a',
      tertiaryColor: getComputedStyle(document.documentElement).getPropertyValue('--color-bg-card').trim() || '#2a2e2a'
    },
    flowchart: { curve: 'basis' },
    sequence: { mirrorActors: false }
  });
}

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

// keyboard navigation
document.addEventListener('keydown', (e) => {
  if (e.key === 'j' || e.key === 'ArrowDown') {
    navigateMessage(1);
  } else if (e.key === 'k' || e.key === 'ArrowUp') {
    navigateMessage(-1);
  } else if (e.key === 'Enter' || e.key === ' ') {
    toggleCurrentDetails();
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
`

// viewHTMLTemplate is the HTML template for the viewer.
const viewHTMLTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Title}} - SageOx Session</title>
    <base target="_blank">
    <meta name="color-scheme" content="dark light">
    <style>{{.Styles}}</style>
    <script src="https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.min.js"></script>
</head>
<body>
    <script>(function(){var p=new URLSearchParams(window.location.search);if(p.get('embed')==='1')document.body.classList.add('embed');if(p.get('theme')==='light')document.body.classList.add('light-theme');})()</script>
    <header class="page-header">
        <div class="header-content">
            <div class="brand">
                <svg class="logo-large" width="48" height="48" viewBox="0 0 48 48" fill="none" xmlns="http://www.w3.org/2000/svg" aria-label="SageOx Logo">
                    <path d="M24 8C24 8 20 6 16 8C12 10 10 14 10 18C10 20 11 22 12 23C12 23 10 24 10 26C10 28 11 30 13 31C13 31 12 32 12 34C12 36 13 38 15 39C15 39 14 40 14 42C14 44 16 46 20 46C22 46 24 45 24 45C24 45 26 46 28 46C32 46 34 44 34 42C34 40 33 39 33 39C35 38 36 36 36 34C36 32 35 31 35 31C37 30 38 28 38 26C38 24 36 23 36 23C37 22 38 20 38 18C38 14 36 10 32 8C28 6 24 8 24 8Z" fill="var(--color-primary)"/>
                    <ellipse cx="18" cy="20" rx="2" ry="2.5" fill="var(--color-bg-dark)"/>
                    <ellipse cx="30" cy="20" rx="2" ry="2.5" fill="var(--color-bg-dark)"/>
                    <path d="M18 28C18 28 20 30 24 30C28 30 30 28 30 28" stroke="var(--color-bg-dark)" stroke-width="1.5" stroke-linecap="round"/>
                    <path d="M12 16C12 16 10 12 8 12C6 12 4 14 6 16C8 18 12 16 12 16Z" fill="var(--color-primary)"/>
                    <path d="M36 16C36 16 38 12 40 12C42 12 44 14 42 16C40 18 36 16 36 16Z" fill="var(--color-primary)"/>
                </svg>
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
        </div>
        {{end}}
    </header>

    <main id="messages" class="messages-container">
        {{range .Messages}}
        <article class="message message-{{.Type}}" id="msg-{{.ID}}" data-type="{{.Type}}" tabindex="0">
            {{if or (eq .Type "tool") (eq .Type "system") (eq .Type "info")}}
            <details class="message-collapsible">
                <summary class="message-header">
                    <span class="message-type message-type-{{.Type}}">{{.SenderLabel}}</span>
                </summary>
                <div class="message-content">{{.Content}}</div>
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
            </div>
            <div class="message-content">{{.Content}}</div>
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
                <svg class="logo-small" width="24" height="24" viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg" aria-label="SageOx">
                    <path d="M12 4C12 4 10 3 8 4C6 5 5 7 5 9C5 10 5.5 11 6 11.5C6 11.5 5 12 5 13C5 14 5.5 15 6.5 15.5C6.5 15.5 6 16 6 17C6 18 6.5 19 7.5 19.5C7.5 19.5 7 20 7 21C7 22 8 23 10 23C11 23 12 22.5 12 22.5C12 22.5 13 23 14 23C16 23 17 22 17 21C17 20 16.5 19.5 16.5 19.5C17.5 19 18 18 18 17C18 16 17.5 15.5 17.5 15.5C18.5 15 19 14 19 13C19 12 18 11.5 18 11.5C18.5 11 19 10 19 9C19 7 18 5 16 4C14 3 12 4 12 4Z" fill="var(--color-primary)"/>
                    <circle cx="9" cy="10" r="1" fill="var(--color-bg-dark)"/>
                    <circle cx="15" cy="10" r="1" fill="var(--color-bg-dark)"/>
                    <path d="M9 14C9 14 10 15 12 15C14 15 15 14 15 14" stroke="var(--color-bg-dark)" stroke-width="0.75" stroke-linecap="round"/>
                </svg>
                <span>Powered by SageOx</span>
            </a>
        </div>
    </footer>

    <script>{{.Scripts}}</script>
</body>
</html>
`
