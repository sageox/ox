package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewStore_EmptyPath(t *testing.T) {
	_, err := NewStore("")
	assert.Error(t, err)
}

func TestNewStore_CreatesDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	contextPath := filepath.Join(tmpDir, "context", "test-repo")

	store, err := NewStore(contextPath)
	require.NoError(t, err)

	// verify sessions base directory exists
	sessionsPath := filepath.Join(contextPath, "sessions")
	_, err = os.Stat(sessionsPath)
	assert.False(t, os.IsNotExist(err), "expected sessions directory to exist: %s", sessionsPath)

	assert.Equal(t, filepath.Join(contextPath, "sessions"), store.basePath)
}

func TestGenerateSessionName(t *testing.T) {
	tests := []struct {
		name      string
		sessionID string
		username  string
		contains  []string
	}{
		{
			name:      "normal inputs",
			sessionID: "Oxa7b3",
			username:  "testuser",
			contains:  []string{"testuser", "Oxa7b3"},
		},
		{
			name:      "empty username",
			sessionID: "Oxa7b3",
			username:  "",
			contains:  []string{"anonymous", "Oxa7b3"},
		},
		{
			name:      "empty sessionID",
			sessionID: "",
			username:  "testuser",
			contains:  []string{"testuser", "unknown"},
		},
		{
			name:      "username with special chars",
			sessionID: "Oxa7b3",
			username:  "test/user:name",
			contains:  []string{"test-user-name", "Oxa7b3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionName := GenerateSessionName(tt.sessionID, tt.username)

			for _, want := range tt.contains {
				assert.Contains(t, sessionName, want)
			}

			// verify timestamp format at start
			require.GreaterOrEqual(t, len(sessionName), 16, "session name too short: %s", sessionName)
			timestampPart := sessionName[:16]
			_, err := time.Parse("2006-01-02T15-04", timestampPart)
			assert.NoError(t, err, "invalid timestamp format in session name: %s", timestampPart)

			// should NOT have .jsonl extension
			assert.False(t, strings.HasSuffix(sessionName, ".jsonl"), "session name should not have .jsonl extension: %s", sessionName)
		})
	}
}

