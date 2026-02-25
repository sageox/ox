package lfs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteAndReadSessionMeta(t *testing.T) {
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, "2026-01-06T14-32-ryan-Ox7f3a")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	meta := &SessionMeta{
		Version:     "1.0",
		SessionName: "2026-01-06T14-32-ryan-Ox7f3a",
		Username:    "ryan@sageox.ai",
		AgentID:     "Ox7f3a",
		AgentType:   "claude-code",
		Model:       "claude-sonnet-4",
		CreatedAt:   time.Date(2026, 1, 6, 14, 32, 0, 0, time.UTC),
		EntryCount:  42,
		Summary:     "Implemented LFS pipeline",
		Files: map[string]FileRef{
			"raw.jsonl":    {OID: "sha256:abc123", Size: 1024},
			"events.jsonl": {OID: "sha256:def456", Size: 512},
			"summary.md":   {OID: "sha256:ghi789", Size: 256},
			"session.md":   {OID: "sha256:jkl012", Size: 2048},
			"session.html": {OID: "sha256:mno345", Size: 4096},
		},
	}

	err := WriteSessionMeta(sessionDir, meta)
	require.NoError(t, err)

	// verify file exists
	_, err = os.Stat(filepath.Join(sessionDir, "meta.json"))
	require.NoError(t, err)

	// read back
	got, err := ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, meta.Version, got.Version)
	assert.Equal(t, meta.SessionName, got.SessionName)
	assert.Equal(t, meta.Username, got.Username)
	assert.Equal(t, meta.AgentID, got.AgentID)
	assert.Equal(t, meta.AgentType, got.AgentType)
	assert.Equal(t, meta.Model, got.Model)
	assert.Equal(t, meta.EntryCount, got.EntryCount)
	assert.Equal(t, meta.Summary, got.Summary)
	assert.Len(t, got.Files, 5)
	assert.Equal(t, "sha256:abc123", got.Files["raw.jsonl"].OID)
	assert.Equal(t, int64(1024), got.Files["raw.jsonl"].Size)
}

func TestWriteSessionMeta_Nil(t *testing.T) {
	err := WriteSessionMeta(t.TempDir(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil session meta")
}

func TestReadSessionMeta_NotFound(t *testing.T) {
	_, err := ReadSessionMeta(t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "meta.json not found")
}

func TestCheckHydrationStatus_Hydrated(t *testing.T) {
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, "session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	// create content files
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte("data"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "events.jsonl"), []byte("data"), 0644))

	meta := &SessionMeta{
		Files: map[string]FileRef{
			"raw.jsonl":    {OID: "sha256:abc", Size: 4},
			"events.jsonl": {OID: "sha256:def", Size: 4},
		},
	}

	status := CheckHydrationStatus(sessionDir, meta)
	assert.Equal(t, HydrationStatusHydrated, status)
}

func TestCheckHydrationStatus_Dehydrated(t *testing.T) {
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, "session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	meta := &SessionMeta{
		Files: map[string]FileRef{
			"raw.jsonl":    {OID: "sha256:abc", Size: 100},
			"events.jsonl": {OID: "sha256:def", Size: 200},
		},
	}

	status := CheckHydrationStatus(sessionDir, meta)
	assert.Equal(t, HydrationStatusDehydrated, status)
}

func TestCheckHydrationStatus_Partial(t *testing.T) {
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, "session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	// only create one of two files
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte("data"), 0644))

	meta := &SessionMeta{
		Files: map[string]FileRef{
			"raw.jsonl":    {OID: "sha256:abc", Size: 4},
			"events.jsonl": {OID: "sha256:def", Size: 200},
		},
	}

	status := CheckHydrationStatus(sessionDir, meta)
	assert.Equal(t, HydrationStatusPartial, status)
}

func TestCheckHydrationStatus_NilMeta(t *testing.T) {
	status := CheckHydrationStatus(t.TempDir(), nil)
	assert.Equal(t, HydrationStatusDehydrated, status)
}

func TestCheckHydrationStatus_EmptyFiles(t *testing.T) {
	meta := &SessionMeta{
		Files: map[string]FileRef{},
	}
	status := CheckHydrationStatus(t.TempDir(), meta)
	assert.Equal(t, HydrationStatusDehydrated, status)
}

func TestNewFileRef(t *testing.T) {
	content := []byte("test content")
	ref := NewFileRef(content)
	assert.Equal(t, int64(len(content)), ref.Size)
	assert.True(t, len(ref.OID) > 7)
	assert.Equal(t, "sha256:", ref.OID[:7])
}

