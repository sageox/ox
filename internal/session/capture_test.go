package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCapturePrior_ValidInput(t *testing.T) {
	jsonl := `{"_meta":{"schema_version":"1","source":"agent_reconstruction","agent_id":"Oxa7b3"}}
{"seq":1,"type":"user","content":"Hello"}
{"seq":2,"type":"assistant","content":"Hi there!"}`

	opts := CaptureOptions{
		AgentID:       "Oxa7b3",
		SkipRedaction: true,
	}

	result, err := CapturePrior(strings.NewReader(jsonl), opts)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 2, result.EntryCount)
	assert.Equal(t, "Oxa7b3", result.AgentID)
	assert.NotEmpty(t, result.Path)
	assert.NotEmpty(t, result.SessionName)
}

func TestCapturePrior_NilReader(t *testing.T) {
	opts := CaptureOptions{AgentID: "test"}

	_, err := CapturePrior(nil, opts)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrHistoryEmptyInput)
}

func TestCapturePrior_MissingAgentID(t *testing.T) {
	// no agent_id in the header and none on the command line: nothing to store it under
	jsonl := `{"_meta":{"schema_version":"1","source":"test","agent_id":""}}
{"seq":1,"type":"user","content":"Hello"}`

	opts := CaptureOptions{}

	_, err := CapturePrior(strings.NewReader(jsonl), opts)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent_id required")
}

// isolateCaptureStorage keeps a CapturePrior test's stored history out of real directories: no
// project under the working directory, and HOME and the XDG cache inside the test's own dir.
func isolateCaptureStorage(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Chdir(t.TempDir())
}

// Failure prevented: capture-prior refuses a header without agent_id or source (an older prime's
// example, or a shorter one an AI coworker writes) although its command line carries the agent ID,
// or stores a history that every later strict reader rejects (ox-dfk7).
func TestCapturePrior_HeaderWithoutAgentIDUsesCommandLine(t *testing.T) {
	tests := []struct {
		name, meta, wantSource string
	}{
		{"older prime example", `{"_meta":{"schema_version":"1","agent_type":"claude-code","session_id":"manual","started_at":"2026-10-12T00:00:00Z"}}`,
			HistorySourceAgentReconstruction},
		{"schema_version only", `{"_meta":{"schema_version":"1"}}`, HistorySourceAgentReconstruction},
		{"a given source is kept", `{"_meta":{"schema_version":"1","source":"planning_history"}}`, HistorySourcePlanningHistory},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateCaptureStorage(t)
			jsonl := tt.meta + "\n" + `{"seq":1,"type":"user","content":"Hello"}` + "\n" + `{"seq":2,"type":"assistant","content":"Hi"}`

			result, err := CapturePrior(strings.NewReader(jsonl), CaptureOptions{AgentID: "Oxa7b3", SkipRedaction: true})

			require.NoError(t, err)
			assert.Equal(t, "Oxa7b3", result.AgentID, "session_id must never stand in for the agent ID")
			stored, err := ParseHistoryFile(result.Path) // the strict reader every later consumer uses
			require.NoError(t, err)
			assert.Equal(t, "Oxa7b3", stored.Meta.AgentID)
			assert.Equal(t, tt.wantSource, stored.Meta.Source)
			assert.Len(t, stored.Entries, 2)
		})
	}
}

// Failure prevented: relaxing the capture-prior header check so far that a line with no
// schema_version, which may not be a header at all, is accepted.
func TestCapturePrior_HeaderStillNeedsSchemaVersion(t *testing.T) {
	jsonl := `{"_meta":{"agent_id":"Oxa7b3"}}` + "\n" + `{"seq":1,"type":"user","content":"Hello"}`

	_, err := CapturePrior(strings.NewReader(jsonl), CaptureOptions{AgentID: "Oxa7b3"})

	require.ErrorIs(t, err, ErrHistoryInvalidMeta)
	assert.Contains(t, err.Error(), "schema_version")
}

// Failure prevented: the capture-prior relaxation leaking into the general validator, whose
// readers (stored prior-history.jsonl) must keep rejecting a header without agent_id.
func TestValidateJSONL_StillRequiresAgentID(t *testing.T) {
	_, err := ValidateJSONL(strings.NewReader(`{"_meta":{"schema_version":"1","source":"agent_reconstruction"}}` + "\n" + `{"seq":1,"type":"user","content":"Hello"}`))

	require.ErrorIs(t, err, ErrHistoryInvalidMeta)
	assert.Contains(t, err.Error(), "agent_id")
}

func TestCapturePrior_UsesAgentIDFromMeta(t *testing.T) {
	jsonl := `{"_meta":{"schema_version":"1","source":"test","agent_id":"FromMeta"}}
{"seq":1,"type":"user","content":"Hello"}`

	opts := CaptureOptions{
		AgentID:       "FromOpts",
		SkipRedaction: true,
	}

	result, err := CapturePrior(strings.NewReader(jsonl), opts)

	require.NoError(t, err)
	assert.Equal(t, "FromMeta", result.AgentID)
}

func TestCapturePrior_AppliesTitle(t *testing.T) {
	jsonl := `{"_meta":{"schema_version":"1","source":"test","agent_id":"test"}}
{"seq":1,"type":"user","content":"Hello"}`

	opts := CaptureOptions{
		Title:         "Planning Session",
		SkipRedaction: true,
	}

	result, err := CapturePrior(strings.NewReader(jsonl), opts)

	require.NoError(t, err)
	assert.Equal(t, "Planning Session", result.Title)
}

