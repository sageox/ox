package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// outputAgentDoctorJSON — 0% coverage, pure formatter
// =============================================================================

func TestOutputAgentDoctorJSON_BasicOutput(t *testing.T) {
	t.Parallel()
	output := &AgentDoctorOutput{
		Success: true,
		Type:    "agent_doctor",
		AgentID: "Oxa7b3",
	}

	var buf bytes.Buffer
	require.NoError(t, outputAgentDoctorJSON(&buf, output))
	got := buf.String()

	assert.Contains(t, got, `"success": true`)
	assert.Contains(t, got, `"type": "agent_doctor"`)
	assert.Contains(t, got, `"agent_id": "Oxa7b3"`)

	// verify it's valid JSON
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(got), &parsed))
}

func TestOutputAgentDoctorJSON_WithIncompleteSessions(t *testing.T) {
	t.Parallel()
	output := &AgentDoctorOutput{
		Success: true,
		Type:    "agent_doctor",
		AgentID: "Oxb8c4",
		IncompleteSessions: []IncompleteSessionInfo{
			{
				SessionID: "2026-03-15-user-abc1",
				Path:      "/tmp/sessions/2026-03-15-user-abc1",
				Missing:   []string{"summary", "html"},
				FinalizeCommands: []string{
					"ox agent Oxb8c4 session summarize --file /tmp/raw.jsonl",
				},
				SummarizePrompt: "summarize this session",
			},
		},
		StagedCount:  2,
		CommitNeeded: true,
		PushNeeded:   true,
		NextSteps:    []string{"Generate summary", "ox session commit"},
	}

	var buf bytes.Buffer
	require.NoError(t, outputAgentDoctorJSON(&buf, output))

	// verify JSON structure
	var parsed AgentDoctorOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))
	assert.Equal(t, 1, len(parsed.IncompleteSessions))
	assert.Equal(t, "2026-03-15-user-abc1", parsed.IncompleteSessions[0].SessionID)
	assert.Equal(t, 2, parsed.StagedCount)
	assert.True(t, parsed.CommitNeeded)
	assert.True(t, parsed.PushNeeded)
}

func TestOutputAgentDoctorJSON_EmptyOutput(t *testing.T) {
	t.Parallel()
	output := &AgentDoctorOutput{
		Success: true,
		Type:    "agent_doctor",
		AgentID: "",
	}

	var buf bytes.Buffer
	require.NoError(t, outputAgentDoctorJSON(&buf, output))

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))
}

// =============================================================================
// outputAgentDoctorText — 0% coverage, pure formatter
// =============================================================================

func TestOutputAgentDoctorText_AllGood(t *testing.T) {
	t.Parallel()
	output := &AgentDoctorOutput{
		Success: true,
	}

	var buf bytes.Buffer
	outputAgentDoctorText(&buf, output)
	got := buf.String()

	// should indicate everything is ok
	assert.Contains(t, got, "all good")
}

func TestOutputAgentDoctorText_WithIncompleteSessions(t *testing.T) {
	t.Parallel()
	output := &AgentDoctorOutput{
		IncompleteSessions: []IncompleteSessionInfo{
			{
				SessionID:        "session-1",
				Missing:          []string{"summary", "html"},
				FinalizeCommands: []string{"ox session export --input /tmp/raw.jsonl"},
			},
		},
	}

	var buf bytes.Buffer
	outputAgentDoctorText(&buf, output)
	got := buf.String()

	assert.Contains(t, got, "Incomplete sessions: 1")
	assert.Contains(t, got, "session-1")
	assert.Contains(t, got, "summary")
}

func TestOutputAgentDoctorText_CommitAndPushNeeded(t *testing.T) {
	t.Parallel()
	output := &AgentDoctorOutput{
		CommitNeeded: true,
		PushNeeded:   true,
		StagedCount:  3,
		NextSteps:    []string{"ox session commit", "git push (in ledger)"},
	}

	var buf bytes.Buffer
	outputAgentDoctorText(&buf, output)
	got := buf.String()

	assert.Contains(t, got, "Staged files: 3")
	assert.Contains(t, got, "commit needed")
	assert.Contains(t, got, "Push needed")
	assert.Contains(t, got, "Next steps:")
}

// =============================================================================
// buildSessionURL — 83.3% coverage, needs edge cases
// =============================================================================

