package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSessionDirName(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantTime  time.Time
		wantUser  string
		wantAgent string
	}{
		{
			name:      "standard format",
			input:     "2026-01-16T10-30-testuser-OxTest",
			wantTime:  time.Date(2026, 1, 16, 10, 30, 0, 0, time.UTC),
			wantUser:  "testuser",
			wantAgent: "OxTest",
		},
		{
			name:      "email-like username with dots",
			input:     "2026-02-05T14-00-ryan.smith-Ox7f3a",
			wantTime:  time.Date(2026, 2, 5, 14, 0, 0, 0, time.UTC),
			wantUser:  "ryan.smith",
			wantAgent: "Ox7f3a",
		},
		{
			name:      "username with hyphens",
			input:     "2026-01-06T14-32-some-user-name-OxAbCd",
			wantTime:  time.Date(2026, 1, 6, 14, 32, 0, 0, time.UTC),
			wantUser:  "some-user-name",
			wantAgent: "OxAbCd",
		},
		{
			name:      "no recognizable timestamp",
			input:     "random-session-name-OxTest",
			wantTime:  time.Time{},
			wantUser:  "",
			wantAgent: "OxTest",
		},
		{
			name:      "just agent ID",
			input:     "OxTest",
			wantTime:  time.Time{},
			wantUser:  "",
			wantAgent: "OxTest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTime, gotUser, gotAgent := parseSessionDirName(tt.input)
			assert.Equal(t, tt.wantTime, gotTime, "timestamp")
			assert.Equal(t, tt.wantUser, gotUser, "username")
			assert.Equal(t, tt.wantAgent, gotAgent, "agentID")
		})
	}
}

func TestHasContentFiles(t *testing.T) {
	t.Run("returns false for empty dir", func(t *testing.T) {
		dir := t.TempDir()
		assert.False(t, hasContentFiles(dir))
	})

	t.Run("returns true when raw.jsonl exists", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, ledgerFileRaw), []byte("{}"), 0644))
		assert.True(t, hasContentFiles(dir))
	})

	t.Run("returns true when summary.md exists", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, ledgerFileSummaryMD), []byte("# Summary"), 0644))
		assert.True(t, hasContentFiles(dir))
	})
}

func TestCountJSONLLines(t *testing.T) {
	t.Run("returns 0 for nonexistent file", func(t *testing.T) {
		assert.Equal(t, 0, countJSONLLines("/nonexistent/path"))
	})

	t.Run("counts lines correctly", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.jsonl")
		content := "{\"seq\":1}\n{\"seq\":2}\n{\"seq\":3}\n"
		require.NoError(t, os.WriteFile(path, []byte(content), 0644))
		assert.Equal(t, 3, countJSONLLines(path))
	})
}

func TestFirstNonEmpty(t *testing.T) {
	assert.Equal(t, "a", firstNonEmpty("a", "b"))
	assert.Equal(t, "b", firstNonEmpty("", "b", "c"))
	assert.Equal(t, "", firstNonEmpty("", ""))
}
