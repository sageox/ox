package session

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// EnrichMessage represents a session entry for enrichment processing.
// Lighter than the old HTML MessageView — no markdown rendering or HTML escaping.
type EnrichMessage struct {
	ID          int
	Type        string // user, assistant, system, tool, info
	Content     string // raw text content (not HTML)
	ToolCall    *EnrichToolCall
	Timestamp   time.Time
	SenderLabel string
}

// EnrichToolCall holds tool invocation details for enrichment.
type EnrichToolCall struct {
	Name   string
	Input  string
	Output string
}

// EnrichChapterView groups conversation turns into a narrative section.
type EnrichChapterView struct {
	ID    int
	Title string
	Items []EnrichChapterItem
}

// EnrichChapterItem is either a conversation message or a work block.
type EnrichChapterItem struct {
	IsWorkBlock bool
	Message     *EnrichMessage
	WorkBlock   *EnrichWorkBlock
}

// EnrichWorkBlock groups consecutive tool/system calls.
type EnrichWorkBlock struct {
	Messages   []EnrichMessage
	ToolCounts map[string]int
	Summary    string
	TotalTools int
	HasEdits   bool
}

// EnrichFileChange represents a file modified during the session.
type EnrichFileChange struct {
	Path    string
	Added   int
	Removed int
}

// EnrichSummary populates computed fields (FilesChanged, Chapters) on a summary
// by building message views from raw session entries.
func EnrichSummary(stored *StoredSession, summary *SummarizeResponse) {
	if stored == nil || summary == nil {
		return
	}

	messages := buildEnrichMessages(stored)

	// extract files changed
	filesChanged := extractFilesChanged(messages)
	summary.FilesChanged = make([]FileSummary, 0, len(filesChanged))
	for _, fc := range filesChanged {
		summary.FilesChanged = append(summary.FilesChanged, FileSummary{
			Path:    fc.Path,
			Added:   fc.Added,
			Removed: fc.Removed,
		})
	}

	// group into chapters
	var chapterTitles []string
	if summary != nil {
		chapterTitles = summary.ChapterTitles
	}
	chapters := groupIntoChapters(messages, chapterTitles)
	summary.Chapters = make([]ChapterSummary, 0, len(chapters))
	for _, ch := range chapters {
		cs := ChapterSummary{
			ID:    ch.ID,
			Title: ch.Title,
		}
		toolCounts := make(map[string]int)
		for _, item := range ch.Items {
			if item.Message != nil {
				if cs.StartSeq == 0 {
					cs.StartSeq = item.Message.ID
				}
				cs.EndSeq = item.Message.ID
			}
			if item.WorkBlock != nil {
				cs.TotalTools += item.WorkBlock.TotalTools
				cs.HasEdits = cs.HasEdits || item.WorkBlock.HasEdits
				for name, count := range item.WorkBlock.ToolCounts {
					toolCounts[name] += count
				}
				for _, wbMsg := range item.WorkBlock.Messages {
					if cs.StartSeq == 0 {
						cs.StartSeq = wbMsg.ID
					}
					cs.EndSeq = wbMsg.ID
				}
			}
		}
		if len(toolCounts) > 0 {
			cs.ToolCounts = toolCounts
		}
		summary.Chapters = append(summary.Chapters, cs)
	}
}

// buildEnrichMessages converts stored session entries to EnrichMessage structs.
func buildEnrichMessages(stored *StoredSession) []EnrichMessage {
	userLabel := "User"
	agentLabel := "Assistant"
	if stored.Meta != nil {
		if stored.Meta.Username != "" {
			userLabel = stored.Meta.Username
		}
		if stored.Meta.AgentType != "" {
			agentLabel = formatAgentName(stored.Meta.AgentType)
		}
	}

	messages := make([]EnrichMessage, 0, len(stored.Entries))
	for i, entry := range stored.Entries {
		msg := buildEnrichMessage(i+1, entry, userLabel, agentLabel)

		// merge tool_result into the preceding tool_call instead of creating a duplicate
		entryType, _ := entry["type"].(string)
		if entryType == "tool_result" && msg.ToolCall != nil && len(messages) > 0 {
			prev := &messages[len(messages)-1]
			if prev.ToolCall != nil && prev.ToolCall.Name == msg.ToolCall.Name && prev.ToolCall.Output == "" {
				prev.ToolCall.Output = msg.ToolCall.Output
				continue
			}
		}

		messages = append(messages, msg)
	}
	return messages
}

