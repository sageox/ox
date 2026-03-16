//go:build !short

package adapters

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise the real ClaudeCodeAdapter.ReadFromOffset pipeline
// with realistic Claude Code JSONL content — the same format written by
// the actual Claude Code client during sessions.

// realClaudeJSONL returns a multi-line string simulating a real Claude Code
// session with user messages, assistant text, tool calls, system reminders,
// and file-history-snapshot entries that should be skipped.
func realClaudeJSONL() string {
	return `{"type":"file-history-snapshot","timestamp":"2026-01-05T10:00:00.000Z","files":[]}
{"type":"user","timestamp":"2026-01-05T10:00:01.000Z","message":{"role":"user","content":"Fix the login bug"},"isMeta":false}
{"type":"assistant","timestamp":"2026-01-05T10:00:03.000Z","message":{"role":"assistant","content":[{"type":"text","text":"I'll look at the login code."},{"type":"tool_use","id":"toolu_01","name":"Read","input":{"file_path":"/src/login.go"}}]}}
{"type":"user","timestamp":"2026-01-05T10:00:04.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"package main\nfunc Login() {}"}]}}
{"type":"assistant","timestamp":"2026-01-05T10:00:06.000Z","message":{"role":"assistant","content":[{"type":"text","text":"I see the issue. Let me fix it."},{"type":"tool_use","id":"toolu_02","name":"Edit","input":{"file_path":"/src/login.go","old_string":"func Login() {}","new_string":"func Login(user string) error { return nil }"}}]}}
{"type":"user","timestamp":"2026-01-05T10:00:07.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_02","content":"File edited successfully."}]}}
{"type":"assistant","timestamp":"2026-01-05T10:00:09.000Z","message":{"role":"assistant","content":[{"type":"text","text":"The login function now accepts a user parameter and returns an error."}]}}
`
}

func TestReadFromOffset_FullSession(t *testing.T) {
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "session.jsonl")
	require.NoError(t, os.WriteFile(sessionFile, []byte(realClaudeJSONL()), 0644))

	adapter := &ClaudeCodeAdapter{}

	// read everything from offset 0
	entries, newOffset, err := adapter.ReadFromOffset(sessionFile, 0)
	require.NoError(t, err)
	assert.Greater(t, newOffset, int64(0), "offset should advance")

	// file-history-snapshot and tool_result entries are skipped by parseLine
	// user "Fix the login bug" → 1 user entry
	// assistant (text + tool_use) → 2 entries (assistant + tool)
	// user (tool_result only) → skipped
	// assistant (text + tool_use) → 2 entries (assistant + tool)
	// user (tool_result only) → skipped
	// assistant (text only) → 1 entry
	// expected: 1 user + 2 assistant text + 2 tool_use + 1 final assistant = 6
	require.Len(t, entries, 6, "should have user + assistant + tool entries (exact count)")

	// verify first entry is user
	assert.Equal(t, "user", entries[0].Role)
	assert.Equal(t, "Fix the login bug", entries[0].Content)

	// verify we got assistant and tool entries
	var hasAssistant, hasTool bool
	for _, e := range entries {
		if e.Role == "assistant" {
			hasAssistant = true
		}
		if e.Role == "tool" {
			hasTool = true
			assert.NotEmpty(t, e.ToolName, "tool entries should have ToolName")
		}
	}
	assert.True(t, hasAssistant, "should have assistant entries")
	assert.True(t, hasTool, "should have tool entries")
}

