package session

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// filePathRegex matches common file extensions in content.
var filePathRegex = regexp.MustCompile(`[/\w.-]+\.(jsonl|json|yaml|yml|bash|html|toml|go|py|ts|js|md|txt|sh|sql|css|xml)`)

// SummaryMarkdownGenerator generates markdown summaries from session data.
type SummaryMarkdownGenerator struct{}

// SummaryView contains the LLM-generated summary content.
type SummaryView struct {
	Text        string   // one paragraph executive summary
	KeyActions  []string // bullet points of key actions taken
	Outcome     string   // success/partial/failed
	TopicsFound []string // topics detected during session
	FinalPlan   string   // final plan/architecture from session
	Diagrams    []string // extracted mermaid diagrams
}

// FileModification represents a file change detected in the session.
type FileModification struct {
	Path   string // file path (relative or absolute)
	Action string // Created, Modified, Deleted
}

// NewSummaryMarkdownGenerator creates a new markdown generator.
func NewSummaryMarkdownGenerator() *SummaryMarkdownGenerator {
	return &SummaryMarkdownGenerator{}
}

// Generate creates a summary markdown from metadata and summary view.
// The entries parameter is used to extract file modifications and diagrams.
func (g *SummaryMarkdownGenerator) Generate(meta *StoreMeta, summary *SummaryView, entries []map[string]any) ([]byte, error) {
	var buf bytes.Buffer

	// header
	buf.WriteString("# Session Summary\n\n")

	// metadata section
	g.writeMetadata(&buf, meta, summary)

	// summary text
	if summary != nil && summary.Text != "" {
		buf.WriteString("## Summary\n\n")
		buf.WriteString(summary.Text)
		buf.WriteString("\n\n")
	}

	// key actions
	if summary != nil && len(summary.KeyActions) > 0 {
		g.writeKeyActions(&buf, summary.KeyActions)
	}

	// diagrams section - prominently displayed
	diagrams := g.collectDiagrams(summary, entries)
	if len(diagrams) > 0 {
		g.writeDiagrams(&buf, diagrams)
	}

	// final plan section
	if summary != nil && summary.FinalPlan != "" {
		g.writeFinalPlan(&buf, summary.FinalPlan)
	}

	// file modifications
	mods := g.extractFileModifications(entries)
	if len(mods) > 0 {
		g.writeFileModifications(&buf, mods)
	}

	// topics
	if summary != nil && len(summary.TopicsFound) > 0 {
		g.writeTopics(&buf, summary.TopicsFound)
	}

	return buf.Bytes(), nil
}

// collectDiagrams gathers diagrams from summary and entries.
func (g *SummaryMarkdownGenerator) collectDiagrams(summary *SummaryView, entries []map[string]any) []string {
	seen := make(map[string]bool)
	var diagrams []string

	// first add diagrams from summary (if API returned them)
	if summary != nil {
		for _, d := range summary.Diagrams {
			if !seen[d] {
				seen[d] = true
				diagrams = append(diagrams, d)
			}
		}
	}

	// then extract from entries (local extraction)
	for _, d := range ExtractDiagramsFromEntries(entries) {
		if !seen[d] {
			seen[d] = true
			diagrams = append(diagrams, d)
		}
	}

	return diagrams
}

// writeDiagrams writes the diagrams section with rendered mermaid.
func (g *SummaryMarkdownGenerator) writeDiagrams(buf *bytes.Buffer, diagrams []string) {
	buf.WriteString("## Diagrams\n\n")

	for i, diagram := range diagrams {
		if i > 0 {
			buf.WriteString("\n---\n\n")
		}

		// render mermaid to ASCII for text compatibility
		ascii, err := RenderMermaidToASCII(diagram)
		if err == nil && ascii != "" {
			buf.WriteString("```\n")
			buf.WriteString(strings.TrimSpace(ascii))
			buf.WriteString("\n```\n\n")
		}

		// also include original mermaid source (collapsible)
		buf.WriteString("<details>\n<summary>Mermaid Source</summary>\n\n")
		buf.WriteString("```mermaid\n")
		buf.WriteString(diagram)
		buf.WriteString("\n```\n\n")
		buf.WriteString("</details>\n\n")
	}
}