// buildEnrichMessage converts a single session entry to an EnrichMessage.
func buildEnrichMessage(id int, entry map[string]any, userLabel, agentLabel string) EnrichMessage {
	msg := EnrichMessage{ID: id}

	entryType, _ := entry["type"].(string)
	msg.Type = enrichMapEntryType(entryType)

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

	// parse timestamp
	if ts, ok := entry["timestamp"].(string); ok {
		msg.Timestamp = parseTS(ts)
	} else if ts, ok := entry["ts"].(string); ok {
		msg.Timestamp = parseTS(ts)
	}

	// extract content (raw text, no HTML rendering)
	if content, ok := entry["content"].(string); ok && content != "" {
		msg.Content = content
	}

	// handle tool entries with root-level fields
	if entryType == "tool" {
		toolName, _ := entry["tool_name"].(string)
		toolInput, _ := entry["tool_input"].(string)
		toolOutput, _ := entry["tool_output"].(string)
		if toolName != "" {
			msg.ToolCall = &EnrichToolCall{
				Name:   toolName,
				Input:  stripANSI(toolInput),
				Output: stripANSI(toolOutput),
			}
		}
	}

	// handle nested data format
	if data, ok := entry["data"].(map[string]any); ok {
		switch entryType {
		case "message":
			if content, _ := data["content"].(string); content != "" {
				msg.Content = content
			}
			if role, ok := data["role"].(string); ok {
				msg.Type = enrichMapRoleToType(role)
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
			msg.ToolCall = &EnrichToolCall{
				Name:  toolName,
				Input: stripANSI(input),
			}
		case "tool_result":
			toolName, _ := data["tool_name"].(string)
			output, _ := data["output"].(string)
			msg.Type = "tool"
			msg.ToolCall = &EnrichToolCall{
				Name:   toolName,
				Output: stripANSI(output),
			}
		default:
			if msg.Content == "" {
				if content, ok := data["content"].(string); ok {
					msg.Content = content
				}
			}
		}
	}

	// reclassify based on content patterns
	msg.Type = enrichReclassifyByContent(msg.Type, msg.Content)
	switch msg.Type {
	case "tool":
		msg.SenderLabel = "Tool Call"
	case "system":
		msg.SenderLabel = "System"
	}

	return msg
}

// --- Grouping logic (moved from html/grouping.go) ---

// groupIntoChapters converts a flat message list into structured chapters.
func groupIntoChapters(messages []EnrichMessage, chapterTitles []string) []EnrichChapterView {
	if len(messages) == 0 {
		return nil
	}

	messages = enrichStripPreamble(messages)
	items := enrichGroupIntoItems(messages)
	if len(items) == 0 {
		return nil
	}

	items = enrichMergeShortAssistantBlocks(items)
	return enrichSplitIntoChapters(items, chapterTitles)
}

func enrichStripPreamble(messages []EnrichMessage) []EnrichMessage {
	for i, msg := range messages {
		if msg.Type == "user" && enrichIsRealUserMessage(msg) {
			return messages[i:]
		}
	}
	return messages
}

func enrichIsRealUserMessage(msg EnrichMessage) bool {
	text := strings.TrimSpace(msg.Content)
	if text == "" {
		return false
	}
	if strings.HasPrefix(text, "/") {
		return false
	}
	if strings.Contains(text, "<system-reminder>") {
		return false
	}
	if strings.Contains(text, "ox-hash:") || strings.Contains(text, "<!-- ox-hash:") {
		return false
	}
	return true
}

func enrichGroupIntoItems(messages []EnrichMessage) []EnrichChapterItem {
	items := make([]EnrichChapterItem, 0, len(messages)/2)
	var currentBlock []EnrichMessage

	flushBlock := func() {
		if len(currentBlock) == 0 {
			return
		}
		wb := enrichBuildWorkBlock(currentBlock)
		items = append(items, EnrichChapterItem{
			IsWorkBlock: true,
			WorkBlock:   &wb,
		})
		currentBlock = nil
	}

	for i := range messages {
		msg := &messages[i]
		if enrichIsConversationMessage(msg) {
			flushBlock()
			m := *msg
			items = append(items, EnrichChapterItem{Message: &m})
		} else {
			currentBlock = append(currentBlock, *msg)
		}
	}

	flushBlock()
	return items
}

func enrichIsConversationMessage(msg *EnrichMessage) bool {
	switch msg.Type {
	case "user", "assistant":
		return strings.TrimSpace(msg.Content) != ""
	default:
		return false
	}
}

func enrichBuildWorkBlock(messages []EnrichMessage) EnrichWorkBlock {
	wb := EnrichWorkBlock{
		Messages:   messages,
		ToolCounts: make(map[string]int),
	}

	for i := range messages {
		msg := &messages[i]
		if msg.ToolCall != nil && msg.ToolCall.Name != "" {
			wb.ToolCounts[msg.ToolCall.Name]++
			wb.TotalTools++

			nameLower := strings.ToLower(msg.ToolCall.Name)
			if nameLower == "edit" || nameLower == "write" || nameLower == "multiedit" {
				wb.HasEdits = true
			}
		} else if msg.Type == "tool" {
			wb.TotalTools++
		}
	}

	wb.Summary = enrichFormatWorkBlockSummary(&wb)
	return wb
}

func enrichFormatWorkBlockSummary(wb *EnrichWorkBlock) string {
	if wb == nil {
		return ""
	}
	if wb.TotalTools == 0 {
		count := len(wb.Messages)
		if count == 0 {
			return ""
		}
		return fmt.Sprintf("%d system %s", count, enrichPluralize(count, "message", "messages"))
	}

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
		enrichPluralize(wb.TotalTools, "call", "calls"),
		strings.Join(parts, ", "))
}