func TestGenerateFilename(t *testing.T) {
	tests := []struct {
		name     string
		username string
		agentID  string
		contains []string
	}{
		{
			name:     "normal inputs",
			username: "testuser",
			agentID:  "Oxa7b3",
			contains: []string{"testuser", "Oxa7b3", ".jsonl"},
		},
		{
			name:     "empty username",
			username: "",
			agentID:  "Oxa7b3",
			contains: []string{"anonymous", "Oxa7b3", ".jsonl"},
		},
		{
			name:     "empty agentID",
			username: "testuser",
			agentID:  "",
			contains: []string{"testuser", "unknown", ".jsonl"},
		},
		{
			name:     "username with special chars",
			username: "test/user:name",
			agentID:  "Oxa7b3",
			contains: []string{"test-user-name", "Oxa7b3", ".jsonl"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filename := GenerateFilename(tt.username, tt.agentID)

			for _, want := range tt.contains {
				assert.Contains(t, filename, want)
			}

			// verify timestamp format at start
			require.GreaterOrEqual(t, len(filename), 16, "filename too short: %s", filename)
			timestampPart := filename[:16]
			_, err := time.Parse("2006-01-02T15-04", timestampPart)
			assert.NoError(t, err, "invalid timestamp format in filename: %s", timestampPart)
		})
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"normal", "normal"},
		{"with/slash", "with-slash"},
		{"with:colon", "with-colon"},
		{"with*star", "with-star"},
		{"multiple///slashes", "multiple-slashes"},
		{"---leading", "leading"},
		{"trailing---", "trailing"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeFilename(tt.input)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestStore_CreateRaw_SessionFolder(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	require.NoError(t, err, "failed to create store")

	sessionName := "2026-01-06T14-32-testuser-Oxa7b3"
	writer, err := store.CreateRaw(sessionName)
	require.NoError(t, err, "failed to create raw session")
	defer writer.Close()

	expectedPath := filepath.Join(tmpDir, "sessions", sessionName, "raw.jsonl")
	assert.Equal(t, expectedPath, writer.FilePath())
	assert.Equal(t, sessionName, writer.SessionName())
}

func TestStore_CreateEvents_SessionFolder(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	require.NoError(t, err, "failed to create store")

	sessionName := "2026-01-06T14-32-testuser-Oxa7b3"
	writer, err := store.CreateEvents(sessionName)
	require.NoError(t, err, "failed to create events session")
	defer writer.Close()

	expectedPath := filepath.Join(tmpDir, "sessions", sessionName, "events.jsonl")
	assert.Equal(t, expectedPath, writer.FilePath())
	assert.Equal(t, sessionName, writer.SessionName())
}

func TestStore_CreateRaw_StripsJsonlExtension(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	// pass a name with .jsonl extension (legacy compatibility)
	writer, err := store.CreateRaw("2026-01-06T14-32-testuser-Oxa7b3.jsonl")
	require.NoError(t, err, "failed to create session")
	defer writer.Close()

	// should strip extension and use as session folder name
	expectedSessionName := "2026-01-06T14-32-testuser-Oxa7b3"
	assert.Equal(t, expectedSessionName, writer.SessionName())
}

func TestStore_CreateSession_EmptyFilename(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	_, err := store.CreateRaw("")
	assert.Error(t, err)
}

// testWritable implements Writable interface for testing
type testWritable struct {
	Message string `json:"message"`
}

func (e *testWritable) EntryType() string {
	return "test"
}

func TestSessionWriter_WriteHeader(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	writer, _ := store.CreateRaw("test-session")

	meta := &StoreMeta{
		Version:   "1.0",
		CreatedAt: time.Now(),
		AgentID:   "Oxa7b3",
		AgentType: "claude-code",
		Username:  "testuser",
	}

	err := writer.WriteHeader(meta)
	require.NoError(t, err, "failed to write header")
	writer.Close()

	// read and verify
	content, _ := os.ReadFile(writer.FilePath())
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")

	require.GreaterOrEqual(t, len(lines), 1, "expected at least 1 line (header)")

	var header map[string]any
	err = json.Unmarshal([]byte(lines[0]), &header)
	require.NoError(t, err, "failed to parse header")

	assert.Equal(t, "header", header["type"])
}

func TestSessionWriter_WriteEntry(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	writer, _ := store.CreateRaw("test-session")

	entry := &testWritable{Message: "hello world"}
	err := writer.WriteEntry(entry)
	require.NoError(t, err, "failed to write entry")

	assert.Equal(t, 1, writer.Count())

	writer.Close()

	// read and verify
	content, _ := os.ReadFile(writer.FilePath())
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")

	// should have entry + footer
	require.GreaterOrEqual(t, len(lines), 2, "expected at least 2 lines")

	var record map[string]any
	err = json.Unmarshal([]byte(lines[0]), &record)
	require.NoError(t, err, "failed to parse entry")

	assert.Equal(t, "test", record["type"])
}

func TestSessionWriter_WriteEntry_NilEntry(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	writer, _ := store.CreateRaw("test-session")
	defer writer.Close()

	err := writer.WriteEntry(nil)
	assert.Error(t, err)
}

func TestSessionWriter_WriteRaw(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	writer, _ := store.CreateRaw("test-session")

	data := map[string]any{
		"type":    "custom",
		"payload": "test data",
	}

	err := writer.WriteRaw(data)
	require.NoError(t, err, "failed to write raw")

	assert.Equal(t, 1, writer.Count())

	writer.Close()
}

func TestSessionWriter_Close_WritesFooter(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	writer, _ := store.CreateRaw("test-session")
	writer.WriteEntry(&testWritable{Message: "test"})
	writer.WriteEntry(&testWritable{Message: "test2"})
	err := writer.Close()
	require.NoError(t, err, "failed to close writer")

	// read and verify footer
	content, _ := os.ReadFile(writer.FilePath())
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")

	require.GreaterOrEqual(t, len(lines), 1, "expected at least 1 line")

	lastLine := lines[len(lines)-1]
	var footer map[string]any
	err = json.Unmarshal([]byte(lastLine), &footer)
	require.NoError(t, err, "failed to parse footer")

	assert.Equal(t, "footer", footer["type"])
	assert.Equal(t, float64(2), footer["entry_count"])
}

func TestStore_ListSessions_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	sessions, err := store.ListAllSessions()
	require.NoError(t, err, "failed to list sessions")

	assert.Empty(t, sessions)
}

func TestStore_ListSessions_SessionFolders(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	// create session folder entries
	sessionNames := []string{
		"2026-01-05T10-30-user1-Oxa7b3",
		"2026-01-05T11-30-user2-Oxb8c4",
	}

	for _, name := range sessionNames {
		writer, _ := store.CreateRaw(name)
		writer.WriteEntry(&testWritable{Message: "test"})
		writer.Close()
	}

	sessions, err := store.ListAllSessions()
	require.NoError(t, err, "failed to list sessions")

	assert.Len(t, sessions, 2)

	// verify session names are set
	for _, s := range sessions {
		assert.NotEmpty(t, s.SessionName, "expected session name to be set")
	}
}

func TestStore_ListSessions_MultipleSessions(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	// create two session folder entries
	writer1, _ := store.CreateRaw("2026-01-05T11-30-user1-Oxa7b3")
	writer1.WriteEntry(&testWritable{Message: "session1"})
	writer1.Close()

	writer2, _ := store.CreateRaw("2026-01-05T10-30-user2-Oxb8c4")
	writer2.WriteEntry(&testWritable{Message: "session2"})
	writer2.Close()

	sessions, err := store.ListAllSessions()
	require.NoError(t, err, "failed to list sessions")

	assert.Len(t, sessions, 2)

	for _, s := range sessions {
		assert.NotEmpty(t, s.SessionName, "expected session name to be set")
	}
}

func TestStore_ListSessions(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	// create session folder entries
	sessionNames := []string{
		"2026-01-05T10-30-user1-Oxa7b3",
		"2026-01-05T11-30-user2-Oxb8c4",
		"2026-01-05T09-00-user3-Oxc9d5",
	}

	for _, name := range sessionNames {
		writer, _ := store.CreateRaw(name)
		writer.Close()
	}

	listedSessions, err := store.ListSessionNames()
	require.NoError(t, err, "failed to list sessions")

	assert.Len(t, listedSessions, 3)

	// verify sorted by timestamp descending (newest first)
	// 11:30 > 10:30 > 09:00
	assert.Equal(t, "2026-01-05T11-30-user2-Oxb8c4", listedSessions[0], "expected newest session first")
}

func TestStore_GetSessionPath(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	sessionName := "2026-01-05T10-30-user1-Oxa7b3"
	expected := filepath.Join(tmpDir, "sessions", sessionName)

	got := store.GetSessionPath(sessionName)
	assert.Equal(t, expected, got)
}

func TestStore_ReadSession_SessionFolder(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	sessionName := "2026-01-05T10-30-user1-Oxa7b3"

	// create and write a session
	writer, _ := store.CreateRaw(sessionName)
	writer.WriteHeader(&StoreMeta{
		Version:   "1.0",
		AgentID:   "Oxa7b3",
		AgentType: "claude-code",
	})
	writer.WriteEntry(&testWritable{Message: "hello"})
	writer.WriteEntry(&testWritable{Message: "world"})
	writer.Close()

	// read it back by session name
	sess, err := store.ReadSession(sessionName)
	require.NoError(t, err, "failed to read session")

	assert.Equal(t, sessionName, sess.Info.SessionName)
	assert.Equal(t, "raw", sess.Info.Type)

	require.NotNil(t, sess.Meta, "expected metadata to be present")
	assert.Equal(t, "Oxa7b3", sess.Meta.AgentID)

	assert.Len(t, sess.Entries, 2)
	assert.NotNil(t, sess.Footer, "expected footer to be present")
}

func TestStore_ReadSession_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	_, err := store.ReadSession("nonexistent")
	assert.Error(t, err)
}

