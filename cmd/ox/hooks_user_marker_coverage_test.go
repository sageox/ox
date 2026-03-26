package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/agentx"
)

func TestAgentDisplayName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		agentType agentx.AgentType
		wantEmpty bool
	}{
		{name: "claude code", agentType: agentx.AgentTypeClaudeCode, wantEmpty: false},
		{name: "cursor", agentType: agentx.AgentTypeCursor, wantEmpty: false},
		{name: "unknown agent falls back to string", agentType: agentx.AgentType("unknown-agent"), wantEmpty: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := agentDisplayName(tt.agentType)
			if tt.wantEmpty && got != "" {
				t.Errorf("expected empty string, got %q", got)
			}
			if !tt.wantEmpty && got == "" {
				t.Error("expected non-empty display name")
			}
		})
	}
}

func TestSupportedUserMarkerAgents(t *testing.T) {
	t.Parallel()

	result := supportedUserMarkerAgents()
	if result == "" {
		t.Error("expected non-empty supported agents string")
	}
	// should contain at least claude-code
	if !strings.Contains(result, string(agentx.AgentTypeClaudeCode)) {
		t.Errorf("expected result to contain claude-code, got %q", result)
	}
}

func TestBackupUserAgentFile_EmptyContent(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.md")

	// empty content should not create backup
	backupUserAgentFile(filePath, "")

	// verify no backup was created
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected no backup file for empty content, got %d files", len(entries))
	}
}

func TestBackupUserAgentFile_CreatesBackup(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "CLAUDE.md")
	content := "# My Claude Config\nSome content here"

	backupUserAgentFile(filePath, content)

	// verify backup was created with timestamp format
	timestamp := time.Now().Format("2006-01-02-15")
	expectedBackup := filePath + ".bak-" + timestamp

	backupContent, err := os.ReadFile(expectedBackup)
	if err != nil {
		t.Fatalf("expected backup file at %s, got error: %v", expectedBackup, err)
	}
	if string(backupContent) != content {
		t.Errorf("backup content mismatch: got %q, want %q", string(backupContent), content)
	}
}

func TestAgentUserContextFile_HasExpectedEntries(t *testing.T) {
	t.Parallel()

	// claude-code should be in the map
	if _, ok := agentUserContextFile[agentx.AgentTypeClaudeCode]; !ok {
		t.Error("expected claude-code in agentUserContextFile map")
	}

	// cursor should NOT be in the map (commented out per source)
	if _, ok := agentUserContextFile[agentx.AgentTypeCursor]; ok {
		t.Error("cursor should not be in agentUserContextFile (not yet supported)")
	}
}

func TestHasUserLevelAgentMarker_UnsupportedAgent(t *testing.T) {
	t.Parallel()

	// an agent type not in the map should return false
	result := hasUserLevelAgentMarker(agentx.AgentType("nonexistent-agent"))
	if result {
		t.Error("expected false for unsupported agent type")
	}
}
