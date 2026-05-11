//go:build !short

package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
)

// TestCheckSageoxFilesTracked_FixForceAdds verifies fix=true force adds untracked .sageox/ files
func TestCheckSageoxFilesTracked_FixForceAdds(t *testing.T) {
	gitRoot := testGitRepo(t)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	cfg := config.GetDefaultProjectConfig()
	if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# SageOx\n"), 0644); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	result := checkSageoxFilesTracked(true)
	if !result.passed {
		t.Errorf("expected passed=true, got false: %s", result.message)
	}
	if result.message != "fixed (added to VCS)" {
		t.Errorf("expected message='fixed (added to VCS)', got %q", result.message)
	}

	cmd := exec.Command("git", "ls-files", "--error-unmatch", ".sageox/config.json")
	cmd.Dir = gitRoot
	if err := cmd.Run(); err != nil {
		t.Error("expected config.json to be tracked after fix")
	}

	cmd = exec.Command("git", "ls-files", "--error-unmatch", ".sageox/README.md")
	cmd.Dir = gitRoot
	if err := cmd.Run(); err != nil {
		t.Error("expected README.md to be tracked after fix")
	}
}

// TestCheckAuthFilePermissions_FixSecures verifies fix=true fixes auth file permissions to 0600.
// Failure prevented: auth tokens stored world-readable after write.
func TestCheckAuthFilePermissions_FixSecures(t *testing.T) {
	tmpDir := t.TempDir()

	// override XDG config so checkAuthFilePermissions finds our test auth file
	// auth path resolves to $XDG_CONFIG_HOME/sageox/auth.json
	sageoxDir := filepath.Join(tmpDir, "sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create sageox config dir: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	authPath := filepath.Join(sageoxDir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"tokens":{}}`), 0644); err != nil {
		t.Fatalf("failed to create auth file: %v", err)
	}

	info, err := os.Stat(authPath)
	if err != nil {
		t.Fatalf("failed to stat auth file: %v", err)
	}
	if info.Mode().Perm() == 0600 {
		t.Fatal("test setup error: auth file already has secure permissions")
	}

	result := checkAuthFilePermissions(true)
	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}

	info, err = os.Stat(authPath)
	if err != nil {
		t.Fatalf("failed to stat auth file: %v", err)
	}

	if info.Mode().Perm() != 0600 {
		t.Errorf("expected permissions 0600, got %04o", info.Mode().Perm())
	}
}

