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
	"github.com/sageox/ox/internal/session/adapters"
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
	assert.True(t, activePhaseBehavior[phasePrompt])

	// noop phases
	assert.False(t, activePhaseBehavior[phaseEnd])
	assert.False(t, activePhaseBehavior[phaseBeforeTool])
}

func TestDispatchPhase_NoopPhases(t *testing.T) {
	ctx := &HookContext{
		AgentType:   "claude-code",
		ProjectRoot: t.TempDir(),
	}

	// noop phases should return nil
	for _, phase := range []string{phaseEnd, phaseBeforeTool} {
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

	adapters.Register(&testClaudeCodeAdapter{})
	t.Cleanup(func() { adapters.Unregister("claude-code") })

	cacheDir := t.TempDir()
	projectRoot = t.TempDir()

	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	cfg := `{"config_version":"2","repo_id":"test-repo-hook","endpoint":"http://test.sageox.local","session_publishing":"manual"}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(cfg), 0644))

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	t.Setenv("XDG_DATA_HOME", cacheDir)

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

// timestamp boundary — entry exactly equal to StartedAt should be EXCLUDED
// (strict After filter prevents pre-session content from leaking at the boundary)
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

	// entry at exact StartedAt should be EXCLUDED (strict After prevents boundary leaks)
	assert.Equal(t, 0, len(entries), "entry at exact StartedAt should be excluded to prevent pre-session content leak")
}

// --- Sub-bug 2a: pre-session content leak ---

// TestHandleAfterTool_PreStartContentLeak_ByOffset verifies that entries
// existing in the source file BEFORE recording started are excluded using
// StartOffset (byte offset at recording start). This is the primary fix
// for pre-session content leaking into raw.jsonl.
//
// Scenario: Claude Code reuses a JSONL file across sessions. The file already
// has entries from a prior conversation when ox starts recording. Without
// StartOffset, those pre-existing entries leak into the new session's raw.jsonl.
func TestHandleAfterTool_PreStartContentLeak_ByOffset(t *testing.T) {
	adapters.Register(&testClaudeCodeAdapter{})
	t.Cleanup(func() { adapters.Unregister("claude-code") })

	cacheDir := t.TempDir()
	projectRoot := t.TempDir()

	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	cfg := `{"config_version":"2","repo_id":"test-repo-leak","endpoint":"http://test.sageox.local","session_publishing":"manual"}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(cfg), 0644))

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	t.Setenv("XDG_DATA_HOME", cacheDir)

	// create source JSONL with pre-existing content (from a prior session)
	// use a timestamp AFTER what will be StartedAt to ensure offset-based
	// filtering is what catches this, not timestamp-based filtering
	sourceDir := t.TempDir()
	sourceFile := filepath.Join(sourceDir, "session.jsonl")

	// these entries have future timestamps but exist in the file BEFORE recording starts
	futureTs := time.Now().Add(1 * time.Hour)
	preContent := `{"type":"user","timestamp":"` + futureTs.Format(time.RFC3339Nano) + `","message":{"role":"user","content":"Old session message"}}` + "\n"
	preContent += `{"type":"assistant","timestamp":"` + futureTs.Add(time.Second).Format(time.RFC3339Nano) + `","message":{"role":"assistant","content":[{"type":"text","text":"Old response"}]}}` + "\n"
	require.NoError(t, os.WriteFile(sourceFile, []byte(preContent), 0644))

	// record file size BEFORE recording starts — this is StartOffset
	preInfo, err := os.Stat(sourceFile)
	require.NoError(t, err)
	startOffset := preInfo.Size()

	agentID := "OxLeak1"
	state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID:     agentID,
		AdapterName: "claude-code",
		Username:    "testuser",
	})
	require.NoError(t, err)

	// set SessionFile and StartOffset in recording state
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
		s.SessionFile = sourceFile
		s.StartOffset = startOffset
	}))

	require.NoError(t, writeRawHeader(projectRoot, state))

	// now append new session content AFTER start
	newTime := time.Now().Add(1 * time.Second)
	appendClaudeEntries(t, sourceFile, newTime,
		`{"type":"user","timestamp":"`+newTime.Format(time.RFC3339Nano)+`","message":{"role":"user","content":"New session message"}}`,
	)

	ctx := &HookContext{
		Phase:       phaseAfterTool,
		ProjectRoot: projectRoot,
		Marker:      &SessionMarker{AgentID: agentID},
	}
	require.NoError(t, handleAfterTool(ctx))

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

	// pre-existing entries must NOT appear
	for _, entry := range entries {
		content, _ := entry["content"].(string)
		assert.NotEqual(t, "Old session message", content,
			"pre-session content must not leak into raw.jsonl")
		assert.NotEqual(t, "Old response", content,
			"pre-session content must not leak into raw.jsonl")
	}

	// new session entry must be present
	require.Len(t, entries, 1, "only the new session entry should be captured")
	assert.Equal(t, "New session message", entries[0]["content"])
}

