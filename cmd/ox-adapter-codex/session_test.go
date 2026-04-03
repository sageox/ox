package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- A. Direct lookup via agent_session_id ---

// TestFindCodexSession_DirectLookup verifies that providing an agent session ID
// finds the file by matching the session_meta.id field in the JSONL.
// Failure prevented: slow scan of all date directories when the session ID is known.
func TestFindCodexSession_DirectLookup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	now := time.Now()
	dateDir := filepath.Join(home, ".codex", "sessions",
		now.Format("2006"), now.Format("01"), now.Format("02"))
	if err := os.MkdirAll(dateDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sessionID := "019d2c0d-ac3c-7b72-bb1d-0f246ad1f0d0"
	repoRoot := "/tmp/test-repo"
	sessionFile := filepath.Join(dateDir, "session-001.jsonl")
	content := fmt.Sprintf(
		`{"timestamp":"2026-04-02T10:00:00Z","type":"session_meta","payload":{"id":"%s","cwd":"%s","cli_version":"0.107.0"}}`+"\n",
		sessionID, repoRoot,
	)
	if err := os.WriteFile(sessionFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findCodexSession(repoRoot, "", "", sessionID)
	if err != nil {
		t.Fatalf("findCodexSession: %v", err)
	}
	if got != sessionFile {
		t.Errorf("got %q, want %q", got, sessionFile)
	}
}

// TestFindCodexSession_DirectLookup_InvalidFallsBack verifies that a
// non-matching session ID gracefully falls back to CWD-based scanning.
// Failure prevented: error instead of fallback when session ID doesn't match any file.
func TestFindCodexSession_DirectLookup_InvalidFallsBack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	now := time.Now()
	dateDir := filepath.Join(home, ".codex", "sessions",
		now.Format("2006"), now.Format("01"), now.Format("02"))
	if err := os.MkdirAll(dateDir, 0o755); err != nil {
		t.Fatal(err)
	}

	repoRoot := "/tmp/test-repo"
	sessionFile := filepath.Join(dateDir, "session-001.jsonl")
	content := fmt.Sprintf(
		`{"timestamp":"2026-04-02T10:00:00Z","type":"session_meta","payload":{"id":"real-id","cwd":"%s","cli_version":"0.107.0"}}`+"\n",
		repoRoot,
	)
	if err := os.WriteFile(sessionFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findCodexSession(repoRoot, "", "", "nonexistent-session-id")
	if err != nil {
		t.Fatalf("findCodexSession: %v", err)
	}
	if got != sessionFile {
		t.Errorf("got %q, want %q (fallback)", got, sessionFile)
	}
}

// --- B. Timestamp-based fallback (existing behavior) ---

// TestFindCodexSession_TimestampFallback verifies that an empty agent session ID
// preserves the existing CWD-match + most-recent-by-modtime behavior.
// Failure prevented: regression in existing session discovery.
func TestFindCodexSession_TimestampFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	now := time.Now()
	dateDir := filepath.Join(home, ".codex", "sessions",
		now.Format("2006"), now.Format("01"), now.Format("02"))
	if err := os.MkdirAll(dateDir, 0o755); err != nil {
		t.Fatal(err)
	}

	repoRoot := "/tmp/test-repo"

	olderFile := filepath.Join(dateDir, "session-older.jsonl")
	newerFile := filepath.Join(dateDir, "session-newer.jsonl")

	content := fmt.Sprintf(
		`{"timestamp":"2026-04-02T10:00:00Z","type":"session_meta","payload":{"id":"id1","cwd":"%s","cli_version":"0.107.0"}}`+"\n",
		repoRoot,
	)
	if err := os.WriteFile(olderFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(olderFile, past, past); err != nil {
		t.Fatal(err)
	}

	content2 := fmt.Sprintf(
		`{"timestamp":"2026-04-02T11:00:00Z","type":"session_meta","payload":{"id":"id2","cwd":"%s","cli_version":"0.107.0"}}`+"\n",
		repoRoot,
	)
	if err := os.WriteFile(newerFile, []byte(content2), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findCodexSession(repoRoot, "", "", "")
	if err != nil {
		t.Fatalf("findCodexSession: %v", err)
	}
	if got != newerFile {
		t.Errorf("got %q, want %q (most recent)", got, newerFile)
	}
}
