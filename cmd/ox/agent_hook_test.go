//go:build !short

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolvePhase(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		event     string
		want      string
	}{
		{"claude SessionStart", "claude-code", "SessionStart", phaseStart},
		{"claude SessionEnd", "claude-code", "SessionEnd", phaseEnd},
		{"claude PreToolUse", "claude-code", "PreToolUse", phaseBeforeTool},
		{"claude PostToolUse", "claude-code", "PostToolUse", phaseAfterTool},
		{"claude UserPromptSubmit", "claude-code", "UserPromptSubmit", phasePrompt},
		{"claude Stop", "claude-code", "Stop", phaseStop},
		{"claude PreCompact", "claude-code", "PreCompact", phaseCompact},
		{"claude unknown event", "claude-code", "SubagentStop", ""},
		{"claude alias resolves", "claudecode", "SessionStart", phaseStart},
		{"claude short alias resolves", "claude", "SessionStart", phaseStart},
		{"unknown agent falls back", "codex", "SessionStart", phaseStart},
		{"unknown agent unknown event", "codex", "FooBar", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolvePhase(tt.agentType, tt.event)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestActivePhaseBehavior(t *testing.T) {
	// phases with behavior
	assert.True(t, activePhaseBehavior[phaseStart])
	assert.True(t, activePhaseBehavior[phaseCompact])
	assert.True(t, activePhaseBehavior[phaseAfterTool])
	assert.True(t, activePhaseBehavior[phaseStop])

	// noop phases
	assert.False(t, activePhaseBehavior[phaseEnd])
	assert.False(t, activePhaseBehavior[phaseBeforeTool])
	assert.False(t, activePhaseBehavior[phasePrompt])
}

func TestDispatchPhase_NoopPhases(t *testing.T) {
	ctx := &HookContext{
		AgentType:   "claude-code",
		ProjectRoot: t.TempDir(),
	}

	// noop phases should return nil
	for _, phase := range []string{phaseEnd, phaseBeforeTool, phasePrompt} {
		ctx.Phase = phase
		err := dispatchPhase(ctx)
		assert.NoError(t, err, "phase %s should be noop", phase)
	}
}

func TestRunAgentHook_NoArgs(t *testing.T) {
	err := runAgentHook([]string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "usage")
}

// --- P0 #1: handleAfterTool direct unit tests ---

// setupHandleAfterToolTest creates a project with an active recording and a
// Claude Code source JSONL file, returning everything needed to call handleAfterTool directly.
func setupHandleAfterToolTest(t *testing.T) (projectRoot string, agentID string, sourceFile string) {
	t.Helper()

	cacheDir := t.TempDir()
	projectRoot = t.TempDir()

	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	cfg := `{"config_version":"2","repo_id":"test-repo-hook"}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(cfg), 0644))

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)

	agentID = "OxHook1"
	state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID:     agentID,
		AdapterName: "claude-code",
		Username:    "testuser",
	})
	require.NoError(t, err)

	// create a source JSONL file (simulates Claude Code's session file)
	sourceDir := t.TempDir()
	sourceFile = filepath.Join(sourceDir, "session.jsonl")
	require.NoError(t, os.WriteFile(sourceFile, []byte(""), 0644))

	// update recording state with source file and session path
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
		s.SessionFile = sourceFile
	}))

	// write the raw.jsonl header
	require.NoError(t, writeRawHeader(projectRoot, state))

	return projectRoot, agentID, sourceFile
}

func TestHandleAfterTool_WritesEntriesToRawJSONL(t *testing.T) {
	projectRoot, agentID, sourceFile := setupHandleAfterToolTest(t)

	// write entries to the source JSONL that are AFTER session start
	now := time.Now().Add(1 * time.Second)
	appendClaudeEntries(t, sourceFile, now,
		`{"type":"user","timestamp":"`+now.Format(time.RFC3339Nano)+`","message":{"role":"user","content":"Fix the bug"}}`,
		`{"type":"assistant","timestamp":"`+now.Add(time.Second).Format(time.RFC3339Nano)+`","message":{"role":"assistant","content":[{"type":"text","text":"Looking at it."}]}}`,
	)

	ctx := &HookContext{
		Phase:       phaseAfterTool,
		ProjectRoot: projectRoot,
		Marker:      &SessionMarker{AgentID: agentID},
	}

	err := handleAfterTool(ctx)
	require.NoError(t, err)

	// verify raw.jsonl has entries
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)

	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
	lines := readJSONLFile(t, rawPath)

	// should have header + 2 entries (user + assistant)
	require.GreaterOrEqual(t, len(lines), 2, "should have header + entries")

	// find non-header entries
	var entries []map[string]any
	for _, line := range lines {
		if line["type"] != "header" {
			entries = append(entries, line)
		}
	}
	require.GreaterOrEqual(t, len(entries), 2, "should have user + assistant entries")
	assert.Equal(t, "user", entries[0]["type"])
	assert.Equal(t, "Fix the bug", entries[0]["content"])
	assert.Equal(t, "assistant", entries[1]["type"])
}

func TestHandleAfterTool_UpdatesOffset(t *testing.T) {
	projectRoot, agentID, sourceFile := setupHandleAfterToolTest(t)

	now := time.Now().Add(1 * time.Second)
	appendClaudeEntries(t, sourceFile, now,
		`{"type":"user","timestamp":"`+now.Format(time.RFC3339Nano)+`","message":{"role":"user","content":"Hello"}}`,
	)

	ctx := &HookContext{
		Phase:       phaseAfterTool,
		ProjectRoot: projectRoot,
		Marker:      &SessionMarker{AgentID: agentID},
	}

	require.NoError(t, handleAfterTool(ctx))

	// verify offset advanced
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	assert.Greater(t, state.SourceOffset, int64(0), "offset should advance after reading entries")
	assert.Greater(t, state.EntryCount, 0, "entry count should increase")
}

// P0 #3: all entries filtered by timestamp — offset must still advance
func TestHandleAfterTool_AllEntriesFilteredByTimestamp(t *testing.T) {
	projectRoot, agentID, sourceFile := setupHandleAfterToolTest(t)

	// write entries with timestamps BEFORE session start (they should be filtered)
	pastTime := time.Now().Add(-1 * time.Hour)
	appendClaudeEntries(t, sourceFile, pastTime,
		`{"type":"user","timestamp":"`+pastTime.Format(time.RFC3339Nano)+`","message":{"role":"user","content":"Old message"}}`,
	)

	ctx := &HookContext{
		Phase:       phaseAfterTool,
		ProjectRoot: projectRoot,
		Marker:      &SessionMarker{AgentID: agentID},
	}

	require.NoError(t, handleAfterTool(ctx))

	// offset should still advance even though all entries were filtered
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	assert.Greater(t, state.SourceOffset, int64(0), "offset must advance even when all entries are filtered")

	// but no entries should be written to raw.jsonl
	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
	lines := readJSONLFile(t, rawPath)
	nonHeaderCount := 0
	for _, line := range lines {
		if line["type"] != "header" {
			nonHeaderCount++
		}
	}
	assert.Equal(t, 0, nonHeaderCount, "no entries should be written when all are filtered by timestamp")
}

// P1 #8: timestamp boundary — entry exactly equal to StartedAt should be included
func TestHandleAfterTool_TimestampBoundary(t *testing.T) {
	projectRoot, agentID, sourceFile := setupHandleAfterToolTest(t)

	// get the exact start time
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	exactStart := state.StartedAt

	// write entry with timestamp exactly equal to StartedAt
	appendClaudeEntries(t, sourceFile, exactStart,
		`{"type":"user","timestamp":"`+exactStart.Format(time.RFC3339Nano)+`","message":{"role":"user","content":"Boundary message"}}`,
	)

	ctx := &HookContext{
		Phase:       phaseAfterTool,
		ProjectRoot: projectRoot,
		Marker:      &SessionMarker{AgentID: agentID},
	}

	require.NoError(t, handleAfterTool(ctx))

	// reload state to get session path
	state, err = session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)

	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
	lines := readJSONLFile(t, rawPath)

	var entries []map[string]any
	for _, line := range lines {
		if line["type"] != "header" {
			entries = append(entries, line)
		}
	}

	// entry at exact StartedAt should be INCLUDED (!Before means "equal or after")
	assert.GreaterOrEqual(t, len(entries), 1, "entry at exact StartedAt should be included")
}

func TestHandleAfterTool_NonIncrementalAdapterNoops(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot := t.TempDir()

	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"),
		[]byte(`{"config_version":"2","repo_id":"test-noninc"}`), 0644))

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)

	agentID := "OxNoInc"
	_, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID:     agentID,
		AdapterName: "generic", // generic adapter doesn't implement IncrementalReader
		Username:    "testuser",
	})
	require.NoError(t, err)

	ctx := &HookContext{
		Phase:       phaseAfterTool,
		ProjectRoot: projectRoot,
		Marker:      &SessionMarker{AgentID: agentID},
	}

	// should silently noop — no panic, no error
	err = handleAfterTool(ctx)
	assert.NoError(t, err)
}

func TestHandleAfterTool_EmptySessionFileNoops(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)

	// clear the session file in recording state
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
		s.SessionFile = ""
	}))

	ctx := &HookContext{
		Phase:       phaseAfterTool,
		ProjectRoot: projectRoot,
		Marker:      &SessionMarker{AgentID: agentID},
	}

	err := handleAfterTool(ctx)
	assert.NoError(t, err, "should noop when SessionFile is empty")
}

// --- P1 #6: rename misleading fsync test ---

func TestAppendEntries_DataOnDiskAfterReturn(t *testing.T) {
	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, "raw.jsonl")

	entries := []session.Entry{
		{Type: session.EntryTypeUser, Content: "durable", Timestamp: time.Now()},
	}

	require.NoError(t, appendRedactedEntries(rawPath, entries))

	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "durable")
}

// --- helpers ---

// appendClaudeEntries appends raw Claude Code JSONL lines to a source file.
func appendClaudeEntries(t *testing.T, path string, _ time.Time, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	require.NoError(t, err)
	defer f.Close()
	for _, line := range lines {
		_, err := f.WriteString(line + "\n")
		require.NoError(t, err)
	}
}

// readJSONLFile reads a JSONL file and returns parsed lines.
func readJSONLFile(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var lines []map[string]any
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m), "invalid JSON: %s", line[:min(len(line), 100)])
		lines = append(lines, m)
	}
	return lines
}
