//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckUserLevelIntegration tests checkUserLevelIntegration function
func TestCheckUserLevelIntegration_Enabled(t *testing.T) {
	tmpHome := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", oldHome)
	t.Setenv("AGENT_ENV", "claude-code")

	claudeDir := filepath.Join(tmpHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatalf("failed to create .claude directory: %v", err)
	}

	claudeMdPath := filepath.Join(claudeDir, "CLAUDE.md")
	content := "# Global Instructions\n\n" + OxPrimeLine + "\n"
	if err := os.WriteFile(claudeMdPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write CLAUDE.md: %v", err)
	}

	result := checkUserLevelIntegration()

	if !result.passed {
		t.Errorf("expected passed=true when user-level ox prime is enabled, got: %+v", result)
	}
	if result.name != "Global ox prime" {
		t.Errorf("expected name='Global ox prime', got: %s", result.name)
	}
	if !strings.Contains(result.message, "enabled") {
		t.Errorf("expected message to contain 'enabled', got: %s", result.message)
	}
	if !strings.Contains(result.message, "CLAUDE.md") {
		t.Errorf("expected message to mention context file, got: %s", result.message)
	}
}

func TestCheckUserLevelIntegration_NotEnabled(t *testing.T) {
	tmpHome := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", oldHome)
	t.Setenv("AGENT_ENV", "claude-code")

	result := checkUserLevelIntegration()

	if !result.skipped {
		t.Error("expected skipped=true when user-level ox prime is not enabled")
	}
	if result.name != "Global ox prime" {
		t.Errorf("expected name='Global ox prime', got: %s", result.name)
	}
	if result.message != "not enabled" {
		t.Errorf("expected message='not enabled', got: %s", result.message)
	}
	if !strings.Contains(result.detail, "ox integrate install --user") {
		t.Errorf("expected detail to suggest installation command, got: %s", result.detail)
	}
}

func TestCheckUserLevelIntegration_WithCanonicalFormat(t *testing.T) {
	tmpHome := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", oldHome)
	t.Setenv("AGENT_ENV", "claude-code")

	claudeDir := filepath.Join(tmpHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatalf("failed to create .claude directory: %v", err)
	}

	claudeMdPath := filepath.Join(claudeDir, "CLAUDE.md")
	content := "# Global Instructions\n\n" + OxPrimeLine + "\n"
	if err := os.WriteFile(claudeMdPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write CLAUDE.md: %v", err)
	}

	result := checkUserLevelIntegration()

	if !result.passed {
		t.Errorf("expected passed=true with canonical format, got: %+v", result)
	}
}

func TestCheckUserLevelIntegration_EmptyCLAUDEMd(t *testing.T) {
	tmpHome := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", oldHome)
	t.Setenv("AGENT_ENV", "claude-code")

	claudeDir := filepath.Join(tmpHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatalf("failed to create .claude directory: %v", err)
	}

	claudeMdPath := filepath.Join(claudeDir, "CLAUDE.md")
	if err := os.WriteFile(claudeMdPath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to write CLAUDE.md: %v", err)
	}

	result := checkUserLevelIntegration()

	// empty file should not be detected
	if !result.skipped {
		t.Error("expected skipped=true when CLAUDE.md is empty")
	}
}

func TestCheckUserLevelIntegration_ClaudeDirNoFile(t *testing.T) {
	tmpHome := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", oldHome)
	t.Setenv("AGENT_ENV", "claude-code")

	// create .claude directory but no CLAUDE.md
	claudeDir := filepath.Join(tmpHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatalf("failed to create .claude directory: %v", err)
	}

	result := checkUserLevelIntegration()

	if !result.skipped {
		t.Error("expected skipped=true when CLAUDE.md doesn't exist")
	}
}