// writeFinalPlan writes the final plan section.
func (g *SummaryMarkdownGenerator) writeFinalPlan(buf *bytes.Buffer, plan string) {
	buf.WriteString("## Final Plan\n\n")

	// process any mermaid blocks in the plan
	processed := ProcessMermaidBlocks(plan)
	buf.WriteString(processed)
	buf.WriteString("\n\n")
}

// writeMetadata writes the session metadata section.
func (g *SummaryMarkdownGenerator) writeMetadata(buf *bytes.Buffer, meta *StoreMeta, summary *SummaryView) {
	buf.WriteString("## Session Info\n\n")

	hasContent := false

	if meta != nil {
		// date
		if !meta.CreatedAt.IsZero() {
			fmt.Fprintf(buf, "- **Date:** %s\n", meta.CreatedAt.Format("2006-01-02 15:04 MST"))
			hasContent = true
		}

		// agent info
		if meta.AgentType != "" {
			agentInfo := meta.AgentType
			if meta.AgentVersion != "" {
				agentInfo += " " + meta.AgentVersion
			}
			fmt.Fprintf(buf, "- **Agent:** %s\n", agentInfo)
			hasContent = true
		}

		// model
		if meta.Model != "" {
			fmt.Fprintf(buf, "- **Model:** %s\n", meta.Model)
			hasContent = true
		}

		// user
		if meta.Username != "" {
			fmt.Fprintf(buf, "- **User:** %s\n", meta.Username)
			hasContent = true
		}

		// agent ID
		if meta.AgentID != "" {
			fmt.Fprintf(buf, "- **Session ID:** %s\n", meta.AgentID)
			hasContent = true
		}
	}

	// outcome (from summary, not meta)
	if summary != nil && summary.Outcome != "" {
		fmt.Fprintf(buf, "- **Outcome:** %s\n", summary.Outcome)
		hasContent = true
	}

	if !hasContent {
		buf.WriteString("_No metadata available_\n")
	}

	buf.WriteString("\n")
}

// writeKeyActions writes the key actions as a bullet list.
func (g *SummaryMarkdownGenerator) writeKeyActions(buf *bytes.Buffer, actions []string) {
	buf.WriteString("## Key Actions\n\n")
	for _, action := range actions {
		fmt.Fprintf(buf, "- %s\n", action)
	}
	buf.WriteString("\n")
}

// writeFileModifications writes the file modifications table.
func (g *SummaryMarkdownGenerator) writeFileModifications(buf *bytes.Buffer, mods []FileModification) {
	buf.WriteString("## Files Modified\n\n")
	buf.WriteString("| Path | Action |\n")
	buf.WriteString("|------|--------|\n")

	for _, mod := range mods {
		// escape pipe characters in path
		path := strings.ReplaceAll(mod.Path, "|", "\\|")
		fmt.Fprintf(buf, "| `%s` | %s |\n", path, mod.Action)
	}
	buf.WriteString("\n")
}

// writeTopics writes topics as inline tags.
func (g *SummaryMarkdownGenerator) writeTopics(buf *bytes.Buffer, topics []string) {
	buf.WriteString("## Topics\n\n")

	var tags []string
	for _, topic := range topics {
		tags = append(tags, fmt.Sprintf("`%s`", topic))
	}
	buf.WriteString(strings.Join(tags, " "))
	buf.WriteString("\n")
}

// extractFileModifications scans entries for file edit and command events.
// Returns deduplicated list of file modifications.
func (g *SummaryMarkdownGenerator) extractFileModifications(entries []map[string]any) []FileModification {
	seen := make(map[string]string) // path -> action (keeps last action)

	for _, entry := range entries {
		entryType, _ := entry["type"].(string)

		switch entryType {
		case "file_edited":
			if file, ok := entry["file"].(string); ok && file != "" {
				seen[file] = "Modified"
			} else if summary, ok := entry["summary"].(string); ok {
				if path := summaryExtractPath(summary); path != "" {
					seen[path] = "Modified"
				}
			}

		case "command_run":
			// look for file creation/deletion in command output
			if summary, ok := entry["summary"].(string); ok {
				g.parseCommandForFiles(summary, seen)
			}

		case "tool":
			// raw tool entries from stored session
			g.parseToolEntry(entry, seen)
		}
	}

	// convert to sorted slice
	var mods []FileModification
	for path, action := range seen {
		mods = append(mods, FileModification{Path: path, Action: action})
	}

	// sort by path for consistent output
	sort.Slice(mods, func(i, j int) bool {
		return mods[i].Path < mods[j].Path
	})

	return mods
}

