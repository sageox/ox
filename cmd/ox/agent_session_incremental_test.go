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

// --- helpers ---

// setupIncrementalTest creates a project root with .sageox/config.json
// and sets XDG env vars for isolated test caching.
func setupIncrementalTest(t *testing.T) string {
	t.Helper()
	cacheDir := t.TempDir()
	projectRoot := t.TempDir()

	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	cfg := `{"config_version":"2","repo_id":"test-repo-incremental"}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(cfg), 0644))

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)

	return projectRoot
}

// startTestRecording starts a recording for the given agent and returns the state.
func startTestRecording(t *testing.T, projectRoot, agentID, adapterName string) *session.RecordingState {
	t.Helper()
	state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID:     agentID,
		AdapterName: adapterName,
		Username:    "testuser",
	})
	require.NoError(t, err)
	require.NotNil(t, state)
	return state
}

// readJSONLLines reads a JSONL file and returns non-empty lines parsed as maps.
func readJSONLLines(t *testing.T, path string) []map[string]any {
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
		require.NoError(t, json.Unmarshal([]byte(line), &m), "invalid JSON line: %s", line)
		lines = append(lines, m)
	}
	return lines
}

// --- writeRawHeader tests ---

func TestWriteRawHeader(t *testing.T) {
	t.Run("writes valid header line", func(t *testing.T) {
		projectRoot := setupIncrementalTest(t)
		state := startTestRecording(t, projectRoot, "OxTest1", "claude-code")

		err := writeRawHeader(projectRoot, state)
		require.NoError(t, err)

		rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
		lines := readJSONLLines(t, rawPath)
		require.Len(t, lines, 1)

		assert.Equal(t, "header", lines[0]["type"])
		meta, ok := lines[0]["metadata"].(map[string]any)
		require.True(t, ok, "metadata should be a map")
		assert.Equal(t, "1.0", meta["version"])
		assert.Equal(t, "OxTest1", meta["agent_id"])
	})

	t.Run("empty session path returns error", func(t *testing.T) {
		state := &session.RecordingState{SessionPath: ""}
		err := writeRawHeader("/tmp", state)
		assert.Error(t, err)
	})

	t.Run("creates directories if missing", func(t *testing.T) {
		projectRoot := setupIncrementalTest(t)
		state := startTestRecording(t, projectRoot, "OxTest2", "claude-code")

		// remove the session path to test directory creation
		os.RemoveAll(state.SessionPath)

		err := writeRawHeader(projectRoot, state)
		require.NoError(t, err)

		rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
		_, statErr := os.Stat(rawPath)
		assert.NoError(t, statErr, "raw.jsonl should exist after writeRawHeader")
	})
}

// --- appendRedactedEntries tests ---

func TestAppendRedactedEntries(t *testing.T) {
	t.Run("appends entries to new file", func(t *testing.T) {
		tmpDir := t.TempDir()
		rawPath := filepath.Join(tmpDir, "raw.jsonl")

		entries := []session.Entry{
			{Type: session.EntryTypeUser, Content: "hello", Timestamp: time.Now()},
			{Type: session.EntryTypeAssistant, Content: "hi there", Timestamp: time.Now()},
		}

		err := appendRedactedEntries(rawPath, entries)
		require.NoError(t, err)

		lines := readJSONLLines(t, rawPath)
		require.Len(t, lines, 2)
		assert.Equal(t, "user", lines[0]["type"])
		assert.Equal(t, "hello", lines[0]["content"])
		assert.Equal(t, "assistant", lines[1]["type"])
		assert.Equal(t, "hi there", lines[1]["content"])
	})

	t.Run("appends to existing file preserving prior content", func(t *testing.T) {
		tmpDir := t.TempDir()
		rawPath := filepath.Join(tmpDir, "raw.jsonl")

		// write header manually
		header := `{"type":"header","metadata":{"version":"1.0"}}` + "\n"
		require.NoError(t, os.WriteFile(rawPath, []byte(header), 0644))

		// append first batch
		batch1 := []session.Entry{
			{Type: session.EntryTypeUser, Content: "msg1", Timestamp: time.Now()},
		}
		require.NoError(t, appendRedactedEntries(rawPath, batch1))

		// append second batch
		batch2 := []session.Entry{
			{Type: session.EntryTypeAssistant, Content: "msg2", Timestamp: time.Now()},
			{Type: session.EntryTypeUser, Content: "msg3", Timestamp: time.Now()},
		}
		require.NoError(t, appendRedactedEntries(rawPath, batch2))

		lines := readJSONLLines(t, rawPath)
		require.Len(t, lines, 4) // header + 3 entries
		assert.Equal(t, "header", lines[0]["type"])
		assert.Equal(t, "msg1", lines[1]["content"])
		assert.Equal(t, "msg2", lines[2]["content"])
		assert.Equal(t, "msg3", lines[3]["content"])
	})

	t.Run("includes tool fields when present", func(t *testing.T) {
		tmpDir := t.TempDir()
		rawPath := filepath.Join(tmpDir, "raw.jsonl")

		entries := []session.Entry{
			{
				Type:      session.EntryTypeTool,
				Content:   "PASS",
				ToolName:  "bash",
				ToolInput: "go test ./...",
				Timestamp: time.Now(),
			},
		}

		require.NoError(t, appendRedactedEntries(rawPath, entries))

		lines := readJSONLLines(t, rawPath)
		require.Len(t, lines, 1)
		assert.Equal(t, "tool", lines[0]["type"])
		assert.Equal(t, "bash", lines[0]["tool_name"])
		assert.Equal(t, "go test ./...", lines[0]["tool_input"])
	})

	t.Run("omits tool fields when empty", func(t *testing.T) {
		tmpDir := t.TempDir()
		rawPath := filepath.Join(tmpDir, "raw.jsonl")

		entries := []session.Entry{
			{Type: session.EntryTypeUser, Content: "no tools", Timestamp: time.Now()},
		}

		require.NoError(t, appendRedactedEntries(rawPath, entries))

		lines := readJSONLLines(t, rawPath)
		require.Len(t, lines, 1)
		_, hasToolName := lines[0]["tool_name"]
		_, hasToolInput := lines[0]["tool_input"]
		assert.False(t, hasToolName, "tool_name should be absent for non-tool entries")
		assert.False(t, hasToolInput, "tool_input should be absent for non-tool entries")
	})

	t.Run("empty entries slice is a noop", func(t *testing.T) {
		tmpDir := t.TempDir()
		rawPath := filepath.Join(tmpDir, "raw.jsonl")

		err := appendRedactedEntries(rawPath, []session.Entry{})
		require.NoError(t, err)

		// file should exist but be empty (json.Encoder writes nothing for empty slice)
		data, _ := os.ReadFile(rawPath)
		assert.Empty(t, strings.TrimSpace(string(data)))
	})
}

// --- rawJSONLHasEntries tests ---

func TestRawJSONLHasEntries(t *testing.T) {
	t.Run("false for nonexistent file", func(t *testing.T) {
		assert.False(t, rawJSONLHasEntries("/nonexistent/raw.jsonl"))
	})

	t.Run("false for empty file", func(t *testing.T) {
		tmpDir := t.TempDir()
		rawPath := filepath.Join(tmpDir, "raw.jsonl")
		require.NoError(t, os.WriteFile(rawPath, []byte{}, 0644))
		assert.False(t, rawJSONLHasEntries(rawPath))
	})

	t.Run("false for header-only file", func(t *testing.T) {
		tmpDir := t.TempDir()
		rawPath := filepath.Join(tmpDir, "raw.jsonl")
		header := `{"type":"header","metadata":{"version":"1.0"}}` + "\n"
		require.NoError(t, os.WriteFile(rawPath, []byte(header), 0644))
		assert.False(t, rawJSONLHasEntries(rawPath))
	})

	t.Run("true for header plus one entry", func(t *testing.T) {
		tmpDir := t.TempDir()
		rawPath := filepath.Join(tmpDir, "raw.jsonl")
		content := `{"type":"header","metadata":{"version":"1.0"}}` + "\n" +
			`{"type":"user","content":"hello"}` + "\n"
		require.NoError(t, os.WriteFile(rawPath, []byte(content), 0644))
		assert.True(t, rawJSONLHasEntries(rawPath))
	})

	t.Run("true for multiple entries", func(t *testing.T) {
		tmpDir := t.TempDir()
		rawPath := filepath.Join(tmpDir, "raw.jsonl")
		content := `{"type":"header"}` + "\n" +
			`{"type":"user","content":"a"}` + "\n" +
			`{"type":"assistant","content":"b"}` + "\n" +
			`{"type":"user","content":"c"}` + "\n"
		require.NoError(t, os.WriteFile(rawPath, []byte(content), 0644))
		assert.True(t, rawJSONLHasEntries(rawPath))
	})
}

// --- Single Claude agent scenario ---

func TestSingleAgentIncrementalRecording(t *testing.T) {
	projectRoot := setupIncrementalTest(t)
	agentID := "OxSingle"

	// start recording
	state := startTestRecording(t, projectRoot, agentID, "claude-code")

	// write header (simulates session start)
	require.NoError(t, writeRawHeader(projectRoot, state))

	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")

	// simulate 3 PostToolUse hooks appending entries
	for i := 0; i < 3; i++ {
		entries := []session.Entry{
			{
				Type:      session.EntryTypeUser,
				Content:   "user message " + string(rune('A'+i)),
				Timestamp: time.Now(),
			},
			{
				Type:      session.EntryTypeAssistant,
				Content:   "assistant response " + string(rune('A'+i)),
				Timestamp: time.Now(),
			},
		}
		require.NoError(t, appendRedactedEntries(rawPath, entries))
	}

	// verify incremental file has header + 6 entries
	lines := readJSONLLines(t, rawPath)
	assert.Len(t, lines, 7, "should have 1 header + 6 entries")
	assert.Equal(t, "header", lines[0]["type"])

	// verify entries are interleaved user/assistant
	for i := 1; i < 7; i += 2 {
		assert.Equal(t, "user", lines[i]["type"])
		assert.Equal(t, "assistant", lines[i+1]["type"])
	}

	// verify rawJSONLHasEntries detects content
	assert.True(t, rawJSONLHasEntries(rawPath))

	// verify recording state can be loaded
	loaded, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, agentID, loaded.AgentID)
}

// --- Subagent scenario (main agent + spawned subagents) ---

func TestSubagentIncrementalRecording(t *testing.T) {
	projectRoot := setupIncrementalTest(t)

	mainAgent := "OxMain"
	subAgents := []string{"OxSub1", "OxSub2", "OxSub3"}

	// start main agent recording
	mainState := startTestRecording(t, projectRoot, mainAgent, "claude-code")
	require.NoError(t, writeRawHeader(projectRoot, mainState))

	// start subagent recordings (generic adapter, no session file)
	subStates := make([]*session.RecordingState, len(subAgents))
	for i, id := range subAgents {
		subStates[i] = startTestRecording(t, projectRoot, id, "claude")
		require.NoError(t, writeRawHeader(projectRoot, subStates[i]))
	}

	// verify all 4 recordings exist independently
	allStates, err := session.LoadAllRecordingStates(projectRoot)
	require.NoError(t, err)
	assert.Len(t, allStates, 4, "main + 3 subagents")

	// append entries to each agent's raw.jsonl
	mainRawPath := filepath.Join(mainState.SessionPath, "raw.jsonl")
	require.NoError(t, appendRedactedEntries(mainRawPath, []session.Entry{
		{Type: session.EntryTypeUser, Content: "main user msg", Timestamp: time.Now()},
		{Type: session.EntryTypeAssistant, Content: "main assistant msg", Timestamp: time.Now()},
	}))

	for i, state := range subStates {
		rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
		require.NoError(t, appendRedactedEntries(rawPath, []session.Entry{
			{Type: session.EntryTypeAssistant, Content: "sub" + subAgents[i] + " response", Timestamp: time.Now()},
		}))
	}

	// verify main agent's file has header + 2 entries
	mainLines := readJSONLLines(t, mainRawPath)
	assert.Len(t, mainLines, 3)
	assert.Equal(t, "main user msg", mainLines[1]["content"])
	assert.Equal(t, "main assistant msg", mainLines[2]["content"])

	// verify each subagent has header + 1 entry
	for i, state := range subStates {
		rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
		subLines := readJSONLLines(t, rawPath)
		assert.Len(t, subLines, 2, "subagent %s should have header + 1 entry", subAgents[i])
		assert.Contains(t, subLines[1]["content"], subAgents[i])
	}

	// verify LoadRecordingStateForAgent returns correct agent
	for _, id := range append([]string{mainAgent}, subAgents...) {
		loaded, err := session.LoadRecordingStateForAgent(projectRoot, id)
		require.NoError(t, err, "should load state for %s", id)
		require.NotNil(t, loaded, "state should not be nil for %s", id)
		assert.Equal(t, id, loaded.AgentID)
	}

	// stop main agent — subagents should remain active
	_, err = session.StopRecording(projectRoot, mainAgent)
	require.NoError(t, err)

	// main agent gone, subagents still recording
	mainLoaded, _ := session.LoadRecordingStateForAgent(projectRoot, mainAgent)
	assert.Nil(t, mainLoaded, "main agent should no longer be recording")

	for _, id := range subAgents {
		loaded, err := session.LoadRecordingStateForAgent(projectRoot, id)
		require.NoError(t, err)
		assert.NotNil(t, loaded, "subagent %s should still be recording", id)
	}
}

// --- Conductor scenario (single session) ---

func TestConductorSingleSession(t *testing.T) {
	projectRoot := setupIncrementalTest(t)
	agentID := "OxConductor1"

	state := startTestRecording(t, projectRoot, agentID, "claude-code")
	require.NoError(t, writeRawHeader(projectRoot, state))

	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")

	// simulate heavy tool usage in Conductor (many rapid appends)
	for i := 0; i < 20; i++ {
		entries := []session.Entry{
			{Type: session.EntryTypeTool, Content: "tool output " + string(rune('0'+i%10)),
				ToolName: "bash", ToolInput: "make test", Timestamp: time.Now()},
		}
		require.NoError(t, appendRedactedEntries(rawPath, entries))
	}

	lines := readJSONLLines(t, rawPath)
	assert.Len(t, lines, 21, "header + 20 tool entries")

	// verify all tool entries have tool_name field
	for _, line := range lines[1:] {
		assert.Equal(t, "tool", line["type"])
		assert.Equal(t, "bash", line["tool_name"])
	}

	// update source offset (simulates handleAfterTool updating state)
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
		s.SourceOffset = 12345
		s.EntryCount = 20
	}))

	// verify offset was persisted
	loaded, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(12345), loaded.SourceOffset)
	assert.Equal(t, 20, loaded.EntryCount)
}

// --- Conductor with multiple sessions on same worktree ---

func TestConductorMultipleSessionsSameWorktree(t *testing.T) {
	projectRoot := setupIncrementalTest(t)

	agents := []string{"OxCond1", "OxCond2", "OxCond3"}
	states := make([]*session.RecordingState, len(agents))

	// all agents start recording on the same project root
	for i, id := range agents {
		states[i] = startTestRecording(t, projectRoot, id, "claude-code")
		require.NoError(t, writeRawHeader(projectRoot, states[i]))
	}

	// verify each agent has its own session path
	sessionPaths := map[string]bool{}
	for _, state := range states {
		assert.NotEmpty(t, state.SessionPath)
		assert.False(t, sessionPaths[state.SessionPath], "duplicate session path: %s", state.SessionPath)
		sessionPaths[state.SessionPath] = true
	}

	// each agent writes entries to its own raw.jsonl
	for i, state := range states {
		rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
		for j := 0; j < 5; j++ {
			entries := []session.Entry{
				{Type: session.EntryTypeUser, Content: agents[i] + " msg " + string(rune('0'+j)), Timestamp: time.Now()},
			}
			require.NoError(t, appendRedactedEntries(rawPath, entries))
		}
	}

	// verify each agent's raw.jsonl has exactly header + 5 entries
	for i, state := range states {
		rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
		lines := readJSONLLines(t, rawPath)
		assert.Len(t, lines, 6, "agent %s should have header + 5 entries", agents[i])

		// verify no cross-contamination — all entries belong to this agent
		for _, line := range lines[1:] {
			content, ok := line["content"].(string)
			require.True(t, ok)
			assert.Contains(t, content, agents[i], "entry should belong to agent %s", agents[i])
		}
	}

	// track offsets independently
	for i, id := range agents {
		require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, id, func(s *session.RecordingState) {
			s.SourceOffset = int64((i + 1) * 1000)
			s.EntryCount = 5
		}))
	}

	// verify each agent's offset is independent
	for i, id := range agents {
		loaded, err := session.LoadRecordingStateForAgent(projectRoot, id)
		require.NoError(t, err)
		assert.Equal(t, int64((i+1)*1000), loaded.SourceOffset, "offset mismatch for %s", id)
	}
}

// --- Per-agent explicit stop isolation ---

func TestConductorExplicitStopIsolation(t *testing.T) {
	projectRoot := setupIncrementalTest(t)

	agentA := "OxCondA"
	agentB := "OxCondB"

	// both agents start recording
	startTestRecording(t, projectRoot, agentA, "claude-code")
	startTestRecording(t, projectRoot, agentB, "claude-code")

	// agent A stops explicitly
	_, err := session.StopRecording(projectRoot, agentA)
	require.NoError(t, err)
	require.NoError(t, session.MarkExplicitStop(projectRoot, agentA))

	// agent A's marker exists — consuming it returns true
	assert.True(t, session.ConsumeExplicitStop(projectRoot, agentA))

	// agent B should NOT be affected by agent A's stop marker
	assert.False(t, session.ConsumeExplicitStop(projectRoot, agentB),
		"agent B should not consume agent A's stop marker")

	// agent B is still recording
	stateB, err := session.LoadRecordingStateForAgent(projectRoot, agentB)
	require.NoError(t, err)
	assert.NotNil(t, stateB, "agent B should still be recording")
	assert.Equal(t, agentB, stateB.AgentID)

	// agent B stops explicitly
	_, err = session.StopRecording(projectRoot, agentB)
	require.NoError(t, err)
	require.NoError(t, session.MarkExplicitStop(projectRoot, agentB))

	// agent B's marker is independent
	assert.True(t, session.ConsumeExplicitStop(projectRoot, agentB))
	assert.False(t, session.ConsumeExplicitStop(projectRoot, agentB), "second consume should return false")
}

// --- Atomic state writes ---

func TestAtomicRecordingStateWrite(t *testing.T) {
	projectRoot := setupIncrementalTest(t)
	agentID := "OxAtomic"

	state := startTestRecording(t, projectRoot, agentID, "claude-code")

	// update multiple times rapidly (simulates rapid PostToolUse hooks)
	for i := 0; i < 10; i++ {
		require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
			s.SourceOffset = int64(i * 100)
			s.EntryCount = i
		}))
	}

	// final state should be consistent
	loaded, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(900), loaded.SourceOffset)
	assert.Equal(t, 9, loaded.EntryCount)

	// verify the recording file is valid JSON (not corrupted by partial writes)
	statePath := filepath.Join(state.SessionPath, ".recording.json")
	data, err := os.ReadFile(statePath)
	require.NoError(t, err)

	var parsed session.RecordingState
	require.NoError(t, json.Unmarshal(data, &parsed), "recording state should be valid JSON")
	assert.Equal(t, agentID, parsed.AgentID)
}

// --- Source offset tracking ---

func TestSourceOffsetTracking(t *testing.T) {
	projectRoot := setupIncrementalTest(t)
	agentID := "OxOffset"

	state := startTestRecording(t, projectRoot, agentID, "claude-code")

	// initial offset should be 0
	assert.Equal(t, int64(0), state.SourceOffset)

	// simulate incremental reads updating the offset
	offsets := []int64{512, 1024, 2048, 4096, 8192}
	for _, offset := range offsets {
		require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
			s.SourceOffset = offset
		}))

		loaded, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
		require.NoError(t, err)
		assert.Equal(t, offset, loaded.SourceOffset)
	}
}

// --- Crash recovery: partial raw.jsonl is still readable ---

func TestCrashRecoveryPartialRawJSONL(t *testing.T) {
	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, "raw.jsonl")

	// write header
	header := `{"type":"header","metadata":{"version":"1.0","agent_id":"OxCrash"}}` + "\n"
	require.NoError(t, os.WriteFile(rawPath, []byte(header), 0644))

	// write 3 complete entries
	entries := []session.Entry{
		{Type: session.EntryTypeUser, Content: "msg1", Timestamp: time.Now()},
		{Type: session.EntryTypeAssistant, Content: "msg2", Timestamp: time.Now()},
		{Type: session.EntryTypeUser, Content: "msg3", Timestamp: time.Now()},
	}
	require.NoError(t, appendRedactedEntries(rawPath, entries))

	// simulate crash: append a partial/truncated JSON line (as if process died mid-write)
	f, err := os.OpenFile(rawPath, os.O_WRONLY|os.O_APPEND, 0644)
	require.NoError(t, err)
	_, err = f.WriteString(`{"type":"assistant","content":"trunca`)
	require.NoError(t, err)
	f.Close()

	// ReadSessionFromPath should still parse the valid entries
	sess, err := session.ReadSessionFromPath(rawPath)
	require.NoError(t, err)

	// should have at least the 3 complete entries (skipping truncated line)
	assert.GreaterOrEqual(t, len(sess.Entries), 3, "should recover 3+ entries from partial file")

	// verify metadata parsed from header
	require.NotNil(t, sess.Meta)
	assert.Equal(t, "OxCrash", sess.Meta.AgentID)
}

// --- No entries written (short session, fallback to batch) ---

func TestNoHooksFiredFallbackDetection(t *testing.T) {
	projectRoot := setupIncrementalTest(t)
	state := startTestRecording(t, projectRoot, "OxNoHooks", "claude-code")

	// write header only (no PostToolUse hooks fired)
	require.NoError(t, writeRawHeader(projectRoot, state))

	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")

	// rawJSONLHasEntries should return false (only header, no entries)
	assert.False(t, rawJSONLHasEntries(rawPath),
		"header-only file should not count as having entries")

	// this signals processAgentSession to fall back to batch reading
}

// --- Concurrent agents with interleaved stop/start ---

func TestConductorInterleavedStopStart(t *testing.T) {
	projectRoot := setupIncrementalTest(t)

	// agent A starts
	stateA := startTestRecording(t, projectRoot, "OxIntA", "claude-code")
	require.NoError(t, writeRawHeader(projectRoot, stateA))

	// agent B starts
	stateB := startTestRecording(t, projectRoot, "OxIntB", "claude-code")
	require.NoError(t, writeRawHeader(projectRoot, stateB))

	// both agents write entries
	rawPathA := filepath.Join(stateA.SessionPath, "raw.jsonl")
	rawPathB := filepath.Join(stateB.SessionPath, "raw.jsonl")

	require.NoError(t, appendRedactedEntries(rawPathA, []session.Entry{
		{Type: session.EntryTypeUser, Content: "A-before-stop", Timestamp: time.Now()},
	}))
	require.NoError(t, appendRedactedEntries(rawPathB, []session.Entry{
		{Type: session.EntryTypeUser, Content: "B-before-stop", Timestamp: time.Now()},
	}))

	// agent A stops
	_, err := session.StopRecording(projectRoot, "OxIntA")
	require.NoError(t, err)
	require.NoError(t, session.MarkExplicitStop(projectRoot, "OxIntA"))

	// agent B continues writing after A stopped
	require.NoError(t, appendRedactedEntries(rawPathB, []session.Entry{
		{Type: session.EntryTypeAssistant, Content: "B-after-A-stop", Timestamp: time.Now()},
	}))

	// verify A's file is unchanged (header + 1 entry)
	linesA := readJSONLLines(t, rawPathA)
	assert.Len(t, linesA, 2)

	// verify B's file has header + 2 entries
	linesB := readJSONLLines(t, rawPathB)
	assert.Len(t, linesB, 3)
	assert.Equal(t, "B-before-stop", linesB[1]["content"])
	assert.Equal(t, "B-after-A-stop", linesB[2]["content"])

	// agent C starts (new agent on same worktree after A left)
	stateC := startTestRecording(t, projectRoot, "OxIntC", "claude-code")
	require.NoError(t, writeRawHeader(projectRoot, stateC))

	rawPathC := filepath.Join(stateC.SessionPath, "raw.jsonl")
	require.NoError(t, appendRedactedEntries(rawPathC, []session.Entry{
		{Type: session.EntryTypeUser, Content: "C-fresh-start", Timestamp: time.Now()},
	}))

	// verify all three files are independent
	linesC := readJSONLLines(t, rawPathC)
	assert.Len(t, linesC, 2)
	assert.Equal(t, "C-fresh-start", linesC[1]["content"])

	// B and C are still recording, A is not
	loadedA, _ := session.LoadRecordingStateForAgent(projectRoot, "OxIntA")
	assert.Nil(t, loadedA)

	loadedB, err := session.LoadRecordingStateForAgent(projectRoot, "OxIntB")
	require.NoError(t, err)
	assert.NotNil(t, loadedB)

	loadedC, err := session.LoadRecordingStateForAgent(projectRoot, "OxIntC")
	require.NoError(t, err)
	assert.NotNil(t, loadedC)
}

// --- dispatch phase routing ---

func TestDispatchPhase_AfterToolAndStop(t *testing.T) {
	// verify phaseAfterTool and phaseStop are now active
	assert.True(t, activePhaseBehavior[phaseAfterTool], "phaseAfterTool should be active")
	assert.True(t, activePhaseBehavior[phaseStop], "phaseStop should be active")
}

func TestHandleAfterTool_NoAgent(t *testing.T) {
	ctx := &HookContext{
		Phase:       phaseAfterTool,
		ProjectRoot: t.TempDir(),
		Marker:      nil, // no marker = no agent ID
	}

	err := handleAfterTool(ctx)
	assert.NoError(t, err, "should silently noop without agent ID")
}

func TestHandleAfterTool_NotRecording(t *testing.T) {
	projectRoot := setupIncrementalTest(t)

	ctx := &HookContext{
		Phase:       phaseAfterTool,
		ProjectRoot: projectRoot,
		Marker:      &SessionMarker{AgentID: "OxGhost"},
	}

	// no recording started for this agent
	err := handleAfterTool(ctx)
	assert.NoError(t, err, "should silently noop when not recording")
}

// --- Large content handling ---

func TestAppendLargeEntries(t *testing.T) {
	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, "raw.jsonl")

	// simulate a large tool output (1MB)
	largeContent := strings.Repeat("x", 1024*1024)
	entries := []session.Entry{
		{Type: session.EntryTypeTool, Content: largeContent, ToolName: "bash",
			ToolInput: "cat bigfile.txt", Timestamp: time.Now()},
	}

	require.NoError(t, appendRedactedEntries(rawPath, entries))

	lines := readJSONLLines(t, rawPath)
	require.Len(t, lines, 1)
	content, ok := lines[0]["content"].(string)
	require.True(t, ok)
	assert.Len(t, content, 1024*1024)
}

