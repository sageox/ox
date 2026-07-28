package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindOrphanedSessions(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T, cacheDir, ledgerDir string)
		expected int
	}{
		{
			name:     "empty cache",
			setup:    func(t *testing.T, cacheDir, ledgerDir string) {},
			expected: 0,
		},
		{
			name: "skips in-progress recording",
			setup: func(t *testing.T, cacheDir, ledgerDir string) {
				dir := filepath.Join(cacheDir, "2026-01-15T10-30-ryan-Oxa1b2")
				os.MkdirAll(dir, 0755)
				writeTestRawJSONL(t, filepath.Join(dir, ledgerFileRaw))
				os.WriteFile(filepath.Join(dir, ".recording.json"), []byte("{}"), 0644)
			},
			expected: 0,
		},
		{
			name: "skips dir without raw.jsonl",
			setup: func(t *testing.T, cacheDir, ledgerDir string) {
				dir := filepath.Join(cacheDir, "2026-01-15T10-30-ryan-Oxa1b2")
				os.MkdirAll(dir, 0755)
				// no raw.jsonl
			},
			expected: 0,
		},
		{
			name: "skips already uploaded",
			setup: func(t *testing.T, cacheDir, ledgerDir string) {
				sessionName := "2026-01-15T10-30-ryan-Oxa1b2"
				dir := filepath.Join(cacheDir, sessionName)
				os.MkdirAll(dir, 0755)
				writeTestRawJSONL(t, filepath.Join(dir, ledgerFileRaw))
				// meta.json exists in ledger
				ledgerSession := filepath.Join(ledgerDir, "sessions", sessionName)
				os.MkdirAll(ledgerSession, 0755)
				os.WriteFile(filepath.Join(ledgerSession, "meta.json"), []byte("{}"), 0644)
			},
			expected: 0,
		},
		{
			name: "detects orphaned session",
			setup: func(t *testing.T, cacheDir, ledgerDir string) {
				dir := filepath.Join(cacheDir, "2026-01-15T10-30-ryan-Oxa1b2")
				os.MkdirAll(dir, 0755)
				writeTestRawJSONL(t, filepath.Join(dir, ledgerFileRaw))
			},
			expected: 1,
		},
		{
			name: "finds multiple orphans",
			setup: func(t *testing.T, cacheDir, ledgerDir string) {
				for _, name := range []string{"2026-01-15T10-30-ryan-Oxa1b2", "2026-01-15T11-00-ryan-Oxc3d4"} {
					dir := filepath.Join(cacheDir, name)
					os.MkdirAll(dir, 0755)
					writeTestRawJSONL(t, filepath.Join(dir, ledgerFileRaw))
				}
			},
			expected: 2,
		},
		{
			name: "skips legacy subdirectories",
			setup: func(t *testing.T, cacheDir, ledgerDir string) {
				// "raw" and "events" are legacy dirs, not session dirs
				os.MkdirAll(filepath.Join(cacheDir, "raw"), 0755)
				os.MkdirAll(filepath.Join(cacheDir, "events"), 0755)
			},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// build a fake cache + ledger structure
			tmpDir := t.TempDir()
			cacheDir := filepath.Join(tmpDir, "cache", "sessions")
			ledgerDir := filepath.Join(tmpDir, "ledger")
			os.MkdirAll(cacheDir, 0755)
			os.MkdirAll(filepath.Join(ledgerDir, "sessions"), 0755)

			tt.setup(t, cacheDir, ledgerDir)

			// call findOrphanedSessions with a shim: we can't easily use the real
			// function because it calls getRepoIDOrDefault + GetContextPath.
			// Instead, test the core scanning logic directly.
			orphans := scanCacheDirForOrphans(cacheDir, ledgerDir)

			if len(orphans) != tt.expected {
				t.Errorf("expected %d orphans, got %d", tt.expected, len(orphans))
			}
		})
	}
}