// TestDoctorFix_CombinedScenario verifies multiple issues fixed in single doctor --fix run
func TestDoctorFix_CombinedScenario(t *testing.T) {
	gitRoot := testGitRepo(t)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	oldConfig := `{
		"config_version": "1",
		"update_frequency_hours": 24
	}`
	configPath := filepath.Join(sageoxDir, "config.json")
	if err := os.WriteFile(configPath, []byte(oldConfig), 0644); err != nil {
		t.Fatalf("failed to write old config: %v", err)
	}

	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("# Agent Instructions\n"), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	rootGitignorePath := filepath.Join(gitRoot, ".gitignore")
	if err := os.WriteFile(rootGitignorePath, []byte(".sageox/\n"), 0644); err != nil {
		t.Fatalf("failed to write .gitignore: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	configResult := checkConfigFile(true)
	if !configResult.passed {
		t.Errorf("config fix failed: %s", configResult.message)
	}

	agentsResult := checkAgentsIntegrationWithFix(io.Discard, true)
	if !agentsResult.passed {
		t.Errorf("agents integration fix failed: %s", agentsResult.message)
	}

	gitignoreResult := checkGitignore(true)
	if !gitignoreResult.passed {
		t.Errorf("gitignore fix failed: %s", gitignoreResult.message)
	}

	readmeResult := checkReadmeFile(true)
	if !readmeResult.passed {
		t.Errorf("readme fix failed: %s", readmeResult.message)
	}

	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if cfg.ConfigVersion != config.CurrentConfigVersion {
		t.Error("config was not upgraded")
	}

	agentsContent, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("failed to read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(agentsContent), OxPrimeLine) {
		t.Error("ox agent prime was not injected")
	}

	gitignoreContent, err := os.ReadFile(rootGitignorePath)
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}
	if strings.Contains(string(gitignoreContent), ".sageox") {
		t.Error(".sageox/ was not removed from .gitignore")
	}

	readmePath := filepath.Join(sageoxDir, "README.md")
	if _, err := os.Stat(readmePath); os.IsNotExist(err) {
		t.Error("README.md was not created")
	}
}

// TestDoctorFix_Idempotent verifies running fix twice doesn't break anything
func TestDoctorFix_Idempotent(t *testing.T) {
	gitRoot := testGitRepo(t)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	configResult1 := checkConfigFile(true)
	if !configResult1.passed {
		t.Fatalf("first config fix failed: %s", configResult1.message)
	}

	gitignoreResult1 := checkSageoxGitignore(true)
	if !gitignoreResult1.passed {
		t.Fatalf("first gitignore fix failed: %s", gitignoreResult1.message)
	}

	readmeResult1 := checkReadmeFile(true)
	if !readmeResult1.passed {
		t.Fatalf("first readme fix failed: %s", readmeResult1.message)
	}

	configResult2 := checkConfigFile(true)
	if !configResult2.passed {
		t.Errorf("second config fix failed: %s", configResult2.message)
	}

	gitignoreResult2 := checkSageoxGitignore(true)
	if !gitignoreResult2.passed {
		t.Errorf("second gitignore fix failed: %s", gitignoreResult2.message)
	}

	readmeResult2 := checkReadmeFile(true)
	if !readmeResult2.passed {
		t.Errorf("second readme fix failed: %s", readmeResult2.message)
	}

	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		t.Fatalf("failed to load config after second fix: %v", err)
	}
	if cfg.ConfigVersion != config.CurrentConfigVersion {
		t.Error("config version changed after second fix")
	}

	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}
	for _, required := range requiredGitignoreEntries {
		if !strings.Contains(string(content), required) {
			t.Errorf("second fix removed required entry: %s", required)
		}
	}
}

// TestDoctorFix_ConfigMissing_NoFix verifies fix=false reports error without creating config
func TestDoctorFix_ConfigMissing_NoFix(t *testing.T) {
	gitRoot := testGitRepo(t)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	result := checkConfigFile(false)
	if result.passed {
		t.Error("expected passed=false when config missing and fix=false")
	}
	if result.message != "not found" {
		t.Errorf("expected message='not found', got %q", result.message)
	}

	configPath := filepath.Join(sageoxDir, "config.json")
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Error("config.json should not be created when fix=false")
	}
}

// TestDoctorFix_GitignoreMissing_NoFix verifies fix=false reports error without creating .gitignore
func TestDoctorFix_GitignoreMissing_NoFix(t *testing.T) {
	gitRoot := testGitRepo(t)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	result := checkSageoxGitignore(false)
	if result.passed {
		t.Error("expected passed=false when .gitignore missing and fix=false")
	}
	if result.message != "not found" {
		t.Errorf("expected message='not found', got %q", result.message)
	}

	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	if _, err := os.Stat(gitignorePath); !os.IsNotExist(err) {
		t.Error(".gitignore should not be created when fix=false")
	}
}

