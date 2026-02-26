package html

import (
	"fmt"
	"sort"
	"strings"
)

// GroupIntoChapters converts a flat message list into a structured chapter view.
// Each chapter groups a conversation turn (user message + assistant responses)
// with tool/system calls collapsed into work blocks between conversation messages.
//
// chapterTitles are optional LLM-generated titles; when provided, they override
// heuristic titles. Titles are matched by position (first title → first chapter).
func GroupIntoChapters(messages []MessageView, chapterTitles []string) []ChapterView {
	if len(messages) == 0 {
		return nil
	}

	// strip pre-conversation noise (system messages, tool calls before first user prompt)
	messages = stripPreamble(messages)

	// first pass: build interleaved items (conversation messages + work blocks)
	items := groupIntoItems(messages)
	if len(items) == 0 {
		return nil
	}

	// merge consecutive short assistant messages between work blocks
	items = mergeShortAssistantBlocks(items)

	// second pass: split items into chapters at each user message
	chapters := splitIntoChapters(items, chapterTitles)

	return chapters
}

// stripPreamble removes all messages before the first real user message.
// Session recordings start with noise: /ox-session-start, system context,
// skill expansions, /clear, /context commands, and tool setup calls.
func stripPreamble(messages []MessageView) []MessageView {
	for i, msg := range messages {
		if msg.Type == "user" && isRealUserMessage(msg) {
			return messages[i:]
		}
	}
	return messages
}

// isRealUserMessage returns true if a user message is genuine human input,
// not a slash command, skill invocation, or framework-injected message.
func isRealUserMessage(msg MessageView) bool {
	text := strings.TrimSpace(string(msg.Content))
	if text == "" {
		return false
	}

	// strip leading HTML tags (content is rendered markdown)
	plain := text
	for strings.HasPrefix(plain, "<") {
		idx := strings.Index(plain, ">")
		if idx < 0 {
			break
		}
		plain = strings.TrimSpace(plain[idx+1:])
	}

	// slash commands: /ox-session-start, /clear, /context, /help, etc.
	if strings.HasPrefix(plain, "/") {
		return false
	}

	// system-reminder injections
	if strings.Contains(text, "&lt;system-reminder&gt;") || strings.Contains(text, "<system-reminder>") {
		return false
	}

	// ox-hash markers from skill expansions
	if strings.Contains(text, "ox-hash:") || strings.Contains(text, "&lt;!-- ox-hash:") {
		return false
	}

	return true
}

// groupIntoItems walks messages and groups consecutive tool/system/info entries
// into work blocks, keeping user/assistant messages as standalone items.
func groupIntoItems(messages []MessageView) []ChapterItem {
	items := make([]ChapterItem, 0, len(messages)/2)
	var currentBlock []MessageView

	flushBlock := func() {
		if len(currentBlock) == 0 {
			return
		}
		wb := buildWorkBlock(currentBlock)
		items = append(items, ChapterItem{
			IsWorkBlock: true,
			WorkBlock:   &wb,
		})
		currentBlock = nil
	}

	for i := range messages {
		msg := &messages[i]

		if isConversationMessage(msg) {
			flushBlock()
			m := *msg // copy
			items = append(items, ChapterItem{
				Message: &m,
			})
		} else {
			currentBlock = append(currentBlock, *msg)
		}
	}

	flushBlock()
	return items
}

// isConversationMessage returns true if the message is part of the human-AI dialog
// (user or assistant with meaningful content), not a tool/system/info entry.
func isConversationMessage(msg *MessageView) bool {
	switch msg.Type {
	case "user", "assistant":
		// only count as conversation if there's actual text content
		// (some assistant entries are just tool call wrappers with no prose)
		return strings.TrimSpace(string(msg.Content)) != ""
	default:
		return false
	}
}

// buildWorkBlock creates a WorkBlockView from a group of tool/system messages.
func buildWorkBlock(messages []MessageView) WorkBlockView {
	wb := WorkBlockView{
		Messages:   messages,
		ToolCounts: make(map[string]int),
	}

	for i := range messages {
		msg := &messages[i]
		if msg.ToolCall != nil && msg.ToolCall.Name != "" {
			name := msg.ToolCall.Name
			wb.ToolCounts[name]++
			wb.TotalTools++

			nameLower := strings.ToLower(name)
			if nameLower == "edit" || nameLower == "write" || nameLower == "multiedit" {
				wb.HasEdits = true
			}
		} else if msg.Type == "tool" {
			wb.TotalTools++
		}
	}

	wb.Summary = FormatWorkBlockSummary(&wb)
	return wb
}

