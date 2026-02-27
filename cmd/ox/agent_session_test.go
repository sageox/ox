package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTitle(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected string
	}{
		{
			name:     "no title flag",
			args:     []string{"--file", "test.jsonl"},
			expected: "",
		},
		{
			name:     "title flag with space",
			args:     []string{"--title", "My Title"},
			expected: "My Title",
		},
		{
			name:     "title flag with equals",
			args:     []string{"--title=My Title"},
			expected: "My Title",
		},
		{
			name:     "title flag mixed with other args",
			args:     []string{"--file", "test.jsonl", "--title", "Test"},
			expected: "Test",
		},
		{
			name:     "empty args",
			args:     []string{},
			expected: "",
		},
		{
			name:     "title with spaces",
			args:     []string{"--title", "AWS Infrastructure Planning"},
			expected: "AWS Infrastructure Planning",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseTitle(tt.args)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsManualSessionAgent(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		want      bool
	}{
		{name: "codex canonical", agentType: "codex", want: true},
		{name: "codex display alias", agentType: "Codex", want: true},
		{name: "claude canonical", agentType: "claude", want: false},
		{name: "claude legacy alias", agentType: "claude-code", want: false},
		{name: "empty", agentType: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isManualSessionAgent(tt.agentType)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatFilterModeDescription(t *testing.T) {
	tests := []struct {
		mode     string
		expected string
	}{
		{"infra", "infrastructure events only"},
		{"all", "all events"},
		{"none", "disabled"},
		{"custom", "custom"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			result := formatFilterModeDescription(tt.mode)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMapRoleToEntryType(t *testing.T) {
	tests := []struct {
		role     string
		expected session.EntryType
	}{
		{"user", session.EntryTypeUser},
		{"assistant", session.EntryTypeAssistant},
		{"system", session.EntryTypeSystem},
		{"tool", session.EntryTypeTool},
		{"unknown", session.EntryTypeSystem}, // fallback
		{"", session.EntryTypeSystem},        // empty fallback
	}

	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			result := mapRoleToEntryType(tt.role)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseRecordEntries(t *testing.T) {
	t.Run("parses JSON array from --entries flag", func(t *testing.T) {
		now := time.Now().Format(time.RFC3339)
		jsonArray := `[{"type":"user","content":"Hello","timestamp":"` + now + `"},{"type":"assistant","content":"Hi there!","timestamp":"` + now + `"}]`

		args := []string{"--entries", jsonArray}
		result, err := parseRecordEntries(args)
		require.NoError(t, err)
		assert.Len(t, result, 2)
		assert.Equal(t, "user", result[0].Type)
		assert.Equal(t, "Hello", result[0].Content)
		assert.Equal(t, "assistant", result[1].Type)
		assert.Equal(t, "Hi there!", result[1].Content)
	})

	t.Run("parses single JSON object from --entries flag", func(t *testing.T) {
		now := time.Now().Format(time.RFC3339)
		jsonObj := `{"type":"user","content":"Hello","timestamp":"` + now + `"}`

		args := []string{"--entries", jsonObj}
		result, err := parseRecordEntries(args)
		require.NoError(t, err)
		assert.Len(t, result, 1)
		assert.Equal(t, "user", result[0].Type)
		assert.Equal(t, "Hello", result[0].Content)
	})

	t.Run("parses --entries= syntax", func(t *testing.T) {
		jsonObj := `{"type":"system","content":"System message"}`
		args := []string{"--entries=" + jsonObj}
		result, err := parseRecordEntries(args)
		require.NoError(t, err)
		assert.Len(t, result, 1)
		assert.Equal(t, "system", result[0].Type)
	})

	t.Run("handles empty args", func(t *testing.T) {
		result, err := parseRecordEntries([]string{})
		require.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("returns error for invalid JSON", func(t *testing.T) {
		args := []string{"--entries", "not valid json"}
		_, err := parseRecordEntries(args)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid JSON")
	})

	t.Run("returns error for malformed JSON array", func(t *testing.T) {
		args := []string{"--entries", `[{"type":"user"`}
		_, err := parseRecordEntries(args)
		assert.Error(t, err)
	})
}

func TestRecordingState(t *testing.T) {
	t.Run("creates recording state with session path", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("HOME", tempHome)
		t.Setenv("XDG_CACHE_HOME", "")

		projectDir := t.TempDir()
		sageoxDir := filepath.Join(projectDir, ".sageox")
		require.NoError(t, os.MkdirAll(sageoxDir, 0755))

		sessionsDir := filepath.Join(projectDir, "sessions")
		sessionName := "2026-01-16T10-30-testuser-OxTest"
		sessionPath := filepath.Join(sessionsDir, sessionName)
		require.NoError(t, os.MkdirAll(sessionPath, 0755))

		recordingState := &session.RecordingState{
			AgentID:     "OxTest",
			StartedAt:   time.Now(),
			SessionPath: sessionPath,
			AdapterName: "claude-code",
		}

		require.NoError(t, session.SaveRecordingState(projectDir, recordingState))

		// verify recording is detected
		assert.True(t, session.IsRecording(projectDir))

		// load and verify
		state, err := session.LoadRecordingState(projectDir)
		require.NoError(t, err)
		require.NotNil(t, state)
		assert.Equal(t, sessionPath, state.SessionPath)
		assert.Equal(t, "OxTest", state.AgentID)
	})

	t.Run("duration calculation works", func(t *testing.T) {
		state := &session.RecordingState{
			StartedAt: time.Now().Add(-5 * time.Minute),
		}
		duration := state.Duration()
		// allow 1 second margin for test execution
		assert.InDelta(t, 5*time.Minute, duration, float64(time.Second))
	})
}

func TestConvertRawEntries(t *testing.T) {
	t.Run("converts raw entries to session entries", func(t *testing.T) {
		// this tests the adapter conversion
		// the actual implementation depends on adapters.RawEntry structure
		// which is tested in the adapters package
	})
}

func TestReadEntriesFromFile(t *testing.T) {
	t.Run("reads valid JSONL file from sessions directory", func(t *testing.T) {
		// set up session-folder structure: <base>/sessions/test/raw.jsonl
		baseDir := t.TempDir()
		sessionDir := filepath.Join(baseDir, "sessions", "test")
		require.NoError(t, os.MkdirAll(sessionDir, 0755))

		now := time.Now().Format(time.RFC3339Nano)
		content := `{"type":"header","metadata":{"version":"1.0"}}
{"type":"user","content":"Hello","ts":"` + now + `"}
{"type":"assistant","content":"Hi there!","ts":"` + now + `"}
{"type":"footer","entry_count":2}`

		tmpFile := filepath.Join(sessionDir, "raw.jsonl")
		require.NoError(t, os.WriteFile(tmpFile, []byte(content), 0644))

		entries, err := readEntriesFromFile(tmpFile)
		require.NoError(t, err)
		assert.Len(t, entries, 2)
	})

	t.Run("handles file not found", func(t *testing.T) {
		_, err := readEntriesFromFile("/nonexistent/path/file.jsonl")
		assert.Error(t, err)
	})

	t.Run("handles empty file in proper structure", func(t *testing.T) {
		// set up session-folder structure
		baseDir := t.TempDir()
		sessionDir := filepath.Join(baseDir, "sessions", "empty")
		require.NoError(t, os.MkdirAll(sessionDir, 0755))

		tmpFile := filepath.Join(sessionDir, "raw.jsonl")
		require.NoError(t, os.WriteFile(tmpFile, []byte(""), 0644))

		entries, err := readEntriesFromFile(tmpFile)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})
}

func TestSessionRecordInput(t *testing.T) {
	t.Run("struct serializes to JSON correctly", func(t *testing.T) {
		input := sessionRecordInput{
			Type:      "user",
			Content:   "Hello world",
			Timestamp: "2026-01-16T10:30:00Z",
			ToolName:  "bash",
			ToolInput: "ls -la",
		}

		jsonBytes, err := json.Marshal(input)
		require.NoError(t, err)

		var parsed map[string]string
		require.NoError(t, json.Unmarshal(jsonBytes, &parsed))

		assert.Equal(t, "user", parsed["type"])
		assert.Equal(t, "Hello world", parsed["content"])
		assert.Equal(t, "bash", parsed["tool_name"])
	})
}

func TestAgentSessionResult(t *testing.T) {
	t.Run("output structure for stop command", func(t *testing.T) {
		result := &agentSessionResult{
			EventsPath:      "/path/to/events.jsonl",
			EntryCount:      10,
			SecretsRedacted: 3,
		}

		assert.NotEmpty(t, result.EventsPath)
		assert.Equal(t, 10, result.EntryCount)
		assert.Equal(t, 3, result.SecretsRedacted)
	})
}

func TestSecretRedaction(t *testing.T) {
	t.Run("redacts AWS access keys", func(t *testing.T) {
		entries := []session.Entry{
			{
				Type:    session.EntryTypeUser,
				Content: "My AWS key is AKIAIOSFODNN7EXAMPLE",
			},
		}

		redactor := session.NewRedactor()
		count := redactor.RedactEntries(entries)

		assert.Greater(t, count, 0)
		assert.Contains(t, entries[0].Content, "[REDACTED_AWS_KEY]")
		assert.NotContains(t, entries[0].Content, "AKIAIOSFODNN7EXAMPLE")
	})

	t.Run("redacts GitHub tokens", func(t *testing.T) {
		entries := []session.Entry{
			{
				Type:    session.EntryTypeUser,
				Content: "Token: ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
			},
		}

		redactor := session.NewRedactor()
		count := redactor.RedactEntries(entries)

		assert.Greater(t, count, 0)
		assert.Contains(t, entries[0].Content, "[REDACTED_GITHUB_TOKEN]")
	})

	t.Run("preserves non-secret content", func(t *testing.T) {
		entries := []session.Entry{
			{
				Type:    session.EntryTypeUser,
				Content: "Hello, how are you?",
			},
		}

		redactor := session.NewRedactor()
		count := redactor.RedactEntries(entries)

		assert.Equal(t, 0, count)
		assert.Equal(t, "Hello, how are you?", entries[0].Content)
	})
}

func TestSessionStore(t *testing.T) {
	t.Run("creates store and lists sessions", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("HOME", tempHome)
		t.Setenv("XDG_CACHE_HOME", "")

		repoContextPath := filepath.Join(tempHome, ".sageox", "cache", "test-repo")
		require.NoError(t, os.MkdirAll(repoContextPath, 0755))

		store, err := session.NewStore(repoContextPath)
		require.NoError(t, err)
		require.NotNil(t, store)

		// list sessions (should be empty initially)
		sessions, err := store.ListSessions()
		require.NoError(t, err)
		assert.Empty(t, sessions)
	})

	t.Run("generates session name with correct format", func(t *testing.T) {
		sessionName := session.GenerateSessionName("OxTest", "testuser")

		// format: YYYY-MM-DDTHH-MM-<username>-<sessionID>
		assert.Contains(t, sessionName, "testuser")
		assert.Contains(t, sessionName, "OxTest")
		assert.Contains(t, sessionName, "T")
	})
}

func TestSessionEntryTypes(t *testing.T) {
	t.Run("entry types are valid", func(t *testing.T) {
		types := []session.EntryType{
			session.EntryTypeUser,
			session.EntryTypeAssistant,
			session.EntryTypeSystem,
			session.EntryTypeTool,
		}

		for _, entryType := range types {
			assert.NotEmpty(t, string(entryType))
		}
	})
}

// Regression test for ox-a9y: session recording must not capture entries
// from before `ox session start` was called. The adapter reads ALL entries
// from the Claude Code JSONL file, so we filter by StartedAt timestamp.
func TestFilterEntriesAfterStart(t *testing.T) {
	startedAt := time.Date(2026, 2, 26, 14, 0, 0, 0, time.UTC)

	t.Run("excludes entries before session start", func(t *testing.T) {
		entries := []adapters.RawEntry{
			{Timestamp: startedAt.Add(-10 * time.Minute), Role: "user", Content: "before start"},
			{Timestamp: startedAt.Add(-5 * time.Minute), Role: "assistant", Content: "also before"},
			{Timestamp: startedAt.Add(1 * time.Second), Role: "user", Content: "after start"},
			{Timestamp: startedAt.Add(2 * time.Minute), Role: "assistant", Content: "well after"},
		}

		filtered := filterEntriesAfterStart(entries, startedAt)
		require.Len(t, filtered, 2)
		assert.Equal(t, "after start", filtered[0].Content)
		assert.Equal(t, "well after", filtered[1].Content)
	})

	t.Run("includes entries exactly at start time", func(t *testing.T) {
		entries := []adapters.RawEntry{
			{Timestamp: startedAt, Role: "user", Content: "exactly at start"},
		}

		filtered := filterEntriesAfterStart(entries, startedAt)
		require.Len(t, filtered, 1)
		assert.Equal(t, "exactly at start", filtered[0].Content)
	})

	t.Run("preserves entries with zero timestamp", func(t *testing.T) {
		entries := []adapters.RawEntry{
			{Timestamp: time.Time{}, Role: "system", Content: "no timestamp"},
			{Timestamp: startedAt.Add(-5 * time.Minute), Role: "user", Content: "before start"},
			{Timestamp: startedAt.Add(1 * time.Minute), Role: "user", Content: "after start"},
		}

		filtered := filterEntriesAfterStart(entries, startedAt)
		require.Len(t, filtered, 2)
		assert.Equal(t, "no timestamp", filtered[0].Content)
		assert.Equal(t, "after start", filtered[1].Content)
	})

	t.Run("returns empty when all entries are before start", func(t *testing.T) {
		entries := []adapters.RawEntry{
			{Timestamp: startedAt.Add(-10 * time.Minute), Role: "user", Content: "old"},
			{Timestamp: startedAt.Add(-1 * time.Second), Role: "assistant", Content: "also old"},
		}

		filtered := filterEntriesAfterStart(entries, startedAt)
		assert.Empty(t, filtered)
	})

	t.Run("handles empty input", func(t *testing.T) {
		filtered := filterEntriesAfterStart(nil, startedAt)
		assert.Empty(t, filtered)
	})
}
