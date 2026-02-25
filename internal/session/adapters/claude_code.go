package adapters

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// userContentClass indicates how a user message should be classified.
// See docs/ai/specs/session-raw-jsonl.md for the entry type definitions
// that these classifications map to in the output format.
type userContentClass int

const (
	userContentUser   userContentClass = iota // genuine human message → type "user"
	userContentSystem                         // system/framework content → type "system"
	userContentSkip                           // omit entirely (e.g., pure tool_result)
)

// regex patterns for stripping system-injected tags from user content.
// Claude Code injects these tags into user turns for framework context;
// they should not be attributed to the human user in session recordings.
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

// debounceDelay is the time to wait after the last write event before reading
// the file. This prevents reading partial writes when multiple events fire rapidly.
const debounceDelay = 100 * time.Millisecond

// ClaudeCodeAdapter reads Claude Code session files stored as JSONL
// in ~/.claude/projects/<project-hash>/*.jsonl
type ClaudeCodeAdapter struct{}

func init() {
	Register(&ClaudeCodeAdapter{})
}

// Name returns the adapter identifier
func (a *ClaudeCodeAdapter) Name() string { return "claude-code" }

// claudeDir returns the Claude Code config directory
func (a *ClaudeCodeAdapter) claudeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// Detect checks if Claude Code session files are present
func (a *ClaudeCodeAdapter) Detect() bool {
	// env var is a stronger signal than filesystem — works in containers
	// or environments where ~/.claude hasn't been created yet
	if os.Getenv("CLAUDE_CODE_ENTRYPOINT") != "" {
		return true
	}

	claudeDir := a.claudeDir()
	if claudeDir == "" {
		return false
	}

	// check for ~/.claude directory
	if _, err := os.Stat(claudeDir); os.IsNotExist(err) {
		return false
	}

	// check for projects subdirectory
	projectsDir := filepath.Join(claudeDir, "projects")
	if _, err := os.Stat(projectsDir); os.IsNotExist(err) {
		return false
	}

	// fallback: check if projects directory has content
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return false
	}
	return len(entries) > 0
}

// FindSessionFile locates the most recent session file matching criteria.
// It searches through all project directories for JSONL files modified after 'since',
// then scans for content matching the agentID (from ox agent prime output).
func (a *ClaudeCodeAdapter) FindSessionFile(agentID string, since time.Time) (string, error) {
	projectsDir := filepath.Join(a.claudeDir(), "projects")
	if projectsDir == "" {
		return "", ErrSessionNotFound
	}

	// get current working directory to find project-specific sessions
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}

	// convert cwd to Claude's project hash format: path separators and underscores become dashes
	// e.g., /Users/ryan/Code/project -> -Users-ryan-Code-project
	projectHash := strings.ReplaceAll(cwd, string(os.PathSeparator), "-")
	projectHash = strings.ReplaceAll(projectHash, "_", "-")
	projectDir := filepath.Join(projectsDir, projectHash)

	// check if project-specific directory exists
	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		return "", fmt.Errorf("%w: no sessions for project %s", ErrSessionNotFound, cwd)
	}

	// find all JSONL files modified after 'since'
	var candidates []sessionCandidate
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return "", fmt.Errorf("failed to read project directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.ModTime().After(since) {
			candidates = append(candidates, sessionCandidate{
				path:    filepath.Join(projectDir, entry.Name()),
				modTime: info.ModTime(),
			})
		}
	}

	if len(candidates) == 0 {
		return "", fmt.Errorf("%w: no sessions modified after %v", ErrSessionNotFound, since)
	}

	// sort by modification time, most recent first
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})

	// if no agentID provided, return most recent
	if agentID == "" {
		return candidates[0].path, nil
	}

	// search for agentID in session content
	for _, c := range candidates {
		if a.sessionContainsAgentID(c.path, agentID) {
			return c.path, nil
		}
	}

	// fallback to most recent if agentID not found
	return candidates[0].path, nil
}

type sessionCandidate struct {
	path    string
	modTime time.Time
}

// sessionContainsAgentID checks if a session file contains the given agentID
func (a *ClaudeCodeAdapter) sessionContainsAgentID(path, agentID string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// increase buffer for potentially long lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		if strings.Contains(scanner.Text(), agentID) {
			return true
		}
	}
	return false
}

