// session.go handles session reading, parsing, discovery, and types.
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

type sessionCandidate struct {
	path    string
	modTime time.Time
}

// piRecord is one line of a Pi transcript. Pi writes a small set of record
// types; only "message" carries conversation content.
//
//	{"type":"session","version":3,"id":…,"cwd":…}
//	{"type":"model_change","provider":"anthropic","modelId":…}
//	{"type":"thinking_level_change","thinkingLevel":"medium"}
//	{"type":"message","timestamp":…,"message":{…}}
type piRecord struct {
	Type      string     `json:"type"`
	Timestamp string     `json:"timestamp,omitempty"`
	Version   int        `json:"version,omitempty"` // session header
	ModelID   string     `json:"modelId,omitempty"` // model_change
	Provider  string     `json:"provider,omitempty"`
	Message   *piMessage `json:"message,omitempty"`
}

// piMessage is the nested message envelope. Tool results arrive as their own
// message with role "toolResult" rather than as a block inside a turn.
type piMessage struct {
	Role       string    `json:"role"` // user | assistant | toolResult
	Content    []piBlock `json:"content"`
	ToolCallID string    `json:"toolCallId,omitempty"`
	ToolName   string    `json:"toolName,omitempty"`
	IsError    bool      `json:"isError,omitempty"`
}

// piBlock is one content block within a message.
type piBlock struct {
	Type      string          `json:"type"` // text | thinking | toolCall
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ID        string          `json:"id,omitempty"`   // toolCall
	Name      string          `json:"name,omitempty"` // toolCall
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// piSupportedVersions are the session-header versions this parser understands.
// Anything else is reported by diagnose rather than silently read as empty.
var piSupportedVersions = map[int]bool{3: true}

// --- session reading ---

func handleRead(p adapterprotocol.ReadParams) (*adapterprotocol.ReadResult, error) {
	entries, err := readPiFile(p.SessionFile)
	if err != nil {
		return nil, err
	}
	meta := extractPiMetadata(p.SessionFile)
	return &adapterprotocol.ReadResult{Entries: entries, Metadata: meta}, nil
}

func handleReadMetadata(p adapterprotocol.ReadParams) (*adapterprotocol.ReadMetadataResult, error) {
	meta := extractPiMetadata(p.SessionFile)
	if meta == nil {
		return &adapterprotocol.ReadMetadataResult{}, nil
	}
	return &adapterprotocol.ReadMetadataResult{
		AgentVersion: meta.AgentVersion,
		Model:        meta.Model,
	}, nil
}

func readPiFile(path string) ([]adapterprotocol.RawEntry, error) {
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
		entries = append(entries, parsePiLine(line)...)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading session file: %w", err)
	}

	return entries, nil
}

// readPiFromOffset resumes a Pi transcript at a byte offset using the shared
// JSONL tail reader (pkg/adapterruntime.TailJSONL). The hand-rolled version
// this replaced advanced the offset to the file's current size on every
// call, which acknowledges bytes that were never parsed: Pi writes its
// transcript incrementally, so the final line read mid-write is frequently
// partial, and advancing past it silently drops the rest of that turn once
// Pi finishes writing it. TailJSONL stops at the last complete newline
// instead.
func readPiFromOffset(path string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
	return adapterruntime.TailJSONL(path, offset, func(line []byte) ([]adapterprotocol.RawEntry, error) {
		return parsePiLine(line), nil
	})
}

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// parsePiLine converts one transcript line into ox entries. A single assistant
// message can hold text and several tool calls, so this returns a slice.
func parsePiLine(line []byte) []adapterprotocol.RawEntry {
	var rec piRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil
	}
	if rec.Type != "message" || rec.Message == nil {
		return nil
	}

	ts := parseTS(rec.Timestamp)
	msg := rec.Message

	if msg.Role == "toolResult" {
		return []adapterprotocol.RawEntry{
			adapterruntime.ToolResultWithID(ts, blockText(msg.Content), msg.IsError, msg.ToolCallID),
		}
	}

	var entries []adapterprotocol.RawEntry
	for _, b := range msg.Content {
		switch b.Type {
		case "text":
			if b.Text == "" {
				continue
			}
			entries = append(entries, makePiEntry(msg.Role, ts, b.Text))

		case "toolCall":
			entries = append(entries, adapterruntime.ToolUseWithID(ts, b.Name, string(b.Arguments), b.ID))

		case "thinking":
			// reasoning content — not user-visible, skip
		}
	}

	return entries
}

