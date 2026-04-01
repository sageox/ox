package adapters

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CodexAdapter reads OpenAI Codex CLI session files stored as JSONL
// in ~/.codex/sessions/YYYY/MM/DD/rollout-TIMESTAMP-UUID.jsonl
//
// Codex session entry types:
//   - session_meta: session start metadata (cwd, cli_version, session id)
//   - turn_context: per-turn config including model
//   - response_item: conversation data (messages, function calls)
//   - event_msg: infrastructure events (task_started, token counts) — skipped
//
// response_item subtypes:
//   - type=message, role=user: user prompt (content[].input_text.text)
//   - type=message, role=assistant: AI response (content[].output_text.text)
//   - type=message, role=developer: system instructions — skipped
//   - type=function_call: tool invocation (name, arguments, call_id)
//   - type=function_call_output: tool result (errors only — success output skipped for lean recordings)
//   - type=reasoning: encrypted model reasoning — skipped
//
// Plans are embedded in the session as assistant messages, not separate files.
type CodexAdapter struct{}

func init() {
	Register(&CodexAdapter{})
}

const codexSessionSearchDays = 14

// Name returns the adapter identifier
func (a *CodexAdapter) Name() string { return "codex" }

// sessionsDir returns the path to ~/.codex/sessions
func (a *CodexAdapter) sessionsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions")
}

// Detect checks if Codex session files are present
func (a *CodexAdapter) Detect() bool {
	dir := a.sessionsDir()
	if dir == "" {
		return false
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// FindSessionFile locates the Codex session file for the current project.
// It searches recent date directories under ~/.codex/sessions/YYYY/MM/DD/,
// matches by cwd and optionally agentID (from ox agent prime output).
func (a *CodexAdapter) FindSessionFile(agentID string, since time.Time) (string, error) {
	sessionsDir := a.sessionsDir()
	if sessionsDir == "" {
		return "", ErrSessionNotFound
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}

	candidates := a.collectCandidates(sessionsDir, since, true)
	// quiet sessions can be active but not recently modified; fall back to
	// project-scoped recent files instead of returning no match.
	if len(candidates) == 0 && !since.IsZero() {
		candidates = a.collectCandidates(sessionsDir, time.Time{}, false)
	}

	if len(candidates) == 0 {
		return "", fmt.Errorf("%w: no Codex sessions found", ErrSessionNotFound)
	}

	// most recent first
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})

	// narrow to sessions matching this project's cwd
	cwdMatches := a.filterByCWD(candidates, cwd)
	if len(cwdMatches) == 0 {
		return "", fmt.Errorf("%w: no Codex sessions found for cwd %s", ErrSessionNotFound, cwd)
	}

	if agentID == "" {
		return cwdMatches[0].path, nil
	}

	// search for agentID in session content (appears in function_call_output from ox agent prime)
	for _, c := range cwdMatches {
		if a.sessionContainsAgentID(c.path, agentID) {
			return c.path, nil
		}
	}

	// fall back to most recent cwd match
	return cwdMatches[0].path, nil
}

func (a *CodexAdapter) collectCandidates(sessionsDir string, since time.Time, requireSince bool) []sessionCandidate {
	var candidates []sessionCandidate
	now := time.Now()

	for day := 0; day < codexSessionSearchDays; day++ {
		t := now.AddDate(0, 0, -day)
		dateDir := filepath.Join(sessionsDir, t.Format("2006"), t.Format("01"), t.Format("02"))
		entries, err := os.ReadDir(dateDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if requireSince && !since.IsZero() && !info.ModTime().After(since) {
				continue
			}
			candidates = append(candidates, sessionCandidate{
				path:    filepath.Join(dateDir, entry.Name()),
				modTime: info.ModTime(),
			})
		}
	}

	return candidates
}

// filterByCWD returns only candidates whose session_meta.payload.cwd matches cwd.
func (a *CodexAdapter) filterByCWD(candidates []sessionCandidate, cwd string) []sessionCandidate {
	var matches []sessionCandidate
	for _, c := range candidates {
		if a.sessionCWD(c.path) == cwd {
			matches = append(matches, c)
		}
	}
	return matches
}

// sessionCWD reads the session_meta entry to extract the cwd field.
func (a *CodexAdapter) sessionCWD(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		var entry codexEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.Type == "session_meta" && entry.Payload != nil {
			return entry.Payload.CWD
		}
		// session_meta is always the first entry — stop after first parseable line
		break
	}
	return ""
}