// TestHandleAfterTool_LegacyTimestampFilter verifies that for legacy recording
// states (StartOffset=0), the timestamp filter uses strict After() to exclude
// entries at exactly the start time.
func TestHandleAfterTool_LegacyTimestampFilter(t *testing.T) {
	projectRoot, agentID, sourceFile := setupHandleAfterToolTest(t)

	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)

	// verify this is a legacy state (StartOffset=0)
	assert.Equal(t, int64(0), state.StartOffset,
		"test setup: recording should have zero StartOffset (legacy)")

	// write entry with timestamp exactly at StartedAt
	exactStart := state.StartedAt
	appendClaudeEntries(t, sourceFile, exactStart,
		`{"type":"user","timestamp":"`+exactStart.Format(time.RFC3339Nano)+`","message":{"role":"user","content":"Exact boundary message"}}`,
	)

	ctx := &HookContext{
		Phase:       phaseAfterTool,
		ProjectRoot: projectRoot,
		Marker:      &SessionMarker{AgentID: agentID},
	}
	require.NoError(t, handleAfterTool(ctx))

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

	// with legacy states (StartOffset=0), exact-boundary entries should be EXCLUDED
	// to prevent pre-session content from leaking in
	for _, entry := range entries {
		content, _ := entry["content"].(string)
		assert.NotEqual(t, "Exact boundary message", content,
			"exact-boundary entry must be excluded for legacy states (StartOffset=0)")
	}
}

// --- Sub-bug 2b: truncation at ExitPlanMode ---

// TestFinalizeIncrementalSession_CapturesAllEntries verifies that the final
// drain in finalizeIncrementalSession captures entries appended after the last
// incremental hook drain (simulating post-plan implementation content).
func TestFinalizeIncrementalSession_CapturesAllEntries(t *testing.T) {
	projectRoot, agentID, sourceFile := setupHandleAfterToolTest(t)

	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)

	// phase 1: incremental drain via handleAfterTool
	after1 := state.StartedAt.Add(1 * time.Second)
	appendClaudeEntries(t, sourceFile, after1,
		`{"type":"user","timestamp":"`+after1.Format(time.RFC3339Nano)+`","message":{"role":"user","content":"Plan mode question"}}`,
		`{"type":"assistant","timestamp":"`+after1.Add(time.Second).Format(time.RFC3339Nano)+`","message":{"role":"assistant","content":[{"type":"text","text":"Here is the plan."}]}}`,
	)

	ctx := &HookContext{
		Phase:       phaseAfterTool,
		ProjectRoot: projectRoot,
		Marker:      &SessionMarker{AgentID: agentID},
	}
	require.NoError(t, handleAfterTool(ctx))

	// phase 2: more entries appended AFTER the last drain (post-plan implementation)
	after2 := state.StartedAt.Add(5 * time.Second)
	appendClaudeEntries(t, sourceFile, after2,
		`{"type":"user","timestamp":"`+after2.Format(time.RFC3339Nano)+`","message":{"role":"user","content":"Now implement it"}}`,
		`{"type":"assistant","timestamp":"`+after2.Add(time.Second).Format(time.RFC3339Nano)+`","message":{"role":"assistant","content":[{"type":"text","text":"Implementing now."}]}}`,
	)

	// reload state (offset was updated by first drain)
	state, err = session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)

	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
	adapter, adapterErr := adapters.GetAdapter(state.AdapterName)
	require.NoError(t, adapterErr)

	result := &agentSessionResult{}
	_, err = finalizeIncrementalSession(projectRoot, state, rawPath, adapter, result)
	require.NoError(t, err)

	// read final raw.jsonl and verify ALL entries present
	lines := readJSONLFile(t, rawPath)

	var entries []map[string]any
	for _, line := range lines {
		if line["type"] != "header" {
			entries = append(entries, line)
		}
	}

	// should have 4 entries: plan question + plan answer + implement question + implement answer
	require.GreaterOrEqual(t, len(entries), 4,
		"final drain must capture post-plan entries; got %d entries", len(entries))

	// verify the post-plan entries are present
	contents := make([]string, 0, len(entries))
	for _, e := range entries {
		if c, ok := e["content"].(string); ok {
			contents = append(contents, c)
		}
	}
	assert.Contains(t, contents, "Now implement it",
		"post-plan user message must be captured")
	assert.Contains(t, contents, "Implementing now.",
		"post-plan assistant response must be captured")
}

