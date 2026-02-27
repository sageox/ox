package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMapEntriesToTyped(t *testing.T) {
	tests := []struct {
		name     string
		input    []map[string]any
		expected []session.Entry
	}{
		{
			name:     "empty input",
			input:    []map[string]any{},
			expected: []session.Entry{},
		},
		{
			name:     "nil input",
			input:    nil,
			expected: []session.Entry{},
		},
		{
			name: "all fields populated",
			input: []map[string]any{
				{
					"type":        "tool",
					"content":     "ran the linter",
					"tool_name":   "Bash",
					"tool_input":  "make lint",
					"tool_output": "no errors",
				},
			},
			expected: []session.Entry{
				{
					Type:       session.SessionEntryTypeTool,
					Content:    "ran the linter",
					ToolName:   "Bash",
					ToolInput:  "make lint",
					ToolOutput: "no errors",
				},
			},
		},
		{
			name: "type and content only",
			input: []map[string]any{
				{
					"type":    "user",
					"content": "hello",
				},
			},
			expected: []session.Entry{
				{
					Type:    session.SessionEntryTypeUser,
					Content: "hello",
				},
			},
		},
		{
			name: "missing all fields yields zero-value entry",
			input: []map[string]any{
				{},
			},
			expected: []session.Entry{
				{},
			},
		},
		{
			name: "non-string values are ignored",
			input: []map[string]any{
				{
					"type":    42,
					"content": true,
				},
			},
			expected: []session.Entry{
				{},
			},
		},
		{
			name: "multiple entries",
			input: []map[string]any{
				{"type": "user", "content": "question"},
				{"type": "assistant", "content": "answer"},
				{"type": "system", "content": "context"},
			},
			expected: []session.Entry{
				{Type: session.SessionEntryTypeUser, Content: "question"},
				{Type: session.SessionEntryTypeAssistant, Content: "answer"},
				{Type: session.SessionEntryTypeSystem, Content: "context"},
			},
		},
		{
			name: "extra unknown fields are silently ignored",
			input: []map[string]any{
				{
					"type":          "user",
					"content":       "hello",
					"unknown_field": "should be ignored",
					"seq":           42,
				},
			},
			expected: []session.Entry{
				{Type: session.SessionEntryTypeUser, Content: "hello"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := mapEntriesToTyped(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestRewriteRawJSONL(t *testing.T) {
	t.Run("round trip preserves header entries and footer", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "raw.jsonl")

		now := time.Now().Truncate(time.Second).UTC()
		original := &session.StoredSession{
			Meta: &session.StoreMeta{
				Version:   "1",
				CreatedAt: now,
				AgentID:   "agent-123",
				AgentType: "claude-code",
				Username:  "person-a",
				OxVersion: "0.9.0",
			},
			Entries: []map[string]any{
				{"type": "user", "content": "what is 2+2?"},
				{"type": "assistant", "content": "4"},
			},
			Footer: map[string]any{
				"type":   "footer",
				"status": "completed",
			},
		}

		err := rewriteRawJSONL(path, original)
		require.NoError(t, err)

		// read back with the canonical reader
		readBack, err := session.ReadSessionFromPath(path)
		require.NoError(t, err)

		// verify header metadata survived
		require.NotNil(t, readBack.Meta)
		assert.Equal(t, "1", readBack.Meta.Version)
		assert.Equal(t, "agent-123", readBack.Meta.AgentID)
		assert.Equal(t, "claude-code", readBack.Meta.AgentType)
		assert.Equal(t, "person-a", readBack.Meta.Username)
		assert.Equal(t, "0.9.0", readBack.Meta.OxVersion)

		// verify entries survived
		require.Len(t, readBack.Entries, 2)
		assert.Equal(t, "user", readBack.Entries[0]["type"])
		assert.Equal(t, "what is 2+2?", readBack.Entries[0]["content"])
		assert.Equal(t, "assistant", readBack.Entries[1]["type"])
		assert.Equal(t, "4", readBack.Entries[1]["content"])

		// verify footer survived
		require.NotNil(t, readBack.Footer)
		assert.Equal(t, "footer", readBack.Footer["type"])
		assert.Equal(t, "completed", readBack.Footer["status"])
	})

	t.Run("no header when Meta is nil", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "raw.jsonl")

		sess := &session.StoredSession{
			Entries: []map[string]any{
				{"type": "user", "content": "hello"},
			},
		}

		err := rewriteRawJSONL(path, sess)
		require.NoError(t, err)

		readBack, err := session.ReadSessionFromPath(path)
		require.NoError(t, err)
		require.Len(t, readBack.Entries, 1)
		assert.Equal(t, "hello", readBack.Entries[0]["content"])
	})

	t.Run("no footer when Footer is nil", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "raw.jsonl")

		sess := &session.StoredSession{
			Meta: &session.StoreMeta{Version: "1"},
			Entries: []map[string]any{
				{"type": "user", "content": "test"},
			},
		}

		err := rewriteRawJSONL(path, sess)
		require.NoError(t, err)

		readBack, err := session.ReadSessionFromPath(path)
		require.NoError(t, err)
		assert.Nil(t, readBack.Footer)
	})

	t.Run("atomic write cleans up temp file on success", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "raw.jsonl")

		sess := &session.StoredSession{
			Entries: []map[string]any{
				{"type": "user", "content": "hello"},
			},
		}

		err := rewriteRawJSONL(path, sess)
		require.NoError(t, err)

		// the .tmp file should not exist after successful write
		_, err = os.Stat(path + ".tmp")
		assert.True(t, os.IsNotExist(err), "temp file should be cleaned up after rename")

		// the target file should exist
		_, err = os.Stat(path)
		assert.NoError(t, err, "target file should exist")
	})

	t.Run("empty entries produces valid file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "raw.jsonl")

		sess := &session.StoredSession{
			Meta:    &session.StoreMeta{Version: "1"},
			Entries: []map[string]any{},
		}

		err := rewriteRawJSONL(path, sess)
		require.NoError(t, err)

		readBack, err := session.ReadSessionFromPath(path)
		require.NoError(t, err)
		assert.Empty(t, readBack.Entries)
	})
}

