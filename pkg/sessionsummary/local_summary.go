package sessionsummary

import (
	"fmt"
	"strings"
)

// localSummaryTopicMaxLen is the max runes to keep from the first user message
// when extracting a topic hint for the local summary.
const localSummaryTopicMaxLen = 120

// LocalSummary generates a simple local summary without API call.
// Used as fallback when API is unavailable.
// Extracts the first substantive user message as a topic hint so the summary
// conveys what the session was about, not just stats.
func LocalSummary(entries []Entry) string {
	if len(entries) == 0 {
		return "Empty session"
	}

	// count message types and find first substantive user message
	var userCount, assistantCount, toolCount int
	var tools []string
	toolSet := make(map[string]bool)
	var firstUserMsg string

	for _, e := range entries {
		switch e.Type {
		case EntryTypeUser:
			userCount++
			if firstUserMsg == "" {
				msg := strings.TrimSpace(e.Content)
				// skip skill invocations (e.g. "/ox-session-start") as topic hints
				if len(msg) > 0 && !isSkillInvocation(msg) {
					firstUserMsg = msg
				}
			}
		case EntryTypeAssistant:
			assistantCount++
		case EntryTypeTool:
			toolCount++
			if e.ToolName != "" && !toolSet[e.ToolName] {
				toolSet[e.ToolName] = true
				tools = append(tools, e.ToolName)
			}
		}
	}

	var sb strings.Builder

	// lead with topic hint from first user message
	if firstUserMsg != "" {
		topic := extractTopicHint(firstUserMsg)
		if topic != "" {
			sb.WriteString(topic)
			sb.WriteString("\n\n")
		}
	}

	// stats line
	var parts []string
	parts = append(parts, fmt.Sprintf("%d user messages, %d assistant responses", userCount, assistantCount))
	if toolCount > 0 {
		parts = append(parts, fmt.Sprintf("%d tool calls", toolCount))
	}
	if len(tools) > 0 {
		if len(tools) > 5 {
			parts = append(parts, fmt.Sprintf("Tools: %s, and %d more", strings.Join(tools[:5], ", "), len(tools)-5))
		} else {
			parts = append(parts, fmt.Sprintf("Tools: %s", strings.Join(tools, ", ")))
		}
	}
	sb.WriteString(strings.Join(parts, ". "))

	return sb.String()
}

// extractTopicHint takes the first user message and returns a concise topic line.
// Strips markdown headers, takes only the first line/sentence, and truncates.
func extractTopicHint(msg string) string {
	// take first non-empty line
	var line string
	for _, l := range strings.Split(msg, "\n") {
		l = strings.TrimSpace(l)
		// skip markdown headers and empty lines
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		line = l
		break
	}
	if line == "" {
		return ""
	}

	// truncate to max runes, preserving word boundaries
	runes := []rune(line)
	if len(runes) > localSummaryTopicMaxLen {
		// find last space before limit
		truncAt := localSummaryTopicMaxLen
		for i := localSummaryTopicMaxLen; i > localSummaryTopicMaxLen/2; i-- {
			if runes[i] == ' ' {
				truncAt = i
				break
			}
		}
		line = string(runes[:truncAt]) + "\u2026"
	}

	return line
}

// isSkillInvocation returns true if the message looks like a slash-command
// skill invocation (e.g. "/ox-session-start", "/commit"). These are not
// meaningful topic hints for session summaries.
func isSkillInvocation(msg string) bool {
	first := strings.SplitN(msg, "\n", 2)[0]
	first = strings.TrimSpace(first)
	return strings.HasPrefix(first, "/")
}