func enrichMergeShortAssistantBlocks(items []EnrichChapterItem) []EnrichChapterItem {
	const shortThreshold = 200

	result := make([]EnrichChapterItem, 0, len(items))
	for i := 0; i < len(items); i++ {
		item := items[i]

		if !item.IsWorkBlock && item.Message != nil && item.Message.Type == "assistant" {
			textLen := len(strings.TrimSpace(item.Message.Content))
			if textLen > 0 && textLen < shortThreshold {
				prevIsWB := len(result) > 0 && result[len(result)-1].IsWorkBlock
				nextIsWB := i+1 < len(items) && items[i+1].IsWorkBlock
				if prevIsWB && nextIsWB {
					prevWB := result[len(result)-1].WorkBlock
					prevWB.Messages = append(prevWB.Messages, *item.Message)
					nextWB := items[i+1].WorkBlock
					prevWB.Messages = append(prevWB.Messages, nextWB.Messages...)
					for name, count := range nextWB.ToolCounts {
						prevWB.ToolCounts[name] += count
					}
					prevWB.TotalTools += nextWB.TotalTools
					prevWB.HasEdits = prevWB.HasEdits || nextWB.HasEdits
					prevWB.Summary = enrichFormatWorkBlockSummary(prevWB)
					i++
					continue
				}
			}
		}

		result = append(result, item)
	}
	return result
}