func TestReadCacheSessionMeta(t *testing.T) {
	t.Run("valid header with footer", func(t *testing.T) {
		tmpDir := t.TempDir()
		rawPath := filepath.Join(tmpDir, ledgerFileRaw)
		writeTestRawJSONLWithEntries(t, rawPath, 5)

		meta, count, err := readCacheSessionMeta(rawPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if meta.AgentID != "Oxtest1" {
			t.Errorf("expected agent_id=Oxtest1, got %q", meta.AgentID)
		}
		if meta.AgentType != "claude-code" {
			t.Errorf("expected agent_type=claude-code, got %q", meta.AgentType)
		}
		if count != 5 {
			t.Errorf("expected entry_count=5, got %d", count)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		_, _, err := readCacheSessionMeta("/nonexistent/raw.jsonl")
		require.Error(t, err, "expected error for missing file")
	})

	t.Run("corrupt header", func(t *testing.T) {
		tmpDir := t.TempDir()
		rawPath := filepath.Join(tmpDir, ledgerFileRaw)
		os.WriteFile(rawPath, []byte("not json\n"), 0644)

		_, _, err := readCacheSessionMeta(rawPath)
		require.Error(t, err, "expected error for corrupt header")
	})

	t.Run("header only no footer", func(t *testing.T) {
		tmpDir := t.TempDir()
		rawPath := filepath.Join(tmpDir, ledgerFileRaw)
		writeTestRawJSONL(t, rawPath) // header only

		meta, count, err := readCacheSessionMeta(rawPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if meta == nil {
			t.Fatal("expected non-nil meta")
		}
		if count != 0 {
			t.Errorf("expected entry_count=0 with no footer, got %d", count)
		}
	})
}

// scanCacheDirForOrphans is a test-friendly version of the core scanning logic
// extracted from findOrphanedSessions, without the config/path resolution.
// This MUST stay in sync with findOrphanedSessions — especially StopIncomplete handling.
//
// Known divergences from production findOrphanedSessions:
// - Production uses session.RecordingState struct instead of anonymous struct
// - Production has stale recording detection (time-based threshold)
// - Production cleans up .lock files
// These divergences are intentional to keep the test helper simple.
func scanCacheDirForOrphans(cacheSessionsDir, ledgerPath string) []orphanedSession {
	entries, err := os.ReadDir(cacheSessionsDir)
	if err != nil {
		return nil
	}

	var orphans []orphanedSession
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		sessionName := entry.Name()
		sessionDir := filepath.Join(cacheSessionsDir, sessionName)

		if sessionName == "raw" || sessionName == "events" {
			continue
		}

		// check if still recording (.recording.json present)
		recordingPath := filepath.Join(sessionDir, ".recording.json")
		if _, err := os.Stat(recordingPath); err == nil {
			// read recording state to check for StopIncomplete
			recData, readErr := os.ReadFile(recordingPath)
			if readErr != nil {
				continue
			}
			var recState struct {
				StopIncomplete bool `json:"stop_incomplete"`
			}
			if json.Unmarshal(recData, &recState) != nil {
				continue // corrupt, skip
			}
			if !recState.StopIncomplete {
				continue // genuinely active recording, skip
			}
			// StopIncomplete: clear the recording state so session can be recovered
			_ = os.Remove(recordingPath)
		}

		rawPath := filepath.Join(sessionDir, ledgerFileRaw)
		if _, err := os.Stat(rawPath); os.IsNotExist(err) {
			continue
		}

		ledgerSessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
		if _, err := os.Stat(filepath.Join(ledgerSessionDir, "meta.json")); err == nil {
			continue
		}

		meta, entryCount, err := readCacheSessionMeta(rawPath)
		if err != nil {
			continue
		}

		orphans = append(orphans, orphanedSession{
			SessionName: sessionName,
			CachePath:   sessionDir,
			Meta:        meta,
			EntryCount:  entryCount,
		})
	}

	return orphans
}

// writeTestRawJSONL creates a minimal raw.jsonl with a valid header line.
func writeTestRawJSONL(t *testing.T, path string) {
	t.Helper()
	header := map[string]any{
		"type": "header",
		"metadata": map[string]any{
			"version":    "1.0",
			"agent_id":   "Oxtest1",
			"agent_type": "claude-code",
			"username":   "test@example.com",
			"created_at": time.Now().Format(time.RFC3339),
		},
	}
	data, _ := json.Marshal(header)
	data = append(data, '\n')
	os.WriteFile(path, data, 0644)
}

// writeTestRawJSONLWithEntries creates a raw.jsonl with header, entries, and footer.
func writeTestRawJSONLWithEntries(t *testing.T, path string, entryCount int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)

	// header
	enc.Encode(map[string]any{
		"type": "header",
		"metadata": map[string]any{
			"version":    "1.0",
			"agent_id":   "Oxtest1",
			"agent_type": "claude-code",
			"username":   "test@example.com",
			"created_at": time.Now().Format(time.RFC3339),
		},
	})

	// entries
	for i := 0; i < entryCount; i++ {
		enc.Encode(map[string]any{
			"type":    "assistant",
			"content": "test entry",
		})
	}

	// footer
	enc.Encode(map[string]any{
		"type":        "footer",
		"entry_count": entryCount,
		"closed_at":   time.Now().Format(time.RFC3339),
	})
}

func TestFindOrphanedSessions_CorruptRawJSONL(t *testing.T) {
	tmpDir := t.TempDir()
	cacheDir := filepath.Join(tmpDir, "cache", "sessions")
	ledgerDir := filepath.Join(tmpDir, "ledger")
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerDir, "sessions"), 0755))

	// create session with corrupt raw.jsonl (not valid JSON header)
	dir := filepath.Join(cacheDir, "2026-01-15T10-30-ryan-OxCorr")
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ledgerFileRaw), []byte("this is not json\n"), 0644))

	orphans := scanCacheDirForOrphans(cacheDir, ledgerDir)
	assert.Empty(t, orphans, "corrupt raw.jsonl should be excluded from orphan list")
}

