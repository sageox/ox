package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
)

func TestFormatSize_ShowCoverage(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
		{1099511627776, "1.0 TB"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatSize(tt.bytes)
			if got != tt.want {
				t.Errorf("formatSize(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}

func TestConvertStoredSession_NilMeta(t *testing.T) {
	st := &session.StoredSession{
		Info: session.SessionInfo{
			Filename: "test.jsonl",
			Type:     "raw",
		},
		Entries: []map[string]any{
			{"type": "message", "content": "hello"},
		},
	}
	result := convertStoredSession(st)
	if result.Metadata != nil {
		t.Error("expected nil metadata when StoredSession.Meta is nil")
	}
	if result.Info.Filename != "test.jsonl" {
		t.Errorf("expected filename test.jsonl, got %s", result.Info.Filename)
	}
	if len(result.Entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(result.Entries))
	}
}

func TestConvertStoredSession_WithMeta(t *testing.T) {
	now := time.Now()
	st := &session.StoredSession{
		Info: session.SessionInfo{
			Filename: "session.jsonl",
		},
		Meta: &session.StoreMeta{
			Version:   "1",
			AgentID:   "Ox1234",
			AgentType: "claude-code",
			Username:  "testuser",
			RepoID:    "repo-abc",
			CreatedAt: now,
		},
		Footer: map[string]any{
			"closed_at":   now.Format(time.RFC3339Nano),
			"entry_count": float64(5),
		},
	}
	result := convertStoredSession(st)
	if result.Metadata == nil {
		t.Fatal("expected non-nil metadata")
	}
	if result.Metadata.Version != "1" {
		t.Errorf("expected version 1, got %s", result.Metadata.Version)
	}
	if result.Metadata.AgentID != "Ox1234" {
		t.Errorf("expected agent ID Ox1234, got %s", result.Metadata.AgentID)
	}
	if result.Metadata.AgentType != "claude-code" {
		t.Errorf("expected agent type claude-code, got %s", result.Metadata.AgentType)
	}
	if result.Metadata.Username != "testuser" {
		t.Errorf("expected username testuser, got %s", result.Metadata.Username)
	}
	if result.Metadata.RepoID != "repo-abc" {
		t.Errorf("expected repo ID repo-abc, got %s", result.Metadata.RepoID)
	}
	if !result.Metadata.CreatedAt.Equal(now) {
		t.Error("createdAt mismatch")
	}
}

func TestShowRawSession_LimitEntries(t *testing.T) {
	data := &sessionShowData{
		Info: session.SessionInfo{Filename: "test.jsonl"},
		Entries: []map[string]any{
			{"seq": 1, "type": "user"},
			{"seq": 2, "type": "assistant"},
			{"seq": 3, "type": "tool"},
			{"seq": 4, "type": "user"},
			{"seq": 5, "type": "assistant"},
		},
	}

	// capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := showRawSession(data, 2)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("showRawSession returned error: %v", err)
	}

	var buf bytes.Buffer
	io.Copy(&buf, r)

	var result sessionShowData
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse output JSON: %v", err)
	}

	if len(result.Entries) != 2 {
		t.Errorf("expected 2 entries after limit, got %d", len(result.Entries))
	}
}

func TestShowRawSession_NoLimit(t *testing.T) {
	data := &sessionShowData{
		Info: session.SessionInfo{Filename: "test.jsonl"},
		Entries: []map[string]any{
			{"seq": 1},
			{"seq": 2},
			{"seq": 3},
		},
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := showRawSession(data, 0)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("showRawSession returned error: %v", err)
	}

	var buf bytes.Buffer
	io.Copy(&buf, r)

	var result sessionShowData
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse output JSON: %v", err)
	}

	if len(result.Entries) != 3 {
		t.Errorf("expected 3 entries with no limit, got %d", len(result.Entries))
	}
}

