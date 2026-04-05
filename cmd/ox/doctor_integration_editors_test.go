//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/session/adapters"
)

// TestDetectOtherAIEditors tests detectOtherAIEditors function
func TestDetectOtherAIEditors_OpenCode(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	opencodeDir := filepath.Join(gitRoot, ".opencode")
	if err := os.MkdirAll(opencodeDir, 0755); err != nil {
		t.Fatalf("failed to create .opencode: %v", err)
	}

	editors := detectOtherAIEditors()

	found := false
	for _, editor := range editors {
		if editor == "OpenCode" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("expected OpenCode in detected editors, got: %v", editors)
	}
}

func TestDetectOtherAIEditors_GeminiCLI(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	geminiDir := filepath.Join(gitRoot, ".gemini")
	if err := os.MkdirAll(geminiDir, 0755); err != nil {
		t.Fatalf("failed to create .gemini: %v", err)
	}

	editors := detectOtherAIEditors()

	found := false
	for _, editor := range editors {
		if editor == "Gemini CLI" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("expected Gemini CLI in detected editors, got: %v", editors)
	}
}

func TestDetectOtherAIEditors_Cursor(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	cursorDir := filepath.Join(gitRoot, ".cursor")
	if err := os.MkdirAll(cursorDir, 0755); err != nil {
		t.Fatalf("failed to create .cursor: %v", err)
	}

	editors := detectOtherAIEditors()

	found := false
	for _, editor := range editors {
		if editor == "Cursor" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("expected Cursor in detected editors, got: %v", editors)
	}
}

func TestDetectOtherAIEditors_Windsurf(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	windsurfDir := filepath.Join(gitRoot, ".windsurf")
	if err := os.MkdirAll(windsurfDir, 0755); err != nil {
		t.Fatalf("failed to create .windsurf: %v", err)
	}

	editors := detectOtherAIEditors()

	found := false
	for _, editor := range editors {
		if editor == "Windsurf" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("expected Windsurf in detected editors, got: %v", editors)
	}
}

func TestDetectOtherAIEditors_VSCode(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	vscodeDir := filepath.Join(gitRoot, ".vscode")
	if err := os.MkdirAll(vscodeDir, 0755); err != nil {
		t.Fatalf("failed to create .vscode: %v", err)
	}

	editors := detectOtherAIEditors()

	found := false
	for _, editor := range editors {
		if editor == "VSCode" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("expected VSCode in detected editors, got: %v", editors)
	}
}

func TestDetectOtherAIEditors_MultipleEditors(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	editorDirs := []string{".opencode", ".gemini"}
	for _, dir := range editorDirs {
		fullPath := filepath.Join(gitRoot, dir)
		if err := os.MkdirAll(fullPath, 0755); err != nil {
			t.Fatalf("failed to create %s: %v", dir, err)
		}
	}

	detected := detectOtherAIEditors()

	if len(detected) < 2 {
		t.Errorf("expected at least 2 editors, got: %v", detected)
	}

	expectedEditors := []string{"OpenCode", "Gemini CLI"}
	for _, expected := range expectedEditors {
		found := false
		for _, d := range detected {
			if d == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %s in detected editors, got: %v", expected, detected)
		}
	}
}

func TestDetectOtherAIEditors_NoEditors(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// just verify function doesn't panic (may detect user-level editors)
	_ = detectOtherAIEditors()
}

