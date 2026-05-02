// session.go handles Claude Code session reading, parsing, and discovery.
//
// Claude Code stores sessions as JSONL in ~/.claude/projects/<path>/sessions/.
// Each line is a JSON object with a "type" field: "human" (user), "assistant",
// "tool_use", "tool_result", or "summary". Timestamps are in the "timestamp"
// field (ISO 8601). Tool entries use "tool_name", "tool_input", and "call_id"
// for correlation.
//
// Format reference: https://docs.anthropic.com/en/docs/claude-code
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

// regex patterns for stripping system-injected tags from user content
var (
	reStripSystemReminder          = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)
	reStripSystemInstructionUndsc  = regexp.MustCompile(`(?s)<system_instruction>.*?</system_instruction>`)
	reStripSystemInstructionHyphen = regexp.MustCompile(`(?s)<system-instruction>.*?</system-instruction>`)
	reStripLocalCommandStdout      = regexp.MustCompile(`(?s)<local-command-stdout>.*?</local-command-stdout>`)
	reStripLocalCommandCaveat      = regexp.MustCompile(`(?s)<local-command-caveat>.*?</local-command-caveat>`)
	reStripCommandName             = regexp.MustCompile(`(?s)<command-name>.*?</command-name>`)
	reStripCommandMessage          = regexp.MustCompile(`(?s)<command-message>.*?</command-message>`)
	reStripCommandArgs             = regexp.MustCompile(`(?s)<command-args>.*?</command-args>`)
	reStripTaskNotification        = regexp.MustCompile(`(?s)<task-notification>.*?</task-notification>`)
)

// userContentClass indicates how a user message should be classified.
type userContentClass int

const (
	userContentUser userContentClass = iota
	userContentSystem
	userContentSkip
)

type sessionCandidate struct {
	path    string
	modTime time.Time
}

// claudeProjectHash converts a directory path to Claude Code's project hash format.
func claudeProjectHash(path string) string {
	h := strings.ReplaceAll(path, string(os.PathSeparator), "-")
	h = strings.ReplaceAll(h, "_", "-")
	h = strings.ReplaceAll(h, ".", "-")
	return h
}

func handleRead(p adapterprotocol.ReadParams) (*adapterprotocol.ReadResult, error) {
	entries, meta, err := readSessionFile(p.SessionFile)
	if err != nil {
		return nil, err
	}
	return &adapterprotocol.ReadResult{
		Entries:  entries,
		Metadata: meta,
	}, nil
}

func handleReadMetadata(p adapterprotocol.ReadParams) (*adapterprotocol.ReadMetadataResult, error) {
	f, err := os.Open(p.SessionFile)
	if err != nil {
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	meta := &adapterprotocol.ReadMetadataResult{}
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry claudeCodeEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry.Version != "" && meta.AgentVersion == "" {
			meta.AgentVersion = entry.Version
		}
		if entry.Type == "assistant" && entry.Message != nil && entry.Message.Model != "" {
			meta.Model = entry.Message.Model
		}
		if meta.AgentVersion != "" && meta.Model != "" {
			break
		}
	}

	return meta, nil
}

func readSessionFile(path string) ([]adapterprotocol.RawEntry, *adapterprotocol.SessionMetadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	var entries []adapterprotocol.RawEntry
	meta := &adapterprotocol.SessionMetadata{}
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		parsed, err := parseLine(line)
		if err != nil {
			continue
		}
		entries = append(entries, parsed...)

		// extract metadata
		var raw claudeCodeEntry
		if json.Unmarshal(line, &raw) == nil {
			if raw.Version != "" && meta.AgentVersion == "" {
				meta.AgentVersion = raw.Version
			}
			if raw.Type == "assistant" && raw.Message != nil && raw.Message.Model != "" {
				meta.Model = raw.Message.Model
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("error reading session file: %w", err)
	}

	return entries, meta, nil
}

func readFromOffset(path string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return nil, offset, fmt.Errorf("failed to seek: %w", err)
		}
	}

	var entries []adapterprotocol.RawEntry
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		parsed, err := parseLine(line)
		if err != nil {
			continue
		}
		entries = append(entries, parsed...)
	}

	if err := scanner.Err(); err != nil {
		return nil, offset, fmt.Errorf("error reading session file: %w", err)
	}

	// calculate new offset
	newOffset := offset
	if info, err := f.Stat(); err == nil {
		newOffset = info.Size()
	}

	return entries, newOffset, nil
}