func TestStore_ReadSessionRaw(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	sessionName := "test-session"
	writer, _ := store.CreateRaw(sessionName)
	writer.WriteEntry(&testWritable{Message: "raw data"})
	writer.Close()

	sess, err := store.ReadSessionRaw(sessionName)
	require.NoError(t, err, "failed to read session raw")

	assert.Equal(t, "raw", sess.Info.Type)
}

func TestStore_ReadSessionEvents(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	sessionName := "test-session"
	writer, _ := store.CreateEvents(sessionName)
	writer.WriteEntry(&testWritable{Message: "events data"})
	writer.Close()

	sess, err := store.ReadSessionEvents(sessionName)
	require.NoError(t, err, "failed to read session events")

	assert.Equal(t, "events", sess.Info.Type)
}

func TestStore_GetLatest(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	// create sessions with different timestamps in names
	sessionNames := []string{
		"2026-01-05T09-00-user1-Oxa7b3",
		"2026-01-05T12-00-user2-Oxb8c4",
		"2026-01-05T10-00-user3-Oxc9d5",
	}

	for _, name := range sessionNames {
		writer, _ := store.CreateRaw(name)
		writer.Close()
	}

	latest, err := store.GetLatest()
	require.NoError(t, err, "failed to get latest")

	// note: the actual latest is determined by mod time, not filename
	// since we create them sequentially, the last one created should be latest
	require.NotNil(t, latest, "expected latest session")
}

