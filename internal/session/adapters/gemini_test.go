package adapters

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeminiAdapter_Name(t *testing.T) {
	a := &GeminiAdapter{}
	assert.Equal(t, "gemini", a.Name())
}

func TestGeminiAdapter_Read_SimpleConversation(t *testing.T) {
	session := geminiSession{
		Messages: []geminiMessage{
			{Role: "user", Parts: []geminiPart{{Text: "Fix the bug in main.go"}}},
			{Role: "model", Parts: []geminiPart{{Text: "I'll investigate the issue."}}},
		},
	}

	path := writeGeminiFixture(t, session)
	a := &GeminiAdapter{}
	entries, err := a.Read(path)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	assert.Equal(t, "user", entries[0].Role)
	assert.Equal(t, "Fix the bug in main.go", entries[0].Content)

	assert.Equal(t, "assistant", entries[1].Role)
	assert.Equal(t, "I'll investigate the issue.", entries[1].Content)
}

func TestGeminiAdapter_Read_WithToolCalls(t *testing.T) {
	session := geminiSession{
		Messages: []geminiMessage{
			{Role: "user", Parts: []geminiPart{{Text: "Read the file"}}},
			{Role: "model", Parts: []geminiPart{
				{Text: "Let me read that file."},
				{FunctionCall: &geminiFunctionCall{
					Name: "readFile",
					Args: map[string]interface{}{"path": "main.go"},
				}},
			}},
			{Role: "tool", Parts: []geminiPart{
				{FunctionResponse: &geminiFunctionResponse{
					Name:     "readFile",
					Response: map[string]interface{}{"content": "package main"},
				}},
			}},
		},
	}

	path := writeGeminiFixture(t, session)
	a := &GeminiAdapter{}
	entries, err := a.Read(path)
	require.NoError(t, err)
	require.Len(t, entries, 4)

	assert.Equal(t, "user", entries[0].Role)
	assert.Equal(t, "assistant", entries[1].Role)
	// function call entry
	assert.Equal(t, "tool", entries[2].Role)
	assert.Equal(t, "readFile", entries[2].ToolName)
	assert.Contains(t, entries[2].ToolInput, "main.go")
	// function response entry (successful output now captured)
	assert.Equal(t, "tool", entries[3].Role)
	assert.Equal(t, "readFile", entries[3].ToolName)
	assert.False(t, entries[3].IsError)
	assert.Contains(t, entries[3].ToolOutput, "package main")
}

func TestGeminiAdapter_Read_ToolError(t *testing.T) {
	session := geminiSession{
		Messages: []geminiMessage{
			{Role: "tool", Parts: []geminiPart{
				{FunctionResponse: &geminiFunctionResponse{
					Name:     "runCommand",
					Response: map[string]interface{}{"error": "command not found"},
				}},
			}},
		},
	}

	path := writeGeminiFixture(t, session)
	a := &GeminiAdapter{}
	entries, err := a.Read(path)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	assert.Equal(t, "tool", entries[0].Role)
	assert.Equal(t, "runCommand", entries[0].ToolName)
	assert.True(t, entries[0].IsError)
	assert.Contains(t, entries[0].ToolOutput, "command not found")
}