// FormatWorkBlockSummary creates a human-readable summary like
// "12 tool calls (Read 5, Grep 4, Bash 3)".
func FormatWorkBlockSummary(wb *WorkBlockView) string {
	if wb == nil {
		return ""
	}
	if wb.TotalTools == 0 {
		count := len(wb.Messages)
		if count == 0 {
			return ""
		}
		return fmt.Sprintf("%d system %s", count, pluralize(count, "message", "messages"))
	}

	// sort tool names by count (descending), then alphabetically
	type toolCount struct {
		name  string
		count int
	}
	var sorted []toolCount
	for name, count := range wb.ToolCounts {
		sorted = append(sorted, toolCount{name, count})
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].count != sorted[j].count {
			return sorted[i].count > sorted[j].count
		}
		return sorted[i].name < sorted[j].name
	})

	// build breakdown (top 4 tools)
	var parts []string
	for i, tc := range sorted {
		if i >= 4 {
			remaining := 0
			for _, r := range sorted[i:] {
				remaining += r.count
			}
			parts = append(parts, fmt.Sprintf("+%d more", remaining))
			break
		}
		parts = append(parts, fmt.Sprintf("%s %d", tc.name, tc.count))
	}

	return fmt.Sprintf("%d tool %s (%s)",
		wb.TotalTools,
		pluralize(wb.TotalTools, "call", "calls"),
		strings.Join(parts, ", "))
}

// mergeShortAssistantBlocks absorbs short assistant messages (< 200 chars) that
// appear between two work blocks into the surrounding work block. This reduces
// visual noise when Claude Code makes many small one-line tweaks between tool calls.
//
// Pattern: [work_block] [short_assistant] [work_block] → [merged_work_block]
// The short assistant text is kept as a narrative note inside the work block.
func mergeShortAssistantBlocks(items []ChapterItem) []ChapterItem {
	const shortThreshold = 200

	result := make([]ChapterItem, 0, len(items))
	for i := 0; i < len(items); i++ {
		item := items[i]

		// check if this is a short assistant message between two work blocks
		if !item.IsWorkBlock && item.Message != nil && item.Message.Type == "assistant" {
			textLen := len(strings.TrimSpace(string(item.Message.Content)))
			if textLen > 0 && textLen < shortThreshold {
				// check: prev is work block AND next is work block
				prevIsWB := len(result) > 0 && result[len(result)-1].IsWorkBlock
				nextIsWB := i+1 < len(items) && items[i+1].IsWorkBlock
				if prevIsWB && nextIsWB {
					// absorb: add assistant msg to prev work block, then merge next work block
					prevWB := result[len(result)-1].WorkBlock
					prevWB.Messages = append(prevWB.Messages, *item.Message)
					// merge next work block into prev
					nextWB := items[i+1].WorkBlock
					prevWB.Messages = append(prevWB.Messages, nextWB.Messages...)
					for name, count := range nextWB.ToolCounts {
						prevWB.ToolCounts[name] += count
					}
					prevWB.TotalTools += nextWB.TotalTools
					prevWB.HasEdits = prevWB.HasEdits || nextWB.HasEdits
					prevWB.Summary = FormatWorkBlockSummary(prevWB)
					i++ // skip next work block (already merged)
					continue
				}
			}
		}

		result = append(result, item)
	}
	return result
}

// splitIntoChapters splits items into chapters, breaking at each user message.
func splitIntoChapters(items []ChapterItem, chapterTitles []string) []ChapterView {
	var chapters []ChapterView
	var currentItems []ChapterItem
	chapterNum := 0

	flush := func() {
		if len(currentItems) == 0 {
			return
		}
		chapterNum++
		title := autoChapterTitle(currentItems, chapterNum)
		if chapterNum-1 < len(chapterTitles) && chapterTitles[chapterNum-1] != "" {
			title = chapterTitles[chapterNum-1]
		}
		chapters = append(chapters, ChapterView{
			ID:    chapterNum,
			Title: title,
			Items: currentItems,
		})
		currentItems = nil
	}

	for _, item := range items {
		// start a new chapter at each user message (but not for the very first item)
		if !item.IsWorkBlock && item.Message != nil && item.Message.Type == "user" && len(currentItems) > 0 {
			flush()
		}
		currentItems = append(currentItems, item)
	}

	flush()
	return chapters
}