func TestStore_GetLatest_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	_, err := store.GetLatest()
	assert.Error(t, err)
}

func TestStore_GetLatestRaw(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	// create raw session
	writer, _ := store.CreateRaw("raw-test-session")
	writer.Close()

	latest, err := store.GetLatestRaw()
	require.NoError(t, err, "failed to get latest raw")

	assert.Equal(t, "raw", latest.Type)
}

func TestStore_Delete_SessionFolder(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	sessionName := "to-delete-session"

	// create raw and events in session
	rawWriter, _ := store.CreateRaw(sessionName)
	rawWriter.Close()

	eventsWriter, _ := store.CreateEvents(sessionName)
	eventsWriter.Close()

	// verify exists
	_, err := store.ReadSession(sessionName)
	require.NoError(t, err, "session should exist")

	// delete session folder
	err = store.Delete(sessionName)
	require.NoError(t, err, "failed to delete")

	// verify gone
	_, err = store.ReadSession(sessionName)
	assert.Error(t, err, "session should be deleted")
}

func TestStore_Delete_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	err := store.Delete("nonexistent")
	assert.Error(t, err)
}

func TestStore_DeleteSession(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	sessionName := "to-delete-session"
	writer, _ := store.CreateRaw(sessionName)
	writer.Close()

	err := store.DeleteSession(sessionName)
	require.NoError(t, err, "failed to delete session")

	// verify gone
	sessions, _ := store.ListSessionNames()
	for _, s := range sessions {
		assert.NotEqual(t, sessionName, s, "session should be deleted")
	}
}

func TestStore_DeleteSession_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	err := store.DeleteSession("nonexistent")
	assert.Error(t, err)
}

func TestStore_Prune(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	// create sessions with timestamps (required for prune to work)
	sessionNames := []string{
		"2020-01-01T10-00-user1-prune1",
		"2020-01-01T11-00-user2-prune2",
		"2020-01-01T12-00-user3-prune3",
	}
	for _, name := range sessionNames {
		writer, _ := store.CreateRaw(name)
		writer.Close()
	}

	// verify all exist
	sessions, _ := store.ListAllSessions()
	require.Len(t, sessions, 3)

	// prune with 0 duration should remove all (timestamps are in the past)
	removed, err := store.Prune(0)
	require.NoError(t, err, "failed to prune")

	assert.Equal(t, 3, removed)

	// verify empty
	sessions, _ = store.ListAllSessions()
	assert.Empty(t, sessions)
}

func TestStore_Prune_KeepsRecent(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	// create session with timestamped session name
	sessionName := GenerateSessionName("Oxa7b3", "testuser")
	writer, _ := store.CreateRaw(sessionName)
	writer.Close()

	// prune with large duration should keep recent
	removed, err := store.Prune(24 * time.Hour)
	require.NoError(t, err, "failed to prune")

	assert.Equal(t, 0, removed)

	// verify still exists
	sessions, _ := store.ListAllSessions()
	assert.Len(t, sessions, 1)
}