func findSessionFile(repoRoot, agentID, since, agentSessionID string) (string, int64, error) {
	if resolved, err := filepath.EvalSymlinks(repoRoot); err == nil {
		repoRoot = resolved
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", 0, fmt.Errorf("cannot determine home directory: %w", err)
	}

	projectsDir := filepath.Join(home, ".claude", "projects")
	projectHash := claudeProjectHash(repoRoot)
	projectDir := filepath.Join(projectsDir, projectHash)

	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		return "", 0, fmt.Errorf("no sessions for project %s", repoRoot)
	}

	// direct lookup via agent session ID: Claude Code files are {sessionId}.jsonl
	if agentSessionID != "" {
		candidate := filepath.Join(projectDir, agentSessionID+".jsonl")
		if _, err := os.Stat(candidate); err == nil {
			sinceTime := time.Time{}
			if since != "" {
				if t, err := time.Parse(time.RFC3339, since); err == nil {
					sinceTime = t
				}
			}
			offset := findStartOffset(candidate, sinceTime)
			return candidate, offset, nil
		}
		// fall through to timestamp-based scanning
	}

	sinceTime := time.Time{}
	if since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			sinceTime = t
		}
	}

	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return "", 0, fmt.Errorf("failed to read project directory: %w", err)
	}

	// subtract a buffer to account for startup timing drift — Claude Code
	// may not write to the session file until after the first human message,
	// so its mtime can lag behind the recorded StartedAt timestamp.
	if !sinceTime.IsZero() {
		sinceTime = sinceTime.Add(-30 * time.Second)
	}

	var candidates []sessionCandidate
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if !sinceTime.IsZero() && info.ModTime().Before(sinceTime) {
			continue
		}
		candidates = append(candidates, sessionCandidate{
			path:    filepath.Join(projectDir, entry.Name()),
			modTime: info.ModTime(),
		})
	}

	if len(candidates) == 0 {
		return "", 0, fmt.Errorf("no sessions modified after %v", since)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})

	if agentID == "" {
		return candidates[0].path, 0, nil
	}

	for _, c := range candidates {
		if sessionContainsAgentID(c.path, agentID) {
			offset := findStartOffset(c.path, sinceTime)
			return c.path, offset, nil
		}
	}

	return candidates[0].path, 0, nil
}

func sessionContainsAgentID(path, agentID string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		if strings.Contains(scanner.Text(), agentID) {
			return true
		}
	}
	return false
}

// findStartOffset locates the byte offset where entries after sinceTime begin.
func findStartOffset(path string, sinceTime time.Time) int64 {
	if sinceTime.IsZero() {
		return 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var offset int64
	for scanner.Scan() {
		line := scanner.Bytes()
		var entry struct {
			Timestamp string `json:"timestamp"`
		}
		if json.Unmarshal(line, &entry) == nil && entry.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339, entry.Timestamp); err == nil {
				if t.After(sinceTime) {
					return offset
				}
			}
		}
		offset += int64(len(line)) + 1 // +1 for newline
	}
	return offset
}

// --- line parsing ---

// parseTS converts a raw timestamp string to time.Time for use with entry constructors.
func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func parseLine(line []byte) ([]adapterprotocol.RawEntry, error) {
	var raw claudeCodeEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}

	switch raw.Type {
	case "user", "assistant":
		// process these
	default:
		return nil, nil
	}

	switch raw.Type {
	case "user":
		return parseUserEntry(&raw)
	case "assistant":
		return parseAssistantEntry(&raw)
	}

	return nil, nil
}

func parseUserEntry(raw *claudeCodeEntry) ([]adapterprotocol.RawEntry, error) {
	ts := parseTS(raw.Timestamp)

	if raw.IsMeta {
		content := extractRawUserText(raw)
		cleaned := strings.TrimSpace(stripSystemTags(content))
		if cleaned == "" {
			cleaned = content
		}
		return []adapterprotocol.RawEntry{adapterruntime.SystemEntry(ts, cleaned)}, nil
	}

	var entries []adapterprotocol.RawEntry

	content, class := classifyUserContent(raw)
	switch class {
	case userContentSkip:
		// still extract tool_result blocks below
	case userContentSystem:
		entries = append(entries, adapterruntime.SystemEntry(ts, content))
	default:
		entries = append(entries, adapterruntime.UserEntry(ts, content))
	}

	// extract tool_result blocks from user content arrays
	entries = append(entries, extractToolResults(raw, ts)...)

	if len(entries) == 0 {
		return nil, nil
	}
	return entries, nil
}

// extractToolResults extracts tool_result content blocks from a user message.
// In Claude's format, tool results appear as content blocks in the user turn
// following an assistant's tool_use.
func extractToolResults(raw *claudeCodeEntry, ts time.Time) []adapterprotocol.RawEntry {
	if raw.Message == nil {
		return nil
	}

	content, ok := raw.Message.Content.([]interface{})
	if !ok {
		return nil
	}

	var entries []adapterprotocol.RawEntry
	for _, item := range content {
		block, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if block["type"] != "tool_result" {
			continue
		}

		toolOutput := extractToolResultContent(block)
		isError, _ := block["is_error"].(bool)
		callID, _ := block["tool_use_id"].(string)
		if callID != "" {
			entries = append(entries, adapterruntime.ToolResultWithID(ts, toolOutput, isError, callID))
		} else {
			entries = append(entries, adapterruntime.ToolResultEntry(ts, toolOutput, isError))
		}
	}
	return entries
}

