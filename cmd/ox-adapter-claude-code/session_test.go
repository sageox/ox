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

// --- D. Symlink resolution ---

// TestFindSessionFile_SymlinkResolution verifies that findSessionFile resolves
// symlinks before hashing, so sessions stored under the real path are found
// when the caller passes a symlink path.
// Failure prevented: session lookup fails when repoRoot is a symlink.
func TestFindSessionFile_SymlinkResolution(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	realBase := t.TempDir()
	realRepo := filepath.Join(realBase, "Documents", "Code", "my-repo")
	if err := os.MkdirAll(realRepo, 0o755); err != nil {
		t.Fatal(err)
	}

	// create symlink: symlinkParent/Code -> realBase/Documents/Code
	symlinkParent := t.TempDir()
	symlinkTarget := filepath.Join(realBase, "Documents", "Code")
	symlinkPath := filepath.Join(symlinkParent, "Code")
	if err := os.Symlink(symlinkTarget, symlinkPath); err != nil {
		t.Fatal(err)
	}
	symlinkRepo := filepath.Join(symlinkParent, "Code", "my-repo")

	// resolve realRepo itself (macOS /var -> /private/var)
	realRepo, _ = filepath.EvalSymlinks(realRepo)

	// store session under the real path's hash
	projectHash := claudeProjectHash(realRepo)
	projectDir := filepath.Join(home, ".claude", "projects", projectHash)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := []byte(`{"type":"user","timestamp":"2026-04-02T10:30:00Z","message":{"role":"user","content":"hello"}}` + "\n")
	sessionFile := filepath.Join(projectDir, "symlink-test-session.jsonl")
	if err := os.WriteFile(sessionFile, content, 0o644); err != nil {
		t.Fatal(err)
	}

	// lookup via symlink path must succeed
	gotSymlink, _, err := findSessionFile(symlinkRepo, "", "", "")
	if err != nil {
		t.Fatalf("findSessionFile(symlink): %v", err)
	}

	// lookup via real path must succeed
	gotReal, _, err := findSessionFile(realRepo, "", "", "")
	if err != nil {
		t.Fatalf("findSessionFile(real): %v", err)
	}

	if gotSymlink != gotReal {
		t.Errorf("symlink and real paths returned different files:\n  symlink: %s\n  real:    %s", gotSymlink, gotReal)
	}
	if gotSymlink != sessionFile {
		t.Errorf("got %q, want %q", gotSymlink, sessionFile)
	}
}

// TestFindSessionFile_SymlinkResolution_DirectLookup verifies that direct
// lookup via agentSessionID also works through symlinks.
// Failure prevented: direct session ID lookup fails when repoRoot is a symlink.
func TestFindSessionFile_SymlinkResolution_DirectLookup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	realBase := t.TempDir()
	realRepo := filepath.Join(realBase, "Documents", "Code", "my-repo")
	if err := os.MkdirAll(realRepo, 0o755); err != nil {
		t.Fatal(err)
	}

	symlinkParent := t.TempDir()
	if err := os.Symlink(filepath.Join(realBase, "Documents", "Code"), filepath.Join(symlinkParent, "Code")); err != nil {
		t.Fatal(err)
	}
	symlinkRepo := filepath.Join(symlinkParent, "Code", "my-repo")

	// resolve realRepo itself (macOS /var -> /private/var)
	realRepo, _ = filepath.EvalSymlinks(realRepo)

	projectHash := claudeProjectHash(realRepo)
	projectDir := filepath.Join(home, ".claude", "projects", projectHash)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sessionID := "direct-symlink-session"
	content := []byte(`{"type":"user","timestamp":"2026-04-02T10:30:00Z","message":{"role":"user","content":"hello"}}` + "\n")
	sessionFile := filepath.Join(projectDir, sessionID+".jsonl")
	if err := os.WriteFile(sessionFile, content, 0o644); err != nil {
		t.Fatal(err)
	}

	got, _, err := findSessionFile(symlinkRepo, "", "", sessionID)
	if err != nil {
		t.Fatalf("findSessionFile(symlink, direct): %v", err)
	}
	if got != sessionFile {
		t.Errorf("got %q, want %q", got, sessionFile)
	}
}

// TestFindSessionFile_SymlinkResolution_MultiLevel verifies that chained
// symlinks (symlink -> symlink -> real dir) are fully resolved.
// Failure prevented: multi-hop symlinks break session discovery.
func TestFindSessionFile_SymlinkResolution_MultiLevel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	realDir := t.TempDir()
	realRepo := filepath.Join(realDir, "repo")
	if err := os.MkdirAll(realRepo, 0o755); err != nil {
		t.Fatal(err)
	}

	// symlink1 -> realDir
	symlink1Parent := t.TempDir()
	symlink1 := filepath.Join(symlink1Parent, "link1")
	if err := os.Symlink(realDir, symlink1); err != nil {
		t.Fatal(err)
	}

	// symlink2 -> symlink1
	symlink2Parent := t.TempDir()
	symlink2 := filepath.Join(symlink2Parent, "link2")
	if err := os.Symlink(symlink1, symlink2); err != nil {
		t.Fatal(err)
	}

	// resolve realRepo itself (macOS /var -> /private/var)
	realRepo, _ = filepath.EvalSymlinks(realRepo)

	projectHash := claudeProjectHash(realRepo)
	projectDir := filepath.Join(home, ".claude", "projects", projectHash)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := []byte(`{"type":"user","timestamp":"2026-04-02T10:30:00Z","message":{"role":"user","content":"hello"}}` + "\n")
	sessionFile := filepath.Join(projectDir, "multi-level.jsonl")
	if err := os.WriteFile(sessionFile, content, 0o644); err != nil {
		t.Fatal(err)
	}

	symlink1Repo := filepath.Join(symlink1, "repo")
	symlink2Repo := filepath.Join(symlink2, "repo")

	got1, _, err := findSessionFile(symlink1Repo, "", "", "")
	if err != nil {
		t.Fatalf("findSessionFile(symlink1): %v", err)
	}
	got2, _, err := findSessionFile(symlink2Repo, "", "", "")
	if err != nil {
		t.Fatalf("findSessionFile(symlink2): %v", err)
	}

	if got1 != sessionFile {
		t.Errorf("symlink1: got %q, want %q", got1, sessionFile)
	}
	if got2 != sessionFile {
		t.Errorf("symlink2: got %q, want %q", got2, sessionFile)
	}
}

// TestClaudeProjectHash_SymlinkEquivalence verifies that resolving symlinks
// before hashing produces identical hashes for symlink and real paths.
// Failure prevented: different hashes for the same physical directory.
func TestClaudeProjectHash_SymlinkEquivalence(t *testing.T) {
	realDir := t.TempDir()

	symlinkParent := t.TempDir()
	symlinkPath := filepath.Join(symlinkParent, "link")
	if err := os.Symlink(realDir, symlinkPath); err != nil {
		t.Fatal(err)
	}

	resolved1, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatal(err)
	}
	resolved2, err := filepath.EvalSymlinks(symlinkPath)
	if err != nil {
		t.Fatal(err)
	}

	hash1 := claudeProjectHash(resolved1)
	hash2 := claudeProjectHash(resolved2)

	if hash1 != hash2 {
		t.Errorf("hashes differ:\n  real:    %s -> %s\n  symlink: %s -> %s", resolved1, hash1, resolved2, hash2)
	}
}