func TestParseFilenameTimestamp(t *testing.T) {
	tests := []struct {
		name     string
		wantYear int
		wantErr  bool
	}{
		{
			name:     "2026-01-05T10-30-user-Oxa7b3.jsonl",
			wantYear: 2026,
		},
		{
			name:     "2026-01-05T10-30-user-Oxa7b3",
			wantYear: 2026,
		},
		{
			name:     "2024-12-31T23-59-user-agent.jsonl",
			wantYear: 2024,
		},
		{
			name:    "short.jsonl",
			wantErr: true,
		},
		{
			name:    "invalid-timestamp-format.jsonl",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseFilenameTimestamp(tt.name)
			if tt.wantErr {
				assert.True(t, got.IsZero(), "expected zero time for invalid filename")
				return
			}

			assert.Equal(t, tt.wantYear, got.Year())
		})
	}
}

func TestGetCacheDir(t *testing.T) {
	t.Run("default mode uses XDG cache", func(t *testing.T) {
		// XDG is now the default
		t.Setenv("OX_XDG_DISABLE", "")
		t.Setenv("XDG_CACHE_HOME", "/custom/cache")

		cacheDir := GetCacheDir()
		assert.Equal(t, "/custom/cache/sageox", cacheDir)
	})

	t.Run("legacy mode uses ~/.sageox/cache", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("HOME", tempHome)
		t.Setenv("OX_XDG_DISABLE", "1")

		cacheDir := GetCacheDir()
		assert.NotEmpty(t, cacheDir)
		assert.Contains(t, cacheDir, "sageox")
		assert.Contains(t, cacheDir, "cache")
	})
}

func TestGetContextPath(t *testing.T) {
	t.Run("default mode uses XDG", func(t *testing.T) {
		// XDG is now the default
		t.Setenv("OX_XDG_DISABLE", "")
		t.Setenv("XDG_CACHE_HOME", "/custom/cache")

		path := GetContextPath("test-repo-id")
		expected := "/custom/cache/sageox/sessions/test-repo-id"
		assert.Equal(t, expected, path)
	})

	t.Run("legacy mode", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("HOME", tempHome)
		t.Setenv("OX_XDG_DISABLE", "1")

		path := GetContextPath("test-repo-id")
		assert.Contains(t, path, "sageox")
		assert.Contains(t, path, "sessions")
		assert.Contains(t, path, "test-repo-id")
	})
}

func TestSessionWriter_MultipleEntries(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	writer, _ := store.CreateRaw("multi-entry-session")
	writer.WriteHeader(&StoreMeta{Version: "1.0"})

	for i := 0; i < 100; i++ {
		writer.WriteEntry(&testWritable{Message: fmt.Sprintf("entry-%d", i)})
	}
	writer.Close()

	assert.Equal(t, 100, writer.Count())

	// read back and verify
	sess, _ := store.ReadSession("multi-entry-session")
	assert.Len(t, sess.Entries, 100)
}

func TestStore_HandlesInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	// create session
	writer, _ := store.CreateRaw("with-invalid-session")
	writer.WriteEntry(&testWritable{Message: "valid1"})
	writer.Close()

	// append invalid JSON directly
	f, _ := os.OpenFile(writer.FilePath(), os.O_APPEND|os.O_WRONLY, 0644)
	f.WriteString("invalid json line\n")
	f.WriteString(`{"type":"test","data":{"message":"valid2"}}` + "\n")
	f.Close()

	// read should skip invalid line
	sess, err := store.ReadSession("with-invalid-session")
	require.NoError(t, err, "failed to read session with invalid JSON")

	// should have valid entries (skipping invalid line)
	assert.GreaterOrEqual(t, len(sess.Entries), 2)
}

// Boundary and error path tests

