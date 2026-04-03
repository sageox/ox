package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- A. Direct lookup via agent_session_id ---

// TestFindGeminiSession_DirectLookup verifies that providing an agent session ID
// finds the matching session-{id}.json file without scanning all candidates.
// Failure prevented: slow full-directory scan when the session ID is already known.
func TestFindGeminiSession_DirectLookup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	sessionID := "abc123"
	chatsDir := filepath.Join(home, ".gemini", "tmp", "project-hash", "chats")
	if err := os.MkdirAll(chatsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sessionFile := filepath.Join(chatsDir, "session-"+sessionID+".json")
	sessionJSON := `{"messages":[],"metadata":{"model":"gemini-pro","agentVersion":"1.0"}}`
	if err := os.WriteFile(sessionFile, []byte(sessionJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findGeminiSession("/tmp/repo", "", "", sessionID)
	if err != nil {
		t.Fatalf("findGeminiSession: %v", err)
	}
	if got != sessionFile {
		t.Errorf("got %q, want %q", got, sessionFile)
	}
}

// TestFindGeminiSession_DirectLookup_InvalidFallsBack verifies that a
// non-existent session ID gracefully falls back to timestamp scanning.
// Failure prevented: error instead of fallback on invalid session ID.
func TestFindGeminiSession_DirectLookup_InvalidFallsBack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	chatsDir := filepath.Join(home, ".gemini", "tmp", "project-hash", "chats")
	if err := os.MkdirAll(chatsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	realFile := filepath.Join(chatsDir, "session-real-id.json")
	sessionJSON := `{"messages":[],"metadata":{"model":"gemini-pro","agentVersion":"1.0"}}`
	if err := os.WriteFile(realFile, []byte(sessionJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findGeminiSession("/tmp/repo", "", "", "nonexistent-id")
	if err != nil {
		t.Fatalf("findGeminiSession: %v", err)
	}
	if got != realFile {
		t.Errorf("got %q, want %q (fallback)", got, realFile)
	}
}

// --- B. Timestamp-based fallback (existing behavior) ---

// TestFindGeminiSession_TimestampFallback verifies that an empty agent session
// ID preserves the existing most-recent-by-modtime behavior.
// Failure prevented: regression in existing session discovery.
func TestFindGeminiSession_TimestampFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	chatsDir := filepath.Join(home, ".gemini", "tmp", "project-hash", "chats")
	if err := os.MkdirAll(chatsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sessionJSON := `{"messages":[],"metadata":{"model":"gemini-pro","agentVersion":"1.0"}}`

	olderFile := filepath.Join(chatsDir, "session-older.json")
	newerFile := filepath.Join(chatsDir, "session-newer.json")
	if err := os.WriteFile(olderFile, []byte(sessionJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(olderFile, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newerFile, []byte(sessionJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findGeminiSession("/tmp/repo", "", "", "")
	if err != nil {
		t.Fatalf("findGeminiSession: %v", err)
	}
	if got != newerFile {
		t.Errorf("got %q, want %q (most recent)", got, newerFile)
	}
}