// blockText concatenates the text blocks of a message, which is how Pi
// represents a tool's output.
func blockText(blocks []piBlock) string {
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func makePiEntry(role string, ts time.Time, content string) adapterprotocol.RawEntry {
	switch role {
	case "user":
		return adapterruntime.UserEntry(ts, content)
	case "system":
		return adapterruntime.SystemEntry(ts, content)
	default:
		return adapterruntime.AssistantEntry(ts, content)
	}
}

// --- session discovery ---

// piSessionsDir returns the base sessions directory for Pi.
func piSessionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".pi", "agent", "sessions"), nil
}

// cwdToDirName converts a working directory path to Pi's session directory name.
// Pi uses -- prefix and replaces / with -- (e.g., /Users/dev/project -> --Users--dev--project).
func cwdToDirName(cwd string) string {
	return "--" + strings.ReplaceAll(strings.TrimPrefix(cwd, "/"), "/", "--")
}

func findPiSession(repoRoot, agentID, since, agentSessionID string) (string, error) {
	baseDir, err := piSessionsDir()
	if err != nil {
		return "", err
	}

	// direct lookup by session ID
	if agentSessionID != "" {
		if err := adapterruntime.ValidateSessionID(agentSessionID); err != nil {
			return "", err
		}

		if repoRoot != "" {
			// Same project-scoping rule as the fallback search below: a
			// project-scoped query must never reach into another project's
			// directory, even when the caller also supplies a session ID.
			// Ledgers are shared with teammates, so returning another
			// repo's session here would upload its conversation into the
			// wrong Ledger.
			direct := filepath.Join(baseDir, cwdToDirName(repoRoot), agentSessionID+".jsonl")
			if _, err := os.Stat(direct); err == nil {
				return direct, nil
			}
		} else {
			// unscoped query: search across all subdirectories for this
			// session ID
			subdirs, _ := os.ReadDir(baseDir)
			for _, d := range subdirs {
				if !d.IsDir() {
					continue
				}
				direct := filepath.Join(baseDir, d.Name(), agentSessionID+".jsonl")
				if _, err := os.Stat(direct); err == nil {
					return direct, nil
				}
			}
		}
	}

	sinceTime := time.Time{}
	if since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			sinceTime = t
		}
	}

	// A project-scoped query (repoRoot set) is confined to that project's own
	// directory — never falling back to "newest session anywhere" — because
	// Ledgers are per-repo and shared with teammates; attributing another
	// project's transcript to this repo would leak its conversation content
	// into the wrong Ledger. Only a genuinely unscoped query (repoRoot == "")
	// searches every subdirectory.
	var searchDirs []string
	if repoRoot != "" {
		projectDir := filepath.Join(baseDir, cwdToDirName(repoRoot))
		if info, err := os.Stat(projectDir); err != nil || !info.IsDir() {
			return "", fmt.Errorf("no pi sessions found for %s", repoRoot)
		}
		searchDirs = append(searchDirs, projectDir)
	} else {
		subdirs, err := os.ReadDir(baseDir)
		if err != nil {
			return "", fmt.Errorf("failed to read sessions dir: %w", err)
		}
		for _, d := range subdirs {
			if d.IsDir() {
				searchDirs = append(searchDirs, filepath.Join(baseDir, d.Name()))
			}
		}
	}

	var candidates []sessionCandidate
	for _, dir := range searchDirs {
		entries, err := os.ReadDir(dir)
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
			if !sinceTime.IsZero() && !info.ModTime().After(sinceTime) {
				continue
			}
			candidates = append(candidates, sessionCandidate{
				path:    filepath.Join(dir, entry.Name()),
				modTime: info.ModTime(),
			})
		}
	}

	if len(candidates) == 0 {
		if repoRoot != "" {
			return "", fmt.Errorf("no pi sessions found for %s", repoRoot)
		}
		return "", fmt.Errorf("no pi sessions found")
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})

	if agentID != "" {
		for _, c := range candidates {
			if sessionContainsText(c.path, agentID) {
				return c.path, nil
			}
		}
	}

	return candidates[0].path, nil
}

func sessionContainsText(path, text string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		if strings.Contains(scanner.Text(), text) {
			return true
		}
	}
	return false
}

func extractPiMetadata(path string) *adapterprotocol.SessionMetadata {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	meta := &adapterprotocol.SessionMetadata{}
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	// the session header and the initial model_change both precede the first
	// message, so stop once the conversation starts
	for scanner.Scan() {
		var rec piRecord
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			continue
		}
		switch rec.Type {
		case "session":
			if rec.Version > 0 {
				meta.AgentVersion = fmt.Sprintf("pi-v%d", rec.Version)
			}
		case "model_change":
			if rec.ModelID != "" {
				meta.Model = rec.ModelID
			}
		case "message":
			if meta.AgentVersion == "" && meta.Model == "" {
				return nil
			}
			return meta
		}
	}

	if meta.AgentVersion == "" && meta.Model == "" {
		return nil
	}
	return meta
}
