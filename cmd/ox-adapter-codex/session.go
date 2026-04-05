// session.go — session reading, parsing, discovery, and types for codex adapter.
//
// Codex CLI stores sessions as JSONL in ~/.codex/sessions/<session-id>.jsonl.
// Each line is a JSON object with "type" field: "user", "assistant",
// "function_call", "function_call_output". Tool entries have "name",
// "arguments" (JSON string), and "call_id" for correlation. Multiple
// function_call/function_call_output pairs may appear consecutively and are
// merged into single tool entries by mergeToolEntries().
//
// Format reference: https://github.com/openai/codex
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

// --- types ---

type codexEntry struct {
	Timestamp string        `json:"timestamp"`
	Type      string        `json:"type"`
	Payload   *codexPayload `json:"payload"`
}

type codexPayload struct {
	ID         string              `json:"id"`
	CWD        string              `json:"cwd"`
	CLIVersion string              `json:"cli_version"`
	Model      string              `json:"model"`
	ItemType   string              `json:"type"`
	Role       string              `json:"role"`
	Content    []codexContentBlock `json:"content"`
	Name       string              `json:"name"`
	Arguments  string              `json:"arguments"`
	CallID     string              `json:"call_id"`
	Output     string              `json:"output"`
	// event_msg fields
	Message string `json:"message,omitempty"`
}

type codexContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type candidate struct {
	path    string
	modTime time.Time
}

// --- session reading ---

func handleRead(p adapterprotocol.ReadParams) (*adapterprotocol.ReadResult, error) {
	entries, err := readCodexFile(p.SessionFile)
	if err != nil {
		return nil, err
	}
	merged := mergeToolEntries(entries)

	// extract metadata
	meta := extractCodexMetadata(p.SessionFile)

	return &adapterprotocol.ReadResult{Entries: merged, Metadata: meta}, nil
}

func handleReadMetadata(p adapterprotocol.ReadParams) (*adapterprotocol.ReadMetadataResult, error) {
	meta := extractCodexMetadata(p.SessionFile)
	if meta == nil {
		return &adapterprotocol.ReadMetadataResult{}, nil
	}
	return &adapterprotocol.ReadMetadataResult{
		AgentVersion: meta.AgentVersion,
		Model:        meta.Model,
	}, nil
}

func readCodexFile(path string) ([]adapterprotocol.RawEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	var entries []adapterprotocol.RawEntry
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		parsed, err := parseCodexLine(line)
		if err != nil {
			continue
		}
		entries = append(entries, parsed...)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading session file: %w", err)
	}

	return entries, nil
}

func readCodexFromOffset(path string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
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
		parsed, err := parseCodexLine(line)
		if err != nil {
			continue
		}
		entries = append(entries, parsed...)
	}

	newOffset := offset
	if info, err := f.Stat(); err == nil {
		newOffset = info.Size()
	}

	return entries, newOffset, nil
}

func parseCodexLine(line []byte) ([]adapterprotocol.RawEntry, error) {
	var raw codexEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}

	switch raw.Type {
	case "response_item":
		return parseResponseItem(raw.Payload, raw.Timestamp)
	case "event_msg":
		return parseEventMsg(raw.Payload, raw.Timestamp)
	}

	return nil, nil
}

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func parseResponseItem(p *codexPayload, ts string) ([]adapterprotocol.RawEntry, error) {
	if p == nil {
		return nil, nil
	}

	switch p.ItemType {
	case "message":
		return parseCodexMessage(p, ts)
	case "function_call":
		if p.Name == "" {
			return nil, nil
		}
		return []adapterprotocol.RawEntry{
			adapterruntime.ToolUseWithID(parseTS(ts), p.Name, p.Arguments, p.CallID),
		}, nil
	case "function_call_output":
		isErr := isCodexToolError(p.Output)
		return []adapterprotocol.RawEntry{
			adapterruntime.ToolResultWithID(parseTS(ts), p.Output, isErr, p.CallID),
		}, nil
	}

	return nil, nil
}

func parseEventMsg(p *codexPayload, _ string) ([]adapterprotocol.RawEntry, error) {
	if p == nil {
		return nil, nil
	}

	// user_message events are skipped — response_item/user already captures the
	// same text with richer context (system instructions, content blocks).
	// event_msg types we could extract in the future: task_started (turn
	// boundaries), token_count (usage telemetry).

	return nil, nil
}