// TestCheckCodexIntegration_ProjectWithHooks verifies passed when hooks installed
func TestCheckCodexIntegration_ProjectWithHooks(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	codexDir := filepath.Join(gitRoot, ".codex")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatalf("failed to create .codex: %v", err)
	}

	// set up fake adapter binary so discovery finds it
	adapterDir := t.TempDir()
	createFakeAdapterWithHooks(t, adapterDir, "codex", "0.1.0", "session", ".codex")
	t.Setenv("OX_ADAPTER_PATH", adapterDir)
	adapters.Unregister("codex") // clear stale entry so discovery re-registers from fake
	t.Cleanup(func() { adapters.Unregister("codex") })

	// install hooks
	if err := installCodexHooks(false); err != nil {
		t.Fatalf("failed to install hooks: %v", err)
	}

	result := checkCodexHooks(false)

	if !result.passed {
		t.Errorf("expected passed=true when hooks installed, got: %+v", result)
	}
	if !strings.Contains(result.message, "installed") {
		t.Errorf("expected message to mention installed, got: %s", result.message)
	}
}

// TestCheckCodexIntegration_ProjectWithoutHooks verifies failed when project detected but no hooks
func TestCheckCodexIntegration_ProjectWithoutHooks(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	codexDir := filepath.Join(gitRoot, ".codex")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatalf("failed to create .codex: %v", err)
	}

	result := checkCodexHooks(false)

	if result.passed {
		t.Error("expected passed=false when .codex exists but no hooks installed")
	}
	if !strings.Contains(result.detail, "ox doctor --fix") {
		t.Errorf("expected detail to suggest ox doctor --fix, got: %s", result.detail)
	}
}

// TestCheckCodexIntegration_ProjectWithoutHooks_Fix verifies auto-fix installs hooks
func TestCheckCodexIntegration_ProjectWithoutHooks_Fix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	codexDir := filepath.Join(gitRoot, ".codex")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatalf("failed to create .codex: %v", err)
	}

	// set up fake adapter binary so discovery finds it
	adapterDir := t.TempDir()
	createFakeAdapterWithHooks(t, adapterDir, "codex", "0.1.0", "session", ".codex")
	t.Setenv("OX_ADAPTER_PATH", adapterDir)
	adapters.Unregister("codex") // clear stale entry so discovery re-registers from fake
	t.Cleanup(func() { adapters.Unregister("codex") })

	result := checkCodexHooks(true)

	if !result.passed {
		t.Errorf("expected passed=true after auto-fix, got: %+v", result)
	}

	// verify hooks were actually written
	hooksPath := filepath.Join(codexDir, "hooks.json")
	if _, err := os.Stat(hooksPath); os.IsNotExist(err) {
		t.Error("expected hooks.json to be created by auto-fix")
	}
}

// TestCheckCodexIntegration_NotDetected verifies skipped when no project found
// Failure prevented: doctor falsely reports hooks as installed when no .codex/ exists
func TestCheckCodexIntegration_NotDetected(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// no .codex/ directory — checkCodexHooks should skip or fail, never pass
	result := checkCodexHooks(false)

	if result.passed {
		t.Error("expected not passed when Codex not configured")
	}
	// without .codex/ dir, the check must be skipped (no project to check)
	if !result.skipped {
		t.Errorf("expected skipped=true when no .codex/ dir exists, got skipped=%v, message=%s", result.skipped, result.message)
	}
}

// TestDetect functions for various AI editors
func TestDetectCodex_WithProjectConfig(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	codexDir := filepath.Join(gitRoot, ".codex")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatalf("failed to create .codex: %v", err)
	}

	detected := detectCodex()

	if !detected {
		t.Error("expected detectCodex()=true when .codex directory exists")
	}
}

func TestDetectCodex_NotDetected(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// test project-level detection only — DetectCLI depends on host PATH
	agent := &CodexAgent{}
	detected := agent.DetectProject()

	if detected {
		t.Error("expected DetectProject()=false when .codex directory does not exist")
	}
}

func TestDetectOpenCode_WithProjectConfig(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	opencodeDir := filepath.Join(gitRoot, ".opencode")
	if err := os.MkdirAll(opencodeDir, 0755); err != nil {
		t.Fatalf("failed to create .opencode: %v", err)
	}

	detected := detectOpenCode()

	if !detected {
		t.Error("expected detectOpenCode()=true when .opencode directory exists")
	}
}