func TestCapturePrior_RedactsSecrets(t *testing.T) {
	jsonl := `{"_meta":{"schema_version":"1","source":"test","agent_id":"test"}}
{"seq":1,"type":"user","content":"Key: AKIAIOSFODNN7EXAMPLE"}`

	opts := CaptureOptions{
		SkipRedaction: false,
	}

	result, err := CapturePrior(strings.NewReader(jsonl), opts)

	require.NoError(t, err)
	assert.Equal(t, 1, result.SecretsRedacted)
}

func TestCapturePrior_SkipRedaction(t *testing.T) {
	jsonl := `{"_meta":{"schema_version":"1","source":"test","agent_id":"test"}}
{"seq":1,"type":"user","content":"Key: AKIAIOSFODNN7EXAMPLE"}`

	opts := CaptureOptions{
		SkipRedaction: true,
	}

	result, err := CapturePrior(strings.NewReader(jsonl), opts)

	require.NoError(t, err)
	assert.Equal(t, 0, result.SecretsRedacted)
}

func TestCapturePrior_InvalidInput(t *testing.T) {
	tests := []struct {
		name    string
		jsonl   string
		wantErr string
	}{
		{
			name:  "missing meta fields",
			jsonl: `{"seq":1,"type":"user","content":"Hello"}`,
			// first line parsed as entry (no _meta), fails validation
			wantErr: "_meta",
		},
		{
			name:    "invalid entry type",
			jsonl:   `{"_meta":{"schema_version":"1","source":"test","agent_id":"test"}}` + "\n" + `{"seq":1,"type":"bogus","content":"Hello"}`,
			wantErr: "invalid",
		},
		{
			name:    "empty entries",
			jsonl:   `{"_meta":{"schema_version":"1","source":"test","agent_id":"test"}}`,
			wantErr: "at least one entry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := CaptureOptions{SkipRedaction: true}
			_, err := CapturePrior(strings.NewReader(tt.jsonl), opts)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestCapturePriorFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	historyPath := filepath.Join(tmpDir, "history.jsonl")

	content := `{"_meta":{"schema_version":"1","source":"test","agent_id":"testfile"}}
{"seq":1,"type":"user","content":"From file"}`

	err := os.WriteFile(historyPath, []byte(content), 0644)
	require.NoError(t, err)

	opts := CaptureOptions{SkipRedaction: true}
	result, err := CapturePriorFromFile(historyPath, opts)

	require.NoError(t, err)
	assert.Equal(t, "testfile", result.AgentID)
	assert.Equal(t, 1, result.EntryCount)
}

func TestCapturePriorFromFile_NotFound(t *testing.T) {
	opts := CaptureOptions{}
	_, err := CapturePriorFromFile("/nonexistent/path/history.jsonl", opts)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "open file")
}

func TestCaptureOutput_ToJSON(t *testing.T) {
	result := &CaptureResult{
		Path:            "/path/to/history.jsonl",
		SessionName:     "2026-01-16T10-00-user-Oxa7b3",
		EntryCount:      42,
		SecretsRedacted: 2,
		Title:           "Planning Session",
		AgentID:         "Oxa7b3",
	}

	output := NewCaptureOutput(result)
	jsonBytes, err := output.ToJSON()

	require.NoError(t, err)
	jsonStr := string(jsonBytes)
	assert.Contains(t, jsonStr, `"success": true`)
	assert.Contains(t, jsonStr, `"type": "session_capture_prior"`)
	assert.Contains(t, jsonStr, `"entry_count": 42`)
	assert.Contains(t, jsonStr, `"secrets_redacted": 2`)
}

func TestCreateCapturedHistoryMeta(t *testing.T) {
	meta := CreateCapturedHistoryMeta("Oxa7b3", "claude-code", "agent_reconstruction", "Test Session")

	assert.Equal(t, "Oxa7b3", meta.AgentID)
	assert.Equal(t, "claude-code", meta.AgentType)
	assert.Equal(t, "agent_reconstruction", meta.Source)
	assert.Equal(t, "Test Session", meta.SessionTitle)
	assert.Equal(t, HistorySchemaVersion, meta.SchemaVersion)
	assert.False(t, meta.CapturedAt.IsZero())
}

func TestGetRepoIDFromProject(t *testing.T) {
	t.Run("returns repo_id from config.json", func(t *testing.T) {
		tmpDir := t.TempDir()
		sageoxDir := filepath.Join(tmpDir, ".sageox")
		require.NoError(t, os.MkdirAll(sageoxDir, 0755))

		configJSON := `{"config_version":"2","repo_id":"repo_019c17fa-5090-7776-ad67-a8db7432b3e4"}`
		require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(configJSON), 0644))

		got := getRepoIDFromProject(tmpDir)
		assert.Equal(t, "repo_019c17fa-5090-7776-ad67-a8db7432b3e4", got)
	})

	t.Run("returns empty when no config", func(t *testing.T) {
		tmpDir := t.TempDir()
		got := getRepoIDFromProject(tmpDir)
		assert.Empty(t, got)
	})

	t.Run("returns empty for empty root", func(t *testing.T) {
		got := getRepoIDFromProject("")
		assert.Empty(t, got)
	})
}