func TestRedactSummaryJSON(t *testing.T) {
	// helper to write a summary.json file
	writeSummary := func(t *testing.T, dir string, summary session.SummarizeResponse) string {
		t.Helper()
		path := filepath.Join(dir, "summary.json")
		data, err := json.MarshalIndent(summary, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, data, 0644))
		return path
	}

	// helper to read summary.json back
	readSummary := func(t *testing.T, path string) session.SummarizeResponse {
		t.Helper()
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		var s session.SummarizeResponse
		require.NoError(t, json.Unmarshal(data, &s))
		return s
	}

	t.Run("redacts all text fields containing secrets", func(t *testing.T) {
		dir := t.TempDir()
		// use a known AWS key pattern that the built-in redactor catches
		fakeKey := "AKIAIOSFODNN7EXAMPLE"

		summary := session.SummarizeResponse{
			Title:       "Session with key " + fakeKey,
			Summary:     "We used " + fakeKey + " for auth",
			Outcome:     "Deployed with " + fakeKey,
			KeyActions:  []string{"Set up " + fakeKey, "Clean action"},
			TopicsFound: []string{fakeKey + " rotation", "No secret here"},
			AhaMoments: []session.AhaMoment{
				{
					Seq:       1,
					Role:      "assistant",
					Type:      "discovery",
					Highlight: "Found key " + fakeKey,
					Why:       "Key " + fakeKey + " was exposed",
				},
			},
		}
		path := writeSummary(t, dir, summary)

		redactor := session.NewRedactor()
		err := redactSummaryJSON(path, redactor)
		require.NoError(t, err)

		result := readSummary(t, path)

		// the fake AWS key should be replaced in all fields
		assert.NotContains(t, result.Title, fakeKey)
		assert.NotContains(t, result.Summary, fakeKey)
		assert.NotContains(t, result.Outcome, fakeKey)
		assert.NotContains(t, result.KeyActions[0], fakeKey)
		assert.Equal(t, "Clean action", result.KeyActions[1], "non-secret action unchanged")
		assert.NotContains(t, result.TopicsFound[0], fakeKey)
		assert.Equal(t, "No secret here", result.TopicsFound[1], "non-secret topic unchanged")
		assert.NotContains(t, result.AhaMoments[0].Highlight, fakeKey)
		assert.NotContains(t, result.AhaMoments[0].Why, fakeKey)
	})

	t.Run("no change when no secrets found", func(t *testing.T) {
		dir := t.TempDir()
		summary := session.SummarizeResponse{
			Title:       "Clean session",
			Summary:     "Nothing secret here",
			Outcome:     "success",
			KeyActions:  []string{"wrote tests"},
			TopicsFound: []string{"testing"},
		}
		path := writeSummary(t, dir, summary)

		// capture original mod time
		info1, err := os.Stat(path)
		require.NoError(t, err)

		redactor := session.NewRedactor()
		err = redactSummaryJSON(path, redactor)
		require.NoError(t, err)

		// file should not have been rewritten (no temp file dance)
		info2, err := os.Stat(path)
		require.NoError(t, err)
		// content should be identical
		result := readSummary(t, path)
		assert.Equal(t, "Clean session", result.Title)
		assert.Equal(t, "Nothing secret here", result.Summary)

		// mod time unchanged indicates no rewrite occurred
		assert.Equal(t, info1.ModTime(), info2.ModTime(), "file should not be rewritten when no secrets found")
	})

	t.Run("handles missing file gracefully", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "nonexistent.json")

		redactor := session.NewRedactor()
		err := redactSummaryJSON(path, redactor)
		assert.Error(t, err)
	})

	t.Run("handles invalid JSON gracefully", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "summary.json")
		require.NoError(t, os.WriteFile(path, []byte("{invalid json"), 0644))

		redactor := session.NewRedactor()
		err := redactSummaryJSON(path, redactor)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "parse summary.json")
	})

	t.Run("redacts FinalPlan field", func(t *testing.T) {
		dir := t.TempDir()
		fakeKey := "AKIAIOSFODNN7EXAMPLE"
		summary := session.SummarizeResponse{
			Title:     "Plan session",
			FinalPlan: "Deploy using " + fakeKey,
		}
		path := writeSummary(t, dir, summary)

		redactor := session.NewRedactor()
		err := redactSummaryJSON(path, redactor)
		require.NoError(t, err)

		result := readSummary(t, path)
		assert.NotContains(t, result.FinalPlan, fakeKey)
	})

	t.Run("atomic write cleans up temp file", func(t *testing.T) {
		dir := t.TempDir()
		fakeKey := "AKIAIOSFODNN7EXAMPLE"
		summary := session.SummarizeResponse{
			Title: "Has secret " + fakeKey,
		}
		path := writeSummary(t, dir, summary)

		redactor := session.NewRedactor()
		err := redactSummaryJSON(path, redactor)
		require.NoError(t, err)

		_, err = os.Stat(path + ".tmp")
		assert.True(t, os.IsNotExist(err), "temp file should not remain after successful write")
	})
}