func TestReadFromOffset_IncrementalReads(t *testing.T) {
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "session.jsonl")

	// write first batch (2 lines)
	batch1 := `{"type":"user","timestamp":"2026-01-05T10:00:01.000Z","message":{"role":"user","content":"Hello"}}
{"type":"assistant","timestamp":"2026-01-05T10:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Hi there!"}]}}
`
	require.NoError(t, os.WriteFile(sessionFile, []byte(batch1), 0644))

	adapter := &ClaudeCodeAdapter{}

	// first read: offset 0
	entries1, offset1, err := adapter.ReadFromOffset(sessionFile, 0)
	require.NoError(t, err)
	require.Len(t, entries1, 2, "first read: user + assistant")
	assert.Equal(t, "user", entries1[0].Role)
	assert.Equal(t, "Hello", entries1[0].Content)
	assert.Equal(t, "assistant", entries1[1].Role)
	assert.Greater(t, offset1, int64(0))

	// append second batch
	batch2 := `{"type":"user","timestamp":"2026-01-05T10:00:03.000Z","message":{"role":"user","content":"Now fix the bug"}}
{"type":"assistant","timestamp":"2026-01-05T10:00:04.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Sure, looking at it now."},{"type":"tool_use","id":"toolu_01","name":"Bash","input":{"command":"go test ./..."}}]}}
`
	f, err := os.OpenFile(sessionFile, os.O_WRONLY|os.O_APPEND, 0644)
	require.NoError(t, err)
	_, err = f.WriteString(batch2)
	require.NoError(t, err)
	f.Close()

	// second read: from previous offset — should only get new entries
	entries2, offset2, err := adapter.ReadFromOffset(sessionFile, offset1)
	require.NoError(t, err)
	assert.Greater(t, offset2, offset1, "offset should advance further")

	// second batch: user + assistant(text) + tool_use = 3 entries
	require.Len(t, entries2, 3)
	assert.Equal(t, "user", entries2[0].Role)
	assert.Equal(t, "Now fix the bug", entries2[0].Content)
	assert.Equal(t, "assistant", entries2[1].Role)
	assert.Equal(t, "tool", entries2[2].Role)
	assert.Equal(t, "Bash", entries2[2].ToolName)

	// third read from final offset: no new entries
	entries3, offset3, err := adapter.ReadFromOffset(sessionFile, offset2)
	require.NoError(t, err)
	assert.Empty(t, entries3, "no new entries")
	assert.Equal(t, offset2, offset3, "offset unchanged")
}

func TestReadFromOffset_SkipsNonConversationEntries(t *testing.T) {
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "session.jsonl")

	content := `{"type":"file-history-snapshot","timestamp":"2026-01-05T10:00:00.000Z","files":["/a.go","/b.go"]}
{"type":"queue-operation","timestamp":"2026-01-05T10:00:00.500Z","operation":"enqueue"}
{"type":"summary","timestamp":"2026-01-05T10:00:00.800Z","summary":"Session about login fix"}
{"type":"user","timestamp":"2026-01-05T10:00:01.000Z","message":{"role":"user","content":"The only real message"}}
`
	require.NoError(t, os.WriteFile(sessionFile, []byte(content), 0644))

	adapter := &ClaudeCodeAdapter{}
	entries, _, err := adapter.ReadFromOffset(sessionFile, 0)
	require.NoError(t, err)

	require.Len(t, entries, 1, "only user message should be parsed")
	assert.Equal(t, "The only real message", entries[0].Content)
}

func TestReadFromOffset_MalformedLinesSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "session.jsonl")

	content := `{"type":"user","timestamp":"2026-01-05T10:00:01.000Z","message":{"role":"user","content":"Before corruption"}}
{this is not valid json at all
{"type":"user","timestamp":"2026-01-05T10:00:02.000Z","message":{"role":"user","content":"After corruption"}}
`
	require.NoError(t, os.WriteFile(sessionFile, []byte(content), 0644))

	adapter := &ClaudeCodeAdapter{}
	entries, _, err := adapter.ReadFromOffset(sessionFile, 0)
	require.NoError(t, err)

	require.Len(t, entries, 2, "should skip malformed line")
	assert.Equal(t, "Before corruption", entries[0].Content)
	assert.Equal(t, "After corruption", entries[1].Content)
}