func enrichSplitIntoChapters(items []EnrichChapterItem, chapterTitles []string) []EnrichChapterView {
	var chapters []EnrichChapterView
	var currentItems []EnrichChapterItem
	chapterNum := 0

	flush := func() {
		if len(currentItems) == 0 {
			return
		}
		chapterNum++
		title := enrichAutoChapterTitle(currentItems, chapterNum)
		if chapterNum-1 < len(chapterTitles) && chapterTitles[chapterNum-1] != "" {
			title = chapterTitles[chapterNum-1]
		}
		chapters = append(chapters, EnrichChapterView{
			ID:    chapterNum,
			Title: title,
			Items: currentItems,
		})
		currentItems = nil
	}

	for _, item := range items {
		if !item.IsWorkBlock && item.Message != nil && item.Message.Type == "user" && len(currentItems) > 0 {
			flush()
		}
		currentItems = append(currentItems, item)
	}

	flush()
	return chapters
}

func enrichAutoChapterTitle(items []EnrichChapterItem, chapterNum int) string {
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
						if enrichIsTestCommand(msg.ToolCall.Input) {
							testCount++
						}
					}
				}
			}
		}
	}

	totalTools := readCount + editCount + bashCount

	if totalTools == 0 {
		if chapterNum == 1 {
			return "Discussion"
		}
		return fmt.Sprintf("Turn %d", chapterNum)
	}

	if testCount > 0 && testCount > bashCount/2 && readCount < editCount && editCount < bashCount {
		return "Testing"
	}
	if readCount > editCount*2 && readCount > bashCount {
		return "Exploration"
	}
	if editCount > readCount && editCount > bashCount {
		return "Implementation"
	}
	if bashCount > readCount && bashCount > editCount {
		if testCount > bashCount/2 {
			return "Testing"
		}
		return "Execution"
	}
	if readCount > 0 && editCount > 0 {
		return "Implementation"
	}

	return fmt.Sprintf("Turn %d", chapterNum)
}

func enrichIsTestCommand(input string) bool {
	cmd := strings.ToLower(strings.TrimSpace(input))
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

// --- File change extraction ---

func extractFilesChanged(messages []EnrichMessage) []EnrichFileChange {
	seen := make(map[string]*EnrichFileChange)

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
			seen[shortPath] = &EnrichFileChange{
				Path:    shortPath,
				Added:   a,
				Removed: r,
			}
		}
	}

	result := make([]EnrichFileChange, 0, len(seen))
	for _, fc := range seen {
		result = append(result, *fc)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Path < result[j].Path
	})

	return result
}

// ExtractFilePathFromInput parses tool input (JSON or raw) to find a file_path field.
func ExtractFilePathFromInput(input string) string {
	if input == "" {
		return ""
	}

	var data map[string]any
	if json.Unmarshal([]byte(input), &data) == nil {
		if fp, ok := data["file_path"].(string); ok && fp != "" {
			return fp
		}
		if fp, ok := data["path"].(string); ok && fp != "" {
			return fp
		}
	}

	return ""
}

// CountDiffLines counts added and removed lines in unified diff output.
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

// shortenPath strips common home directory prefixes for readability.
func shortenPath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		if (part == "Users" || part == "home") && i+2 < len(parts) {
			return strings.Join(parts[i+2:], "/")
		}
	}
	return p
}

// --- Helper functions ---

// ansiEscapeRe matches ANSI escape sequences.
var enrichAnsiEscapeRe = regexp.MustCompile(`\x1b(?:\[[0-9;]*[a-zA-Z]|\][^\x07]*\x07|[\(\)][AB012])`)

func stripANSI(s string) string {
	return enrichAnsiEscapeRe.ReplaceAllString(s, "")
}

func enrichMapEntryType(entryType string) string {
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

func enrichMapRoleToType(role string) string {
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

func enrichReclassifyByContent(msgType string, content string) string {
	if msgType != "user" {
		return msgType
	}

	if strings.Contains(content, "<!-- ox-hash:") {
		return "system"
	}
	if strings.Contains(content, "<system-reminder>") {
		return "system"
	}

	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "\u23fa ") {
		return "tool"
	}
	if strings.HasPrefix(trimmed, "\u23bf") {
		return "tool"
	}

	return msgType
}

// formatAgentName is defined in markdown.go — reused by enrichment.

func parseTS(ts string) time.Time {
	if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return parsed
	}
	if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
		return parsed
	}
	return time.Time{}
}

func enrichPluralize(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}
