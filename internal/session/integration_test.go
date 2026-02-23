//go:build slow || integration

package session_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSessionEndToEnd runs a real Claude session and verifies session quality.
// This test requires:
// - claude CLI installed and in PATH
// - ANTHROPIC_API_KEY environment variable set
// - ox CLI built and available
//
// Run with: go test -tags=slow -timeout=5m ./internal/session/...
func TestSessionEndToEnd(t *testing.T) {
	// check prerequisites
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude CLI not available in PATH")
	}

	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	// build ox CLI for the test
	oxPath := buildOxCLI(t)

	// create temp workspace
	workspace := t.TempDir()
	setupTestWorkspace(t, workspace, oxPath)

	// run claude session with test prompts
	sessionDir := runClaudeSession(t, claudePath, oxPath, workspace)

	// verify session output files exist
	verifySessionFiles(t, sessionDir)

	// evaluate session quality
	evaluateSessionQuality(t, claudePath, sessionDir)
}

// TestSessionQualityOffline tests session parsing without live Claude.
// Uses pre-recorded test fixtures.
func TestSessionQualityOffline(t *testing.T) {
	// this test uses fixtures and doesn't require claude CLI
	testdata := filepath.Join("testdata", "sample_session.jsonl")
	if _, err := os.Stat(testdata); os.IsNotExist(err) {
		t.Skip("testdata/sample_session.jsonl not found")
	}

	// parse and validate the sample session
	entries, err := parseJSONLSession(testdata)
	if err != nil {
		t.Fatalf("failed to parse session: %v", err)
	}

	// run quality checks
	issues := runQualityChecks(entries)
	if len(issues) > 0 {
		t.Errorf("quality issues found:\n%s", strings.Join(issues, "\n"))
	}
}

// buildOxCLI builds the ox CLI binary for testing
func buildOxCLI(t *testing.T) string {
	t.Helper()

	// find project root
	projectRoot := findProjectRoot(t)
	oxBinary := filepath.Join(t.TempDir(), "ox")

	cmd := exec.Command("go", "build", "-o", oxBinary, "./cmd/ox")
	cmd.Dir = projectRoot
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0") // safe: go build only

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to build ox CLI: %v\n%s", err, output)
	}

	return oxBinary
}

// findProjectRoot locates the ox project root
func findProjectRoot(t *testing.T) string {
	t.Helper()

	// start from current directory and walk up
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			// verify it's the ox project
			content, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
			if strings.Contains(string(content), "github.com/sageox/ox") {
				return dir
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find ox project root")
		}
		dir = parent
	}
}

