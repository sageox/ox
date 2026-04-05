// session.go handles Factory Droid session reading, parsing, and discovery.
//
// Droid stores sessions as JSONL in ~/.factory/projects/<project-slug>/<uuid>.jsonl.
// Each JSONL line has a top-level "type" field:
//   - "session_start": first line, contains session metadata (id, title, cwd)
//   - "message": all subsequent lines, wraps a nested "message" object with
//     role (user/assistant) and content (array of blocks: text, thinking,
//     tool_use, tool_result, image)
//
// Companion metadata lives in <uuid>.settings.json alongside the JSONL file.
//
// The project slug algorithm is not publicly documented, so we scan all project
// directories and match on the session_start entry's "cwd" field.
//
// Format reference: https://docs.factory.ai
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

type sessionCandidate struct {
	path    string
	modTime time.Time
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
	meta := &adapterprotocol.ReadMetadataResult{}

	// check companion .settings.json for model info
	settingsPath := strings.TrimSuffix(p.SessionFile, ".jsonl") + ".settings.json"
	if data, err := os.ReadFile(settingsPath); err == nil {
		var companion companionSettings
		if json.Unmarshal(data, &companion) == nil {
			meta.Model = companion.Model
		}
	}

	// scan JSONL for version from session_start
	f, err := os.Open(p.SessionFile)
	if err != nil {
		return meta, nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry droidEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry.Type == "session_start" && entry.Version > 0 {
			meta.AgentVersion = fmt.Sprintf("%d", entry.Version)
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

	// check companion settings for model
	settingsPath := strings.TrimSuffix(path, ".jsonl") + ".settings.json"
	if data, err := os.ReadFile(settingsPath); err == nil {
		var companion companionSettings
		if json.Unmarshal(data, &companion) == nil {
			meta.Model = companion.Model
		}
	}

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

		// extract version from session_start
		if meta.AgentVersion == "" {
			var raw droidEntry
			if json.Unmarshal(line, &raw) == nil && raw.Type == "session_start" && raw.Version > 0 {
				meta.AgentVersion = fmt.Sprintf("%d", raw.Version)
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

	newOffset := offset
	if info, err := f.Stat(); err == nil {
		newOffset = info.Size()
	}

	return entries, newOffset, nil
}

// findSessionFile locates a Droid session file for the given repo.
// Since the project slug algorithm is undocumented, we scan all project
// directories and match on the session_start cwd field.
func findSessionFile(repoRoot, agentID, since, agentSessionID string) (string, int64, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", 0, fmt.Errorf("cannot determine home directory: %w", err)
	}

	projectsDir := filepath.Join(home, ".factory", "projects")

	// direct lookup via session UUID across all project dirs
	if agentSessionID != "" {
		if err := adapterruntime.ValidateSessionID(agentSessionID); err != nil {
			return "", 0, err
		}
		if path, ok := findSessionByUUID(projectsDir, agentSessionID); ok {
			sinceTime := parseSince(since)
			offset := findStartOffset(path, sinceTime)
			return path, offset, nil
		}
	}

	sinceTime := parseSince(since)

	// find project dirs that match this repo root
	projectDir := findProjectDirForRepo(projectsDir, repoRoot)
	if projectDir == "" {
		return "", 0, fmt.Errorf("no sessions for project %s", repoRoot)
	}

	candidates := collectCandidates(projectDir, sinceTime)
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

// findSessionByUUID searches all project dirs for a session file matching the UUID.
func findSessionByUUID(projectsDir, uuid string) (string, bool) {
	target := uuid + ".jsonl"
	projectDirs, err := os.ReadDir(projectsDir)
	if err != nil {
		return "", false
	}
	for _, d := range projectDirs {
		if !d.IsDir() {
			continue
		}
		candidate := filepath.Join(projectsDir, d.Name(), target)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
	}
	return "", false
}

// findProjectDirForRepo scans project directories to find one whose sessions
// were created from the given repo root. Checks the session_start cwd field.
func findProjectDirForRepo(projectsDir, repoRoot string) string {
	dirs, err := os.ReadDir(projectsDir)
	if err != nil {
		return ""
	}

	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dirPath := filepath.Join(projectsDir, d.Name())
		if projectDirMatchesRepo(dirPath, repoRoot) {
			return dirPath
		}
	}
	return ""
}

// projectDirMatchesRepo checks if any session in the dir has a session_start
// cwd matching the repo root.
func projectDirMatchesRepo(dirPath, repoRoot string) bool {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return false
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dirPath, e.Name())
		if cwd := readSessionCWD(path); cwd != "" {
			// normalize trailing slashes for comparison
			return filepath.Clean(cwd) == filepath.Clean(repoRoot)
		}
	}
	return false
}

// readSessionCWD reads the cwd from the first session_start line.
func readSessionCWD(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 16*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry struct {
			Type string `json:"type"`
			CWD  string `json:"cwd"`
		}
		if json.Unmarshal(line, &entry) == nil && entry.Type == "session_start" {
			return entry.CWD
		}
		// session_start should be the first line; stop after first valid entry
		break
	}
	return ""
}

