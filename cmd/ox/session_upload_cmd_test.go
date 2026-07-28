package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/sessionid"
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

// writeUploadTestRawHeader writes a minimal raw.jsonl carrying the ox
// header format, optionally with a session_id, for buildSessionMeta tests.
func writeUploadTestRawHeader(t *testing.T, rawPath, agentID, sessionID string) {
	t.Helper()
	metadata := map[string]any{
		"version":    "1.0",
		"agent_id":   agentID,
		"agent_type": "claude-code",
	}
	if sessionID != "" {
		metadata["session_id"] = sessionID
	}
	header := map[string]any{"type": "header", "metadata": metadata}
	data, err := json.Marshal(header)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(rawPath, append(data, '\n'), 0644))
}

// TestBuildSessionMeta_HeaderIDPreservedWhenNoMetaJSON is the manual-upload
// counterpart of the ox-5n8e fix: when meta.json is genuinely absent (a
// session dir with only raw.jsonl, e.g. copied in by hand or left behind by
// a partial upload), the SessionID already carried in the raw.jsonl header
// must be reused, not discarded in favor of a fresh mint.
func TestBuildSessionMeta_HeaderIDPreservedWhenNoMetaJSON(t *testing.T) {
	sessionPath := t.TempDir()
	const headerID = "ses_019f633e-29f3-7566-9ab4-a3da5b666fe5"
	writeUploadTestRawHeader(t, filepath.Join(sessionPath, ledgerFileRaw), "OxUP01", headerID)

	meta, err := buildSessionMeta(sessionPath, "2026-05-08T19-25-ajit-OxUP01", "", nil)
	require.NoError(t, err)
	assert.Equal(t, headerID, meta.SessionID, "must preserve the header-carried SessionID, not mint a new one")
}

// TestBuildSessionMeta_CorruptMetaJSONRefusesToMint mirrors the
// unreadable-events-never-mints-new-id shape from the plan backfill
// hardening (PR #723): an existing-but-corrupt meta.json must abort with an
// error rather than silently falling through to "construct from directory
// name", which would mint a fresh SessionID and rotate away from whatever
// identity the unreadable file held.
func TestBuildSessionMeta_CorruptMetaJSONRefusesToMint(t *testing.T) {
	sessionPath := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, "meta.json"), []byte("{not valid json"), 0644))
	writeUploadTestRawHeader(t, filepath.Join(sessionPath, ledgerFileRaw), "OxBAD2", "ses_019f633e-29f3-7566-9ab4-a3da5b666fe5")

	meta, err := buildSessionMeta(sessionPath, "corrupt-meta-session", "", nil)
	require.Error(t, err, "corrupt meta.json must refuse to build a fresh one")
	assert.Nil(t, meta)
}

// TestBuildSessionMeta_NoIDAnywhereMintsExactlyOne covers the genuinely
// first-publish case: no meta.json and no header SessionID (legacy
// recording). Minting is the only correct behavior, and it must produce a
// valid ID.
func TestBuildSessionMeta_NoIDAnywhereMintsExactlyOne(t *testing.T) {
	sessionPath := t.TempDir()
	writeUploadTestRawHeader(t, filepath.Join(sessionPath, ledgerFileRaw), "OxLEG2", "")

	meta, err := buildSessionMeta(sessionPath, "legacy-manual-session", "", nil)
	require.NoError(t, err)
	assert.True(t, sessionid.IsValidSessionID(meta.SessionID), "must mint a valid ses_<UUIDv7> when no ID exists anywhere, got %q", meta.SessionID)
}