func TestReadFromOffset_TimestampsParsed(t *testing.T) {
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "session.jsonl")

	content := `{"type":"user","timestamp":"2026-03-11T14:30:00.000Z","message":{"role":"user","content":"Timestamped"}}
`
	require.NoError(t, os.WriteFile(sessionFile, []byte(content), 0644))

	adapter := &ClaudeCodeAdapter{}
	entries, _, err := adapter.ReadFromOffset(sessionFile, 0)
	require.NoError(t, err)

	require.Len(t, entries, 1)
	assert.Equal(t, 2026, entries[0].Timestamp.Year())
	assert.Equal(t, time.March, entries[0].Timestamp.Month())
	assert.Equal(t, 11, entries[0].Timestamp.Day())
}

func TestReadFromOffset_SystemRemindersClassifiedAsSystem(t *testing.T) {
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "session.jsonl")

	// system reminders use type:"user" but content starts with <system-reminder>
	content := `{"type":"user","timestamp":"2026-01-05T10:00:01.000Z","message":{"role":"user","content":"<system-reminder>\nYou have tools available.\n</system-reminder>"}}
{"type":"user","timestamp":"2026-01-05T10:00:02.000Z","message":{"role":"user","content":"Real user message"}}
`
	require.NoError(t, os.WriteFile(sessionFile, []byte(content), 0644))

	adapter := &ClaudeCodeAdapter{}
	entries, _, err := adapter.ReadFromOffset(sessionFile, 0)
	require.NoError(t, err)

	require.Len(t, entries, 2)
	assert.Equal(t, "system", entries[0].Role, "system-reminder should be classified as system")
	assert.Equal(t, "user", entries[1].Role)
}

func TestReadFromOffset_MultiBlockAssistantExpands(t *testing.T) {
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "session.jsonl")

	// single assistant line with text + 2 tool_use blocks → 3 entries
	content := `{"type":"assistant","timestamp":"2026-01-05T10:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"I'll read both files."},{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/a.go"}},{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"/b.go"}}]}}
`
	require.NoError(t, os.WriteFile(sessionFile, []byte(content), 0644))

	adapter := &ClaudeCodeAdapter{}
	entries, _, err := adapter.ReadFromOffset(sessionFile, 0)
	require.NoError(t, err)

	require.Len(t, entries, 3)
	assert.Equal(t, "assistant", entries[0].Role)
	assert.Equal(t, "I'll read both files.", entries[0].Content)
	assert.Equal(t, "tool", entries[1].Role)
	assert.Equal(t, "Read", entries[1].ToolName)
	assert.Equal(t, "tool", entries[2].Role)
	assert.Equal(t, "Read", entries[2].ToolName)
}

// --- Simulating the full hook-driven incremental pipeline ---