func TestStore_ListSessions_LargeCount(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	// create many sessions
	numSessions := 100
	for i := 0; i < numSessions; i++ {
		sessionName := fmt.Sprintf("2026-01-05T10-%02d-user-Oxa%03d", i%60, i)
		writer, _ := store.CreateRaw(sessionName)
		writer.WriteHeader(&StoreMeta{Version: "1.0"})
		writer.Close()
	}

	sessions, err := store.ListAllSessions()
	require.NoError(t, err, "failed to list sessions")

	assert.Len(t, sessions, numSessions)

	// verify sorting (most recent first)
	for i := 0; i < len(sessions)-1; i++ {
		assert.False(t, sessions[i].ModTime.Before(sessions[i+1].ModTime), "sessions not sorted by mod time descending at index %d", i)
	}
}

func TestStore_ListSessions_IncludesInflightRecordings(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	// create a completed session with raw.jsonl
	writer, _ := store.CreateRaw("2026-01-05T10-30-user1-Oxa7b3")
	writer.WriteEntry(&testWritable{Message: "completed"})
	writer.Close()

	// create an inflight recording (only .recording.json, no raw.jsonl or meta.json)
	inflightDir := filepath.Join(store.basePath, "2026-01-05T11-00-user2-Oxb8c4")
	require.NoError(t, os.MkdirAll(inflightDir, 0755))
	recState := RecordingState{
		AgentID:   "Oxb8c4",
		StartedAt: time.Now().Add(-5 * time.Minute),
	}
	recData, _ := json.Marshal(recState)
	require.NoError(t, os.WriteFile(filepath.Join(inflightDir, recordingFile), recData, 0644))

	// create an inflight recording WITH raw.jsonl (recording in progress with entries)
	inflightWithRaw := filepath.Join(store.basePath, "2026-01-05T11-30-user3-Oxc9d5")
	require.NoError(t, os.MkdirAll(inflightWithRaw, 0755))
	recState2 := RecordingState{
		AgentID:   "Oxc9d5",
		StartedAt: time.Now().Add(-10 * time.Minute),
	}
	recData2, _ := json.Marshal(recState2)
	require.NoError(t, os.WriteFile(filepath.Join(inflightWithRaw, recordingFile), recData2, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(inflightWithRaw, "raw.jsonl"), []byte(`{"type":"header"}`+"\n"), 0644))

	sessions, err := store.ListAllSessions()
	require.NoError(t, err)

	// all three should be listed
	assert.Len(t, sessions, 3, "should include completed + 2 inflight recordings")

	// build lookup by session name
	byName := map[string]SessionInfo{}
	for _, s := range sessions {
		byName[s.SessionName] = s
	}

	// completed session: not recording
	completed := byName["2026-01-05T10-30-user1-Oxa7b3"]
	assert.False(t, completed.Recording, "completed session should not be recording")

	// inflight without raw.jsonl: recording
	inflightNoRaw := byName["2026-01-05T11-00-user2-Oxb8c4"]
	assert.True(t, inflightNoRaw.Recording, "inflight session without raw.jsonl should be recording")
	assert.Equal(t, "Oxb8c4", inflightNoRaw.AgentID)

	// inflight with raw.jsonl: recording
	inflightHasRaw := byName["2026-01-05T11-30-user3-Oxc9d5"]
	assert.True(t, inflightHasRaw.Recording, "inflight session with raw.jsonl should be recording")
	assert.Equal(t, "Oxc9d5", inflightHasRaw.AgentID)
}

func TestStore_ReadSession_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	// create empty session
	writer, _ := store.CreateRaw("empty-session")
	// close without writing anything
	err := writer.file.Close()
	require.NoError(t, err, "failed to close")

	sess, err := store.ReadSession("empty-session")
	require.NoError(t, err, "failed to read empty session")

	assert.Empty(t, sess.Entries)
}

func TestStore_ReadSession_CorruptedJSON(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	// create session folder and file with various JSON corruption patterns
	sessionName := "corrupted-session"
	sessionPath := filepath.Join(tmpDir, "sessions", sessionName)
	os.MkdirAll(sessionPath, 0755)

	rawPath := filepath.Join(sessionPath, "raw.jsonl")
	content := []byte(`{"type":"header","metadata":{"version":"1.0"}}
{"type":"test","data":{"message":"valid"}}
{incomplete json
{"unterminated":"string
{"type":"test","data":{"message":"valid2"}}
null
{"type":"footer"}
`)
	err := os.WriteFile(rawPath, content, 0644)
	require.NoError(t, err, "failed to write test file")

	sess, err := store.ReadSession(sessionName)
	require.NoError(t, err, "failed to read corrupted session")

	// should have parsed valid entries
	assert.GreaterOrEqual(t, len(sess.Entries), 2)
}