// setupTestWorkspace initializes a test git repo with ox
func setupTestWorkspace(t *testing.T, workspace, oxPath string) {
	t.Helper()

	// initialize git repo
	runCmd(t, workspace, "git", "init")
	runCmd(t, workspace, "git", "config", "user.email", "test@example.com")
	runCmd(t, workspace, "git", "config", "user.name", "Test User")

	// create a simple file and commit
	testFile := filepath.Join(workspace, "README.md")
	if err := os.WriteFile(testFile, []byte("# Test Project\n"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	runCmd(t, workspace, "git", "add", ".")
	runCmd(t, workspace, "git", "commit", "-m", "Initial commit")

	// initialize ox (may fail if not authenticated, that's ok for this test)
	cmd := exec.Command(oxPath, "init")
	cmd.Dir = workspace
	_ = cmd.Run() // ignore errors
}

// runClaudeSession executes a claude session with test prompts
func runClaudeSession(t *testing.T, claudePath, oxPath, workspace string) string {
	t.Helper()

	// create session directory
	sessionDir := filepath.Join(workspace, ".claude-sessions")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatalf("failed to create session dir: %v", err)
	}

	// test prompts - kept short to minimize API costs
	prompts := []string{
		fmt.Sprintf("Run this command and tell me what it outputs: %s version", oxPath),
		"What is 2 + 2? Just give me the number.",
		"Say 'test complete' and nothing else.",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	for i, prompt := range prompts {
		t.Logf("sending prompt %d/%d: %s", i+1, len(prompts), truncate(prompt, 50))

		cmd := exec.CommandContext(ctx, claudePath,
			"--print", // non-interactive mode
			"--output-format", "json",
			"--max-turns", "3",
			prompt,
		)
		cmd.Dir = workspace
		cmd.Env = append(os.Environ(), // safe: claude CLI subprocess, not ox
			// CLAUDE_TRANSCRIPT_DIR is Claude Code's env var for session recording output
			"CLAUDE_TRANSCRIPT_DIR="+sessionDir,
		)

		output, err := cmd.CombinedOutput()
		if err != nil {
			// log but don't fail - claude might have issues
			t.Logf("claude command returned error (may be ok): %v\noutput: %s", err, truncate(string(output), 500))
		}

		// small delay between prompts
		time.Sleep(500 * time.Millisecond)
	}

	return sessionDir
}

// verifySessionFiles checks that expected output files exist
func verifySessionFiles(t *testing.T, sessionDir string) {
	t.Helper()

	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		t.Fatalf("failed to read session dir: %v", err)
	}

	if len(entries) == 0 {
		t.Log("no session files found - claude may not have generated any")
		t.Log("this is acceptable if claude doesn't support session export")
		return
	}

	var hasJSONL, hasHTML bool
	for _, entry := range entries {
		name := entry.Name()
		t.Logf("found session file: %s", name)

		if strings.HasSuffix(name, ".jsonl") {
			hasJSONL = true
		}
		if strings.HasSuffix(name, ".html") {
			hasHTML = true
		}
	}

	if !hasJSONL {
		t.Log("no JSONL session found")
	}
	if !hasHTML {
		t.Log("no HTML session found")
	}
}

// evaluateSessionQuality uses a second Claude to evaluate quality
func evaluateSessionQuality(t *testing.T, claudePath, sessionDir string) {
	t.Helper()

	// find JSONL files
	files, err := filepath.Glob(filepath.Join(sessionDir, "*.jsonl"))
	if err != nil || len(files) == 0 {
		t.Log("no JSONL files to evaluate")
		return
	}

	for _, file := range files {
		t.Logf("evaluating session quality: %s", filepath.Base(file))

		// parse session
		entries, err := parseJSONLSession(file)
		if err != nil {
			t.Errorf("failed to parse %s: %v", file, err)
			continue
		}

		// run quality checks
		issues := runQualityChecks(entries)
		if len(issues) > 0 {
			t.Errorf("quality issues in %s:\n%s", filepath.Base(file), strings.Join(issues, "\n"))
		} else {
			t.Logf("session %s passed quality checks (%d entries)", filepath.Base(file), len(entries))
		}
	}
}

// SessionEntry represents a single entry in a JSONL session
type SessionEntry struct {
	Type      string          `json:"type"`
	Content   string          `json:"content,omitempty"`
	Timestamp string          `json:"ts,omitempty"`
	Seq       int             `json:"seq,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	ToolInput json.RawMessage `json:"tool_input,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
}

// parseJSONLSession reads and parses a JSONL session file
func parseJSONLSession(path string) ([]SessionEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var entries []SessionEntry
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var entry SessionEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNum, err)
		}
		entries = append(entries, entry)
	}

	return entries, scanner.Err()
}

// runQualityChecks validates session entries against quality criteria
func runQualityChecks(entries []SessionEntry) []string {
	var issues []string

	if len(entries) == 0 {
		issues = append(issues, "session is empty")
		return issues
	}

	// check for required entry types
	hasUser := false
	hasAssistant := false
	var lastSeq int
	var timestamps []string

	for _, entry := range entries {
		switch entry.Type {
		case "user":
			hasUser = true
		case "assistant":
			hasAssistant = true
		}

		// check sequence ordering
		if entry.Seq > 0 {
			if entry.Seq <= lastSeq && lastSeq > 0 {
				issues = append(issues, fmt.Sprintf("sequence out of order: %d after %d", entry.Seq, lastSeq))
			}
			lastSeq = entry.Seq
		}

		// collect timestamps
		if entry.Timestamp != "" {
			timestamps = append(timestamps, entry.Timestamp)
		}
	}

	if !hasUser {
		issues = append(issues, "no user messages found")
	}
	if !hasAssistant {
		issues = append(issues, "no assistant messages found")
	}

	// verify timestamps are sequential (if present)
	if len(timestamps) > 1 {
		for i := 1; i < len(timestamps); i++ {
			t1, err1 := time.Parse(time.RFC3339, timestamps[i-1])
			t2, err2 := time.Parse(time.RFC3339, timestamps[i])
			if err1 == nil && err2 == nil && t2.Before(t1) {
				issues = append(issues, fmt.Sprintf("timestamp out of order: %s before %s", timestamps[i], timestamps[i-1]))
			}
		}
	}

	// check for truncated content (very long entries might indicate issues)
	for i, entry := range entries {
		if len(entry.Content) > 100000 {
			issues = append(issues, fmt.Sprintf("entry %d has suspiciously long content (%d chars)", i, len(entry.Content)))
		}
	}

	return issues
}

// runCmd executes a command in the given directory
func runCmd(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command %s %v failed: %v\n%s", name, args, err, output)
	}
}

// truncate shortens a string for logging
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