// --- Empty session stop path ---

func TestRawJSONLHasEntries_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, "raw.jsonl")
	require.NoError(t, os.WriteFile(rawPath, []byte{}, 0644))
	assert.False(t, rawJSONLHasEntries(rawPath))
}

func TestRawJSONLHasEntries_HeaderOnly(t *testing.T) {
	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, "raw.jsonl")
	header := `{"_meta":{"schema_version":"1","agent_type":"claude-code"}}` + "\n"
	require.NoError(t, os.WriteFile(rawPath, []byte(header), 0644))
	assert.False(t, rawJSONLHasEntries(rawPath))
}

func TestRawJSONLHasEntries_WithContent(t *testing.T) {
	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, "raw.jsonl")
	content := `{"_meta":{"schema_version":"1","agent_type":"claude-code"}}` + "\n" +
		`{"type":"user","content":"hello world","timestamp":"2026-01-01T00:00:00Z"}` + "\n"
	require.NoError(t, os.WriteFile(rawPath, []byte(content), 0644))
	assert.True(t, rawJSONLHasEntries(rawPath))
}

func TestRawJSONLHasEntries_MissingFile(t *testing.T) {
	assert.False(t, rawJSONLHasEntries(filepath.Join(t.TempDir(), "does-not-exist.jsonl")))
}

func TestFinalizeIncrementalSession_EmptySession(t *testing.T) {
	projectRoot := setupIncrementalTest(t)
	state := startTestRecording(t, projectRoot, "OxEmpty", "claude-code")

	// write header only (no conversation entries) to raw.jsonl
	require.NoError(t, writeRawHeader(projectRoot, state))

	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")

	// confirm raw.jsonl has no entries beyond the header
	assert.False(t, rawJSONLHasEntries(rawPath))

	adapter, err := adapters.GetAdapter("claude-code")
	require.NoError(t, err)

	result := &agentSessionResult{}
	got, err := finalizeIncrementalSession(projectRoot, state, rawPath, adapter, result)
	require.NoError(t, err)
	require.NotNil(t, got)

	// finalize should return cleanly with zero entries and no artifact paths
	assert.Equal(t, 0, got.EntryCount)
	assert.Equal(t, rawPath, got.RawPath, "RawPath should still be set even for empty sessions")
	assert.Empty(t, got.HTMLPath, "no HTML should be generated for empty session")
	assert.Empty(t, got.SummaryMDPath, "no summary should be generated for empty session")
	assert.Empty(t, got.SessionMDPath, "no session markdown should be generated for empty session")
}