func TestReadCacheSessionMeta_DirectoryPath(t *testing.T) {
	dirPath := t.TempDir()
	_, _, err := readCacheSessionMeta(dirPath)
	if err == nil {
		t.Fatal("expected error for directory path, got nil")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("expected 'not a regular file' error, got: %v", err)
	}
}

func TestValidateRawJSONLHeader_DirectoryPath(t *testing.T) {
	dirPath := t.TempDir()
	err := validateRawJSONLHeader(dirPath)
	if err == nil {
		t.Fatal("expected error for directory path, got nil")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("expected 'not a regular file' error, got: %v", err)
	}
}

func TestCopyFile_DirectoryPath(t *testing.T) {
	dirPath := t.TempDir()
	dstPath := filepath.Join(t.TempDir(), "out.txt")
	err := copyFile(dirPath, dstPath)
	if err == nil {
		t.Fatal("expected error for directory path, got nil")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("expected 'not a regular file' error, got: %v", err)
	}
}

func TestFindOrphanedSessions_ActiveRecordingWithRawJSONL(t *testing.T) {
	tmpDir := t.TempDir()
	cacheDir := filepath.Join(tmpDir, "cache", "sessions")
	ledgerDir := filepath.Join(tmpDir, "ledger")
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerDir, "sessions"), 0755))

	// session has BOTH .recording.json AND raw.jsonl — still actively recording
	dir := filepath.Join(cacheDir, "2026-01-15T10-30-ryan-OxActv")
	require.NoError(t, os.MkdirAll(dir, 0755))
	writeTestRawJSONL(t, filepath.Join(dir, ledgerFileRaw))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), []byte(`{"agent_id":"OxActv"}`), 0644))

	orphans := scanCacheDirForOrphans(cacheDir, ledgerDir)
	assert.Empty(t, orphans,
		"session with .recording.json should be excluded even if raw.jsonl exists")
}

func TestFindOrphanedSessions_StopIncompleteRecovery(t *testing.T) {
	tmpDir := t.TempDir()
	cacheDir := filepath.Join(tmpDir, "cache", "sessions")
	ledgerDir := filepath.Join(tmpDir, "ledger")
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerDir, "sessions"), 0755))

	// session has .recording.json with stop_incomplete=true — should be recovered
	dir := filepath.Join(cacheDir, "2026-01-15T10-30-ryan-OxStop")
	require.NoError(t, os.MkdirAll(dir, 0755))
	writeTestRawJSONL(t, filepath.Join(dir, ledgerFileRaw))
	recState := `{"agent_id":"OxStop","stop_incomplete":true}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), []byte(recState), 0644))

	orphans := scanCacheDirForOrphans(cacheDir, ledgerDir)
	assert.Len(t, orphans, 1, "stop_incomplete session should be treated as orphan")
	assert.Equal(t, "2026-01-15T10-30-ryan-OxStop", orphans[0].SessionName)

	// .recording.json should have been removed
	_, err := os.Stat(filepath.Join(dir, ".recording.json"))
	assert.True(t, os.IsNotExist(err), ".recording.json should be removed for stop_incomplete sessions")
}

func TestFindOrphanedSessions_StopIncompleteActiveNotRecovered(t *testing.T) {
	tmpDir := t.TempDir()
	cacheDir := filepath.Join(tmpDir, "cache", "sessions")
	ledgerDir := filepath.Join(tmpDir, "ledger")
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerDir, "sessions"), 0755))

	// session has .recording.json with stop_incomplete=false — genuinely active
	dir := filepath.Join(cacheDir, "2026-01-15T10-30-ryan-OxLive")
	require.NoError(t, os.MkdirAll(dir, 0755))
	writeTestRawJSONL(t, filepath.Join(dir, ledgerFileRaw))
	recState := `{"agent_id":"OxLive","stop_incomplete":false}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), []byte(recState), 0644))

	orphans := scanCacheDirForOrphans(cacheDir, ledgerDir)
	assert.Empty(t, orphans, "active recording (stop_incomplete=false) should not be treated as orphan")

	// .recording.json should still exist
	_, err := os.Stat(filepath.Join(dir, ".recording.json"))
	assert.NoError(t, err, ".recording.json should remain for active recordings")
}