func TestIncrementalPipeline_SimulatePostToolUseHooks(t *testing.T) {
	// this test simulates the full hook pipeline:
	// 1. Claude Code writes entries to its JSONL
	// 2. PostToolUse hook calls ReadFromOffset to get new entries
	// 3. Entries are appended to raw.jsonl
	// 4. Offset is updated for next read
	// simulating what handleAfterTool does end-to-end

	tmpDir := t.TempDir()
	sourceFile := filepath.Join(tmpDir, "claude-session.jsonl")

	// start with empty file (Claude Code creates it)
	require.NoError(t, os.WriteFile(sourceFile, []byte(""), 0644))

	adapter := &ClaudeCodeAdapter{}
	var currentOffset int64

	// --- turn 1: user sends first message, Claude responds ---
	appendToFile(t, sourceFile,
		`{"type":"user","timestamp":"2026-01-05T10:00:01.000Z","message":{"role":"user","content":"Fix the bug"}}`,
		`{"type":"assistant","timestamp":"2026-01-05T10:00:03.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Looking at it."},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go test ./..."}}]}}`,
	)

	// PostToolUse hook fires
	entries1, newOffset1, err := adapter.ReadFromOffset(sourceFile, currentOffset)
	require.NoError(t, err)
	require.Len(t, entries1, 3) // user + assistant text + tool
	assert.Equal(t, "Fix the bug", entries1[0].Content)
	assert.Equal(t, "Looking at it.", entries1[1].Content)
	assert.Equal(t, "Bash", entries1[2].ToolName)
	currentOffset = newOffset1

	// --- turn 2: tool result + assistant response ---
	appendToFile(t, sourceFile,
		`{"type":"user","timestamp":"2026-01-05T10:00:04.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"PASS\nok  pkg/login 0.5s"}]}}`,
		`{"type":"assistant","timestamp":"2026-01-05T10:00:06.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Tests pass. The bug is fixed."}]}}`,
	)

	// PostToolUse hook fires again
	entries2, newOffset2, err := adapter.ReadFromOffset(sourceFile, currentOffset)
	require.NoError(t, err)
	// tool_result-only user entries are typically skipped by the adapter
	assert.GreaterOrEqual(t, len(entries2), 1, "should have at least the assistant response")
	currentOffset = newOffset2

	// --- turn 3: user sends follow-up ---
	appendToFile(t, sourceFile,
		`{"type":"user","timestamp":"2026-01-05T10:00:07.000Z","message":{"role":"user","content":"Now add tests"}}`,
		`{"type":"assistant","timestamp":"2026-01-05T10:00:09.000Z","message":{"role":"assistant","content":[{"type":"text","text":"I'll add tests for the login function."},{"type":"tool_use","id":"t2","name":"Write","input":{"file_path":"/src/login_test.go","content":"package main\nfunc TestLogin(t *testing.T) {}"}}]}}`,
	)

	entries3, newOffset3, err := adapter.ReadFromOffset(sourceFile, currentOffset)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(entries3), 2) // user + assistant text (+ tool)
	assert.Equal(t, "Now add tests", entries3[0].Content)
	assert.Greater(t, newOffset3, currentOffset)
	currentOffset = newOffset3

	// --- final: no more entries ---
	entriesFinal, finalOffset, err := adapter.ReadFromOffset(sourceFile, currentOffset)
	require.NoError(t, err)
	assert.Empty(t, entriesFinal)
	assert.Equal(t, currentOffset, finalOffset)
}

func TestIncrementalPipeline_CompactionSimulation(t *testing.T) {
	// simulates compaction: Claude Code creates a NEW session file
	// mid-conversation. The offset must reset to 0 for the new file.

	tmpDir := t.TempDir()

	// old file with entries
	oldFile := filepath.Join(tmpDir, "old-session.jsonl")
	appendToFile(t, oldFile,
		`{"type":"user","timestamp":"2026-01-05T10:00:01.000Z","message":{"role":"user","content":"Pre-compaction message"}}`,
		`{"type":"assistant","timestamp":"2026-01-05T10:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Pre-compaction response"}]}}`,
	)

	adapter := &ClaudeCodeAdapter{}

	// read from old file
	entries1, offset1, err := adapter.ReadFromOffset(oldFile, 0)
	require.NoError(t, err)
	require.Len(t, entries1, 2)

	// compaction happens — Claude Code creates new file
	newFile := filepath.Join(tmpDir, "new-session.jsonl")
	appendToFile(t, newFile,
		`{"type":"user","timestamp":"2026-01-05T10:05:00.000Z","message":{"role":"user","content":"Post-compaction message"}}`,
		`{"type":"assistant","timestamp":"2026-01-05T10:05:01.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Post-compaction response"}]}}`,
	)

	// if we try to read old file from old offset, no new entries
	entriesOld, _, err := adapter.ReadFromOffset(oldFile, offset1)
	require.NoError(t, err)
	assert.Empty(t, entriesOld)

	// read new file from offset 0 (reset after detecting new file)
	entriesNew, offset2, err := adapter.ReadFromOffset(newFile, 0)
	require.NoError(t, err)
	require.Len(t, entriesNew, 2)
	assert.Equal(t, "Post-compaction message", entriesNew[0].Content)
	assert.Greater(t, offset2, int64(0))
}