func TestStore_ReadSession_LargeEntries(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	// create session with large content
	writer, _ := store.CreateRaw("large-entries-session")
	writer.WriteHeader(&StoreMeta{Version: "1.0"})

	// write entry with 1MB content
	largeContent := strings.Repeat("x", 1024*1024)
	writer.WriteRaw(map[string]any{
		"type":    "large",
		"content": largeContent,
	})
	writer.Close()

	sess, err := store.ReadSession("large-entries-session")
	require.NoError(t, err, "failed to read session with large entries")

	assert.Len(t, sess.Entries, 1)
}

func TestStore_Delete_WhileReading(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	// create session
	writer, _ := store.CreateRaw("to-delete-session")
	writer.WriteHeader(&StoreMeta{Version: "1.0"})
	writer.WriteEntry(&testWritable{Message: "test"})
	writer.Close()

	// delete it
	err := store.Delete("to-delete-session")
	require.NoError(t, err, "failed to delete")

	// subsequent read should fail
	_, err = store.ReadSession("to-delete-session")
	assert.Error(t, err)
}

func TestStore_Prune_EdgeCases(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	// prune empty store should succeed
	removed, err := store.Prune(time.Hour)
	require.NoError(t, err, "prune empty store failed")
	assert.Equal(t, 0, removed)

	// create a session with timestamp in name
	sessionName := GenerateSessionName("Oxa7b3", "testuser")
	writer, _ := store.CreateRaw(sessionName)
	writer.WriteHeader(&StoreMeta{Version: "1.0"})
	writer.Close()

	// prune with 0 duration should remove everything
	removed, err = store.Prune(0)
	require.NoError(t, err, "prune failed")
	assert.Equal(t, 1, removed)
}

func TestParseFilenameTimestamp_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		wantZero bool
	}{
		{"valid timestamp", "2026-01-05T10-30-user-agent.jsonl", false},
		{"valid session name", "2026-01-05T10-30-user-agent", false},
		{"too short", "2026-01.jsonl", true},
		{"invalid format", "not-a-timestamp.jsonl", true},
		{"empty string", "", true},
		{"just extension", ".jsonl", true},
		{"valid with no suffix", "2026-01-05T10-30", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseFilenameTimestamp(tt.filename)
			if tt.wantZero {
				assert.True(t, result.IsZero(), "expected zero time for %q", tt.filename)
			} else {
				assert.False(t, result.IsZero(), "expected non-zero time for %q", tt.filename)
			}
		})
	}
}