func TestHandleAfterTool_NonIncrementalAdapterNoops(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot := t.TempDir()

	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"),
		[]byte(`{"config_version":"2","repo_id":"test-noninc","endpoint":"http://test.sageox.local","session_publishing":"manual"}`), 0644))

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	t.Setenv("XDG_DATA_HOME", cacheDir)

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

// --- Session recording lifecycle regression tests ---

// TestAfterTool_FindsRecordingFromSessionStart verifies that handleAfterTool
// can find a recording created during the SessionStart flow (via StartRecording).
// This catches path resolution mismatches between StartRecording (which creates the
// session in XDG cache) and LoadRecordingStateForAgent (which searches for it).
func TestAfterTool_FindsRecordingFromSessionStart(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot := t.TempDir()

	// initialize project
	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	cfg := `{"config_version":"2","repo_id":"test-repo-pathcheck","endpoint":"http://test.sageox.local","session_publishing":"manual"}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(cfg), 0644))

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	t.Setenv("XDG_DATA_HOME", cacheDir)

	agentID := "OxPath1"

	// simulate SessionStart: create recording via StartRecording (same as prime does)
	state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID:     agentID,
		AdapterName: "claude-code",
		Username:    "testuser",
		ParentPID:   os.Getpid(),
	})
	require.NoError(t, err)
	require.NotEmpty(t, state.SessionPath)

	// simulate AfterTool: load recording state using the SAME projectRoot
	// this is the exact path handleAfterTool takes (line 216 in agent_hook.go)
	loaded, loadErr := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, loadErr)
	require.NotNil(t, loaded, "AfterTool must find recording created by SessionStart")
	assert.Equal(t, agentID, loaded.AgentID)
	assert.Equal(t, state.SessionPath, loaded.SessionPath)
}

// TestGhostCleanup_DoesNotEatFreshRecordingWithAlivePID is a regression test for the
// pre-fix scenario: if ParentPID is the test process (alive), ghost cleanup must not
// remove the session even if it has no raw.jsonl data yet.
func TestGhostCleanup_DoesNotEatFreshRecordingWithAlivePID(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot := t.TempDir()

	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	cfg := `{"config_version":"2","repo_id":"test-repo-ghost","endpoint":"http://test.sageox.local","session_publishing":"manual"}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(cfg), 0644))

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	t.Setenv("XDG_DATA_HOME", cacheDir)

	// create a recording with alive PID but NO raw.jsonl data (fresh session)
	state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID:     "OxFresh",
		AdapterName: "claude-code",
		Username:    "testuser",
		ParentPID:   os.Getpid(), // alive
	})
	require.NoError(t, err)

	// run ghost cleanup — should NOT remove this session
	result := session.CleanupGhostSessions(projectRoot)
	assert.Equal(t, 0, result.Removed, "ghost cleanup must not remove session with alive PID")

	// verify recording is still findable
	loaded, loadErr := session.LoadRecordingStateForAgent(projectRoot, "OxFresh")
	require.NoError(t, loadErr)
	require.NotNil(t, loaded, "recording should survive ghost cleanup")
	assert.Equal(t, state.SessionPath, loaded.SessionPath)
}