// captureStdout runs fn while capturing stdout, returning the output.
func captureStdout(fn func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

func TestPrintSessionEntry_MessageType(t *testing.T) {
	entry := map[string]any{
		"type":      "message",
		"timestamp": time.Now().Format(time.RFC3339Nano),
		"data": map[string]any{
			"role":    "user",
			"content": "hello world",
		},
	}
	output := captureStdout(func() { printSessionEntry(1, entry) })
	assert.Contains(t, output, "user")
}

func TestPrintSessionEntry_ToolCallType(t *testing.T) {
	entry := map[string]any{
		"type": "tool_call",
		"data": map[string]any{
			"tool_name": "read_file",
			"input":     "/path/to/file",
		},
	}
	output := captureStdout(func() { printSessionEntry(2, entry) })
	assert.Contains(t, output, "read_file")
}

func TestPrintSessionEntry_ToolResultType(t *testing.T) {
	entry := map[string]any{
		"type": "tool_result",
		"data": map[string]any{
			"tool_name": "write_file",
			"success":   true,
			"output":    "file written successfully",
		},
	}
	output := captureStdout(func() { printSessionEntry(3, entry) })
	assert.Contains(t, output, "write_file")
}

func TestPrintSessionEntry_ToolResultFailed(t *testing.T) {
	entry := map[string]any{
		"type": "tool_result",
		"data": map[string]any{
			"tool_name": "write_file",
			"success":   false,
			"output":    "permission denied",
		},
	}
	output := captureStdout(func() { printSessionEntry(4, entry) })
	assert.Contains(t, output, "write_file")
}

func TestPrintSessionEntry_UnknownType(t *testing.T) {
	entry := map[string]any{
		"type": "custom_event",
		"data": map[string]any{
			"key": "value",
		},
	}
	output := captureStdout(func() { printSessionEntry(5, entry) })
	assert.NotEmpty(t, output)
}

func TestPrintSessionEntry_EmptyType(t *testing.T) {
	old := os.Stdout
	_, w, _ := os.Pipe()
	os.Stdout = w

	entry := map[string]any{
		"timestamp": "2026-01-01T00:00:00Z",
	}
	printSessionEntry(1, entry)

	w.Close()
	os.Stdout = old
}

func TestPrintMessageEntry_LongContent(t *testing.T) {
	longContent := strings.Repeat("x", 250)
	entry := map[string]any{
		"data": map[string]any{
			"role":    "assistant",
			"content": longContent,
		},
	}
	output := captureStdout(func() { printMessageEntry(entry) })
	assert.Contains(t, output, "assistant")
}

func TestPrintMessageEntry_MultilineContent(t *testing.T) {
	entry := map[string]any{
		"data": map[string]any{
			"role":    "user",
			"content": "line1\nline2\nline3\nline4\nline5\nline6\nline7",
		},
	}
	output := captureStdout(func() { printMessageEntry(entry) })
	assert.Contains(t, output, "user")
}

func TestPrintMessageEntry_NoData(t *testing.T) {
	old := os.Stdout
	_, w, _ := os.Pipe()
	os.Stdout = w

	// no "data" key — should return silently
	entry := map[string]any{}
	printMessageEntry(entry)

	w.Close()
	os.Stdout = old
}

func TestPrintMessageEntry_NoRole(t *testing.T) {
	old := os.Stdout
	_, w, _ := os.Pipe()
	os.Stdout = w

	entry := map[string]any{
		"data": map[string]any{
			"content": "anonymous message",
		},
	}
	printMessageEntry(entry)

	w.Close()
	os.Stdout = old
}

func TestPrintToolCallEntry_NoData(t *testing.T) {
	old := os.Stdout
	_, w, _ := os.Pipe()
	os.Stdout = w

	entry := map[string]any{}
	printToolCallEntry(entry)

	w.Close()
	os.Stdout = old
}

func TestPrintToolCallEntry_LongInput(t *testing.T) {
	longInput := strings.Repeat("y", 150)
	entry := map[string]any{
		"data": map[string]any{
			"tool_name": "bash",
			"input":     longInput,
		},
	}
	output := captureStdout(func() { printToolCallEntry(entry) })
	assert.Contains(t, output, "bash")
}

func TestPrintToolResultEntry_NoData(t *testing.T) {
	old := os.Stdout
	_, w, _ := os.Pipe()
	os.Stdout = w

	entry := map[string]any{}
	printToolResultEntry(entry)

	w.Close()
	os.Stdout = old
}

func TestPrintToolResultEntry_LongOutput(t *testing.T) {
	longOutput := strings.Repeat("z", 150)
	entry := map[string]any{
		"data": map[string]any{
			"tool_name": "bash",
			"success":   true,
			"output":    longOutput,
		},
	}
	output := captureStdout(func() { printToolResultEntry(entry) })
	assert.NotEmpty(t, output)
}

func TestPrintGenericEntry_NoData(t *testing.T) {
	old := os.Stdout
	_, w, _ := os.Pipe()
	os.Stdout = w

	entry := map[string]any{}
	printGenericEntry(entry)

	w.Close()
	os.Stdout = old
}

func TestPrintGenericEntry_LongJSON(t *testing.T) {
	longVal := strings.Repeat("a", 200)
	entry := map[string]any{
		"data": map[string]any{
			"long_key": longVal,
		},
	}
	output := captureStdout(func() { printGenericEntry(entry) })
	assert.NotEmpty(t, output)
}

func TestShowFormattedSession_MetadataOnly(t *testing.T) {
	now := time.Now()
	data := &sessionShowData{
		Info: session.SessionInfo{
			Filename:  "test.jsonl",
			Type:      "raw",
			Size:      1234,
			CreatedAt: now,
			ModTime:   now,
		},
		Metadata: &sessionMetadata{
			Version:   "1",
			AgentID:   "OxABC",
			AgentType: "claude-code",
			Username:  "testuser",
			RepoID:    "repo-123",
			CreatedAt: now,
		},
		Footer: map[string]any{
			"closed_at":   now.Format(time.RFC3339Nano),
			"entry_count": float64(10),
		},
		Entries: []map[string]any{
			{"type": "message"},
		},
	}

	var err error
	output := captureStdout(func() { err = showFormattedSession(data, true, 0) })

	if err != nil {
		t.Fatalf("showFormattedSession returned error: %v", err)
	}
	assert.Contains(t, output, "OxABC")
}

func TestShowFormattedSession_NoEntries(t *testing.T) {
	now := time.Now()
	data := &sessionShowData{
		Info: session.SessionInfo{
			Filename:  "test.jsonl",
			Type:      "raw",
			Size:      100,
			CreatedAt: now,
			ModTime:   now,
		},
		Entries: nil,
	}

	var err error
	output := captureStdout(func() { err = showFormattedSession(data, false, 0) })

	if err != nil {
		t.Fatalf("showFormattedSession returned error: %v", err)
	}
	assert.Contains(t, output, "test.jsonl")
}

func TestShowFormattedSession_WithLimit(t *testing.T) {
	now := time.Now()
	data := &sessionShowData{
		Info: session.SessionInfo{
			Filename:  "test.jsonl",
			Type:      "raw",
			Size:      100,
			CreatedAt: now,
			ModTime:   now,
		},
		Entries: []map[string]any{
			{"type": "message", "data": map[string]any{"role": "user", "content": "a"}},
			{"type": "message", "data": map[string]any{"role": "assistant", "content": "b"}},
			{"type": "message", "data": map[string]any{"role": "user", "content": "c"}},
		},
	}

	var err error
	output := captureStdout(func() { err = showFormattedSession(data, false, 1) })

	if err != nil {
		t.Fatalf("showFormattedSession returned error: %v", err)
	}
	assert.NotEmpty(t, output)
}

func TestViewAsJSON_MetadataOnly(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	now := time.Now()
	st := &session.StoredSession{
		Info: session.SessionInfo{Filename: "test.jsonl", CreatedAt: now, ModTime: now},
		Meta: &session.StoreMeta{Version: "1", AgentID: "OxABC"},
		Entries: []map[string]any{
			{"type": "message"},
			{"type": "tool_call"},
		},
	}

	err := viewAsJSON(st, true, 0)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("viewAsJSON returned error: %v", err)
	}

	var buf bytes.Buffer
	io.Copy(&buf, r)

	var result sessionShowData
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse output: %v", err)
	}

	// metadata-only mode should omit entries
	if len(result.Entries) != 0 {
		t.Errorf("expected 0 entries in metadata-only mode, got %d", len(result.Entries))
	}
}

func TestViewAsJSON_WithEntries(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	now := time.Now()
	st := &session.StoredSession{
		Info: session.SessionInfo{Filename: "test.jsonl", CreatedAt: now, ModTime: now},
		Entries: []map[string]any{
			{"type": "message"},
			{"type": "tool_call"},
		},
	}

	err := viewAsJSON(st, false, 1)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("viewAsJSON returned error: %v", err)
	}

	var buf bytes.Buffer
	io.Copy(&buf, r)

	var result sessionShowData
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse output: %v", err)
	}

	if len(result.Entries) != 1 {
		t.Errorf("expected 1 entry with limit=1, got %d", len(result.Entries))
	}
}

func TestPrintSessionEntry_InvalidTimestamp(t *testing.T) {
	entry := map[string]any{
		"type":      "message",
		"timestamp": "not-a-timestamp",
		"data": map[string]any{
			"role":    "user",
			"content": "test",
		},
	}
	output := captureStdout(func() { printSessionEntry(1, entry) })
	assert.Contains(t, output, "user")
}