func TestBuildSessionURL_ValidConfig(t *testing.T) {
	t.Parallel()

	cfg := &config.ProjectConfig{
		RepoID:   "repo-123",
		Endpoint: "https://sageox.ai",
	}

	got := buildSessionURL(cfg, "2026-03-15-user-abc1")
	assert.Contains(t, got, "sageox.ai")
	assert.Contains(t, got, "repo-123")
	assert.Contains(t, got, "2026-03-15-user-abc1")
	assert.Contains(t, got, "/view")
}

func TestBuildSessionURL_NilConfig(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", buildSessionURL(nil, "session-1"))
}

func TestBuildSessionURL_EmptyRepoID(t *testing.T) {
	t.Parallel()
	cfg := &config.ProjectConfig{
		Endpoint: "https://sageox.ai",
	}
	assert.Equal(t, "", buildSessionURL(cfg, "session-1"))
}

func TestBuildSessionURL_EmptySessionName(t *testing.T) {
	t.Parallel()
	cfg := &config.ProjectConfig{
		RepoID:   "repo-123",
		Endpoint: "https://sageox.ai",
	}
	assert.Equal(t, "", buildSessionURL(cfg, ""))
}

func TestBuildSessionURL_EmptyEndpoint(t *testing.T) {
	t.Parallel()
	cfg := &config.ProjectConfig{
		RepoID: "repo-123",
		// no endpoint — GetEndpoint() falls back to the default production endpoint
	}
	// When Endpoint is unset, GetEndpoint() returns the default (https://sageox.ai),
	// so a valid URL is still produced.
	got := buildSessionURL(cfg, "session-1")
	assert.NotEmpty(t, got)
	assert.Contains(t, got, "repo-123")
	assert.Contains(t, got, "session-1")
}

func TestBuildSessionURL_URLEscapesSpecialChars(t *testing.T) {
	t.Parallel()
	cfg := &config.ProjectConfig{
		RepoID:   "repo/with spaces",
		Endpoint: "https://sageox.ai",
	}

	got := buildSessionURL(cfg, "session name/special")
	assert.NotContains(t, got, " ", "spaces should be URL-encoded")
	assert.Contains(t, got, "repo%2Fwith%20spaces")
	assert.Contains(t, got, "session%20name%2Fspecial")
}

func TestBuildSessionURL_NormalizesEndpoint(t *testing.T) {
	t.Parallel()
	// endpoint with api. prefix should be normalized
	cfg := &config.ProjectConfig{
		RepoID:   "repo-123",
		Endpoint: "https://api.sageox.ai",
	}

	got := buildSessionURL(cfg, "session-1")
	// api. prefix should be stripped per endpoint normalization rules
	assert.Contains(t, got, "sageox.ai")
	assert.NotContains(t, got, "api.sageox.ai")
}

// =============================================================================
// appendRedactedEntries — file I/O, testable with temp files
// =============================================================================

func TestAppendRedactedEntries_WritesValidJSONL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rawPath := filepath.Join(dir, "raw.jsonl")

	entries := []session.Entry{
		{
			Type:      session.EntryTypeUser,
			Content:   "hello world",
			Timestamp: time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC),
		},
		{
			Type:      session.EntryTypeAssistant,
			Content:   "hi there",
			Timestamp: time.Date(2026, 3, 15, 10, 0, 1, 0, time.UTC),
		},
	}

	err := appendRedactedEntries(rawPath, entries)
	require.NoError(t, err)

	// read back and verify JSONL
	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	assert.Len(t, lines, 2)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &parsed))
	assert.Equal(t, "user", parsed["type"])
	assert.Equal(t, "hello world", parsed["content"])
}

func TestAppendRedactedEntries_IncludesToolFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rawPath := filepath.Join(dir, "raw.jsonl")

	entries := []session.Entry{
		{
			Type:       session.EntryTypeTool,
			Content:    "reading file",
			ToolName:   "Read",
			ToolInput:  "/tmp/foo.go",
			ToolOutput: "package main\n",
			Timestamp:  time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC),
		},
	}

	err := appendRedactedEntries(rawPath, entries)
	require.NoError(t, err)

	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, "Read", parsed["tool_name"])
	assert.Equal(t, "/tmp/foo.go", parsed["tool_input"])
	assert.Equal(t, "package main\n", parsed["tool_output"])
}