// sessionContainsAgentID scans for the agentID string in session content.
// The agent ID appears in function_call_output entries that captured `ox agent prime` output.
func (a *CodexAdapter) sessionContainsAgentID(path, agentID string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		if strings.Contains(scanner.Text(), agentID) {
			return true
		}
	}
	return false
}

// ReadMetadata extracts session metadata (CLI version and model) from a Codex session.
// CLI version comes from session_meta, model from the first turn_context.
func (a *CodexAdapter) ReadMetadata(sessionPath string) (*SessionMetadata, error) {
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
		var entry codexEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.Payload == nil {
			continue
		}
		switch entry.Type {
		case "session_meta":
			if meta.AgentVersion == "" {
				meta.AgentVersion = entry.Payload.CLIVersion
			}
		case "turn_context":
			if meta.Model == "" {
				meta.Model = entry.Payload.Model
			}
		}
		if meta.AgentVersion != "" && meta.Model != "" {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading session file: %w", err)
	}

	if meta.AgentVersion == "" && meta.Model == "" {
		return nil, nil
	}
	return meta, nil
}

// Read parses all conversation entries from a Codex JSONL session file.
// Plans are embedded as assistant messages — no separate plan file exists.
func (a *CodexAdapter) Read(sessionPath string) ([]RawEntry, error) {
	f, err := os.Open(sessionPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	var entries []RawEntry
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		parsed, err := a.parseLine(line)
		if err != nil {
			continue // skip malformed lines
		}
		entries = append(entries, parsed...)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading session file: %w", err)
	}

	return mergeToolEntries(entries), nil
}

// Watch monitors a Codex session file for new entries using fsnotify with debouncing.
// mergeToolEntries is applied per-batch via WithBatchTransform, so function_call and
// function_call_output arriving in separate batches won't be merged here. This is
// intentional: unmerged entries in raw.jsonl are independently useful, and cross-batch
// merge is applied at read-time during finalization.
func (a *CodexAdapter) Watch(ctx context.Context, sessionPath string) (<-chan RawEntry, error) {
	var offset int64
	if info, err := os.Stat(sessionPath); err == nil {
		offset = info.Size()
	}
	tw := NewTailWatcher(sessionPath, offset, a.parseLine).WithBatchTransform(mergeToolEntries)
	return tw.Watch(ctx)
}

// ReadFromOffset reads new entries starting at the given byte offset.
func (a *CodexAdapter) ReadFromOffset(path string, offset int64) ([]RawEntry, int64, error) {
	tw := NewTailWatcher(path, offset, a.parseLine).WithBatchTransform(mergeToolEntries)
	return tw.ReadFromOffset(offset)
}

// parseLine converts a Codex JSONL line into zero or more RawEntries.
func (a *CodexAdapter) parseLine(line []byte) ([]RawEntry, error) {
	var raw codexEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}

	// only response_item carries conversation data
	if raw.Type != "response_item" || raw.Payload == nil {
		return nil, nil
	}

	p := raw.Payload
	ts := parseCodexTimestamp(raw.Timestamp)

	switch p.ItemType {
	case "message":
		return a.parseMessage(p, ts, line)
	case "function_call":
		if p.Name == "" {
			return nil, nil
		}
		return []RawEntry{{
			Timestamp: ts,
			Role:      "tool",
			ToolName:  p.Name,
			ToolInput: p.Arguments,
			CallID:    p.CallID,
			Raw:       json.RawMessage(line),
		}}, nil
	case "function_call_output":
		if !isCodexToolError(p.Output) {
			return nil, nil
		}
		return []RawEntry{{
			Timestamp:  ts,
			Role:       "tool",
			ToolOutput: p.Output,
			IsError:    true,
			CallID:     p.CallID,
			Raw:        json.RawMessage(line),
		}}, nil
	default:
		// reasoning, web_search_call, etc. — skip
		return nil, nil
	}
}