func TestIncrementalPipeline_ConcurrentAgentsDifferentFiles(t *testing.T) {
	// simulates Conductor: multiple agents reading different source files
	// with independent offset tracking

	tmpDir := t.TempDir()
	adapter := &ClaudeCodeAdapter{}

	type agentState struct {
		sourceFile string
		offset     int64
		entryCount int
	}

	agents := map[string]*agentState{
		"OxAgent1": {sourceFile: filepath.Join(tmpDir, "agent1.jsonl")},
		"OxAgent2": {sourceFile: filepath.Join(tmpDir, "agent2.jsonl")},
		"OxAgent3": {sourceFile: filepath.Join(tmpDir, "agent3.jsonl")},
	}

	// each agent starts with a user message
	for name, state := range agents {
		appendToFile(t, state.sourceFile,
			`{"type":"user","timestamp":"2026-01-05T10:00:01.000Z","message":{"role":"user","content":"`+name+` first message"}}`,
		)
	}

	// first hook read for each agent
	for name, state := range agents {
		entries, newOffset, err := adapter.ReadFromOffset(state.sourceFile, state.offset)
		require.NoError(t, err, "agent %s first read", name)
		require.Len(t, entries, 1)
		assert.Contains(t, entries[0].Content, name)
		state.offset = newOffset
		state.entryCount += len(entries)
	}

	// agent1 gets more activity, agent2 and agent3 are idle
	appendToFile(t, agents["OxAgent1"].sourceFile,
		`{"type":"assistant","timestamp":"2026-01-05T10:00:03.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Working on it."},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`,
		`{"type":"user","timestamp":"2026-01-05T10:00:04.000Z","message":{"role":"user","content":"Thanks"}}`,
	)

	// second hook read: only agent1 gets new entries
	for name, state := range agents {
		entries, newOffset, err := adapter.ReadFromOffset(state.sourceFile, state.offset)
		require.NoError(t, err)

		if name == "OxAgent1" {
			assert.GreaterOrEqual(t, len(entries), 2, "agent1 should have new entries")
		} else {
			assert.Empty(t, entries, "%s should have no new entries", name)
			assert.Equal(t, state.offset, newOffset, "%s offset unchanged", name)
		}
		state.offset = newOffset
		state.entryCount += len(entries)
	}

	// verify agent1 accumulated more entries than others
	assert.Greater(t, agents["OxAgent1"].entryCount, agents["OxAgent2"].entryCount)
	assert.Greater(t, agents["OxAgent1"].entryCount, agents["OxAgent3"].entryCount)
	assert.Equal(t, agents["OxAgent2"].entryCount, agents["OxAgent3"].entryCount)
}

func TestIncrementalPipeline_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "empty.jsonl")
	require.NoError(t, os.WriteFile(sessionFile, []byte(""), 0644))

	adapter := &ClaudeCodeAdapter{}
	entries, offset, err := adapter.ReadFromOffset(sessionFile, 0)
	require.NoError(t, err)
	assert.Empty(t, entries)
	assert.Equal(t, int64(0), offset)
}

func TestIncrementalPipeline_FileNotFound(t *testing.T) {
	adapter := &ClaudeCodeAdapter{}
	_, _, err := adapter.ReadFromOffset("/nonexistent/session.jsonl", 0)
	assert.Error(t, err)
}

func TestIncrementalReader_InterfaceCompliance(t *testing.T) {
	// verify ClaudeCodeAdapter implements IncrementalReader
	var adapter interface{} = &ClaudeCodeAdapter{}
	_, ok := adapter.(IncrementalReader)
	assert.True(t, ok, "ClaudeCodeAdapter should implement IncrementalReader")
}

// --- helpers ---

// appendToFile appends lines to a file, each followed by a newline.
func appendToFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	require.NoError(t, err)
	defer f.Close()
	for _, line := range lines {
		_, err := f.WriteString(line + "\n")
		require.NoError(t, err)
	}
}