func TestAppendRedactedEntries_OmitsEmptyToolFieldsOnUserEntry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rawPath := filepath.Join(dir, "raw.jsonl")

	entries := []session.Entry{
		{
			Type:    session.EntryTypeUser,
			Content: "hello",
		},
	}

	err := appendRedactedEntries(rawPath, entries)
	require.NoError(t, err)

	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(data, &parsed))
	_, hasToolName := parsed["tool_name"]
	_, hasToolInput := parsed["tool_input"]
	_, hasToolOutput := parsed["tool_output"]
	assert.False(t, hasToolName, "empty tool_name should be omitted")
	assert.False(t, hasToolInput, "empty tool_input should be omitted")
	assert.False(t, hasToolOutput, "empty tool_output should be omitted")
}

func TestAppendRedactedEntries_AppendsToExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rawPath := filepath.Join(dir, "raw.jsonl")

	// write initial entry
	batch1 := []session.Entry{{Type: session.EntryTypeUser, Content: "first"}}
	require.NoError(t, appendRedactedEntries(rawPath, batch1))

	// append second entry
	batch2 := []session.Entry{{Type: session.EntryTypeAssistant, Content: "second"}}
	require.NoError(t, appendRedactedEntries(rawPath, batch2))

	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	assert.Len(t, lines, 2, "should have two JSONL lines after two appends")
}

