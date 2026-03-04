package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
				writeTestRawJSONL(t, filepath.Join(dir, "raw.jsonl"))
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
				writeTestRawJSONL(t, filepath.Join(dir, "raw.jsonl"))
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
				writeTestRawJSONL(t, filepath.Join(dir, "raw.jsonl"))
			},
			expected: 1,
		},
		{
			name: "finds multiple orphans",
			setup: func(t *testing.T, cacheDir, ledgerDir string) {
				for _, name := range []string{"2026-01-15T10-30-ryan-Oxa1b2", "2026-01-15T11-00-ryan-Oxc3d4"} {
					dir := filepath.Join(cacheDir, name)
					os.MkdirAll(dir, 0755)
					writeTestRawJSONL(t, filepath.Join(dir, "raw.jsonl"))
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
		rawPath := filepath.Join(tmpDir, "raw.jsonl")
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
		if err == nil {
			t.Error("expected error for missing file")
		}
	})

	t.Run("corrupt header", func(t *testing.T) {
		tmpDir := t.TempDir()
		rawPath := filepath.Join(tmpDir, "raw.jsonl")
		os.WriteFile(rawPath, []byte("not json\n"), 0644)

		_, _, err := readCacheSessionMeta(rawPath)
		if err == nil {
			t.Error("expected error for corrupt header")
		}
	})

	t.Run("header only no footer", func(t *testing.T) {
		tmpDir := t.TempDir()
		rawPath := filepath.Join(tmpDir, "raw.jsonl")
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

		if _, err := os.Stat(filepath.Join(sessionDir, ".recording.json")); err == nil {
			continue
		}

		rawPath := filepath.Join(sessionDir, "raw.jsonl")
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
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte("this is not json\n"), 0644))

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
	writeTestRawJSONL(t, filepath.Join(dir, "raw.jsonl"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), []byte(`{"agent_id":"OxActv"}`), 0644))

	orphans := scanCacheDirForOrphans(cacheDir, ledgerDir)
	assert.Empty(t, orphans,
		"session with .recording.json should be excluded even if raw.jsonl exists")
}