// TestGhostCleanup_ProtectsFreshRecordingWithDeadPID verifies that the ghost
// grace period prevents fresh recordings from being removed even when their PID
// is dead. This is the fix for sessions disappearing when FindAgentAncestorPID()
// returns a transient shell PID.
// Failure prevented: recording removed within seconds of creation due to dead transient PID.
func TestGhostCleanup_ProtectsFreshRecordingWithDeadPID(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot := t.TempDir()

	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	cfg := `{"config_version":"2","repo_id":"test-repo-deadpid","endpoint":"http://test.sageox.local","session_publishing":"manual"}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(cfg), 0644))

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	t.Setenv("XDG_DATA_HOME", cacheDir)

	// create a recording with a dead PID (simulates transient shell PID)
	_, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID:     "OxDead",
		AdapterName: "claude-code",
		Username:    "testuser",
		ParentPID:   99999999, // dead
	})
	require.NoError(t, err)

	// ghost cleanup should NOT remove it — recording is within grace period
	result := session.CleanupGhostSessions(projectRoot)
	assert.Equal(t, 0, result.Removed, "fresh recording with dead PID should be protected by grace period")

	// recording should still exist
	loaded, loadErr := session.LoadRecordingStateForAgent(projectRoot, "OxDead")
	require.NoError(t, loadErr)
	assert.NotNil(t, loaded, "fresh recording should survive ghost cleanup")
}

// TestStartRecording_IdempotentWhenAlreadyRecording verifies that calling
// StartRecording twice for the same agent returns ErrAlreadyRecording.
// This is the behavior the safety-net call in handleStart depends on.
func TestStartRecording_IdempotentWhenAlreadyRecording(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot := t.TempDir()

	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	cfg := `{"config_version":"2","repo_id":"test-repo-idemp","endpoint":"http://test.sageox.local","session_publishing":"manual"}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(cfg), 0644))

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	t.Setenv("XDG_DATA_HOME", cacheDir)

	agentID := "OxIdem"
	opts := session.StartRecordingOptions{
		AgentID:     agentID,
		AdapterName: "claude-code",
		Username:    "testuser",
	}

	// first call succeeds
	_, err := session.StartRecording(projectRoot, opts)
	require.NoError(t, err)

	// second call returns ErrAlreadyRecording
	_, err = session.StartRecording(projectRoot, opts)
	require.Error(t, err)
	assert.ErrorIs(t, err, session.ErrAlreadyRecording)

	// a different agent can still start recording
	opts2 := opts
	opts2.AgentID = "OxOthr"
	_, err = session.StartRecording(projectRoot, opts2)
	require.NoError(t, err, "different agent should be able to start recording concurrently")
}

// TestAfterTool_NilMarkerSkipsGracefully verifies that handleAfterTool
// returns nil (not an error) when ctx.Marker is nil. This happens when
// the hook fires before prime has run (e.g., tool use before session start).
func TestAfterTool_NilMarkerSkipsGracefully(t *testing.T) {
	projectRoot := t.TempDir()

	ctx := &HookContext{
		Phase:       phaseAfterTool,
		ProjectRoot: projectRoot,
		Marker:      nil, // no marker — prime hasn't run yet
	}

	err := handleAfterTool(ctx)
	assert.NoError(t, err, "handleAfterTool should noop gracefully with nil marker")
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
