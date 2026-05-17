package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// --- A. Direct lookup via agent_session_id ---

// TestFindSessionFile_RejectsTraversalInAgentSessionID pins the trust-boundary
// fix for the AgentSessionID path-traversal class. AgentSessionID arrives via
// adapterprotocol JSON-RPC from the daemon; without validation, a malicious
// project's hook could pass "../../sensitive-export" and the adapter would
// read attacker-chosen files through the normal session pipeline.
//
// Failure prevented: SECREVIEW MEDIUM on cmd/ox-adapter-claude-code/session.go.
// The other 6 ox adapters already call adapterruntime.ValidateSessionID;
// claude-code was the lone outlier. This test pins that the call is present.
func TestFindSessionFile_RejectsTraversalInAgentSessionID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := "/tmp/test-repo"
	projectHash := claudeProjectHash(repoRoot)
	projectDir := filepath.Join(home, ".claude", "projects", projectHash)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Lay down a victim file outside projectDir that the traversal would target.
	// findSessionFile must NEVER return this path or stat-probe it successfully.
	victimDir := filepath.Join(home, "secret")
	if err := os.MkdirAll(victimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(victimDir, "exfil.jsonl")
	if err := os.WriteFile(victim, []byte(`{"type":"user","content":"victim"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []string{
		"../../secret/exfil", // would resolve to victim path with .jsonl suffix
		"a/b",                // contains separator
		"a\\b",               // backslash separator (windows shape)
		"foo/../bar",         // traversal disguised
	}
	for _, badID := range cases {
		t.Run(badID, func(t *testing.T) {
			got, _, err := findSessionFile(repoRoot, "", "", badID)
			if err == nil {
				t.Errorf("expected error for agentSessionID=%q, got path=%q", badID, got)
			}
			if got != "" {
				t.Errorf("expected empty path on rejection, got %q", got)
			}
		})
	}
}

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

// --- B2. Mtime filter boundary (regression: off-by-one and timing buffer) ---

// TestFindSessionFile_MtimeBoundary_ExactMatch verifies that a file whose mtime
// equals sinceTime is NOT rejected. Before the fix, strict After() comparison
// meant mtime == sinceTime was skipped.
// Failure prevented: session stop fails with "no sessions modified after ..." when
// the session file was created at the exact same second as StartedAt.
func TestFindSessionFile_MtimeBoundary_ExactMatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := "/tmp/test-repo-boundary"
	projectHash := claudeProjectHash(repoRoot)
	projectDir := filepath.Join(home, ".claude", "projects", projectHash)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sinceTime := time.Date(2026, 4, 8, 6, 30, 5, 0, time.UTC)

	sessionFile := filepath.Join(projectDir, "boundary-session.jsonl")
	content := []byte(`{"type":"user","timestamp":"2026-04-08T06:30:05Z","message":{"role":"user","content":"hello"}}` + "\n")
	if err := os.WriteFile(sessionFile, content, 0o644); err != nil {
		t.Fatal(err)
	}
	// set mtime to exactly sinceTime
	if err := os.Chtimes(sessionFile, sinceTime, sinceTime); err != nil {
		t.Fatal(err)
	}

	got, _, err := findSessionFile(repoRoot, "", sinceTime.Format(time.RFC3339), "")
	if err != nil {
		t.Fatalf("findSessionFile should find file at exact boundary, got error: %v", err)
	}
	if got != sessionFile {
		t.Errorf("got %q, want %q", got, sessionFile)
	}
}

// TestFindSessionFile_MtimeBuffer verifies that a file whose mtime is up to
// 30 seconds before sinceTime is still found (timing buffer for startup drift).
// Failure prevented: session file written slightly before StartedAt is rejected.
func TestFindSessionFile_MtimeBuffer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := "/tmp/test-repo-buffer"
	projectHash := claudeProjectHash(repoRoot)
	projectDir := filepath.Join(home, ".claude", "projects", projectHash)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sinceTime := time.Date(2026, 4, 8, 6, 30, 5, 0, time.UTC)

	// file mtime is 20 seconds before sinceTime — within the 30s buffer
	withinBuffer := filepath.Join(projectDir, "within-buffer.jsonl")
	content := []byte(`{"type":"user","timestamp":"2026-04-08T06:29:45Z","message":{"role":"user","content":"hello"}}` + "\n")
	if err := os.WriteFile(withinBuffer, content, 0o644); err != nil {
		t.Fatal(err)
	}
	mtime := sinceTime.Add(-20 * time.Second)
	if err := os.Chtimes(withinBuffer, mtime, mtime); err != nil {
		t.Fatal(err)
	}

	got, _, err := findSessionFile(repoRoot, "", sinceTime.Format(time.RFC3339), "")
	if err != nil {
		t.Fatalf("findSessionFile should find file within 30s buffer, got error: %v", err)
	}
	if got != withinBuffer {
		t.Errorf("got %q, want %q", got, withinBuffer)
	}

	// file mtime is exactly 30 seconds before sinceTime — at the buffer boundary
	// the buffer subtracts 30s, so mtime == (since - 30s) should pass (>= comparison)
	os.Remove(withinBuffer)
	atBoundary := filepath.Join(projectDir, "at-boundary.jsonl")
	if err := os.WriteFile(atBoundary, content, 0o644); err != nil {
		t.Fatal(err)
	}
	boundaryMtime := sinceTime.Add(-30 * time.Second)
	if err := os.Chtimes(atBoundary, boundaryMtime, boundaryMtime); err != nil {
		t.Fatal(err)
	}

	got, _, err = findSessionFile(repoRoot, "", sinceTime.Format(time.RFC3339), "")
	if err != nil {
		t.Fatalf("findSessionFile should find file at exact 30s buffer boundary, got error: %v", err)
	}
	if got != atBoundary {
		t.Errorf("got %q, want %q", got, atBoundary)
	}

	// file mtime is 60 seconds before sinceTime — outside the 30s buffer
	os.Remove(atBoundary)
	outsideBuffer := filepath.Join(projectDir, "outside-buffer.jsonl")
	if err := os.WriteFile(outsideBuffer, content, 0o644); err != nil {
		t.Fatal(err)
	}
	oldMtime := sinceTime.Add(-60 * time.Second)
	if err := os.Chtimes(outsideBuffer, oldMtime, oldMtime); err != nil {
		t.Fatal(err)
	}

	_, _, err = findSessionFile(repoRoot, "", sinceTime.Format(time.RFC3339), "")
	if err == nil {
		t.Fatal("findSessionFile should reject file outside 30s buffer")
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

// --- E. Tool extraction from content blocks ---

// TestParseAssistantEntry_ToolUseWithCallID verifies that tool_use blocks in
// assistant entries emit ToolUseWithID entries with the correlation ID.
// Failure prevented: tool call IDs dropped, breaking call/result correlation.
func TestParseAssistantEntry_ToolUseWithCallID(t *testing.T) {
	line := []byte(`{"type":"assistant","timestamp":"2026-04-07T10:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"Let me read that file."},{"type":"tool_use","id":"toolu_abc123","name":"Read","input":{"file_path":"/src/main.go"}}]}}`)

	entries, err := parseLine(line)
	if err != nil {
		t.Fatalf("parseLine: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}

	// first entry: text
	if entries[0].Role != adapterprotocol.RoleAssistant {
		t.Errorf("entry[0].Role = %q, want %q", entries[0].Role, adapterprotocol.RoleAssistant)
	}
	if entries[0].Content != "Let me read that file." {
		t.Errorf("entry[0].Content = %q, want %q", entries[0].Content, "Let me read that file.")
	}

	// second entry: tool_use with callID
	if entries[1].Role != adapterprotocol.RoleTool {
		t.Errorf("entry[1].Role = %q, want %q", entries[1].Role, adapterprotocol.RoleTool)
	}
	if entries[1].ToolName != "Read" {
		t.Errorf("entry[1].ToolName = %q, want %q", entries[1].ToolName, "Read")
	}
	if entries[1].CallID != "toolu_abc123" {
		t.Errorf("entry[1].CallID = %q, want %q", entries[1].CallID, "toolu_abc123")
	}

	// verify tool input is valid JSON
	var input map[string]interface{}
	if err := json.Unmarshal([]byte(entries[1].ToolInput), &input); err != nil {
		t.Fatalf("entry[1].ToolInput not valid JSON: %v", err)
	}
	if input["file_path"] != "/src/main.go" {
		t.Errorf("tool input file_path = %v, want /src/main.go", input["file_path"])
	}
}

// TestParseUserEntry_ToolResultExtracted verifies that tool_result blocks in
// user entries are emitted as ToolResult entries instead of being silently dropped.
// Failure prevented: tool results lost from captured sessions (the core bug).
func TestParseUserEntry_ToolResultExtracted(t *testing.T) {
	line := []byte(`{"type":"user","timestamp":"2026-04-07T10:00:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_abc123","content":"file contents here","is_error":false}]}}`)

	entries, err := parseLine(line)
	if err != nil {
		t.Fatalf("parseLine: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 (tool_result)", len(entries))
	}

	e := entries[0]
	if e.Role != adapterprotocol.RoleTool {
		t.Errorf("Role = %q, want %q", e.Role, adapterprotocol.RoleTool)
	}
	if e.ToolOutput != "file contents here" {
		t.Errorf("ToolOutput = %q, want %q", e.ToolOutput, "file contents here")
	}
	if e.CallID != "toolu_abc123" {
		t.Errorf("CallID = %q, want %q", e.CallID, "toolu_abc123")
	}
	if e.IsError {
		t.Error("IsError = true, want false")
	}
}

// TestParseUserEntry_MixedTextAndToolResult verifies that user entries with
// both text and tool_result blocks emit entries for both.
// Failure prevented: text lost when tool_result is present in same turn.
func TestParseUserEntry_MixedTextAndToolResult(t *testing.T) {
	line := []byte(`{"type":"user","timestamp":"2026-04-07T10:00:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_xyz","content":"output data","is_error":false},{"type":"text","text":"Now do this next thing"}]}}`)

	entries, err := parseLine(line)
	if err != nil {
		t.Fatalf("parseLine: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (user text + tool_result)", len(entries))
	}

	// should have a user text entry and a tool result entry
	hasUser := false
	hasTool := false
	for _, e := range entries {
		if e.Role == adapterprotocol.RoleUser && e.Content == "Now do this next thing" {
			hasUser = true
		}
		if e.Role == adapterprotocol.RoleTool && e.ToolOutput == "output data" {
			hasTool = true
		}
	}
	if !hasUser {
		t.Error("missing user text entry")
	}
	if !hasTool {
		t.Error("missing tool result entry")
	}
}

// TestParseUserEntry_ToolResultWithNestedContent verifies that tool_result
// blocks with nested content arrays (text sub-blocks) are properly extracted.
// Failure prevented: nested tool result content silently dropped.
func TestParseUserEntry_ToolResultWithNestedContent(t *testing.T) {
	line := []byte(`{"type":"user","timestamp":"2026-04-07T10:00:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_nested","content":[{"type":"text","text":"line 1"},{"type":"text","text":"line 2"}],"is_error":true}]}}`)

	entries, err := parseLine(line)
	if err != nil {
		t.Fatalf("parseLine: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}

	e := entries[0]
	if e.ToolOutput != "line 1\nline 2" {
		t.Errorf("ToolOutput = %q, want %q", e.ToolOutput, "line 1\nline 2")
	}
	if !e.IsError {
		t.Error("IsError = false, want true")
	}
}

// TestParseAssistantEntry_ToolUseWithoutCallID verifies fallback when
// tool_use blocks lack an id field.
// Failure prevented: panic or missing entry when id is absent.
func TestParseAssistantEntry_ToolUseWithoutCallID(t *testing.T) {
	line := []byte(`{"type":"assistant","timestamp":"2026-04-07T10:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`)

	entries, err := parseLine(line)
	if err != nil {
		t.Fatalf("parseLine: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}

	if entries[0].CallID != "" {
		t.Errorf("CallID = %q, want empty", entries[0].CallID)
	}
	if entries[0].ToolName != "Bash" {
		t.Errorf("ToolName = %q, want %q", entries[0].ToolName, "Bash")
	}
}