// autoChapterTitle generates a heuristic chapter title based on the content
// of a chapter's items. Analyzes tool usage patterns to determine the phase.
func autoChapterTitle(items []ChapterItem, chapterNum int) string {
	var readCount, editCount, bashCount, testCount int

	for _, item := range items {
		if !item.IsWorkBlock || item.WorkBlock == nil {
			continue
		}
		for name, count := range item.WorkBlock.ToolCounts {
			switch strings.ToLower(name) {
			case "read", "glob", "grep", "webfetch", "websearch":
				readCount += count
			case "edit", "write", "multiedit":
				editCount += count
			case "bash":
				bashCount += count
				for _, msg := range item.WorkBlock.Messages {
					if msg.ToolCall != nil && strings.ToLower(msg.ToolCall.Name) == "bash" {
						if isTestCommand(msg.ToolCall.Input) {
							testCount++
						}
					}
				}
			}
		}
	}

	totalTools := readCount + editCount + bashCount

	// no tools: probably pure conversation
	if totalTools == 0 {
		if chapterNum == 1 {
			return "Discussion"
		}
		return fmt.Sprintf("Turn %d", chapterNum)
	}

	// "Testing" only when test commands are the dominant bash activity
	// AND there aren't many reads/edits happening (which indicate exploration/implementation)
	if testCount > 0 && testCount > bashCount/2 && readCount < editCount && editCount < bashCount {
		return "Testing"
	}

	// reads dominate: exploration / investigation
	if readCount > editCount*2 && readCount > bashCount {
		return "Exploration"
	}

	// edits dominate: implementation
	if editCount > readCount && editCount > bashCount {
		return "Implementation"
	}

	// bash dominates with no significant reads or edits
	if bashCount > readCount && bashCount > editCount {
		if testCount > bashCount/2 {
			return "Testing"
		}
		return "Execution"
	}

	// mixed activity — use a generic but informative label
	if readCount > 0 && editCount > 0 {
		return "Implementation"
	}

	return fmt.Sprintf("Turn %d", chapterNum)
}

// isTestCommand checks if a bash command is specifically a test runner invocation,
// not just any command that happens to contain the word "test".
func isTestCommand(input string) bool {
	cmd := strings.ToLower(strings.TrimSpace(input))
	// match explicit test runner commands
	testPrefixes := []string{
		"go test", "pytest", "jest", "vitest", "make test",
		"npm test", "npm run test", "yarn test", "pnpm test",
		"cargo test", "mix test", "rspec", "phpunit",
	}
	for _, prefix := range testPrefixes {
		if strings.HasPrefix(cmd, prefix) {
			return true
		}
	}
	return false
}

// ExtractFilesChanged scans messages for Edit/Write/MultiEdit tool calls
// and extracts the files that were modified along with diff stats.
func ExtractFilesChanged(messages []MessageView) []FileChangeView {
	seen := make(map[string]*FileChangeView)

	for i := range messages {
		msg := &messages[i]
		if msg.ToolCall == nil {
			continue
		}
		name := strings.ToLower(msg.ToolCall.Name)
		if name != "edit" && name != "write" && name != "multiedit" {
			continue
		}

		path := ExtractFilePathFromInput(msg.ToolCall.Input)
		if path == "" {
			continue
		}
		shortPath := shortenPath(path)

		if existing, ok := seen[shortPath]; ok {
			a, r := CountDiffLines(msg.ToolCall.Output)
			existing.Added += a
			existing.Removed += r
		} else {
			a, r := CountDiffLines(msg.ToolCall.Output)
			seen[shortPath] = &FileChangeView{
				Path:    shortPath,
				Added:   a,
				Removed: r,
			}
		}
	}

	// convert to sorted slice (by path)
	result := make([]FileChangeView, 0, len(seen))
	for _, fc := range seen {
		result = append(result, *fc)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Path < result[j].Path
	})

	return result
}

func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}
