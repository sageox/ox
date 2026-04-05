// session.go handles Amp session reading, parsing, discovery, and types.
//
// Amp stores sessions as JSONL in ~/.amp/sessions/<session-id>.jsonl. Each line
// is a JSON object with "type" field: "user", "assistant", "tool_use",
// "tool_result", "system", or "session_meta". Tool entries use "tool_name",
// "tool_input", and "call_id" for correlation. Session metadata (model,
// agent_version) is in a "session_meta" entry.
//
// Format reference: https://sourcegraph.com/docs/amp
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

type ampEntry struct {
	Type         string `json:"type"`
	Timestamp    string `json:"timestamp"`
	Content      string `json:"content"`
	ToolName     string `json:"tool_name,omitempty"`
	ToolInput    string `json:"tool_input,omitempty"`
	CallID       string `json:"call_id,omitempty"`
	IsError      bool   `json:"is_error,omitempty"`
	Model        string `json:"model,omitempty"`
	AgentVersion string `json:"agent_version,omitempty"`
}

// --- session reading ---

func handleRead(p adapterprotocol.ReadParams) (*adapterprotocol.ReadResult, error) {
	entries, err := readAmpFile(p.SessionFile)
	if err != nil {
		return nil, err
	}
	meta := extractAmpMetadata(p.SessionFile)
	return &adapterprotocol.ReadResult{Entries: entries, Metadata: meta}, nil
}

func handleReadMetadata(p adapterprotocol.ReadParams) (*adapterprotocol.ReadMetadataResult, error) {
	meta := extractAmpMetadata(p.SessionFile)
	if meta == nil {
		return &adapterprotocol.ReadMetadataResult{}, nil
	}
	return &adapterprotocol.ReadMetadataResult{
		AgentVersion: meta.AgentVersion,
		Model:        meta.Model,
	}, nil
}

func readAmpFile(path string) ([]adapterprotocol.RawEntry, error) {
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
		parsed := parseAmpLine(line)
		if parsed != nil {
			entries = append(entries, *parsed)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading session file: %w", err)
	}

	return entries, nil
}

func readAmpFromOffset(path string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
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
		parsed := parseAmpLine(line)
		if parsed != nil {
			entries = append(entries, *parsed)
		}
	}

	if err := scanner.Err(); err != nil {
		return entries, offset, fmt.Errorf("error reading session file: %w", err)
	}

	newOffset := offset
	if info, err := f.Stat(); err == nil {
		newOffset = info.Size()
	}

	return entries, newOffset, nil
}

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func parseAmpLine(line []byte) *adapterprotocol.RawEntry {
	var raw ampEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil
	}

	ts := parseTS(raw.Timestamp)

	switch raw.Type {
	case "user":
		if raw.Content == "" {
			return nil
		}
		e := adapterruntime.UserEntry(ts, raw.Content)
		return &e

	case "assistant":
		if raw.Content == "" {
			return nil
		}
		e := adapterruntime.AssistantEntry(ts, raw.Content)
		return &e

	case "tool_use":
		e := adapterruntime.ToolUseWithID(ts, raw.ToolName, raw.ToolInput, raw.CallID)
		return &e

	case "tool_result":
		e := adapterruntime.ToolResultWithID(ts, raw.Content, raw.IsError, raw.CallID)
		return &e

	case "system":
		if raw.Content == "" {
			return nil
		}
		e := adapterruntime.SystemEntry(ts, raw.Content)
		return &e
	}

	return nil
}

// --- session discovery ---

func findAmpSession(_, agentID, since, agentSessionID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}

	sessionsDir := filepath.Join(home, ".amp", "sessions")

	// direct lookup by session ID
	if agentSessionID != "" {
		if err := adapterruntime.ValidateSessionID(agentSessionID); err != nil {
			return "", err
		}
		direct := filepath.Join(sessionsDir, agentSessionID+".jsonl")
		if _, err := os.Stat(direct); err == nil {
			return direct, nil
		}
	}

	sinceTime := time.Time{}
	if since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			sinceTime = t
		}
	}

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return "", fmt.Errorf("failed to read sessions dir: %w", err)
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
		if !sinceTime.IsZero() && !info.ModTime().After(sinceTime) {
			continue
		}
		candidates = append(candidates, sessionCandidate{
			path:    filepath.Join(sessionsDir, entry.Name()),
			modTime: info.ModTime(),
		})
	}

	if len(candidates) == 0 {
		return "", fmt.Errorf("no amp sessions found")
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

func extractAmpMetadata(path string) *adapterprotocol.SessionMetadata {
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
		var raw ampEntry
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			continue
		}
		if raw.Type == "session_meta" {
			if raw.Model != "" {
				meta.Model = raw.Model
			}
			if raw.AgentVersion != "" {
				meta.AgentVersion = raw.AgentVersion
			}
			break
		}
	}

	if meta.AgentVersion == "" && meta.Model == "" {
		return nil
	}
	return meta
}
