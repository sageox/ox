package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/session/adapters"
)

// testClaudeCodeAdapter is a minimal mock of the removed ClaudeCodeAdapter.
// Parses the Claude Code JSONL format used in hook and session tests.
type testClaudeCodeAdapter struct{}

func (a *testClaudeCodeAdapter) Name() string { return "claude-code" }
func (a *testClaudeCodeAdapter) Detect() bool { return false }
func (a *testClaudeCodeAdapter) FindSessionFile(_ adapters.SessionLookup) (string, error) {
	return "", adapters.ErrSessionNotFound
}
func (a *testClaudeCodeAdapter) ReadMetadata(_ string) (*adapters.SessionMetadata, error) {
	return nil, nil
}
func (a *testClaudeCodeAdapter) Watch(_ context.Context, _ string) (<-chan adapters.RawEntry, error) {
	return nil, adapters.ErrWatchNotSupported
}

func (a *testClaudeCodeAdapter) Read(sessionPath string) ([]adapters.RawEntry, error) {
	entries, _, err := a.readFrom(sessionPath, 0)
	return entries, err
}

func (a *testClaudeCodeAdapter) ReadFromOffset(path string, offset int64) ([]adapters.RawEntry, int64, error) {
	return a.readFrom(path, offset)
}

// readFrom parses Claude Code JSONL format from the given offset.
func (a *testClaudeCodeAdapter) readFrom(path string, offset int64) ([]adapters.RawEntry, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()

	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return nil, offset, err
		}
	}

	var entries []adapters.RawEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		parsed := a.parseLine(line)
		entries = append(entries, parsed...)
	}

	newOffset := offset
	if fi, err := f.Stat(); err == nil {
		newOffset = fi.Size()
	}
	return entries, newOffset, scanner.Err()
}

// parseLine parses a single Claude Code JSONL line into RawEntries.
func (a *testClaudeCodeAdapter) parseLine(line []byte) []adapters.RawEntry {
	var raw struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		IsMeta    bool   `json:"isMeta"`
		Message   struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil
	}
	if raw.IsMeta {
		return nil
	}

	ts, _ := time.Parse(time.RFC3339Nano, raw.Timestamp)
	if ts.IsZero() {
		ts, _ = time.Parse(time.RFC3339, raw.Timestamp)
	}

	switch raw.Type {
	case "user":
		// content can be string or array
		var text string
		if err := json.Unmarshal(raw.Message.Content, &text); err == nil {
			return []adapters.RawEntry{{
				Timestamp: ts,
				Role:      "user",
				Content:   text,
				Raw:       line,
			}}
		}
		return nil

	case "assistant":
		// content is array of blocks
		var blocks []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
			ID    string          `json:"id"`
		}
		if err := json.Unmarshal(raw.Message.Content, &blocks); err != nil {
			return nil
		}
		var entries []adapters.RawEntry
		for _, b := range blocks {
			switch b.Type {
			case "text":
				entries = append(entries, adapters.RawEntry{
					Timestamp: ts,
					Role:      "assistant",
					Content:   b.Text,
					Raw:       line,
				})
			case "tool_use":
				inputStr := string(b.Input)
				entries = append(entries, adapters.RawEntry{
					Timestamp: ts,
					Role:      "tool",
					ToolName:  b.Name,
					ToolInput: inputStr,
					CallID:    b.ID,
					Raw:       line,
				})
			}
		}
		return entries
	}
	return nil
}

// testCodexAdapter is a minimal mock of the removed CodexAdapter.
// Parses the Codex JSONL format used in manual publish tests.
type testCodexAdapter struct{}

func (a *testCodexAdapter) Name() string { return "codex" }
func (a *testCodexAdapter) Detect() bool { return false }
func (a *testCodexAdapter) FindSessionFile(lookup adapters.SessionLookup) (string, error) {
	// scan ~/.codex/sessions/ for recent session files (mirrors real CodexAdapter behavior)
	home, err := os.UserHomeDir()
	if err != nil {
		return "", adapters.ErrSessionNotFound
	}
	sessionsDir := filepath.Join(home, ".codex", "sessions")
	now := time.Now()
	for day := 0; day < 2; day++ {
		d := now.AddDate(0, 0, -day)
		dateDir := filepath.Join(sessionsDir, d.Format("2006"), d.Format("01"), d.Format("02"))
		entries, err := os.ReadDir(dateDir)
		if err != nil {
			continue
		}
		for i := len(entries) - 1; i >= 0; i-- {
			e := entries[i]
			if !e.IsDir() && filepath.Ext(e.Name()) == ".jsonl" {
				return filepath.Join(dateDir, e.Name()), nil
			}
		}
	}
	return "", adapters.ErrSessionNotFound
}
func (a *testCodexAdapter) ReadMetadata(sessionPath string) (*adapters.SessionMetadata, error) {
	f, err := os.Open(sessionPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var raw struct {
			Type    string `json:"type"`
			Version string `json:"version"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			continue
		}
		if raw.Type == "session_meta" {
			return &adapters.SessionMetadata{AgentVersion: raw.Version}, nil
		}
	}
	return nil, nil
}
func (a *testCodexAdapter) Watch(_ context.Context, _ string) (<-chan adapters.RawEntry, error) {
	return nil, adapters.ErrWatchNotSupported
}

func (a *testCodexAdapter) Read(sessionPath string) ([]adapters.RawEntry, error) {
	entries, _, err := a.readFrom(sessionPath, 0)
	return entries, err
}

func (a *testCodexAdapter) ReadFromOffset(path string, offset int64) ([]adapters.RawEntry, int64, error) {
	return a.readFrom(path, offset)
}

func (a *testCodexAdapter) readFrom(path string, offset int64) ([]adapters.RawEntry, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()

	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return nil, offset, err
		}
	}

	var entries []adapters.RawEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var raw struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Payload   struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}
		switch raw.Type {
		case "session_meta", "turn_context":
			continue
		case "response_item":
			if raw.Payload.Type != "message" || raw.Payload.Role == "" {
				continue
			}
			ts, _ := time.Parse(time.RFC3339Nano, raw.Timestamp)
			var text string
			for _, c := range raw.Payload.Content {
				if c.Type == "input_text" || c.Type == "output_text" {
					text += c.Text
				}
			}
			entries = append(entries, adapters.RawEntry{
				Timestamp: ts,
				Role:      raw.Payload.Role,
				Content:   text,
				Raw:       line,
			})
		}
	}

	newOffset := offset
	if fi, err := f.Stat(); err == nil {
		newOffset = fi.Size()
	}
	return entries, newOffset, scanner.Err()
}