func TestSanitizeFilename_Comprehensive(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"normal", "normal"},
		{"with/slash", "with-slash"},
		{"with\\backslash", "with-backslash"},
		{"with:colon", "with-colon"},
		{"with*star", "with-star"},
		{"with?question", "with-question"},
		{"with\"quote", "with-quote"},
		{"with<less", "with-less"},
		{"with>greater", "with-greater"},
		{"with|pipe", "with-pipe"},
		{"multiple---dashes", "multiple-dashes"},
		{"-leading-dash", "leading-dash"},
		{"trailing-dash-", "trailing-dash"},
		{"-both-ends-", "both-ends"},
		{"///multiple///", "multiple"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := sanitizeFilename(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSessionWriter_WriteRaw_NilData(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	writer, _ := store.CreateRaw("nil-data-session")
	defer writer.Close()

	err := writer.WriteRaw(nil)
	assert.Error(t, err)
}

func TestSessionWriter_SessionName(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	sessionName := "test-session-name"
	writer, _ := store.CreateRaw(sessionName)
	defer writer.Close()

	assert.Equal(t, sessionName, writer.SessionName())
}

func TestStore_BothRawAndEventsInSession(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir)

	sessionName := "dual-session"

	// create both raw and events
	rawWriter, _ := store.CreateRaw(sessionName)
	rawWriter.WriteEntry(&testWritable{Message: "raw"})
	rawWriter.Close()

	eventsWriter, _ := store.CreateEvents(sessionName)
	eventsWriter.WriteEntry(&testWritable{Message: "events"})
	eventsWriter.Close()

	// verify both files exist in same session folder
	sessionPath := store.GetSessionPath(sessionName)

	rawPath := filepath.Join(sessionPath, "raw.jsonl")
	_, err := os.Stat(rawPath)
	assert.False(t, os.IsNotExist(err), "expected raw.jsonl to exist in session folder")

	eventsPath := filepath.Join(sessionPath, "events.jsonl")
	_, err = os.Stat(eventsPath)
	assert.False(t, os.IsNotExist(err), "expected events.jsonl to exist in session folder")

	// read both back
	rawSess, _ := store.ReadSessionRaw(sessionName)
	assert.Equal(t, "raw", rawSess.Info.Type)

	eventsSess, _ := store.ReadSessionEvents(sessionName)
	assert.Equal(t, "events", eventsSess.Info.Type)
}

func TestReadSessionFromPath_ValidFile(t *testing.T) {
	tmpDir := t.TempDir()

	// create a JSONL file directly
	filePath := filepath.Join(tmpDir, "test-session.jsonl")
	content := []string{
		`{"type":"header","metadata":{"version":"1.0","agent_type":"test-agent","model":"gpt-4"}}`,
		`{"type":"message","timestamp":"2026-01-14T10:00:00Z","data":{"role":"user","content":"hello"}}`,
		`{"type":"message","timestamp":"2026-01-14T10:00:01Z","data":{"role":"assistant","content":"hi there"}}`,
		`{"type":"footer","closed_at":"2026-01-14T10:00:02Z"}`,
	}
	err := os.WriteFile(filePath, []byte(strings.Join(content, "\n")), 0644)
	require.NoError(t, err)

	// read using ReadSessionFromPath
	sess, err := ReadSessionFromPath(filePath)
	require.NoError(t, err)

	// verify metadata was parsed
	assert.NotNil(t, sess.Meta)
	assert.Equal(t, "1.0", sess.Meta.Version)
	assert.Equal(t, "test-agent", sess.Meta.AgentType)
	assert.Equal(t, "gpt-4", sess.Meta.Model)

	// verify entries (excluding header/footer)
	assert.Len(t, sess.Entries, 2)

	// verify footer was captured
	assert.NotNil(t, sess.Footer)

	// verify info
	assert.Equal(t, "test-session.jsonl", sess.Info.Filename)
	assert.True(t, strings.HasSuffix(sess.Info.FilePath, filePath))
}

func TestReadSessionFromPath_RelativePath(t *testing.T) {
	// create temp file in current directory
	f, err := os.CreateTemp("", "session-*.jsonl")
	require.NoError(t, err)
	defer os.Remove(f.Name())

	content := `{"type":"message","data":{"content":"test"}}` + "\n"
	_, err = f.WriteString(content)
	require.NoError(t, err)
	f.Close()

	// use absolute path
	sess, err := ReadSessionFromPath(f.Name())
	require.NoError(t, err)
	assert.Len(t, sess.Entries, 1)
}

func TestReadSessionFromPath_FileNotFound(t *testing.T) {
	_, err := ReadSessionFromPath("/nonexistent/path/session.jsonl")
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrSessionNotFound))
}

func TestReadSessionFromPath_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "empty.jsonl")
	err := os.WriteFile(filePath, []byte{}, 0644)
	require.NoError(t, err)

	sess, err := ReadSessionFromPath(filePath)
	require.NoError(t, err)
	assert.Empty(t, sess.Entries)
}

func TestReadSessionFromPath_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "invalid.jsonl")
	content := "not valid json\n{\"type\":\"message\"}\nmore invalid\n"
	err := os.WriteFile(filePath, []byte(content), 0644)
	require.NoError(t, err)

	// should skip invalid lines and parse valid ones
	sess, err := ReadSessionFromPath(filePath)
	require.NoError(t, err)
	assert.Len(t, sess.Entries, 1)
}