// parseMessage extracts a conversation entry from a response_item/message payload.
func (a *CodexAdapter) parseMessage(p *codexPayload, ts time.Time, raw []byte) ([]RawEntry, error) {
	switch p.Role {
	case "user":
		text, isSystem := classifyCodexUserContent(p.Content)
		if text == "" {
			return nil, nil
		}
		role := "user"
		if isSystem {
			role = "system"
		}
		return []RawEntry{{
			Timestamp: ts,
			Role:      role,
			Content:   text,
			Raw:       json.RawMessage(raw),
		}}, nil

	case "assistant":
		var parts []string
		for _, block := range p.Content {
			if block.Type == "output_text" && block.Text != "" {
				parts = append(parts, block.Text)
			}
		}
		if len(parts) == 0 {
			return nil, nil
		}
		return []RawEntry{{
			Timestamp: ts,
			Role:      "assistant",
			Content:   strings.Join(parts, "\n"),
			Raw:       json.RawMessage(raw),
		}}, nil

	default:
		// role=developer: system/permission instructions — skip
		return nil, nil
	}
}

// classifyCodexUserContent extracts text from user message content blocks and
// classifies it as user-authored or system-injected.
// Codex injects AGENTS.md, permissions, and environment_context into user turns.
func classifyCodexUserContent(blocks []codexContentBlock) (string, bool) {
	var parts []string
	for _, block := range blocks {
		if block.Type == "input_text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	text := strings.Join(parts, "\n")
	trimmed := strings.TrimSpace(text)

	// system-injected prefixes that Codex adds to user turns
	if strings.HasPrefix(trimmed, "# AGENTS.md instructions") ||
		strings.HasPrefix(trimmed, "<permissions instructions>") ||
		strings.HasPrefix(trimmed, "<environment_context>") {
		return text, true
	}

	return text, false
}

// isCodexToolError checks if a function_call_output indicates an error.
// Codex reports non-zero exit codes as "Process exited with code N" in the output.
// isCodexToolError returns true only for known error patterns.
// Unknown patterns default to non-error (conservative: keeps recordings lean).
func isCodexToolError(output string) bool {
	if output == "" {
		return false
	}
	if strings.HasPrefix(output, "Process exited with code ") {
		return output != "Process exited with code 0"
	}
	return false
}

// mergeToolEntries merges consecutive function_call and function_call_output entries
// that share the same CallID. The first entry carries ToolName+ToolInput, the second
// carries ToolOutput+IsError. After merging, the output entry is removed.
//
// NOTE: Only works for batch reads (Read, ReadFromOffset) where entries are in a slice.
// In the Watch/tail path, entries arrive one-by-one through a channel and are written
// to raw.jsonl unmerged. This is acceptable: each entry is independently useful,
// and merge can be applied at read-time during finalization.
func mergeToolEntries(entries []RawEntry) []RawEntry {
	if len(entries) < 2 {
		return entries
	}
	result := make([]RawEntry, 0, len(entries))
	i := 0
	for i < len(entries) {
		e := entries[i]
		// look for function_call followed by function_call_output with matching CallID
		if e.Role == "tool" && e.ToolName != "" && e.CallID != "" && i+1 < len(entries) {
			next := entries[i+1]
			if next.Role == "tool" && next.ToolOutput != "" && next.CallID == e.CallID {
				e.ToolOutput = next.ToolOutput
				e.IsError = next.IsError
				result = append(result, e)
				i += 2
				continue
			}
		}
		result = append(result, e)
		i++
	}
	return result
}

// parseCodexTimestamp parses RFC3339/RFC3339Nano timestamps from Codex entries.
func parseCodexTimestamp(s string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

// codexEntry is the top-level structure of each JSONL line in a Codex session.
type codexEntry struct {
	Timestamp string        `json:"timestamp"`
	Type      string        `json:"type"` // "session_meta", "turn_context", "response_item", "event_msg"
	Payload   *codexPayload `json:"payload"`
}

// codexPayload covers the payload field across all Codex entry types.
// Fields are shared: CWD appears in both session_meta and turn_context;
// ItemType/Role/Content are response_item-specific.
type codexPayload struct {
	// session_meta fields
	CWD        string `json:"cwd"`
	CLIVersion string `json:"cli_version"`

	// turn_context fields
	Model string `json:"model"`

	// response_item subtype dispatch
	ItemType string              `json:"type"` // "message", "function_call", "function_call_output", "reasoning"
	Role     string              `json:"role"` // "user", "assistant", "developer"
	Content  []codexContentBlock `json:"content"`

	// function_call / function_call_output fields
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	CallID    string `json:"call_id"`
	Output    string `json:"output"`
}

// codexContentBlock represents a single content block within a message.
type codexContentBlock struct {
	Type string `json:"type"` // "input_text" (user), "output_text" (assistant)
	Text string `json:"text"`
}