func parseCodexMessage(p *codexPayload, ts string) ([]adapterprotocol.RawEntry, error) {
	t := parseTS(ts)
	switch p.Role {
	case "user":
		text, isSystem := classifyCodexUserContent(p.Content)
		if text == "" {
			return nil, nil
		}
		if isSystem {
			return []adapterprotocol.RawEntry{adapterruntime.SystemEntry(t, text)}, nil
		}
		return []adapterprotocol.RawEntry{adapterruntime.UserEntry(t, text)}, nil

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
		return []adapterprotocol.RawEntry{adapterruntime.AssistantEntry(t, strings.Join(parts, "\n"))}, nil
	}

	return nil, nil
}

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
	if strings.HasPrefix(trimmed, "# AGENTS.md instructions") ||
		strings.HasPrefix(trimmed, "<permissions instructions>") ||
		strings.HasPrefix(trimmed, "<environment_context>") {
		return text, true
	}
	return text, false
}

func isCodexToolError(output string) bool {
	if output == "" {
		return false
	}
	if strings.HasPrefix(output, "Process exited with code ") {
		return output != "Process exited with code 0"
	}
	return false
}

func mergeToolEntries(entries []adapterprotocol.RawEntry) []adapterprotocol.RawEntry {
	if len(entries) < 2 {
		return entries
	}
	result := make([]adapterprotocol.RawEntry, 0, len(entries))
	i := 0
	for i < len(entries) {
		e := entries[i]
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

// --- session discovery ---

func findCodexSession(repoRoot, agentID, since, agentSessionID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}

	sessionsDir := filepath.Join(home, ".codex", "sessions")

	// direct lookup: scan recent date dirs for a file whose session_meta.id matches
	if agentSessionID != "" {
		if err := adapterruntime.ValidateSessionID(agentSessionID); err != nil {
			return "", err
		}
		if path, err := findCodexBySessionID(sessionsDir, agentSessionID); err == nil {
			return path, nil
		}
		// fall through to timestamp-based scanning
	}

	sinceTime := time.Time{}
	if since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			sinceTime = t
		}
	}

	candidates := collectCodexCandidates(sessionsDir, sinceTime)
	if len(candidates) == 0 {
		return "", fmt.Errorf("no codex sessions found")
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})

	// filter by cwd (repoRoot)
	cwdMatches := filterCodexByCWD(candidates, repoRoot)
	if len(cwdMatches) == 0 {
		return "", fmt.Errorf("no codex sessions found for %s", repoRoot)
	}

	if agentID != "" {
		for _, c := range cwdMatches {
			if codexSessionContainsAgentID(c.path, agentID) {
				return c.path, nil
			}
		}
	}

	return cwdMatches[0].path, nil
}

// findCodexBySessionID scans recent date directories for a JSONL file whose
// session_meta entry has a matching id field.
func findCodexBySessionID(sessionsDir, sessionID string) (string, error) {
	now := time.Now()
	for day := 0; day < searchDays; day++ {
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
			path := filepath.Join(dateDir, entry.Name())
			if codexFileHasSessionID(path, sessionID) {
				return path, nil
			}
		}
	}
	return "", fmt.Errorf("codex session %s not found", sessionID)
}

// codexFileHasSessionID checks if the first session_meta entry in a file
// has the given session ID.
func codexFileHasSessionID(path, sessionID string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
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
			return entry.Payload.ID == sessionID
		}
		// session_meta is always the first entry; stop after first line
		break
	}
	return false
}

func collectCodexCandidates(sessionsDir string, since time.Time) []candidate {
	var candidates []candidate
	now := time.Now()

	for day := 0; day < searchDays; day++ {
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
			if !since.IsZero() && !info.ModTime().After(since) {
				continue
			}
			candidates = append(candidates, candidate{
				path:    filepath.Join(dateDir, entry.Name()),
				modTime: info.ModTime(),
			})
		}
	}

	return candidates
}

func filterCodexByCWD(candidates []candidate, cwd string) []candidate {
	var matches []candidate
	for _, c := range candidates {
		if codexSessionCWD(c.path) == cwd {
			matches = append(matches, c)
		}
	}
	return matches
}

func codexSessionCWD(path string) string {
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
		break
	}
	return ""
}

func codexSessionContainsAgentID(path, agentID string) bool {
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

func extractCodexMetadata(path string) *adapterprotocol.SessionMetadata {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	meta := &adapterprotocol.SessionMetadata{}
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

	if meta.AgentVersion == "" && meta.Model == "" {
		return nil
	}
	return meta
}