// parseToolEntry extracts file modifications from raw tool entries.
func (g *SummaryMarkdownGenerator) parseToolEntry(entry map[string]any, seen map[string]string) {
	// extract data if nested
	data, ok := entry["data"].(map[string]any)
	if !ok {
		data = entry
	}

	toolName, _ := data["tool_name"].(string)
	toolNameLower := strings.ToLower(toolName)

	// write/edit tools indicate file modifications
	if toolNameLower == "write" || toolNameLower == "edit" {
		path := g.extractPathFromToolInput(data)
		if path != "" {
			action := "Modified"
			// write tool typically creates new files or overwrites
			if toolNameLower == "write" {
				action = "Created"
			}
			// if we've already seen this file, it's being modified again
			if _, exists := seen[path]; exists {
				action = "Modified"
			}
			seen[path] = action
		}
	}

	// bash commands might create/delete files
	if toolNameLower == "bash" {
		if input, ok := data["tool_input"].(string); ok {
			g.parseCommandForFiles(input, seen)
		}
	}
}

// extractPathFromToolInput extracts the file path from tool input data.
func (g *SummaryMarkdownGenerator) extractPathFromToolInput(data map[string]any) string {
	// try tool_input as string first
	if input, ok := data["tool_input"].(string); ok {
		return summaryExtractPathFromContent(input)
	}

	// try content field
	if content, ok := data["content"].(string); ok {
		return summaryExtractPathFromContent(content)
	}

	return ""
}

// parseCommandForFiles detects file operations in shell commands.
func (g *SummaryMarkdownGenerator) parseCommandForFiles(cmd string, seen map[string]string) {
	cmdLower := strings.ToLower(cmd)

	// detect mkdir - not tracking directory creation as file modification
	if strings.Contains(cmdLower, "mkdir ") {
		return
	}

	// detect touch (creates files)
	if strings.Contains(cmdLower, "touch ") {
		parts := strings.Fields(cmd)
		for i, part := range parts {
			if strings.ToLower(part) == "touch" && i+1 < len(parts) {
				for j := i + 1; j < len(parts); j++ {
					if !strings.HasPrefix(parts[j], "-") {
						seen[parts[j]] = "Created"
					}
				}
				break
			}
		}
	}

	// detect rm (deletes files)
	if strings.Contains(cmdLower, "rm ") {
		parts := strings.Fields(cmd)
		for i, part := range parts {
			if strings.ToLower(part) == "rm" && i+1 < len(parts) {
				for j := i + 1; j < len(parts); j++ {
					arg := parts[j]
					if !strings.HasPrefix(arg, "-") {
						seen[arg] = "Deleted"
					}
				}
				break
			}
		}
	}

	// detect mv (renames/moves)
	if strings.Contains(cmdLower, "mv ") {
		parts := strings.Fields(cmd)
		for i, part := range parts {
			if strings.ToLower(part) == "mv" && i+2 < len(parts) {
				// skip flags
				srcIdx := i + 1
				for srcIdx < len(parts) && strings.HasPrefix(parts[srcIdx], "-") {
					srcIdx++
				}
				if srcIdx+1 < len(parts) {
					seen[parts[srcIdx]] = "Deleted"
					seen[parts[srcIdx+1]] = "Created"
				}
				break
			}
		}
	}
}

// summaryExtractPath extracts a file path from a summary string.
func summaryExtractPath(summary string) string {
	// look for "edited <path>" pattern
	if strings.HasPrefix(strings.ToLower(summary), "edited ") {
		return strings.TrimPrefix(summary, "edited ")
	}
	return summaryExtractPathFromContent(summary)
}

// summaryExtractPathFromContent extracts a file path from content using the regex.
func summaryExtractPathFromContent(content string) string {
	match := filePathRegex.FindString(content)
	if match != "" {
		// clean up path
		if strings.HasPrefix(match, "/") || !strings.Contains(match, "/") {
			return match
		}
		// handle potential prefix junk
		if idx := strings.LastIndex(match, "/"); idx > 0 {
			// find start of actual path
			for i := idx - 1; i >= 0; i-- {
				if match[i] == ' ' || match[i] == '"' || match[i] == '\'' {
					return match[i+1:]
				}
			}
		}
	}
	return match
}