// ReadMetadata extracts session metadata (agent version, model) from a Claude Code session.
// It scans the session file for version and model information from the JSONL entries.
func (a *ClaudeCodeAdapter) ReadMetadata(sessionPath string) (*SessionMetadata, error) {
	f, err := os.Open(sessionPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	meta := &SessionMetadata{}
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

		// extract version from any entry that has it
		if entry.Version != "" && meta.AgentVersion == "" {
			meta.AgentVersion = entry.Version
		}

		// extract model from assistant messages
		if entry.Type == "assistant" && entry.Message != nil && entry.Message.Model != "" {
			meta.Model = entry.Message.Model
		}

		// stop early if we have both
		if meta.AgentVersion != "" && meta.Model != "" {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading session file: %w", err)
	}

	// return nil if no metadata found
	if meta.AgentVersion == "" && meta.Model == "" {
		return nil, nil
	}

	return meta, nil
}

// Read parses all entries from a Claude Code JSONL session file
func (a *ClaudeCodeAdapter) Read(sessionPath string) ([]RawEntry, error) {
	f, err := os.Open(sessionPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	var entries []RawEntry
	scanner := bufio.NewScanner(f)
	// increase buffer for potentially long lines (tool outputs can be large)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024) // 10MB max line size

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		entry, err := a.parseLine(line)
		if err != nil {
			continue // skip malformed lines
		}
		if entry != nil {
			entries = append(entries, *entry)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading session file: %w", err)
	}

	return entries, nil
}

// Watch monitors a session file for new entries using fsnotify.
// It uses debouncing to avoid reading partial writes when multiple
// file system events fire rapidly during a single write operation.
func (a *ClaudeCodeAdapter) Watch(ctx context.Context, sessionPath string) (<-chan RawEntry, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create file watcher: %w", err)
	}

	if err := watcher.Add(sessionPath); err != nil {
		watcher.Close()
		return nil, fmt.Errorf("failed to watch session file: %w", err)
	}

	ch := make(chan RawEntry, 100)

	go func() {
		defer close(ch)
		defer watcher.Close()

		// track file position
		var offset int64 = 0

		// get initial file size
		info, err := os.Stat(sessionPath)
		if err == nil {
			offset = info.Size()
		}

		// debounce timer to coalesce rapid write events
		debounceTimer := time.NewTimer(0)
		if !debounceTimer.Stop() {
			<-debounceTimer.C
		}
		pendingRead := false

		for {
			select {
			case <-ctx.Done():
				debounceTimer.Stop()
				return

			case <-debounceTimer.C:
				// debounce period elapsed, perform the read
				if pendingRead {
					entries, newOffset, err := a.readFromOffset(sessionPath, offset)
					if err == nil {
						offset = newOffset
						for _, entry := range entries {
							select {
							case ch <- entry:
							case <-ctx.Done():
								return
							}
						}
					}
					pendingRead = false
				}

			case event, ok := <-watcher.Events:
				if !ok {
					debounceTimer.Stop()
					return
				}
				if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
					// reset timer on each event, only fire after debounce period
					pendingRead = true
					if !debounceTimer.Stop() {
						// drain timer channel if it already fired
						select {
						case <-debounceTimer.C:
						default:
						}
					}
					debounceTimer.Reset(debounceDelay)
				}

			case _, ok := <-watcher.Errors:
				if !ok {
					debounceTimer.Stop()
					return
				}
			}
		}
	}()

	return ch, nil
}

// readFromOffset reads new entries from a file starting at the given byte offset
func (a *ClaudeCodeAdapter) readFromOffset(path string, offset int64) ([]RawEntry, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()

	if _, err := f.Seek(offset, 0); err != nil {
		return nil, offset, err
	}

	var entries []RawEntry
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		entry, err := a.parseLine(line)
		if err != nil {
			continue
		}
		if entry != nil {
			entries = append(entries, *entry)
		}
	}

	// get new offset
	newOffset, err := f.Seek(0, 1) // current position
	if err != nil {
		newOffset = offset
	}

	return entries, newOffset, nil
}

// parseLine converts a JSONL line into a RawEntry
func (a *ClaudeCodeAdapter) parseLine(line []byte) (*RawEntry, error) {
	var raw claudeCodeEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}

	// skip non-conversation entries
	switch raw.Type {
	case "user", "assistant":
		// process these
	case "file-history-snapshot", "queue-operation", "summary":
		return nil, nil // skip these types
	default:
		return nil, nil // skip unknown types
	}

	entry := &RawEntry{
		Raw: json.RawMessage(line),
	}

	// parse timestamp
	if raw.Timestamp != "" {
		t, err := time.Parse(time.RFC3339, raw.Timestamp)
		if err == nil {
			entry.Timestamp = t
		}
	}

	// process based on type
	switch raw.Type {
	case "user":
		content, class := a.classifyUserContent(&raw)
		switch class {
		case userContentSkip:
			return nil, nil
		case userContentSystem:
			entry.Role = "system"
		default:
			entry.Role = "user"
		}
		entry.Content = content
	case "assistant":
		entries := a.extractAssistantContent(&raw)
		if len(entries) == 0 {
			return nil, nil
		}
		// return first entry, additional entries would need different handling
		return &entries[0], nil
	}

	return entry, nil
}