func TestGeminiAdapter_Read_EmptySession(t *testing.T) {
	session := geminiSession{Messages: nil}
	path := writeGeminiFixture(t, session)

	a := &GeminiAdapter{}
	entries, err := a.Read(path)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestGeminiAdapter_ReadFromOffset(t *testing.T) {
	session := geminiSession{
		Messages: []geminiMessage{
			{Role: "user", Parts: []geminiPart{{Text: "msg1"}}},
			{Role: "model", Parts: []geminiPart{{Text: "resp1"}}},
			{Role: "user", Parts: []geminiPart{{Text: "msg2"}}},
			{Role: "model", Parts: []geminiPart{{Text: "resp2"}}},
		},
	}

	path := writeGeminiFixture(t, session)
	a := &GeminiAdapter{}

	// read from offset 0: get all entries
	entries, newOffset, err := a.ReadFromOffset(path, 0)
	require.NoError(t, err)
	assert.Len(t, entries, 4)
	assert.Equal(t, int64(4), newOffset)

	// read from offset 2: get last 2 entries
	entries, newOffset, err = a.ReadFromOffset(path, 2)
	require.NoError(t, err)
	assert.Len(t, entries, 2)
	assert.Equal(t, int64(4), newOffset)
	assert.Equal(t, "msg2", entries[0].Content)

	// read from current offset: no new entries
	entries, newOffset, err = a.ReadFromOffset(path, 4)
	require.NoError(t, err)
	assert.Empty(t, entries)
	assert.Equal(t, int64(4), newOffset)
}

func TestGeminiAdapter_ReadMetadata(t *testing.T) {
	session := geminiSession{
		Messages: []geminiMessage{
			{Role: "user", Parts: []geminiPart{{Text: "hello"}}},
		},
		Metadata: geminiMetadata{
			Model:        "gemini-2.5-pro",
			AgentVersion: "0.1.0",
		},
	}

	path := writeGeminiFixture(t, session)
	a := &GeminiAdapter{}

	meta, err := a.ReadMetadata(path)
	require.NoError(t, err)
	require.NotNil(t, meta)
	assert.Equal(t, "gemini-2.5-pro", meta.Model)
	assert.Equal(t, "0.1.0", meta.AgentVersion)
}

func TestGeminiAdapter_ReadMetadata_NoMetadata(t *testing.T) {
	session := geminiSession{
		Messages: []geminiMessage{
			{Role: "user", Parts: []geminiPart{{Text: "hello"}}},
		},
	}

	path := writeGeminiFixture(t, session)
	a := &GeminiAdapter{}

	meta, err := a.ReadMetadata(path)
	require.NoError(t, err)
	assert.Nil(t, meta)
}

func TestGeminiAdapter_Watch(t *testing.T) {
	if testing.Short() {
		t.Skip("short: fsnotify watch test")
	}

	// write initial session
	dir := t.TempDir()
	path := filepath.Join(dir, "session-test.json")

	initial := geminiSession{
		Messages: []geminiMessage{
			{Role: "user", Parts: []geminiPart{{Text: "hello"}}},
			{Role: "model", Parts: []geminiPart{{Text: "hi"}}},
		},
	}
	writeGeminiFixtureTo(t, path, initial)

	a := &GeminiAdapter{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := a.Watch(ctx, path)
	require.NoError(t, err)

	// wait for watcher to establish baseline
	time.Sleep(200 * time.Millisecond)

	// update session with new messages
	updated := geminiSession{
		Messages: []geminiMessage{
			{Role: "user", Parts: []geminiPart{{Text: "hello"}}},
			{Role: "model", Parts: []geminiPart{{Text: "hi"}}},
			{Role: "user", Parts: []geminiPart{{Text: "new msg"}}},
		},
	}
	writeGeminiFixtureTo(t, path, updated)

	// collect entries with timeout
	var received []RawEntry
	timeout := time.After(3 * time.Second)
	for {
		select {
		case entry, ok := <-ch:
			if !ok {
				goto done
			}
			received = append(received, entry)
			if len(received) >= 1 {
				goto done
			}
		case <-timeout:
			goto done
		}
	}
done:

	require.Len(t, received, 1)
	assert.Equal(t, "user", received[0].Role)
	assert.Equal(t, "new msg", received[0].Content)
}

func TestGeminiAdapter_FindSessionFile(t *testing.T) {
	if testing.Short() {
		t.Skip("short: filesystem scan test")
	}

	// create mock Gemini directory structure
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	chatsDir := filepath.Join(tmpHome, ".gemini", "tmp", "proj-hash-1", "chats")
	require.NoError(t, os.MkdirAll(chatsDir, 0755))

	session := geminiSession{
		Messages: []geminiMessage{
			{Role: "user", Parts: []geminiPart{{Text: "hello"}}},
		},
	}
	path := filepath.Join(chatsDir, "session-123.json")
	writeGeminiFixtureTo(t, path, session)

	a := &GeminiAdapter{}
	found, err := a.FindSessionFile("", time.Now().Add(-1*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, path, found)
}

// helpers

func writeGeminiFixture(t *testing.T, session geminiSession) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "session.json")
	writeGeminiFixtureTo(t, path, session)
	return path
}

func writeGeminiFixtureTo(t *testing.T, path string, session geminiSession) {
	t.Helper()
	data, err := json.MarshalIndent(session, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.WriteFile(path, data, 0644))
}