func TestRegenerateArtifacts(t *testing.T) {
	t.Run("creates expected output files", func(t *testing.T) {
		sessionPath := t.TempDir()
		now := time.Now().Truncate(time.Second).UTC()

		rawSession := &session.StoredSession{
			Meta: &session.StoreMeta{
				Version:   "1",
				CreatedAt: now,
				AgentType: "claude-code",
				Username:  "person-a",
			},
			Entries: []map[string]any{
				{"type": "user", "content": "Please write a function to add two numbers."},
				{"type": "assistant", "content": "Here is the function:\n```go\nfunc add(a, b int) int { return a + b }\n```"},
				{"type": "user", "content": "Looks good, thanks!"},
				{"type": "assistant", "content": "Happy to help!"},
			},
		}

		err := regenerateArtifacts(sessionPath, rawSession)
		require.NoError(t, err)

		// events.jsonl should be created
		eventsPath := filepath.Join(sessionPath, "events.jsonl")
		_, err = os.Stat(eventsPath)
		assert.NoError(t, err, "events.jsonl should exist")
		eventsData, err := os.ReadFile(eventsPath)
		require.NoError(t, err)
		assert.NotEmpty(t, eventsData, "events.jsonl should have content")

		// session.html should be created
		htmlPath := filepath.Join(sessionPath, "session.html")
		_, err = os.Stat(htmlPath)
		assert.NoError(t, err, "session.html should exist")
		htmlData, err := os.ReadFile(htmlPath)
		require.NoError(t, err)
		assert.Contains(t, string(htmlData), "<!DOCTYPE html", "session.html should contain HTML")

		// session.md should be created
		mdPath := filepath.Join(sessionPath, "session.md")
		_, err = os.Stat(mdPath)
		assert.NoError(t, err, "session.md should exist")
		mdData, err := os.ReadFile(mdPath)
		require.NoError(t, err)
		assert.NotEmpty(t, mdData, "session.md should have content")
	})

	t.Run("generates summary.md when summary.json exists", func(t *testing.T) {
		sessionPath := t.TempDir()
		now := time.Now().Truncate(time.Second).UTC()

		rawSession := &session.StoredSession{
			Meta: &session.StoreMeta{
				Version:   "1",
				CreatedAt: now,
				AgentType: "claude-code",
			},
			Entries: []map[string]any{
				{"type": "user", "content": "Do something"},
				{"type": "assistant", "content": "Done"},
			},
		}

		// write a summary.json so that summary.md generation triggers
		summaryResp := session.SummarizeResponse{
			Title:       "Test session",
			Summary:     "A test session about doing something.",
			KeyActions:  []string{"Did something"},
			Outcome:     "success",
			TopicsFound: []string{"testing"},
		}
		summaryData, err := json.MarshalIndent(summaryResp, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(sessionPath, "summary.json"), summaryData, 0644))

		err = regenerateArtifacts(sessionPath, rawSession)
		require.NoError(t, err)

		summaryMdPath := filepath.Join(sessionPath, "summary.md")
		_, err = os.Stat(summaryMdPath)
		assert.NoError(t, err, "summary.md should exist when summary.json is present")
		summaryMd, err := os.ReadFile(summaryMdPath)
		require.NoError(t, err)
		assert.NotEmpty(t, summaryMd)
	})

	t.Run("no summary.md when summary.json absent", func(t *testing.T) {
		sessionPath := t.TempDir()

		rawSession := &session.StoredSession{
			Meta: &session.StoreMeta{
				Version: "1",
			},
			Entries: []map[string]any{
				{"type": "user", "content": "hello"},
			},
		}

		err := regenerateArtifacts(sessionPath, rawSession)
		require.NoError(t, err)

		summaryMdPath := filepath.Join(sessionPath, "summary.md")
		_, err = os.Stat(summaryMdPath)
		assert.True(t, os.IsNotExist(err), "summary.md should not exist without summary.json")
	})

	t.Run("handles empty entries without error", func(t *testing.T) {
		sessionPath := t.TempDir()

		rawSession := &session.StoredSession{
			Meta: &session.StoreMeta{
				Version: "1",
			},
			Entries: []map[string]any{},
		}

		err := regenerateArtifacts(sessionPath, rawSession)
		require.NoError(t, err)
	})
}

func TestSessionRegenerateRedactFlagValidation(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		flags     map[string]string
		expectErr string
	}{
		{
			name:      "cannot use both session name and --all with --redact",
			args:      []string{"some-session"},
			flags:     map[string]string{"redact": "true", "all": "true"},
			expectErr: "cannot use both a session name and --all",
		},
		{
			name:      "must provide session name or --all with --redact",
			args:      []string{},
			flags:     map[string]string{"redact": "true"},
			expectErr: "provide a session name or use --all",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := sessionRegenerateCmd

			// reset flags to defaults before each test
			cmd.Flags().Set("redact", "false")
			cmd.Flags().Set("all", "false")
			cmd.Flags().Set("dry-run", "false")
			cmd.Flags().Set("force", "false")

			for k, v := range tc.flags {
				require.NoError(t, cmd.Flags().Set(k, v))
			}

			err := cmd.RunE(cmd, tc.args)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.expectErr)
		})
	}
}