func TestFindOrphanedSessions_CorruptRecordingJSON(t *testing.T) {
	tmpDir := t.TempDir()
	cacheDir := filepath.Join(tmpDir, "cache", "sessions")
	ledgerDir := filepath.Join(tmpDir, "ledger")
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerDir, "sessions"), 0755))

	// session has .recording.json with invalid JSON — should be skipped (not crashed)
	dir := filepath.Join(cacheDir, "2026-01-15T10-30-ryan-OxBad")
	require.NoError(t, os.MkdirAll(dir, 0755))
	writeTestRawJSONL(t, filepath.Join(dir, ledgerFileRaw))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), []byte("not json{{{"), 0644))

	orphans := scanCacheDirForOrphans(cacheDir, ledgerDir)
	assert.Empty(t, orphans, "corrupt .recording.json should be skipped, not crash")
}

func TestFindOrphanedSessions_MixedStates(t *testing.T) {
	tmpDir := t.TempDir()
	cacheDir := filepath.Join(tmpDir, "cache", "sessions")
	ledgerDir := filepath.Join(tmpDir, "ledger")
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerDir, "sessions"), 0755))

	// 1. orphan (no .recording.json, has raw.jsonl)
	orphanDir := filepath.Join(cacheDir, "2026-01-15T10-00-ryan-Ox0001")
	require.NoError(t, os.MkdirAll(orphanDir, 0755))
	writeTestRawJSONL(t, filepath.Join(orphanDir, ledgerFileRaw))

	// 2. active (has .recording.json)
	activeDir := filepath.Join(cacheDir, "2026-01-15T10-10-ryan-Ox0002")
	require.NoError(t, os.MkdirAll(activeDir, 0755))
	writeTestRawJSONL(t, filepath.Join(activeDir, ledgerFileRaw))
	require.NoError(t, os.WriteFile(filepath.Join(activeDir, ".recording.json"), []byte(`{"agent_id":"Ox0002"}`), 0644))

	// 3. already uploaded (meta.json in ledger)
	uploadedDir := filepath.Join(cacheDir, "2026-01-15T10-20-ryan-Ox0003")
	require.NoError(t, os.MkdirAll(uploadedDir, 0755))
	writeTestRawJSONL(t, filepath.Join(uploadedDir, ledgerFileRaw))
	ledgerSession := filepath.Join(ledgerDir, "sessions", "2026-01-15T10-20-ryan-Ox0003")
	require.NoError(t, os.MkdirAll(ledgerSession, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(ledgerSession, "meta.json"), []byte("{}"), 0644))

	// 4. stop_incomplete (should be recovered)
	incompleteDir := filepath.Join(cacheDir, "2026-01-15T10-30-ryan-Ox0004")
	require.NoError(t, os.MkdirAll(incompleteDir, 0755))
	writeTestRawJSONL(t, filepath.Join(incompleteDir, ledgerFileRaw))
	require.NoError(t, os.WriteFile(filepath.Join(incompleteDir, ".recording.json"), []byte(`{"agent_id":"Ox0004","stop_incomplete":true}`), 0644))

	// 5. no raw.jsonl (empty dir)
	emptyDir := filepath.Join(cacheDir, "2026-01-15T10-40-ryan-Ox0005")
	require.NoError(t, os.MkdirAll(emptyDir, 0755))

	orphans := scanCacheDirForOrphans(cacheDir, ledgerDir)
	assert.Len(t, orphans, 2, "should find exactly 2 orphans: plain orphan + stop_incomplete")

	names := make(map[string]bool)
	for _, o := range orphans {
		names[o.SessionName] = true
	}
	assert.True(t, names["2026-01-15T10-00-ryan-Ox0001"], "plain orphan should be found")
	assert.True(t, names["2026-01-15T10-30-ryan-Ox0004"], "stop_incomplete should be found")
}