// extractToolResultContent extracts text from a tool_result content block.
// The content field can be a string or an array of content blocks.
func extractToolResultContent(block map[string]interface{}) string {
	// content can be a direct string
	if s, ok := block["content"].(string); ok {
		return s
	}

	// or an array of content blocks
	contentArr, ok := block["content"].([]interface{})
	if !ok {
		return ""
	}

	var parts []string
	for _, item := range contentArr {
		if sub, ok := item.(map[string]interface{}); ok {
			if sub["type"] == "text" {
				if text, ok := sub["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
	}
	return strings.Join(parts, "\n")
}

func parseAssistantEntry(raw *claudeCodeEntry) ([]adapterprotocol.RawEntry, error) {
	if raw.Message == nil {
		return nil, nil
	}

	var entries []adapterprotocol.RawEntry
	content, ok := raw.Message.Content.([]interface{})
	if !ok {
		return nil, nil
	}

	ts := parseTS(raw.Timestamp)
	for _, item := range content {
		block, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		blockType, _ := block["type"].(string)

		switch blockType {
		case "text":
			text, _ := block["text"].(string)
			if text != "" {
				entries = append(entries, adapterruntime.AssistantEntry(ts, text))
			}
		case "tool_use":
			toolName, _ := block["name"].(string)
			callID, _ := block["id"].(string)
			input := block["input"]
			inputJSON, _ := json.Marshal(input)
			if callID != "" {
				entries = append(entries, adapterruntime.ToolUseWithID(ts, toolName, string(inputJSON), callID))
			} else {
				entries = append(entries, adapterruntime.ToolUseEntry(ts, toolName, string(inputJSON)))
			}
		}
	}

	return entries, nil
}

func classifyUserContent(raw *claudeCodeEntry) (string, userContentClass) {
	if raw.Message == nil {
		return "", userContentSkip
	}

	switch content := raw.Message.Content.(type) {
	case string:
		return classifyTextContent(content)
	case []interface{}:
		var textParts []string
		hasToolResult := false
		hasText := false

		for _, item := range content {
			block, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			switch block["type"] {
			case "text":
				if text, ok := block["text"].(string); ok {
					hasText = true
					textParts = append(textParts, text)
				}
			case "tool_result":
				hasToolResult = true
			}
		}

		if hasToolResult && !hasText {
			return "", userContentSkip
		}

		joined := strings.Join(textParts, "\n")
		return classifyTextContent(joined)
	}

	return "", userContentSkip
}

func extractRawUserText(raw *claudeCodeEntry) string {
	if raw.Message == nil {
		return ""
	}
	switch content := raw.Message.Content.(type) {
	case string:
		return content
	case []interface{}:
		var parts []string
		for _, item := range content {
			if block, ok := item.(map[string]interface{}); ok {
				if block["type"] == "text" {
					if text, ok := block["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func classifyTextContent(text string) (string, userContentClass) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", userContentSkip
	}

	if strings.Contains(text, "<!-- ox-hash:") {
		return text, userContentSystem
	}

	if strings.HasPrefix(trimmed, "Entered plan mode.") ||
		strings.HasPrefix(trimmed, "Plan mode is active.") ||
		strings.HasPrefix(trimmed, "Plan mode still active") {
		return text, userContentSystem
	}

	if strings.HasPrefix(trimmed, "This session is being continued from a previous conversation") {
		return text, userContentSystem
	}

	if strings.HasPrefix(trimmed, "\u23fa ") || strings.HasPrefix(trimmed, "⏺ ") ||
		strings.HasPrefix(trimmed, "\u23bf") || strings.HasPrefix(trimmed, "⎿") {
		return text, userContentSystem
	}

	if strings.HasPrefix(trimmed, "[Request interrupted") {
		return text, userContentSystem
	}

	cleaned := stripSystemTags(text)
	cleanedTrimmed := strings.TrimSpace(cleaned)
	if cleanedTrimmed == "" {
		return text, userContentSystem
	}

	return cleanedTrimmed, userContentUser
}

func stripSystemTags(text string) string {
	text = reStripSystemReminder.ReplaceAllString(text, "")
	text = reStripSystemInstructionUndsc.ReplaceAllString(text, "")
	text = reStripSystemInstructionHyphen.ReplaceAllString(text, "")
	text = reStripLocalCommandStdout.ReplaceAllString(text, "")
	text = reStripLocalCommandCaveat.ReplaceAllString(text, "")
	text = reStripCommandName.ReplaceAllString(text, "")
	text = reStripCommandMessage.ReplaceAllString(text, "")
	text = reStripCommandArgs.ReplaceAllString(text, "")
	text = reStripTaskNotification.ReplaceAllString(text, "")
	return text
}

// --- types ---

type claudeCodeEntry struct {
	Type      string             `json:"type"`
	Timestamp string             `json:"timestamp"`
	Version   string             `json:"version"`
	Message   *claudeCodeMessage `json:"message"`
	IsMeta    bool               `json:"isMeta"`
}

type claudeCodeMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
	Model   string      `json:"model"`
}