func TestNewSessionMeta_Builder(t *testing.T) {
	ts := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	meta := NewSessionMeta("session-1", "user@test.com", "Ox1234", "claude-code", ts).
		Model("claude-sonnet-4").
		Title("Test session").
		Summary("Did some work").
		EntryCount(10).
		UserID("usr_abc123").
		RepoID("repo_xyz789").
		Build()

	assert.Equal(t, "1.0", meta.Version)
	assert.Equal(t, "session-1", meta.SessionName)
	assert.Equal(t, "user@test.com", meta.Username)
	assert.Equal(t, "Ox1234", meta.AgentID)
	assert.Equal(t, "claude-code", meta.AgentType)
	assert.Equal(t, ts, meta.CreatedAt)
	assert.Equal(t, "claude-sonnet-4", meta.Model)
	assert.Equal(t, "Test session", meta.Title)
	assert.Equal(t, "Did some work", meta.Summary)
	assert.Equal(t, 10, meta.EntryCount)
	assert.Equal(t, "usr_abc123", meta.UserID)
	assert.Equal(t, "repo_xyz789", meta.RepoID)
	assert.NotNil(t, meta.Files)
	assert.Empty(t, meta.Files)
}

func TestNewSessionMeta_DefaultFiles(t *testing.T) {
	meta := NewSessionMeta("s", "u", "a", "t", time.Now()).Build()
	assert.NotNil(t, meta.Files)
	assert.Empty(t, meta.Files)
}

func TestNewSessionMeta_WithFiles(t *testing.T) {
	files := map[string]FileRef{
		"raw.jsonl": {OID: "sha256:abc", Size: 100},
	}
	meta := NewSessionMeta("s", "u", "a", "t", time.Now()).
		WithFiles(files).
		Build()
	assert.Equal(t, files, meta.Files)
}

func TestNewSessionMeta_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, "test-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	meta := NewSessionMeta("test-session", "user@test.com", "Ox1234", "claude-code",
		time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)).
		Model("claude-sonnet-4").
		UserID("usr_abc").
		RepoID("repo_xyz").
		WithFiles(map[string]FileRef{
			"raw.jsonl": {OID: "sha256:abc123", Size: 1024},
		}).
		Build()

	err := WriteSessionMeta(sessionDir, meta)
	require.NoError(t, err)

	got, err := ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, meta.UserID, got.UserID)
	assert.Equal(t, meta.RepoID, got.RepoID)
	assert.Equal(t, meta.Model, got.Model)
	assert.Equal(t, "sha256:abc123", got.Files["raw.jsonl"].OID)
}

func TestUpdateMetaSummary(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T, dir string)
		summary     string
		wantErr     bool
		wantErrMsg  string
		wantSummary string
		wantSession string
		wantUser    string
	}{
		{
			name: "updates summary and preserves other fields",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				meta := NewSessionMeta("test-session", "user@test.com", "Ox1234", "claude-code",
					time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)).
					Summary("/ox-session-start\n\n5 user messages, 10 assistant responses").
					Build()
				require.NoError(t, WriteSessionMeta(dir, meta))
			},
			summary:     "Implemented auth system with OAuth2 flow",
			wantSummary: "Implemented auth system with OAuth2 flow",
			wantSession: "test-session",
			wantUser:    "user@test.com",
		},
		{
			name:       "returns error when meta.json missing",
			setup:      func(t *testing.T, dir string) { t.Helper() },
			summary:    "some summary",
			wantErr:    true,
			wantErrMsg: "meta.json not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "session")
			require.NoError(t, os.MkdirAll(dir, 0755))
			tt.setup(t, dir)

			err := UpdateMetaSummary(dir, tt.summary)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrMsg)
				return
			}
			require.NoError(t, err)

			got, err := ReadSessionMeta(dir)
			require.NoError(t, err)
			assert.Equal(t, tt.wantSummary, got.Summary)
			assert.Equal(t, tt.wantSession, got.SessionName)
			assert.Equal(t, tt.wantUser, got.Username)
		})
	}
}

func TestFileRef_BareOID(t *testing.T) {
	tests := []struct {
		oid      string
		expected string
	}{
		{"sha256:abc123", "abc123"},
		{"abc123", "abc123"},
		{"sha256:", "sha256:"},
		{"short", "short"},
	}

	for _, tt := range tests {
		ref := FileRef{OID: tt.oid}
		assert.Equal(t, tt.expected, ref.BareOID(), "BareOID for %q", tt.oid)
	}
}