// TestDoctorFix_Idempotent_ThreeRuns verifies running fix 3+ times produces same result
func TestDoctorFix_Idempotent_ThreeRuns(t *testing.T) {
	gitRoot := testGitRepo(t)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	// run fix three times
	for i := 1; i <= 3; i++ {
		configResult := checkConfigFile(true)
		if !configResult.passed {
			t.Errorf("run %d: config fix failed: %s", i, configResult.message)
		}

		gitignoreResult := checkSageoxGitignore(true)
		if !gitignoreResult.passed {
			t.Errorf("run %d: gitignore fix failed: %s", i, gitignoreResult.message)
		}

		readmeResult := checkReadmeFile(true)
		if !readmeResult.passed {
			t.Errorf("run %d: readme fix failed: %s", i, readmeResult.message)
		}
	}

	// verify all files are still valid after 3 runs
	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		t.Fatalf("failed to load config after 3 runs: %v", err)
	}
	if cfg.ConfigVersion != config.CurrentConfigVersion {
		t.Error("config version changed after multiple runs")
	}

	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	gitignoreContent, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("failed to read .gitignore after 3 runs: %v", err)
	}
	for _, required := range requiredGitignoreEntries {
		if !strings.Contains(string(gitignoreContent), required) {
			t.Errorf("required entry %q missing after multiple runs", required)
		}
	}

	readmePath := filepath.Join(sageoxDir, "README.md")
	if _, err := os.Stat(readmePath); os.IsNotExist(err) {
		t.Error("README.md missing after multiple runs")
	}
}

// TestDoctorFix_CombinedMultipleIssues verifies multiple issues fixed at once
func TestDoctorFix_CombinedMultipleIssues(t *testing.T) {
	gitRoot := testGitRepo(t)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	// create multiple problems at once:
	// 1. outdated config version
	oldConfig := `{"config_version": "1", "update_frequency_hours": 24}`
	configPath := filepath.Join(sageoxDir, "config.json")
	if err := os.WriteFile(configPath, []byte(oldConfig), 0644); err != nil {
		t.Fatalf("failed to write old config: %v", err)
	}

	// 2. missing .gitignore
	// (don't create it)

	// 3. missing README.md
	// (don't create it)

	// 4. .sageox/ in root .gitignore
	rootGitignorePath := filepath.Join(gitRoot, ".gitignore")
	if err := os.WriteFile(rootGitignorePath, []byte("node_modules/\n.sageox/\n"), 0644); err != nil {
		t.Fatalf("failed to write root .gitignore: %v", err)
	}

	// 5. AGENTS.md without ox agent prime
	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("# Agent Instructions\nSome content\n"), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	// fix all issues in one pass
	configResult := checkConfigFile(true)
	if !configResult.passed {
		t.Errorf("config fix failed: %s", configResult.message)
	}

	gitignoreResult := checkSageoxGitignore(true)
	if !gitignoreResult.passed {
		t.Errorf("sageox gitignore fix failed: %s", gitignoreResult.message)
	}

	readmeResult := checkReadmeFile(true)
	if !readmeResult.passed {
		t.Errorf("readme fix failed: %s", readmeResult.message)
	}

	rootGitignoreResult := checkGitignore(true)
	if !rootGitignoreResult.passed {
		t.Errorf("root gitignore fix failed: %s", rootGitignoreResult.message)
	}

	agentsResult := checkAgentsIntegrationWithFix(io.Discard, true)
	if !agentsResult.passed {
		t.Errorf("agents integration fix failed: %s", agentsResult.message)
	}

	// verify all issues were fixed
	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if cfg.ConfigVersion != config.CurrentConfigVersion {
		t.Error("config version not upgraded")
	}

	sageoxGitignorePath := filepath.Join(sageoxDir, ".gitignore")
	if _, err := os.Stat(sageoxGitignorePath); os.IsNotExist(err) {
		t.Error(".sageox/.gitignore was not created")
	}

	readmePath := filepath.Join(sageoxDir, "README.md")
	if _, err := os.Stat(readmePath); os.IsNotExist(err) {
		t.Error("README.md was not created")
	}

	rootGitignoreContent, err := os.ReadFile(rootGitignorePath)
	if err != nil {
		t.Fatalf("failed to read root .gitignore: %v", err)
	}
	if strings.Contains(string(rootGitignoreContent), ".sageox") {
		t.Error(".sageox/ was not removed from root .gitignore")
	}

	agentsContent, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("failed to read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(agentsContent), OxPrimeLine) {
		t.Error("ox agent prime was not injected into AGENTS.md")
	}
}