func TestReadCacheSessionMeta_EntriesButNoFooter(t *testing.T) {
	// raw.jsonl with header and entries but no footer line (e.g., crash during recording)
	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, ledgerFileRaw)

	f, err := os.Create(rawPath)
	require.NoError(t, err)
	enc := json.NewEncoder(f)
	enc.Encode(map[string]any{
		"type": "header",
		"metadata": map[string]any{
			"version":    "1.0",
			"agent_id":   "OxCrash",
			"agent_type": "claude-code",
			"username":   "test@example.com",
			"created_at": time.Now().Format(time.RFC3339),
		},
	})
	// entries but no footer
	for i := 0; i < 3; i++ {
		enc.Encode(map[string]any{"type": "assistant", "content": "entry"})
	}
	f.Close()

	meta, count, err := readCacheSessionMeta(rawPath)
	require.NoError(t, err)
	assert.Equal(t, "OxCrash", meta.AgentID)
	// last line is an entry, not a footer — entry_count should be 0
	assert.Equal(t, 0, count, "entries without footer should yield entry_count=0")
}

func TestReadCacheSessionMeta_HeaderWithNoMetadataKey(t *testing.T) {
	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, ledgerFileRaw)
	require.NoError(t, os.WriteFile(rawPath, []byte(`{"type":"header","version":"1.0"}`+"\n"), 0644))

	_, _, err := readCacheSessionMeta(rawPath)
	assert.Error(t, err, "header without metadata key should fail")
	assert.Contains(t, err.Error(), "no metadata in header")
}

func TestReadCacheSessionMeta_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, ledgerFileRaw)
	require.NoError(t, os.WriteFile(rawPath, []byte{}, 0644))

	_, _, err := readCacheSessionMeta(rawPath)
	assert.Error(t, err, "empty file should fail")
	assert.Contains(t, err.Error(), "empty file")
}

func TestRetrySessionUpload_SkipsZeroEntries(t *testing.T) {
	orphan := orphanedSession{
		SessionName: "2026-01-20T10-00-testuser-OxZero",
		CachePath:   t.TempDir(),
		Meta:        nil,
		EntryCount:  0,
	}

	// create bare+clone for ledger (retrySessionUpload needs a valid ledger)
	_, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	err := retrySessionUpload(clonePath, clonePath, orphan)
	assert.NoError(t, err, "zero-entry session should return nil (skip silently)")

	// no session dir should be created in the ledger
	sessionDir := filepath.Join(clonePath, "sessions", orphan.SessionName)
	_, statErr := os.Stat(sessionDir)
	assert.True(t, os.IsNotExist(statErr),
		"no session directory should be created for zero-entry sessions")
}

func TestRetrySessionUpload_SkipsCorruptRawJSONL(t *testing.T) {
	tmpDir := t.TempDir()
	cacheSessionDir := filepath.Join(tmpDir, "cache-session")
	require.NoError(t, os.MkdirAll(cacheSessionDir, 0755))

	// write corrupt raw.jsonl (not valid JSONL header)
	require.NoError(t, os.WriteFile(
		filepath.Join(cacheSessionDir, ledgerFileRaw),
		[]byte("this is not valid json at all\ngarbage\n"),
		0644,
	))

	orphan := orphanedSession{
		SessionName: "2026-01-20T10-00-testuser-OxCorr",
		CachePath:   cacheSessionDir,
		Meta:        nil,
		EntryCount:  5,
	}

	_, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	err := retrySessionUpload(clonePath, clonePath, orphan)
	require.Error(t, err, "corrupt raw.jsonl should cause retrySessionUpload to fail")
	assert.Contains(t, err.Error(), "validation failed",
		"error should indicate validation failure")
}

