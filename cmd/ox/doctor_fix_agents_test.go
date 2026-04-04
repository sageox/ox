//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/session/adapters"
)

// TestMergeGitignoreEntries_NoChanges verifies that when all required entries are present
// and there are no conflicts, mergeGitignoreEntries returns false for changed.
func TestMergeGitignoreEntries_NoChanges(t *testing.T) {
	existingContent := sageoxGitignoreContent

	merged, changed := mergeGitignoreEntries(existingContent)

	if changed {
		t.Errorf("expected changed=false when all required entries present, got changed=true")
	}

	if merged != existingContent {
		t.Errorf("content should not be modified when no changes needed")
	}
}

// TestCheckAgentsIntegration_FixInjects verifies fix=true injects ox agent prime
func TestCheckAgentsIntegration_FixInjects(t *testing.T) {
	gitRoot := testGitRepo(t)

	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("# Agent Instructions\n"), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	result := checkAgentsIntegrationWithFix(true)
	if !result.passed {
		t.Errorf("expected passed=true, got false: %s", result.message)
	}
	if result.message != "injected" {
		t.Errorf("expected message='injected', got %q", result.message)
	}

	content, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("failed to read AGENTS.md: %v", err)
	}

	if !strings.Contains(string(content), OxPrimeLine) {
		t.Error("expected AGENTS.md to contain OxPrimeLine after fix")
	}
}

// TestCheckClaudeCodeHooks_FixInstalls verifies fix=true installs project-level Claude Code hooks
func TestCheckClaudeCodeHooks_FixInstalls(t *testing.T) {
	gitRoot := testGitRepo(t)

	claudeDir := filepath.Join(gitRoot, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatalf("failed to create .claude: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	result := checkClaudeCodeHooks(true)
	if !result.passed {
		t.Errorf("expected passed=true, got false: %s", result.message)
	}
	if result.message != "installed (shared)" {
		t.Errorf("expected message='installed (shared)', got %q", result.message)
	}

	if !HasProjectClaudeHooks(gitRoot) {
		t.Error("expected project-level hooks to be installed")
	}
}

// TestCheckOpenCodeHooks_FixInstalls verifies fix=true installs OpenCode hooks
func TestCheckOpenCodeHooks_FixInstalls(t *testing.T) {
	gitRoot := testGitRepo(t)

	opencodeDir := filepath.Join(gitRoot, ".opencode")
	if err := os.MkdirAll(opencodeDir, 0755); err != nil {
		t.Fatalf("failed to create .opencode: %v", err)
	}

	// set up fake adapter binary so discovery finds it
	adapterDir := t.TempDir()
	createFakeAdapterWithHooks(t, adapterDir, "opencode", "0.1.0", "session", ".opencode")
	t.Setenv("OX_ADAPTER_PATH", adapterDir)
	adapters.Unregister("opencode") // clear stale entry so discovery re-registers from fake
	t.Cleanup(func() { adapters.Unregister("opencode") })

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	result := checkOpenCodeHooks(true)
	if !result.passed {
		t.Errorf("expected passed=true, got false: %s", result.message)
	}
	if strings.Contains(result.message, "failed") {
		t.Errorf("expected successful install, got %q", result.message)
	}
}

// TestCheckGeminiHooks_FixInstalls verifies fix=true installs Gemini hooks
func TestCheckGeminiHooks_FixInstalls(t *testing.T) {
	gitRoot := testGitRepo(t)

	geminiDir := filepath.Join(gitRoot, ".gemini")
	if err := os.MkdirAll(geminiDir, 0755); err != nil {
		t.Fatalf("failed to create .gemini: %v", err)
	}

	// set up fake adapter binary so discovery finds it
	adapterDir := t.TempDir()
	createFakeAdapterWithHooks(t, adapterDir, "gemini", "0.1.0", "session", ".gemini")
	t.Setenv("OX_ADAPTER_PATH", adapterDir)
	adapters.Unregister("gemini") // clear stale entry so discovery re-registers from fake
	t.Cleanup(func() { adapters.Unregister("gemini") })

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	result := checkGeminiHooks(true)
	if !result.passed {
		t.Errorf("expected passed=true, got false: %s", result.message)
	}
	if strings.Contains(result.message, "failed") {
		t.Errorf("expected successful install, got %q", result.message)
	}
}