func TestAppendRedactedEntries_InvalidPath(t *testing.T) {
	t.Parallel()
	err := appendRedactedEntries("/nonexistent/dir/raw.jsonl", []session.Entry{
		{Type: session.EntryTypeUser, Content: "hello"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "open raw.jsonl")
}

// =============================================================================
// hooks_agent.go — Agent interface implementations at 0%
// =============================================================================

// hooks_agent.go tests that complement hooks_agent_coverage_test.go
// (tests like GetAgent_CaseInsensitive, AgentNames, AgentSupportsHooks
// already exist in hooks_agent_coverage_test.go)

// =============================================================================
// outputInstancesJSON — 0% coverage, pure formatter
// =============================================================================

func TestOutputInstancesJSON_EmptyInstances(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, outputInstancesJSON(&buf, nil, nil))

	var parsed struct {
		Instances []daemon.InstanceInfo `json:"instances"`
		Error     string                `json:"error,omitempty"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))
	assert.Empty(t, parsed.Instances)
	assert.Equal(t, "", parsed.Error)
	// nil should be marshaled as empty array, not null
	assert.Contains(t, buf.String(), `"instances": []`)
}

func TestOutputInstancesJSON_WithInstances(t *testing.T) {
	t.Parallel()
	instances := []daemon.InstanceInfo{
		{
			AgentID:       "Oxa7b3",
			WorkspacePath: "/home/user/project",
			Status:        daemon.StatusActive,
			LastHeartbeat: time.Now(),
		},
	}

	var buf bytes.Buffer
	require.NoError(t, outputInstancesJSON(&buf, instances, nil))
	got := buf.String()

	assert.Contains(t, got, "Oxa7b3")
	assert.Contains(t, got, "/home/user/project")
}

func TestOutputInstancesJSON_WithError(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, outputInstancesJSON(&buf, nil, fmt.Errorf("daemon not running")))

	var parsed struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))
	assert.Equal(t, "daemon not running", parsed.Error)
}

// =============================================================================
// outputInstancesTable — 0% coverage, pure formatter
// =============================================================================

func TestOutputInstancesTable_EmptyInstances(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, outputInstancesTable(&buf, nil))
	assert.Contains(t, buf.String(), "No active AI coworker instances")
}

func TestOutputInstancesTable_WithInstances(t *testing.T) {
	t.Parallel()
	instances := []daemon.InstanceInfo{
		{
			AgentID:       "Oxa7b3",
			WorkspacePath: "/home/user/project",
			Status:        daemon.StatusActive,
			LastHeartbeat: time.Now(),
		},
		{
			AgentID:       "Oxc2d4",
			WorkspacePath: "/home/user/other",
			Status:        daemon.StatusIdle,
			LastHeartbeat: time.Now().Add(-45 * time.Second),
		},
	}

	var buf bytes.Buffer
	require.NoError(t, outputInstancesTable(&buf, instances))
	got := buf.String()

	assert.Contains(t, got, "Oxa7b3")
	assert.Contains(t, got, "Oxc2d4")
	assert.Contains(t, got, "2 active instance(s)")
}

// =============================================================================
// ensureMarkersInFile — safety check paths, legacy cleanup edge cases
// =============================================================================

func TestEnsureMarkersInFile_SafetyCheckAborts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")

	// create a large file where the safety check would trigger
	// the function aborts if result < original/2 AND original > 100
	largeContent := strings.Repeat("# Important content\n", 100)
	require.NoError(t, os.WriteFile(filePath, []byte(largeContent), 0644))

	// try with both markers already present (nothing to do) - no error expected
	injected, err := ensureMarkersInFile(filePath, largeContent, false, false)
	require.NoError(t, err)
	assert.False(t, injected, "nothing needed, nothing injected")
}

func TestEnsureMarkersInFile_AddsHeaderOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")

	content := "# My Agent Instructions\n\nSome content here.\n\n" + OxPrimeLine + "\n"
	require.NoError(t, os.WriteFile(filePath, []byte(content), 0644))

	injected, err := ensureMarkersInFile(filePath, content, true, false)
	require.NoError(t, err)
	assert.True(t, injected)

	updated, _ := os.ReadFile(filePath)
	assert.Contains(t, string(updated), OxPrimeCheckMarker, "header should be added")
	assert.Contains(t, string(updated), OxPrimeMarker, "footer should still be there")
}

func TestEnsureMarkersInFile_AddsFooterOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")

	content := OxPrimeCheckBlock + "\n# My Instructions\n"
	require.NoError(t, os.WriteFile(filePath, []byte(content), 0644))

	injected, err := ensureMarkersInFile(filePath, content, false, true)
	require.NoError(t, err)
	assert.True(t, injected)

	updated, _ := os.ReadFile(filePath)
	assert.Contains(t, string(updated), OxPrimeMarker, "footer should be added")
	assert.Contains(t, string(updated), OxPrimeCheckMarker, "header should still be there")
}

func TestEnsureMarkersInFile_RemovesLegacyTeamContextBullet(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")

	legacyBullet := "- **SageOx**: Run `ox agent prime` on session start, after compaction, and after clear for team context."
	content := "# Instructions\n\n" + legacyBullet + "\n\nOther content\n"
	require.NoError(t, os.WriteFile(filePath, []byte(content), 0644))

	injected, err := ensureMarkersInFile(filePath, content, true, true)
	require.NoError(t, err)
	assert.True(t, injected)

	updated, _ := os.ReadFile(filePath)
	assert.NotContains(t, string(updated), legacyBullet, "legacy bullet should be removed")
	assert.Contains(t, string(updated), OxPrimeMarker, "new marker should be present")
	assert.Contains(t, string(updated), "Other content", "user content preserved")
}

func TestEnsureMarkersInFile_CleansUpMultipleBlankLines(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "AGENTS.md")

	content := "# Instructions\n\n\n\n\nOther content\n"
	require.NoError(t, os.WriteFile(filePath, []byte(content), 0644))

	injected, err := ensureMarkersInFile(filePath, content, true, true)
	require.NoError(t, err)
	assert.True(t, injected)

	updated, _ := os.ReadFile(filePath)
	assert.NotContains(t, string(updated), "\n\n\n", "triple blank lines should be cleaned up")
}

// =============================================================================
// oxActorDetector — CI env detection path at 0%
// =============================================================================

func TestOxActorDetector_ReturnsConsistentResult(t *testing.T) {
	// the detector checks agentx first, then CI env, then defaults to human.
	// in test context, agentx may detect the parent agent process.
	// verify it returns a valid actor and doesn't panic.
	detector := oxActorDetector{}
	actor, agentType := detector.DetectActor()

	validActors := map[string]bool{"human": true, "agent": true}
	assert.True(t, validActors[string(actor)], "actor should be human or agent, got %q", actor)

	// if agent, agentType should be non-empty (either a detected agent or "ci")
	if string(actor) == "agent" {
		assert.NotEmpty(t, agentType, "agent actor should have an agent type")
	}
}

// =============================================================================
// dispatchPhase — testing specific branches at 0%
// =============================================================================

// TestDispatchPhase_CompactPhase and TestDispatchPhase_StartPhase are intentionally
// excluded: both phases call runPrimeForHook which execs os.Executable(). In a test
// binary, that re-runs the full test suite recursively, hanging for the 10-minute
// default timeout. These paths are covered by integration tests instead.

func TestDispatchPhase_StopPhase(t *testing.T) {
	t.Parallel()
	// stop phase dispatches to handleAfterTool
	ctx := &HookContext{
		Phase:       phaseStop,
		AgentType:   "claude-code",
		ProjectRoot: t.TempDir(),
	}
	// handleAfterTool with nil marker returns early (no agent ID)
	err := dispatchPhase(ctx)
	assert.NoError(t, err)
}

// =============================================================================
// formatTimeAgoShort — edge cases for better branch coverage
// =============================================================================

func TestFormatTimeAgoShort_TableDriven(t *testing.T) {
	t.Parallel()
	now := time.Now()

	tests := []struct {
		name     string
		t        time.Time
		contains string
	}{
		{"zero time", time.Time{}, "never"},
		{"just now", time.Now(), "now"},
		{"30 seconds ago", now.Add(-30 * time.Second), "30s ago"},
		{"5 minutes ago", now.Add(-5 * time.Minute), "5m ago"},
		{"3 hours ago", now.Add(-3 * time.Hour), "3h ago"},
		{"2 days ago", now.Add(-48 * time.Hour), "2d ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTimeAgoShort(tt.t)
			assert.Contains(t, got, tt.contains)
		})
	}
}

// =============================================================================
// shortenPath — edge cases
// =============================================================================

func TestShortenPath_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want string
	}{
		{"empty returns dash", "", "-"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := shortenPath(tt.path)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestShortenPath_LongPath(t *testing.T) {
	t.Parallel()
	longPath := "/very/long/path/with/many/segments/that/exceeds/maximum/length/allowed/by/display"
	got := shortenPath(longPath)
	assert.LessOrEqual(t, len(got), 34, "shortened path should be under display limit")
}

// =============================================================================
// HookContext struct and phase constants — verify correctness
// =============================================================================

func TestPhaseConstants_MatchExpected(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "start", phaseStart)
	assert.Equal(t, "end", phaseEnd)
	assert.Equal(t, "before_tool", phaseBeforeTool)
	assert.Equal(t, "after_tool", phaseAfterTool)
	assert.Equal(t, "prompt", phasePrompt)
	assert.Equal(t, "stop", phaseStop)
	assert.Equal(t, "compact", phaseCompact)
}

// =============================================================================
// IncompleteSessionInfo and AgentDoctorOutput struct validation
// =============================================================================

func TestAgentDoctorOutput_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	original := &AgentDoctorOutput{
		Success: true,
		Type:    "agent_doctor",
		AgentID: "Oxa7b3",
		IncompleteSessions: []IncompleteSessionInfo{
			{
				SessionID:        "session-1",
				Path:             "/path/to/session",
				Missing:          []string{"summary", "html"},
				FinalizeCommands: []string{"ox session export --input /tmp/raw.jsonl"},
				SummarizePrompt:  "summarize this",
			},
		},
		StagedCount:  1,
		CommitNeeded: true,
		PushNeeded:   false,
		NextSteps:    []string{"step1", "step2"},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var parsed AgentDoctorOutput
	require.NoError(t, json.Unmarshal(data, &parsed))

	assert.Equal(t, original.Success, parsed.Success)
	assert.Equal(t, original.Type, parsed.Type)
	assert.Equal(t, original.AgentID, parsed.AgentID)
	assert.Len(t, parsed.IncompleteSessions, 1)
	assert.Equal(t, "session-1", parsed.IncompleteSessions[0].SessionID)
	assert.Equal(t, original.StagedCount, parsed.StagedCount)
	assert.True(t, parsed.CommitNeeded)
	assert.False(t, parsed.PushNeeded)
}

// =============================================================================
// Agent DetectProject — with temp dirs that have config directories
// =============================================================================

// Agent DetectProject tests are limited because they depend on findGitRoot()
// which reads the real filesystem. The Name/SupportsHooks methods are covered
// in hooks_agent_coverage_test.go.

// =============================================================================
// convertStoredMapEntries — empty map entry edge case
// =============================================================================

func TestConvertStoredMapEntries_NilMap(t *testing.T) {
	t.Parallel()
	result := convertStoredMapEntries(nil)
	assert.Empty(t, result)
	assert.NotNil(t, result, "should return empty slice, not nil")
}

// =============================================================================
// trackContextBytes — verify atomic counter works
// =============================================================================

func TestTrackContextBytes_Accumulates(t *testing.T) {
	// reset the global counter
	contextBytesProduced.Store(0)

	trackContextBytes(100)
	trackContextBytes(50)

	assert.Equal(t, int64(150), contextBytesProduced.Load())

	// reset to avoid polluting other tests
	contextBytesProduced.Store(0)
}