func TestRetrySessionUpload_CopiesFromCache(t *testing.T) {
	tmpDir := t.TempDir()
	cacheSessionDir := filepath.Join(tmpDir, "cache-session")
	require.NoError(t, os.MkdirAll(cacheSessionDir, 0755))

	// write valid raw.jsonl with proper header
	rawContent := `{"metadata":{"agent_id":"OxCopy","agent_type":"claude-code","username":"test@example.com","created_at":"2026-01-20T10:00:00Z"},"type":"header"}
{"type":"user","content":"hello","seq":1}
{"type":"assistant","content":"hi","seq":2}
{"entry_count":2,"type":"footer"}
`
	require.NoError(t, os.WriteFile(
		filepath.Join(cacheSessionDir, ledgerFileRaw),
		[]byte(rawContent),
		0644,
	))

	orphan := orphanedSession{
		SessionName: "2026-01-20T10-00-testuser-OxCopy",
		CachePath:   cacheSessionDir,
		Meta: &session.StoreMeta{
			AgentID:   "OxCopy",
			AgentType: "claude-code",
			Username:  "test@example.com",
		},
		EntryCount: 2,
	}

	_, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	// retrySessionUpload will fail on LFS upload (no LFS server), but the
	// critical thing is that it copies raw.jsonl from cache to ledger first.
	err := retrySessionUpload(clonePath, clonePath, orphan)
	// expect error since LFS upload will fail (no LFS server configured)
	// but the copy should have happened before the failure
	if err != nil {
		// verify the error is from LFS upload, not from the copy
		assert.Contains(t, err.Error(), "LFS upload",
			"error should be from LFS upload phase, not from file copy")
	}

	// raw.jsonl MUST exist in the ledger session dir — the copy happens before LFS upload
	ledgerRawPath := filepath.Join(clonePath, "sessions", orphan.SessionName, ledgerFileRaw)
	require.FileExists(t, ledgerRawPath, "raw.jsonl must be copied to ledger before LFS upload")
	ledgerRaw, readErr := os.ReadFile(ledgerRawPath)
	require.NoError(t, readErr)
	require.Equal(t, rawContent, string(ledgerRaw),
		"ledger raw.jsonl should match cache content")

	// verify cache content is still intact regardless of retry outcome
	cacheRaw, readErr := os.ReadFile(filepath.Join(cacheSessionDir, ledgerFileRaw))
	require.NoError(t, readErr)
	assert.Equal(t, rawContent, string(cacheRaw),
		"cache raw.jsonl must be preserved after retry attempt")
}

func TestValidateRawJSONLHeader(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name:    "valid header",
			content: `{"type":"header","metadata":{"agent_id":"Ox1"}}` + "\n",
		},
		{
			name:    "empty file",
			content: "",
			wantErr: "empty file",
		},
		{
			name:    "invalid json",
			content: "not json\n",
			wantErr: "invalid JSON",
		},
		{
			name:    "missing metadata key",
			content: `{"type":"header","version":"1.0"}` + "\n",
			wantErr: "missing 'metadata' key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rawPath := filepath.Join(tmpDir, ledgerFileRaw)
			require.NoError(t, os.WriteFile(rawPath, []byte(tt.content), 0644))

			err := validateRawJSONLHeader(rawPath)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

// TestRetrySessionUpload_ZeroEntryGuard verifies that retrySessionUpload is a no-op
// when EntryCount is zero, returning nil without touching the ledger.
// This guards against uploading empty sessions that have no substantive content.
func TestRetrySessionUpload_ZeroEntryGuard(t *testing.T) {
	tmpDir := t.TempDir()
	cacheDir := filepath.Join(tmpDir, "cache")
	ledgerDir := filepath.Join(tmpDir, "ledger")
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerDir, "sessions"), 0755))

	sessionName := "2026-01-15T14-00-testuser-Ox0ENT"
	cacheSession := filepath.Join(cacheDir, sessionName)
	require.NoError(t, os.MkdirAll(cacheSession, 0755))
	writeTestRawJSONL(t, filepath.Join(cacheSession, ledgerFileRaw))

	orphan := orphanedSession{
		SessionName: sessionName,
		CachePath:   cacheSession,
		Meta: &session.StoreMeta{
			AgentID:   "Ox0ENT",
			AgentType: "claude-code",
		},
		EntryCount: 0, // zero entries — must be skipped
	}

	err := retrySessionUpload("", ledgerDir, orphan)
	assert.NoError(t, err, "zero-entry session should return nil without error")

	// ledger session dir must not be created (no work done)
	ledgerSessionDir := filepath.Join(ledgerDir, "sessions", sessionName)
	_, statErr := os.Stat(ledgerSessionDir)
	assert.True(t, os.IsNotExist(statErr),
		"ledger session dir must not be created for a zero-entry session")
}

