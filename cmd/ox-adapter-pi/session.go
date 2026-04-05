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

type piEntry struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp,omitempty"`
	Content   string `json:"content,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     string `json:"input,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	Model     string `json:"model,omitempty"`
	Version   int    `json:"version,omitempty"`
	ID        string `json:"id,omitempty"`
	CWD       string `json:"cwd,omitempty"`
}

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
		parsed := parsePiLine(line)
		if parsed != nil {
			entries = append(entries, *parsed)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading session file: %w", err)
	}

	return entries, nil
}

func readPiFromOffset(path string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
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
		parsed := parsePiLine(line)
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

func parsePiLine(line []byte) *adapterprotocol.RawEntry {
	var raw piEntry
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

	case "tool_call":
		e := adapterruntime.ToolUseWithID(ts, raw.Name, raw.Input, raw.CallID)
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
		// search across all subdirectories for this session ID
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

	sinceTime := time.Time{}
	if since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			sinceTime = t
		}
	}

	// if repoRoot is provided, look in the specific project subdirectory first
	var searchDirs []string
	if repoRoot != "" {
		projectDir := filepath.Join(baseDir, cwdToDirName(repoRoot))
		if info, err := os.Stat(projectDir); err == nil && info.IsDir() {
			searchDirs = append(searchDirs, projectDir)
		}
	}

	// fall back to searching all subdirectories
	if len(searchDirs) == 0 {
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

	for scanner.Scan() {
		var raw piEntry
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			continue
		}
		// session header contains version info
		if raw.Type == "session" {
			if raw.Version > 0 {
				meta.AgentVersion = fmt.Sprintf("pi-v%d", raw.Version)
			}
			if raw.Model != "" {
				meta.Model = raw.Model
			}
			break
		}
	}

	if meta.AgentVersion == "" && meta.Model == "" {
		return nil
	}
	return meta
}