// classifyUserContent extracts text content from user messages and classifies
// the entry as user, system, or skip based on content analysis.
//
// Claude Code's JSONL uses type:"user" for ALL messages sent to the model,
// including system reminders, tool results, skill expansions, and plan mode
// instructions. This function separates genuine human messages from
// framework-injected content so raw.jsonl stores correct entry types.
//
// See docs/ai/specs/session-raw-jsonl.md for the output format spec.
// tool_result blocks are omitted because the corresponding tool call is
// already captured from the assistant's tool_use block (deduplication).
func (a *ClaudeCodeAdapter) classifyUserContent(raw *claudeCodeEntry) (string, userContentClass) {
	if raw.Message == nil {
		return "", userContentSkip
	}

	// isMeta entries are framework context, not human messages.
	// Strip system tags for consistency with other system-classified entries.
	if raw.IsMeta {
		content := a.extractRawUserText(raw)
		cleaned := strings.TrimSpace(stripSystemTags(content))
		if cleaned == "" {
			cleaned = content
		}
		return cleaned, userContentSystem
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
				// tool_result blocks are protocol plumbing — the tool call
				// is already captured from the assistant's tool_use block
			}
		}

		// pure tool_result with no text → skip entirely
		if hasToolResult && !hasText {
			return "", userContentSkip
		}

		// classify the joined text content
		joined := strings.Join(textParts, "\n")
		return classifyTextContent(joined)
	}

	return "", userContentSkip
}

// extractRawUserText extracts text from a user message without classification.
func (a *ClaudeCodeAdapter) extractRawUserText(raw *claudeCodeEntry) string {
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

// classifyTextContent analyzes text content and returns cleaned text + classification.
// It detects framework-injected patterns and strips system tags, returning only
// genuine human content when possible.
func classifyTextContent(text string) (string, userContentClass) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", userContentSkip
	}

	// skill expansion marker (ox skills embed this comment)
	if strings.Contains(text, "<!-- ox-hash:") {
		return text, userContentSystem
	}

	// plan mode boilerplate injected by the framework
	if strings.HasPrefix(trimmed, "Entered plan mode.") ||
		strings.HasPrefix(trimmed, "Plan mode is active.") ||
		strings.HasPrefix(trimmed, "Plan mode still active") {
		return text, userContentSystem
	}

	// context compaction continuation injected when a session resumes after
	// hitting the context window limit
	if strings.HasPrefix(trimmed, "This session is being continued from a previous conversation") {
		return text, userContentSystem
	}

	// Claude Code UI prefixes for tool call/result display that leak into
	// user turns. U+23FA (black circle) = tool call, U+23BF (dentistry) = tool result.
	if strings.HasPrefix(trimmed, "\u23fa ") || strings.HasPrefix(trimmed, "⏺ ") ||
		strings.HasPrefix(trimmed, "\u23bf") || strings.HasPrefix(trimmed, "⎿") {
		return text, userContentSystem
	}

	// framework interrupt messages injected when user cancels tool execution
	if strings.HasPrefix(trimmed, "[Request interrupted") {
		return text, userContentSystem
	}

	// strip system tags and check what remains
	cleaned := stripSystemTags(text)
	cleanedTrimmed := strings.TrimSpace(cleaned)

	if cleanedTrimmed == "" {
		// content was entirely system tags
		return text, userContentSystem
	}

	// always return the cleaned form for consistency
	return cleanedTrimmed, userContentUser
}

// stripSystemTags removes system-injected XML tags from content.
// These tags are framework-level context that should not appear in
// human-attributed session entries.
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

// extractAssistantContent extracts content and tool calls from assistant messages
func (a *ClaudeCodeAdapter) extractAssistantContent(raw *claudeCodeEntry) []RawEntry {
	if raw.Message == nil {
		return nil
	}

	var entries []RawEntry

	// message.content is typically an array for assistant messages
	content, ok := raw.Message.Content.([]interface{})
	if !ok {
		return nil
	}

	timestamp := time.Time{}
	if raw.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339, raw.Timestamp); err == nil {
			timestamp = t
		}
	}

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
				entries = append(entries, RawEntry{
					Timestamp: timestamp,
					Role:      "assistant",
					Content:   text,
				})
			}
		case "tool_use":
			toolName, _ := block["name"].(string)
			input := block["input"]
			inputJSON, _ := json.Marshal(input)

			entries = append(entries, RawEntry{
				Timestamp: timestamp,
				Role:      "tool",
				ToolName:  toolName,
				ToolInput: string(inputJSON),
			})
		case "thinking":
			// skip thinking blocks for now
		}
	}

	return entries
}

// claudeCodeEntry represents the raw JSONL structure from Claude Code
type claudeCodeEntry struct {
	Type       string             `json:"type"`
	Timestamp  string             `json:"timestamp"`
	SessionID  string             `json:"sessionId"`
	UUID       string             `json:"uuid"`
	ParentUUID string             `json:"parentUuid"`
	CWD        string             `json:"cwd"`
	GitBranch  string             `json:"gitBranch"`
	Version    string             `json:"version"`
	Message    *claudeCodeMessage `json:"message"`
	IsMeta     bool               `json:"isMeta"`
	UserType   string             `json:"userType"`
	RequestID  string             `json:"requestId"`
}

// claudeCodeMessage represents the message field in JSONL entries
type claudeCodeMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // can be string or []ContentBlock
	Model   string      `json:"model"`
	ID      string      `json:"id"`
}