// TestRetrySessionUpload_ContentFilesNotPointers_OnLFSFailure is a regression test for
// bug #291 in the doctor retry path. It verifies that when LFS upload fails (no
// credentials with empty projectRoot), raw.jsonl copied to the ledger session dir is
// NOT replaced with an LFS pointer stub.
//
// The bug was: WriteSessionMeta (with fileRefs) was called before git push, replacing
// content files with pointer stubs. If push then failed, the content was lost.
// The fix: WriteSessionMetaOnly is used before push; WritePointerFiles runs only after
// a successful push. When LFS fails, retrySessionUpload returns an error before any
// write step that would corrupt content.
func TestRetrySessionUpload_ContentFilesNotPointers_OnLFSFailure(t *testing.T) {
	tmpDir := t.TempDir()
	cacheDir := filepath.Join(tmpDir, "cache")
	ledgerDir := filepath.Join(tmpDir, "ledger")
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerDir, "sessions"), 0755))

	sessionName := "2026-01-15T15-00-testuser-OxLFSF"
	cacheSession := filepath.Join(cacheDir, sessionName)
	require.NoError(t, os.MkdirAll(cacheSession, 0755))

	rawPath := filepath.Join(cacheSession, ledgerFileRaw)
	writeTestRawJSONLWithEntries(t, rawPath, 3)

	orphan := orphanedSession{
		SessionName: sessionName,
		CachePath:   cacheSession,
		Meta: &session.StoreMeta{
			AgentID:   "OxLFSF",
			AgentType: "claude-code",
		},
		EntryCount: 3,
	}

	// empty projectRoot with no git repo → retrySessionUpload will fail somewhere in the
	// upload/commit pipeline (LFS, credentials, or git — depending on environment)
	err := retrySessionUpload("", ledgerDir, orphan)
	require.Error(t, err, "expected upload error with no project or git repo")

	// CRITICAL (bug #291 regression): raw.jsonl copied to the ledger session dir must
	// NOT be replaced with an LFS pointer stub at any point before a successful push.
	// If retrySessionUpload failed after copying raw.jsonl but before the push, the
	// content file must remain as real bytes — not a tiny pointer.
	// Before the fix, WriteSessionMeta (with fileRefs) was called before commitAndPush,
	// so a push failure would leave only pointer stubs with no remote blob backing.
	// check ALL content files in the ledger session dir survive as real content
	ledgerSessionDir := filepath.Join(ledgerDir, "sessions", sessionName)
	contentFiles := []string{ledgerFileRaw}
	// check for any additional files that were copied
	if entries, err := os.ReadDir(ledgerSessionDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				contentFiles = append(contentFiles, e.Name())
			}
		}
	}
	// deduplicate
	seen := make(map[string]bool)
	for _, f := range contentFiles {
		if seen[f] {
			continue
		}
		seen[f] = true
		fPath := filepath.Join(ledgerSessionDir, f)
		require.FileExists(t, fPath, "%s should exist in ledger after failure", f)
		assert.False(t, lfs.IsPointerFile(fPath),
			"%s must remain real content after a failed upload (bug #291 regression)", f)
	}
}

// --- resolveOrphanSessionID: ox-5n8e regression coverage ---
//
// Production evidence: the same session (identical session_name, username,
// agent_id, created_at, entry_count, and per-file LFS OIDs) ended up with
// two different session_id values in meta.json on two replicas of the same
// ledger. Root cause: retrySessionUpload only ever consulted a preserved
// meta.json ID and, when none was present yet (true for every orphan by
// construction — findOrphanedSessions already filters out sessions with an
// existing ledger meta.json), silently minted a brand new ID instead of
// falling back to the ID already carried in the orphan's raw.jsonl header
// (orphan.Meta.SessionID). Any session retried more than once before its
// first successful push — a stale/reset local ledger clone, a second
// developer machine, a repeated `ox doctor` run — got a different random ID
// each time.

// TestResolveOrphanSessionID_HeaderIDPreservedWhenNoLedgerMetaYet is the
// direct regression test for the bug: this is the exact state every orphan
// is in on its first (and any subsequent, still-failing) retry attempt —
// no meta.json in the ledger session dir — which is precisely where the
// header-carried ID must NOT be discarded.
func TestResolveOrphanSessionID_HeaderIDPreservedWhenNoLedgerMetaYet(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "sessions", "2026-05-08T19-25-ajit-OxF9dp")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))
	// no meta.json written — mirrors every orphan retrySessionUpload sees

	const headerID = "ses_019f633e-29f3-7566-9ab4-a3da5b666fe5"
	orphan := orphanedSession{
		SessionName: "2026-05-08T19-25-ajit-OxF9dp",
		Meta:        &session.StoreMeta{AgentID: "OxF9dp", SessionID: headerID},
	}

	got, err := resolveOrphanSessionID(sessionDir, orphan)
	require.NoError(t, err)
	assert.Equal(t, headerID, got, "must preserve the header-carried SessionID, not mint a new one")
}

