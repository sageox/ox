package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- A. Direct lookup via agent_session_id ---

// TestFindSessionFile_DirectLookup verifies that providing an agent session ID
// skips timestamp scanning and returns the file directly.
// Failure prevented: slow directory scan when the session ID is already known.
func TestFindSessionFile_DirectLookup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := "/tmp/test-repo"
	projectHash := claudeProjectHash(repoRoot)
	projectDir := filepath.Join(home, ".claude", "projects", projectHash)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sessionID := "d8a6d16b-10fe-4c0f-865f-7e05b74e405d"
	sessionFile := filepath.Join(projectDir, sessionID+".jsonl")
	if err := os.WriteFile(sessionFile, []byte(`{"type":"user","timestamp":"2026-04-02T10:30:00Z","message":{"role":"user","content":"hello"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, _, err := findSessionFile(repoRoot, "", "", sessionID)
	if err != nil {
		t.Fatalf("findSessionFile: %v", err)
	}
	if got != sessionFile {
		t.Errorf("got %q, want %q", got, sessionFile)
	}
}

// TestFindSessionFile_DirectLookup_InvalidFallsBack verifies that an invalid
// agent session ID gracefully falls back to timestamp-based scanning.
// Failure prevented: error returned instead of fallback when session ID doesn't match a file.
func TestFindSessionFile_DirectLookup_InvalidFallsBack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := "/tmp/test-repo"
	projectHash := claudeProjectHash(repoRoot)
	projectDir := filepath.Join(home, ".claude", "projects", projectHash)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// create a session file with a different name
	realFile := filepath.Join(projectDir, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl")
	if err := os.WriteFile(realFile, []byte(`{"type":"user","timestamp":"2026-04-02T10:30:00Z","message":{"role":"user","content":"hello"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// use a non-existent session ID -- should fall back to most recent file
	got, _, err := findSessionFile(repoRoot, "", "", "nonexistent-session-id")
	if err != nil {
		t.Fatalf("findSessionFile: %v", err)
	}
	if got != realFile {
		t.Errorf("got %q, want %q (fallback to most recent)", got, realFile)
	}
}

// --- B. Timestamp-based fallback (existing behavior) ---

// TestFindSessionFile_TimestampFallback verifies that an empty agent session ID
// produces the same behavior as before the field was added.
// Failure prevented: regression in existing timestamp-based session discovery.
func TestFindSessionFile_TimestampFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := "/tmp/test-repo"
	projectHash := claudeProjectHash(repoRoot)
	projectDir := filepath.Join(home, ".claude", "projects", projectHash)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// create two session files with different mod times
	olderFile := filepath.Join(projectDir, "older-session.jsonl")
	newerFile := filepath.Join(projectDir, "newer-session.jsonl")

	content := []byte(`{"type":"user","timestamp":"2026-04-02T10:30:00Z","message":{"role":"user","content":"hello"}}` + "\n")
	if err := os.WriteFile(olderFile, content, 0o644); err != nil {
		t.Fatal(err)
	}
	// ensure newer file has a later mod time
	past := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(olderFile, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newerFile, content, 0o644); err != nil {
		t.Fatal(err)
	}

	// empty agent session ID should use timestamp scanning, returning the most recent
	got, _, err := findSessionFile(repoRoot, "", "", "")
	if err != nil {
		t.Fatalf("findSessionFile: %v", err)
	}
	if got != newerFile {
		t.Errorf("got %q, want %q (most recent by mod time)", got, newerFile)
	}
}

// --- C. Direct lookup with offset ---

// TestFindSessionFile_DirectLookup_RespectsOffset verifies that direct lookup
// still computes a start offset when the since parameter is provided.
// Failure prevented: offset always 0 when using direct lookup, missing incremental reads.
func TestFindSessionFile_DirectLookup_RespectsOffset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := "/tmp/test-repo"
	projectHash := claudeProjectHash(repoRoot)
	projectDir := filepath.Join(home, ".claude", "projects", projectHash)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sessionID := "offset-test-session"
	line1 := `{"type":"user","timestamp":"2026-04-02T09:00:00Z","message":{"role":"user","content":"old"}}` + "\n"
	line2 := `{"type":"user","timestamp":"2026-04-02T11:00:00Z","message":{"role":"user","content":"new"}}` + "\n"
	sessionFile := filepath.Join(projectDir, sessionID+".jsonl")
	if err := os.WriteFile(sessionFile, []byte(line1+line2), 0o644); err != nil {
		t.Fatal(err)
	}

	// since is between the two entries
	_, offset, err := findSessionFile(repoRoot, "", "2026-04-02T10:00:00Z", sessionID)
	if err != nil {
		t.Fatalf("findSessionFile: %v", err)
	}

	// offset should point past line1 (to the start of line2)
	expectedOffset := int64(len(line1))
	if offset != expectedOffset {
		t.Errorf("offset = %d, want %d", offset, expectedOffset)
	}
}