func collectCandidates(projectDir string, sinceTime time.Time) []sessionCandidate {
	dirEntries, err := os.ReadDir(projectDir)
	if err != nil {
		return nil
	}

	var candidates []sessionCandidate
	for _, entry := range dirEntries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if !sinceTime.IsZero() && !info.ModTime().After(sinceTime) {
			continue
		}
		candidates = append(candidates, sessionCandidate{
			path:    filepath.Join(projectDir, entry.Name()),
			modTime: info.ModTime(),
		})
	}
	return candidates
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
			if t, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
				if t.After(sinceTime) {
					return offset
				}
			}
		}
		offset += int64(len(line)) + 1 // +1 for newline
	}
	return offset
}

func parseSince(since string) time.Time {
	if since == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, since)
	return t
}

// --- line parsing ---

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// parseLine converts a single JSONL line into adapter entries.
// Droid uses two top-level types: "session_start" (metadata, skipped) and
// "message" (conversation turns with nested message.role and message.content).
func parseLine(line []byte) ([]adapterprotocol.RawEntry, error) {
	var raw droidEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}

	if raw.Type != "message" || raw.Message == nil {
		return nil, nil
	}

	switch raw.Message.Role {
	case "user":
		return parseUserMessage(&raw)
	case "assistant":
		return parseAssistantMessage(&raw)
	default:
		return nil, nil
	}
}

func parseUserMessage(raw *droidEntry) ([]adapterprotocol.RawEntry, error) {
	ts := parseTS(raw.Timestamp)
	text := extractTextFromContent(raw.Message.Content)
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, nil
	}
	return []adapterprotocol.RawEntry{adapterruntime.UserEntry(ts, trimmed)}, nil
}

func parseAssistantMessage(raw *droidEntry) ([]adapterprotocol.RawEntry, error) {
	ts := parseTS(raw.Timestamp)

	var blocks []droidBlock
	blockData, err := json.Marshal(raw.Message.Content)
	if err != nil {
		return nil, nil
	}
	if err := json.Unmarshal(blockData, &blocks); err != nil {
		return nil, nil
	}

	var entries []adapterprotocol.RawEntry
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				entries = append(entries, adapterruntime.AssistantEntry(ts, block.Text))
			}
		case "tool_use":
			inputJSON, _ := json.Marshal(block.Input)
			entries = append(entries, adapterruntime.ToolUseEntry(ts, block.Name, string(inputJSON)))
		case "tool_result":
			// tool_result blocks in Droid contain the result content inline
			if block.Content != "" {
				entries = append(entries, adapterruntime.ToolResultEntry(ts, block.Content, block.IsError))
			}
		}
	}

	return entries, nil
}

// extractTextFromContent pulls text from a content array (used for user messages).
func extractTextFromContent(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, item := range c {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if block["type"] == "text" {
				if text, ok := block["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// --- types ---

// droidEntry represents a top-level JSONL line from a Droid session.
// Type is "session_start" for the first line, "message" for conversation turns.
type droidEntry struct {
	Type      string        `json:"type"`
	ID        string        `json:"id"`
	Timestamp string        `json:"timestamp"`
	ParentID  string        `json:"parentId,omitempty"`
	Message   *droidMessage `json:"message,omitempty"`
	// session_start fields
	Title   string `json:"title,omitempty"`
	CWD     string `json:"cwd,omitempty"`
	Version int    `json:"version,omitempty"`
}

// droidMessage is the nested message object inside a "message" entry.
type droidMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// droidBlock represents a content block inside message.content[].
type droidBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// companionSettings is the <uuid>.settings.json file alongside each session.
type companionSettings struct {
	Model string `json:"model"`
}