// TestResolveOrphanSessionID_PreservedMetaWinsOverHeader verifies the
// documented precedence: a meta.json already written to the ledger session
// dir (an earlier retry that got as far as the write before the push
// failed) outranks the header ID.
func TestResolveOrphanSessionID_PreservedMetaWinsOverHeader(t *testing.T) {
	sessionDir := t.TempDir()
	const preservedID = "ses_019f63cb-97ff-761e-ab36-e7a967b4438f"
	require.NoError(t, lfs.WriteSessionMeta(sessionDir, &lfs.SessionMeta{
		Version:     "1.0",
		SessionName: "2026-05-08T19-25-ajit-OxF9dp",
		SessionID:   preservedID,
		CreatedAt:   time.Now(),
	}))

	orphan := orphanedSession{
		SessionName: "2026-05-08T19-25-ajit-OxF9dp",
		Meta:        &session.StoreMeta{AgentID: "OxF9dp", SessionID: "ses_019f633e-29f3-7566-9ab4-a3da5b666fe5"},
	}

	got, err := resolveOrphanSessionID(sessionDir, orphan)
	require.NoError(t, err)
	assert.Equal(t, preservedID, got, "preserved meta.json ID must win over the header-carried ID")
}

// TestResolveOrphanSessionID_CorruptMetaRefusesToMint mirrors the
// unreadable-events-never-mints-new-id shape from the plan backfill
// hardening (PR #723): a meta.json that exists but cannot be parsed must
// abort with an error, never silently fall through to minting a fresh
// SessionID that would rotate away from an ID we simply failed to read.
func TestResolveOrphanSessionID_CorruptMetaRefusesToMint(t *testing.T) {
	sessionDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "meta.json"), []byte("{not valid json"), 0644))

	orphan := orphanedSession{
		SessionName: "corrupt-meta-session",
		Meta:        &session.StoreMeta{AgentID: "OxBAD1", SessionID: "ses_019f633e-29f3-7566-9ab4-a3da5b666fe5"},
	}

	got, err := resolveOrphanSessionID(sessionDir, orphan)
	require.Error(t, err, "corrupt meta.json must refuse to resolve, not mint a fresh ID")
	assert.Empty(t, got)
}

// TestResolveOrphanSessionID_NoIDAnywhereMintsExactlyOne covers the
// genuinely-legacy case: no ledger meta.json and no header SessionID (a
// recording from before SessionID-at-birth minting). Minting is the only
// correct behavior here, and it must produce a valid, well-formed ID.
func TestResolveOrphanSessionID_NoIDAnywhereMintsExactlyOne(t *testing.T) {
	sessionDir := t.TempDir()
	orphan := orphanedSession{
		SessionName: "legacy-session",
		Meta:        &session.StoreMeta{AgentID: "OxLEG1"}, // no SessionID
	}

	got, err := resolveOrphanSessionID(sessionDir, orphan)
	require.NoError(t, err)
	assert.True(t, sessionid.IsValidSessionID(got), "must mint a valid ses_<UUIDv7> when no ID exists anywhere, got %q", got)
}

// TestResolveOrphanSessionID_RegisteredTwiceYieldsOneID reproduces the
// exact production shape: the same session_name/created_at is retried
// twice (e.g. the first retry wrote meta.json locally but the push failed,
// and a later `ox doctor` run retries again). Both calls must resolve to
// the SAME session_id.
func TestResolveOrphanSessionID_RegisteredTwiceYieldsOneID(t *testing.T) {
	sessionDir := t.TempDir()
	orphan := orphanedSession{
		SessionName: "2026-05-08T19-25-ajit-OxF9dp",
		Meta:        &session.StoreMeta{AgentID: "OxF9dp"}, // legacy: no header ID either
	}

	// first registration: nothing anywhere yet — mints ID_A and (as
	// retrySessionUpload does in production) writes it to meta.json before
	// attempting the push.
	first, err := resolveOrphanSessionID(sessionDir, orphan)
	require.NoError(t, err)
	require.True(t, sessionid.IsValidSessionID(first))
	require.NoError(t, lfs.WriteSessionMeta(sessionDir, &lfs.SessionMeta{
		Version:     "1.0",
		SessionName: orphan.SessionName,
		SessionID:   first,
		CreatedAt:   time.Now(),
	}))

	// second registration: same session, same cache content, retried again
	// (push failed the first time). Must return the SAME id, not mint a
	// second one.
	second, err := resolveOrphanSessionID(sessionDir, orphan)
	require.NoError(t, err)
	assert.Equal(t, first, second, "retrying the same session must yield one durable session_id, not two")
}