// additional edge case tests for checkOpenCodeHooks, checkGeminiHooks

func TestCheckOpenCodeHooks_ProjectConfigNoHooks(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .opencode directory (project detection)
	opencodeDir := filepath.Join(gitRoot, ".opencode")
	if err := os.MkdirAll(opencodeDir, 0755); err != nil {
		t.Fatalf("failed to create .opencode: %v", err)
	}

	result := checkOpenCodeHooks(false)

	// project detected but hooks not installed - should fail
	if result.passed {
		t.Error("expected passed=false when project config exists but hooks not installed")
	}
	if !strings.Contains(result.detail, "ox hooks install") {
		t.Errorf("expected detail to suggest installation, got: %s", result.detail)
	}
}

func TestCheckGeminiHooks_ProjectConfigNoHooks(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	geminiDir := filepath.Join(gitRoot, ".gemini")
	if err := os.MkdirAll(geminiDir, 0755); err != nil {
		t.Fatalf("failed to create .gemini: %v", err)
	}

	result := checkGeminiHooks(false)

	if result.passed {
		t.Error("expected passed=false when project config exists but hooks not installed")
	}
	if !strings.Contains(result.detail, "ox hooks install") {
		t.Errorf("expected detail to suggest installation, got: %s", result.detail)
	}
}

func TestCheckOpenCodeHooks_NotDetected(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// set minimal PATH to prevent detection of opencode CLI on dev machines
	t.Setenv("PATH", "/usr/bin:/bin")

	result := checkOpenCodeHooks(false)

	if !result.skipped {
		t.Error("expected skipped=true when OpenCode not detected")
	}
	if !strings.Contains(result.message, "not detected") {
		t.Errorf("expected message to mention 'not detected', got: %s", result.message)
	}
}

func TestCheckGeminiHooks_NotDetected(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// clamp PATH so host-installed gemini CLI doesn't cause false positive
	t.Setenv("PATH", "/usr/bin:/bin")

	result := checkGeminiHooks(false)

	if !result.skipped {
		t.Error("expected skipped=true when Gemini not detected")
	}
	if !strings.Contains(result.message, "not detected") {
		t.Errorf("expected message to mention 'not detected', got: %s", result.message)
	}
}

// additional edge case tests for detectOtherAIEditors

func TestDetectOtherAIEditors_AllEditors(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create all editor directories
	editorDirs := []string{".opencode", ".gemini", ".cursor", ".windsurf", ".vscode"}
	for _, dir := range editorDirs {
		fullPath := filepath.Join(gitRoot, dir)
		if err := os.MkdirAll(fullPath, 0755); err != nil {
			t.Fatalf("failed to create %s: %v", dir, err)
		}
	}

	detected := detectOtherAIEditors()

	if len(detected) < 5 {
		t.Errorf("expected at least 5 editors detected, got %d: %v", len(detected), detected)
	}

	expectedEditors := []string{"OpenCode", "Gemini CLI", "Cursor", "Windsurf", "VSCode"}
	for _, expected := range expectedEditors {
		found := false
		for _, d := range detected {
			if d == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %s in detected editors, got: %v", expected, detected)
		}
	}
}

func TestDetectOtherAIEditors_NoDuplicates(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create multiple config paths for same editor
	opencodeDir := filepath.Join(gitRoot, ".opencode")
	if err := os.MkdirAll(opencodeDir, 0755); err != nil {
		t.Fatalf("failed to create .opencode: %v", err)
	}

	detected := detectOtherAIEditors()

	// check for duplicates
	seen := make(map[string]bool)
	for _, editor := range detected {
		if seen[editor] {
			t.Errorf("duplicate editor detected: %s", editor)
		}
		seen[editor] = true
	}
}

func TestDetectOtherAIEditors_EmptyRepo(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// no editor directories
	detected := detectOtherAIEditors()

	// should return empty or only user-level editors (not error)
	// just verify it doesn't panic
	_ = detected
}