// TestDoctorFix_PartialFiles_ConfigOnly verifies fix when only config.json exists
func TestDoctorFix_PartialFiles_ConfigOnly(t *testing.T) {
	gitRoot := testGitRepo(t)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	// create only config.json
	cfg := config.GetDefaultProjectConfig()
	if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// README.md and .gitignore don't exist

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	// fix should create missing files
	readmeResult := checkReadmeFile(true)
	if !readmeResult.passed {
		t.Errorf("readme fix failed: %s", readmeResult.message)
	}
	if readmeResult.message != "created" {
		t.Errorf("expected message='created', got %q", readmeResult.message)
	}

	gitignoreResult := checkSageoxGitignore(true)
	if !gitignoreResult.passed {
		t.Errorf("gitignore fix failed: %s", gitignoreResult.message)
	}
	if gitignoreResult.message != "created" {
		t.Errorf("expected message='created', got %q", gitignoreResult.message)
	}

	configResult := checkConfigFile(true)
	if !configResult.passed {
		t.Errorf("config check failed: %s", configResult.message)
	}

	// verify all files now exist
	readmePath := filepath.Join(sageoxDir, "README.md")
	if _, err := os.Stat(readmePath); os.IsNotExist(err) {
		t.Error("README.md was not created")
	}

	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
		t.Error(".gitignore was not created")
	}
}

// TestDoctorFix_PartialFiles_ReadmeOnly verifies fix when only README.md exists
func TestDoctorFix_PartialFiles_ReadmeOnly(t *testing.T) {
	gitRoot := testGitRepo(t)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	// create only README.md
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte(GetSageoxReadmeContent(nil)), 0644); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	// config.json and .gitignore don't exist

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	// fix should create missing files
	configResult := checkConfigFile(true)
	if !configResult.passed {
		t.Errorf("config fix failed: %s", configResult.message)
	}
	if configResult.message != "created" {
		t.Errorf("expected message='created', got %q", configResult.message)
	}

	gitignoreResult := checkSageoxGitignore(true)
	if !gitignoreResult.passed {
		t.Errorf("gitignore fix failed: %s", gitignoreResult.message)
	}
	if gitignoreResult.message != "created" {
		t.Errorf("expected message='created', got %q", gitignoreResult.message)
	}

	readmeResult := checkReadmeFile(true)
	if !readmeResult.passed {
		t.Errorf("readme check failed: %s", readmeResult.message)
	}

	// verify all files now exist
	configPath := filepath.Join(sageoxDir, "config.json")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Error("config.json was not created")
	}

	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
		t.Error(".gitignore was not created")
	}
}

// TestDoctorFix_UncommittedChanges verifies fix behavior when git has uncommitted changes
func TestDoctorFix_UncommittedChanges(t *testing.T) {
	gitRoot := testGitRepo(t)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	// create some uncommitted file in the repo
	testFilePath := filepath.Join(gitRoot, "uncommitted.txt")
	if err := os.WriteFile(testFilePath, []byte("uncommitted content\n"), 0644); err != nil {
		t.Fatalf("failed to create uncommitted file: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	// fix should still work even with uncommitted changes
	configResult := checkConfigFile(true)
	if !configResult.passed {
		t.Errorf("config fix should work with uncommitted changes: %s", configResult.message)
	}

	readmeResult := checkReadmeFile(true)
	if !readmeResult.passed {
		t.Errorf("readme fix should work with uncommitted changes: %s", readmeResult.message)
	}

	gitignoreResult := checkSageoxGitignore(true)
	if !gitignoreResult.passed {
		t.Errorf("gitignore fix should work with uncommitted changes: %s", gitignoreResult.message)
	}

	// verify files were created
	configPath := filepath.Join(sageoxDir, "config.json")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Error("config.json was not created despite uncommitted changes")
	}

	readmePath := filepath.Join(sageoxDir, "README.md")
	if _, err := os.Stat(readmePath); os.IsNotExist(err) {
		t.Error("README.md was not created despite uncommitted changes")
	}

	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
		t.Error(".gitignore was not created despite uncommitted changes")
	}

	// verify the uncommitted file is still there
	if _, err := os.Stat(testFilePath); os.IsNotExist(err) {
		t.Error("uncommitted file was removed")
	}
}
