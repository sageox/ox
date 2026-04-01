//go:build !short

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/agentx"
)

func TestCheckAgentsIntegration_NoFiles(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	result := checkAgentsIntegrationWithFix(false)

	if result.passed {
		t.Error("expected passed=false when no agent files exist")
	}
	if result.name != "ox agent prime integration" {
		t.Errorf("unexpected name: %s", result.name)
	}
	if result.message != "not configured" {
		t.Errorf("unexpected message: %s", result.message)
	}
	if !strings.Contains(result.detail, "ox init") {
		t.Errorf("expected detail to suggest 'ox init', got: %s", result.detail)
	}
}

func TestCheckAgentsIntegration_AgentsMdExists(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create AGENTS.md with both header and footer markers (canonical format)
	content := OxPrimeCheckBlock + "\n# Project Instructions\n\n" + OxPrimeLine + "\n"
	if err := os.WriteFile(filepath.Join(gitRoot, "AGENTS.md"), []byte(content), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	result := checkAgentsIntegrationWithFix(false)

	if !result.passed {
		t.Errorf("expected passed=true when AGENTS.md has OxPrimeLine, got: %+v", result)
	}
	if !strings.Contains(result.name, "AGENTS.md") {
		t.Errorf("expected name to mention AGENTS.md, got: %s", result.name)
	}
	if result.message != "canonical" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckAgentsIntegration_MissingPrimeLine(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create AGENTS.md without OxPrimeLine
	content := "# Project Instructions\n\nSome other content\n"
	if err := os.WriteFile(filepath.Join(gitRoot, "AGENTS.md"), []byte(content), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	result := checkAgentsIntegrationWithFix(false)

	if result.passed {
		t.Error("expected passed=false when OxPrimeLine is missing")
	}
	if result.name != "ox agent prime integration" {
		t.Errorf("unexpected name: %s", result.name)
	}
	if result.message != "not configured" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckAgentsIntegration_FixInjectsIntoExistingAgentsMd(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create AGENTS.md without OxPrimeLine
	originalContent := "# Project Instructions\n\nSome content\n"
	agentsMdPath := filepath.Join(gitRoot, "AGENTS.md")
	if err := os.WriteFile(agentsMdPath, []byte(originalContent), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	result := checkAgentsIntegrationWithFix(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}
	if !strings.Contains(result.name, "AGENTS.md") {
		t.Errorf("expected name to mention AGENTS.md, got: %s", result.name)
	}
	if result.message != "injected" {
		t.Errorf("unexpected message: %s", result.message)
	}

	// verify OxPrimeLine was injected
	content, err := os.ReadFile(agentsMdPath)
	if err != nil {
		t.Fatalf("failed to read AGENTS.md: %v", err)
	}

	if !strings.Contains(string(content), OxPrimeLine) {
		t.Error("expected AGENTS.md to contain OxPrimeLine after fix")
	}

	// verify original content is preserved
	if !strings.Contains(string(content), "Some content") {
		t.Error("original content should be preserved")
	}
}

func TestCheckAgentsIntegration_LegacyFormat(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create CLAUDE.md with legacy format
	content := "# Project Instructions\n\nRun ox agent prime on session start\n"
	if err := os.WriteFile(filepath.Join(gitRoot, "CLAUDE.md"), []byte(content), 0644); err != nil {
		t.Fatalf("failed to create CLAUDE.md: %v", err)
	}

	result := checkAgentsIntegrationWithFix(false)

	if !result.passed {
		t.Error("expected passed=true for legacy format (with warning)")
	}
	if !result.warning {
		t.Error("expected warning=true for legacy format")
	}
	if result.message != "legacy format" {
		t.Errorf("unexpected message: %s", result.message)
	}
	if !strings.Contains(result.detail, "ox doctor --fix") {
		t.Errorf("expected detail to suggest 'ox doctor --fix', got: %s", result.detail)
	}
}

func TestCheckAgentsIntegration_ClaudeMdExists(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create CLAUDE.md with both header and footer markers (canonical format)
	content := OxPrimeCheckBlock + "\n# Project Instructions\n\n" + OxPrimeLine + "\n"
	if err := os.WriteFile(filepath.Join(gitRoot, "CLAUDE.md"), []byte(content), 0644); err != nil {
		t.Fatalf("failed to create CLAUDE.md: %v", err)
	}

	result := checkAgentsIntegrationWithFix(false)

	if !result.passed {
		t.Errorf("expected passed=true when CLAUDE.md has OxPrimeLine, got: %+v", result)
	}
	if !strings.Contains(result.name, "CLAUDE.md") {
		t.Errorf("expected name to mention CLAUDE.md, got: %s", result.name)
	}
	if result.message != "canonical" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckAgentsIntegration_FixInjectsIntoClaude(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create CLAUDE.md without OxPrimeLine
	originalContent := "# Project Instructions\n\nSome content\n"
	claudeMdPath := filepath.Join(gitRoot, "CLAUDE.md")
	if err := os.WriteFile(claudeMdPath, []byte(originalContent), 0644); err != nil {
		t.Fatalf("failed to create CLAUDE.md: %v", err)
	}

	result := checkAgentsIntegrationWithFix(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}
	if result.message != "injected" {
		t.Errorf("unexpected message: %s", result.message)
	}

	// verify OxPrimeLine was injected
	content, err := os.ReadFile(claudeMdPath)
	if err != nil {
		t.Fatalf("failed to read CLAUDE.md: %v", err)
	}

	if !strings.Contains(string(content), OxPrimeLine) {
		t.Error("expected CLAUDE.md to contain OxPrimeLine after fix")
	}
}

// TestCheckAgentFileExists tests checkAgentFileExists function

func TestCheckAgentFileExists_AGENTSMdExists(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("# Agent Instructions\n"), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	result := checkAgentFileExists()

	if !result.passed {
		t.Errorf("expected passed=true when AGENTS.md exists, got: %+v", result)
	}
	if result.message != "AGENTS.md" {
		t.Errorf("expected message='AGENTS.md', got: %s", result.message)
	}
}

func TestCheckAgentFileExists_CLAUDEMdExists(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	claudePath := filepath.Join(gitRoot, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte("# Project Instructions\n"), 0644); err != nil {
		t.Fatalf("failed to create CLAUDE.md: %v", err)
	}

	result := checkAgentFileExists()

	if !result.passed {
		t.Errorf("expected passed=true when CLAUDE.md exists, got: %+v", result)
	}
	if result.message != "CLAUDE.md" {
		t.Errorf("expected message='CLAUDE.md', got: %s", result.message)
	}
}

func TestCheckAgentFileExists_CopilotInstructionsExists(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	copilotPath := filepath.Join(gitRoot, ".copilot-instructions.md")
	if err := os.WriteFile(copilotPath, []byte("# Copilot Instructions\n"), 0644); err != nil {
		t.Fatalf("failed to create .copilot-instructions.md: %v", err)
	}

	result := checkAgentFileExists()

	if !result.passed {
		t.Errorf("expected passed=true when .copilot-instructions.md exists, got: %+v", result)
	}
	if result.message != ".copilot-instructions.md" {
		t.Errorf("expected message='.copilot-instructions.md', got: %s", result.message)
	}
}

func TestDetectGemini_WithProjectConfig(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	geminiDir := filepath.Join(gitRoot, ".gemini")
	if err := os.MkdirAll(geminiDir, 0755); err != nil {
		t.Fatalf("failed to create .gemini: %v", err)
	}

	detected := detectGemini()

	if !detected {
		t.Error("expected detectGemini()=true when .gemini directory exists")
	}
}

func TestDetectCodePuppy_WithProjectConfig(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	codePuppyDir := filepath.Join(gitRoot, ".code_puppy")
	if err := os.MkdirAll(codePuppyDir, 0755); err != nil {
		t.Fatalf("failed to create .code_puppy: %v", err)
	}

	detected := detectCodePuppy()

	if !detected {
		t.Error("expected detectCodePuppy()=true when .code_puppy directory exists")
	}
}

// TestCheckAgentsIntegration_LegacyPatterns tests legacy pattern detection
func TestCheckAgentsIntegration_LegacyOxAgentPrime(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	content := "# Project Instructions\n\nRun ox agent prime at session start\n"
	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	result := checkAgentsIntegration()

	if !result.warning {
		t.Error("expected warning=true for legacy pattern")
	}
	if !strings.Contains(result.message, "legacy") {
		t.Errorf("expected message to mention 'legacy', got: %s", result.message)
	}
	if !strings.Contains(result.detail, "ox doctor --fix") {
		t.Errorf("expected detail to suggest fix, got: %s", result.detail)
	}
}

func TestCheckAgentsIntegration_LegacyOxPrime(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	content := "# Instructions\n\nRun ox prime for infrastructure superpowers\n"
	claudePath := filepath.Join(gitRoot, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create CLAUDE.md: %v", err)
	}

	result := checkAgentsIntegration()

	if !result.warning {
		t.Error("expected warning=true for legacy 'ox prime' pattern")
	}
	if !strings.Contains(result.message, "legacy") {
		t.Errorf("expected message to mention 'legacy', got: %s", result.message)
	}
}

func TestCheckAgentsIntegration_CopilotInstructions(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	content := "# Copilot Instructions\n\n" + OxPrimeLine + "\n"
	copilotPath := filepath.Join(gitRoot, ".copilot-instructions.md")
	if err := os.WriteFile(copilotPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create .copilot-instructions.md: %v", err)
	}

	result := checkAgentsIntegration()

	if !result.passed {
		t.Errorf("expected passed=true when .copilot-instructions.md has OxPrimeLine, got: %+v", result)
	}
	if !strings.Contains(result.name, ".copilot-instructions.md") {
		t.Errorf("expected check name to contain '.copilot-instructions.md', got: %s", result.name)
	}
}

// TestCheckAgentHooks tests the checkAgentHooks function
func TestCheckAgentHooks_NotDetected(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	agent := &mockAgent{
		detectProject: false,
		detectCLI:     false,
	}

	result := checkAgentHooks(agent, "TestAgent", false)

	if !result.skipped {
		t.Error("expected skipped=true when agent not detected")
	}
	if result.name != "TestAgent hooks" {
		t.Errorf("expected name='TestAgent hooks', got: %s", result.name)
	}
	if result.message != "TestAgent not detected" {
		t.Errorf("expected message='TestAgent not detected', got: %s", result.message)
	}
}

func TestCheckAgentHooks_ProjectInstalled(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	agent := &mockAgent{
		detectProject: true,
		hasHooks:      true,
	}

	result := checkAgentHooks(agent, "TestAgent", false)

	if !result.passed {
		t.Errorf("expected passed=true when hooks are installed, got: %+v", result)
	}
	if !strings.Contains(result.message, "installed") {
		t.Errorf("expected message to contain 'installed', got: %s", result.message)
	}
	if !strings.Contains(result.message, "project") {
		t.Errorf("expected message to contain 'project', got: %s", result.message)
	}
}

func TestCheckAgentHooks_UserInstalled(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	agent := &mockAgent{
		detectProject: true,
		hasHooks:      false,
		hasUserHooks:  true,
	}

	result := checkAgentHooks(agent, "TestAgent", false)

	if !result.passed {
		t.Errorf("expected passed=true when user-level hooks are installed, got: %+v", result)
	}
	if !strings.Contains(result.message, "installed") {
		t.Errorf("expected message to contain 'installed', got: %s", result.message)
	}
	if !strings.Contains(result.message, "user") {
		t.Errorf("expected message to contain 'user', got: %s", result.message)
	}
}

func TestCheckAgentHooks_ProjectDetectedNotInstalled(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	agent := &mockAgent{
		detectProject: true,
		hasHooks:      false,
	}

	result := checkAgentHooks(agent, "TestAgent", false)

	if result.passed {
		t.Error("expected passed=false when project detected but hooks not installed")
	}
	if result.message != "not installed" {
		t.Errorf("expected message='not installed', got: %s", result.message)
	}
	if !strings.Contains(result.detail, "ox hooks install") {
		t.Errorf("expected detail to suggest installation command, got: %s", result.detail)
	}
	if !strings.Contains(result.detail, "ox doctor --fix") {
		t.Errorf("expected detail to mention ox doctor --fix, got: %s", result.detail)
	}
}

func TestCheckAgentHooks_CLIOnlyDetected(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	agent := &mockAgent{
		detectProject: false,
		detectCLI:     true,
		hasHooks:      false,
	}

	result := checkAgentHooks(agent, "TestAgent", false)

	if !result.skipped {
		t.Error("expected skipped=true when only CLI detected (no project config)")
	}
	if !strings.Contains(result.message, "CLI detected") {
		t.Errorf("expected message to mention CLI detected, got: %s", result.message)
	}
	if !strings.Contains(result.message, "no project config") {
		t.Errorf("expected message to mention no project config, got: %s", result.message)
	}
	if !strings.Contains(result.detail, "ox hooks install") {
		t.Errorf("expected detail to suggest installation, got: %s", result.detail)
	}
}

func TestCheckAgentHooks_FixInstalls(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	agent := &mockAgent{
		detectProject: true,
		hasHooks:      false,
		installCalled: false,
	}

	result := checkAgentHooks(agent, "TestAgent", true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}
	if !agent.installCalled {
		t.Error("expected Install to be called when fix=true")
	}
	if !strings.Contains(result.message, "installed") {
		t.Errorf("expected message to contain 'installed', got: %s", result.message)
	}
}

// additional edge case tests for checkAgentFileExists

func TestCheckAgentFileExists_BothAGENTSAndCLAUDE(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create both files
	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	claudePath := filepath.Join(gitRoot, "CLAUDE.md")
	if err := os.WriteFile(agentsPath, []byte("# Agent Instructions\n"), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}
	if err := os.WriteFile(claudePath, []byte("# Project Instructions\n"), 0644); err != nil {
		t.Fatalf("failed to create CLAUDE.md: %v", err)
	}

	result := checkAgentFileExists()

	if !result.passed {
		t.Errorf("expected passed=true when both files exist, got: %+v", result)
	}
	// should prefer AGENTS.md
	if result.message != "AGENTS.md" {
		t.Errorf("expected message='AGENTS.md' (preferred), got: %s", result.message)
	}
}

func TestCheckAgentFileExists_AllThreeFiles(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	files := map[string]string{
		"AGENTS.md":                "# Agent Instructions\n",
		"CLAUDE.md":                "# Project Instructions\n",
		".copilot-instructions.md": "# Copilot Instructions\n",
	}

	for name, content := range files {
		path := filepath.Join(gitRoot, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("failed to create %s: %v", name, err)
		}
	}

	result := checkAgentFileExists()

	if !result.passed {
		t.Errorf("expected passed=true when all files exist, got: %+v", result)
	}
	// should prefer AGENTS.md first
	if result.message != "AGENTS.md" {
		t.Errorf("expected message='AGENTS.md' (highest priority), got: %s", result.message)
	}
}

func TestCheckAgentFileExists_Symlink(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create actual file
	actualPath := filepath.Join(gitRoot, "actual_agents.md")
	if err := os.WriteFile(actualPath, []byte("# Agent Instructions\n"), 0644); err != nil {
		t.Fatalf("failed to create actual file: %v", err)
	}

	// create symlink
	symlinkPath := filepath.Join(gitRoot, "AGENTS.md")
	if err := os.Symlink(actualPath, symlinkPath); err != nil {
		t.Skipf("unable to create symlink (may not be supported): %v", err)
	}

	result := checkAgentFileExists()

	if !result.passed {
		t.Errorf("expected passed=true when symlink to agent file exists, got: %+v", result)
	}
	if result.message != "AGENTS.md" {
		t.Errorf("expected message='AGENTS.md', got: %s", result.message)
	}
}

func TestCheckAgentFileExists_EmptyFile(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create empty AGENTS.md
	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	result := checkAgentFileExists()

	// should still pass - checkAgentFileExists only checks existence, not content
	if !result.passed {
		t.Errorf("expected passed=true even with empty file, got: %+v", result)
	}
}

// additional edge case tests for checkAgentsIntegrationWithFix

func TestCheckAgentsIntegration_PartialOxPrimeMatch(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create AGENTS.md with text containing "ox" and "prime" but not as command
	content := "# Instructions\n\nThis is an ox for priming the database\n"
	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	result := checkAgentsIntegration()

	// should not detect partial matches
	if result.passed {
		t.Error("expected passed=false for partial word matches")
	}
}

func TestCheckAgentsIntegration_LegacyWithTypo(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create with typo - should not match
	content := "# Instructions\n\nRun ox agnt prime on session start\n"
	claudePath := filepath.Join(gitRoot, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create CLAUDE.md: %v", err)
	}

	result := checkAgentsIntegration()

	if result.passed {
		t.Error("expected passed=false when legacy pattern has typo")
	}
}

func TestCheckAgentsIntegration_CanonicalInCopilotInstructions(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	content := "# Copilot Instructions\n\n" + OxPrimeLine + "\n"
	copilotPath := filepath.Join(gitRoot, ".copilot-instructions.md")
	if err := os.WriteFile(copilotPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create .copilot-instructions.md: %v", err)
	}

	result := checkAgentsIntegrationWithFix(false)

	if !result.passed {
		t.Errorf("expected passed=true for canonical format in .copilot-instructions.md, got: %+v", result)
	}
	if !strings.Contains(result.name, ".copilot-instructions.md") {
		t.Errorf("expected name to mention .copilot-instructions.md, got: %s", result.name)
	}
}

func TestCheckAgentsIntegration_MultipleFilesFirstWins(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create AGENTS.md with canonical (both markers), CLAUDE.md with legacy
	agentsContent := OxPrimeCheckBlock + "\n# Agent Instructions\n\n" + OxPrimeLine + "\n"
	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte(agentsContent), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	claudeContent := "# Project Instructions\n\nRun ox agent prime on session start\n"
	claudePath := filepath.Join(gitRoot, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(claudeContent), 0644); err != nil {
		t.Fatalf("failed to create CLAUDE.md: %v", err)
	}

	result := checkAgentsIntegration()

	// should detect canonical in AGENTS.md first (array order)
	if !result.passed {
		t.Errorf("expected passed=true when canonical found in first file, got: %+v", result)
	}
	if !strings.Contains(result.name, "AGENTS.md") {
		t.Errorf("expected check to reference AGENTS.md, got: %s", result.name)
	}
	if result.warning {
		t.Error("expected warning=false when canonical format found")
	}
}

func TestCheckAgentsIntegration_CaseInsensitivePattern(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// test if patterns are case-sensitive (they should be)
	content := "# Instructions\n\nRun OX AGENT PRIME on session start\n"
	claudePath := filepath.Join(gitRoot, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create CLAUDE.md: %v", err)
	}

	result := checkAgentsIntegration()

	// patterns should be case-sensitive, so uppercase shouldn't match
	if result.passed || result.warning {
		t.Error("expected patterns to be case-sensitive, uppercase should not match")
	}
}

func TestCheckAgentsIntegration_FixNoExistingFiles(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// no agent files exist - fix should create AGENTS.md with the marker
	result := checkAgentsIntegrationWithFix(true)

	if !result.passed {
		t.Errorf("expected passed=true when fix creates AGENTS.md, got: %+v", result)
	}
	if result.message != "injected" {
		t.Errorf("expected message='injected', got: %s", result.message)
	}
	if !strings.Contains(result.name, "AGENTS.md") {
		t.Errorf("expected name to mention AGENTS.md, got: %s", result.name)
	}

	// verify AGENTS.md was created with the marker
	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	content, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("expected AGENTS.md to be created: %v", err)
	}
	if !strings.Contains(string(content), OxPrimeMarker) {
		t.Error("expected AGENTS.md to contain OxPrimeMarker")
	}
}

// additional edge case tests for checkAgentHooks (tiered detection)

func TestCheckAgentHooks_BothProjectAndUserInstalled(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	agent := &mockAgent{
		detectProject: true,
		hasHooks:      true,
		hasUserHooks:  true,
	}

	result := checkAgentHooks(agent, "TestAgent", false)

	if !result.passed {
		t.Errorf("expected passed=true when both project and user hooks installed, got: %+v", result)
	}
	// should report project (takes precedence)
	if !strings.Contains(result.message, "project") {
		t.Errorf("expected message to mention 'project' (precedence), got: %s", result.message)
	}
}

func TestCheckAgentHooks_ProjectDetectedUserInstalled(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	agent := &mockAgent{
		detectProject: true,
		hasHooks:      false,
		hasUserHooks:  true,
	}

	result := checkAgentHooks(agent, "TestAgent", false)

	if !result.passed {
		t.Errorf("expected passed=true when user hooks installed, got: %+v", result)
	}
	if !strings.Contains(result.message, "user") {
		t.Errorf("expected message to mention 'user', got: %s", result.message)
	}
}

func TestCheckAgentHooks_FixErrorHandling(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	agent := &mockAgentWithError{
		detectProject: true,
	}

	result := checkAgentHooks(agent, "TestAgent", true)

	if result.passed {
		t.Error("expected passed=false when install fails")
	}
	if !strings.Contains(result.message, "install failed") {
		t.Errorf("expected message to mention install failed, got: %s", result.message)
	}
}

func TestCheckAgentHooks_CLIDetectedNoProjectConfig(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	agent := &mockAgent{
		detectProject: false,
		detectCLI:     true,
	}

	result := checkAgentHooks(agent, "TestAgent", false)

	// CLI-only detection should be skipped (info), not error
	if !result.skipped {
		t.Error("expected skipped=true for CLI-only detection")
	}
	if !strings.Contains(result.message, "CLI detected") {
		t.Errorf("expected message to mention CLI detected, got: %s", result.message)
	}
	if !strings.Contains(result.message, "no project config") {
		t.Errorf("expected message to mention no project config, got: %s", result.message)
	}
}

// TestUpgradeToCanonical tests the upgradeToCanonical function
func TestUpgradeToCanonical(t *testing.T) {
	tests := []struct {
		name            string
		content         string
		wantRemoved     string
		wantError       bool
		wantModified    bool
		verifyCanonical bool
	}{
		{
			name:            "legacy ox agent prime",
			content:         "# Instructions\n\nRun ox agent prime at session start\n\nMore content\n",
			wantRemoved:     "Run ox agent prime at session start",
			wantModified:    true,
			verifyCanonical: true,
		},
		{
			name:            "legacy ox prime",
			content:         "# Instructions\n\nUse ox prime for infrastructure guidance\n",
			wantRemoved:     "Use ox prime for infrastructure guidance",
			wantModified:    true,
			verifyCanonical: true,
		},
		{
			name:            "already canonical",
			content:         "# Instructions\n\n" + OxPrimeLine + "\n",
			wantRemoved:     "",
			wantModified:    false,
			verifyCanonical: true,
		},
		{
			name:            "multiple legacy lines",
			content:         "# Instructions\n\nRun ox agent prime\n\nContent\n\nRun ox prime again\n",
			wantRemoved:     "Run ox agent prime",
			wantModified:    true,
			verifyCanonical: true,
		},
		{
			name:            "line with SageOx marker but not full canonical",
			content:         "# Instructions\n\n- **SageOx**: Run `ox agent prime` on session start\n",
			wantRemoved:     "",
			wantModified:    true,
			verifyCanonical: true,
		},
		{
			name:            "no ox prime references",
			content:         "# Instructions\n\nSome other content\n",
			wantRemoved:     "",
			wantModified:    true,
			verifyCanonical: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			testFile := filepath.Join(tmpDir, "AGENTS.md")

			if err := os.WriteFile(testFile, []byte(tt.content), 0644); err != nil {
				t.Fatalf("failed to create test file: %v", err)
			}

			originalContent := tt.content

			removedLine, err := upgradeToCanonical(testFile)

			if (err != nil) != tt.wantError {
				t.Errorf("upgradeToCanonical() error = %v, wantError %v", err, tt.wantError)
				return
			}

			if removedLine != tt.wantRemoved {
				t.Errorf("upgradeToCanonical() removedLine = %q, want %q", removedLine, tt.wantRemoved)
			}

			newContent, err := os.ReadFile(testFile)
			if err != nil {
				t.Fatalf("failed to read modified file: %v", err)
			}

			contentModified := string(newContent) != originalContent

			if contentModified != tt.wantModified {
				t.Errorf("content modified = %v, want %v", contentModified, tt.wantModified)
			}

			if tt.verifyCanonical {
				if !strings.Contains(string(newContent), OxPrimeLine) {
					t.Error("expected canonical OxPrimeLine in output")
				}
			}

			if tt.wantRemoved != "" {
				if strings.Contains(string(newContent), tt.wantRemoved) {
					t.Errorf("expected legacy line to be removed, but found: %q", tt.wantRemoved)
				}
			}
		})
	}
}

// TestCheckAgentsIntegrationWithFix_LegacyUpgrade tests doctor --fix with legacy format
// Note: This test may behave differently depending on whether it's run inside an agent context
func TestCheckAgentsIntegrationWithFix_LegacyUpgrade(t *testing.T) {
	tests := []struct {
		name        string
		filename    string
		content     string
		fix         bool
		wantPassed  bool
		wantWarning bool
	}{
		{
			name:        "legacy format without fix",
			filename:    "AGENTS.md",
			content:     "# Instructions\n\nRun ox agent prime at session start\n",
			fix:         false,
			wantPassed:  true,
			wantWarning: true,
		},
		{
			name:        "legacy format with fix",
			filename:    "AGENTS.md",
			content:     "# Instructions\n\nRun ox agent prime at session start\n",
			fix:         true,
			wantPassed:  true,
			wantWarning: false,
		},
		{
			name:        "legacy ox prime with fix",
			filename:    "CLAUDE.md",
			content:     "# Instructions\n\nUse ox prime for superpowers\n",
			fix:         true,
			wantPassed:  true,
			wantWarning: false,
		},
		{
			name:       "canonical format with fix is no-op",
			filename:   "AGENTS.md",
			content:    "# Instructions\n\n" + OxPrimeLine + "\n",
			fix:        true,
			wantPassed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitRoot, cleanup := setupTempGitRepo(t)
			defer cleanup()

			restoreCwd := changeToDir(t, gitRoot)
			defer restoreCwd()

			testFile := filepath.Join(gitRoot, tt.filename)
			if err := os.WriteFile(testFile, []byte(tt.content), 0644); err != nil {
				t.Fatalf("failed to create test file: %v", err)
			}

			result := checkAgentsIntegrationWithFix(tt.fix)

			if result.passed != tt.wantPassed {
				t.Errorf("passed = %v, want %v (result: %+v)", result.passed, tt.wantPassed, result)
			}

			if result.warning != tt.wantWarning {
				t.Errorf("warning = %v, want %v", result.warning, tt.wantWarning)
			}

			// verify file has canonical format after fix (unless already canonical)
			// note: in agent context, instructions are output but file is not modified
			if tt.fix && !strings.Contains(tt.content, OxPrimeLine) {
				newContent, err := os.ReadFile(testFile)
				if err != nil {
					t.Fatalf("failed to read file after check: %v", err)
				}

				// check if agent context - if so, file won't be modified
				isAgentContext := agentx.IsAgentContext()
				hasCanonical := strings.Contains(string(newContent), OxPrimeLine)

				if !isAgentContext && !hasCanonical {
					t.Error("expected canonical OxPrimeLine after fix (outside agent context)")
				}
				// in agent context, file should NOT be modified
				if isAgentContext && hasCanonical && string(newContent) != tt.content {
					t.Error("file should not be modified in agent context (instructions output instead)")
				}
			}
		})
	}
}

// TestOutputAgentUpgradeInstructions tests outputAgentUpgradeInstructions function
func TestOutputAgentUpgradeInstructions(t *testing.T) {
	tests := []struct {
		name         string
		filename     string
		content      string
		wantPassed   bool
		wantMessage  string
		verifyOutput func(t *testing.T, output string)
	}{
		{
			name:        "legacy ox agent prime",
			filename:    "AGENTS.md",
			content:     "# Instructions\n\nRun ox agent prime at session start\n",
			wantPassed:  true,
			wantMessage: "agent instructions provided",
			verifyOutput: func(t *testing.T, output string) {
				if !strings.Contains(output, "AGENT INSTRUCTION") {
					t.Error("expected output to contain instruction header")
				}
				if !strings.Contains(output, "REMOVE") {
					t.Error("expected output to contain REMOVE section")
				}
				if !strings.Contains(output, "ADD") {
					t.Error("expected output to contain ADD section")
				}
				if !strings.Contains(output, "SageOx") {
					t.Error("expected output to contain SageOx content")
				}
				if !strings.Contains(output, "ox agent prime") {
					t.Error("expected output to show legacy line to remove")
				}
			},
		},
		{
			name:        "legacy ox prime",
			filename:    "CLAUDE.md",
			content:     "# Instructions\n\nUse ox prime for infrastructure\n",
			wantPassed:  true,
			wantMessage: "agent instructions provided",
			verifyOutput: func(t *testing.T, output string) {
				if !strings.Contains(output, "PLACEMENT HINTS") {
					t.Error("expected output to contain placement hints")
				}
				if !strings.Contains(output, "ox doctor") {
					t.Error("expected output to mention verification command")
				}
			},
		},
		{
			name:        "multiple legacy lines",
			filename:    "AGENTS.md",
			content:     "# Instructions\n\nRun ox agent prime\n\n**SageOx** guidance\n\nRun ox prime again\n",
			wantPassed:  true,
			wantMessage: "agent instructions provided",
			verifyOutput: func(t *testing.T, output string) {
				if !strings.Contains(output, "SageOx") {
					t.Error("expected output to identify SageOx related lines")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			testFile := filepath.Join(tmpDir, tt.filename)

			if err := os.WriteFile(testFile, []byte(tt.content), 0644); err != nil {
				t.Fatalf("failed to create test file: %v", err)
			}

			oldStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			result := outputAgentUpgradeInstructions(tt.filename, testFile, tt.content)

			w.Close()
			os.Stdout = oldStdout

			var buf strings.Builder
			io.Copy(&buf, r)
			output := buf.String()

			if result.passed != tt.wantPassed {
				t.Errorf("passed = %v, want %v", result.passed, tt.wantPassed)
			}

			if !strings.Contains(result.message, tt.wantMessage) {
				t.Errorf("message = %q, want to contain %q", result.message, tt.wantMessage)
			}

			if !strings.Contains(result.name, tt.filename) {
				t.Errorf("result name = %q, want to contain %q", result.name, tt.filename)
			}

			if tt.verifyOutput != nil {
				tt.verifyOutput(t, output)
			}
		})
	}
}

// TestCheckAgentsIntegrationWithFix_InAgentContext tests behavior in agent context
func TestCheckAgentsIntegrationWithFix_InAgentContext(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	content := "# Instructions\n\nRun ox agent prime at session start\n"
	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	t.Setenv("CLAUDE_CODE_SESSION_ID", "test-session-id")

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	result := checkAgentsIntegrationWithFix(true)

	w.Close()
	os.Stdout = oldStdout

	var buf strings.Builder
	io.Copy(&buf, r)
	output := buf.String()

	if !result.passed {
		t.Errorf("expected passed=true in agent context, got: %+v", result)
	}

	if !strings.Contains(result.message, "agent instructions provided") {
		t.Errorf("expected message about agent instructions, got: %q", result.message)
	}

	if !strings.Contains(output, "AGENT INSTRUCTION") {
		t.Error("expected agent upgrade instructions to be output")
	}

	newContent, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if string(newContent) != content {
		t.Error("file should not be modified in agent context")
	}
}
