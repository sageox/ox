//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
)

// TestMergeGitignoreEntries_PreservesUserEntries verifies that user custom entries
// that don't conflict with required entries are preserved.
func TestMergeGitignoreEntries_PreservesUserEntries(t *testing.T) {
	existingContent := `logs/
cache/
session.jsonl

# User custom entries
*.swp
*.tmp
node_modules/
`

	merged, changed := mergeGitignoreEntries(existingContent)

	if !changed {
		t.Errorf("expected changed=true when entries are missing, got changed=false")
	}

	// verify user custom entries were preserved
	if !strings.Contains(merged, "*.swp") {
		t.Errorf("user entry '*.swp' was not preserved:\n%s", merged)
	}

	if !strings.Contains(merged, "*.tmp") {
		t.Errorf("user entry '*.tmp' was not preserved:\n%s", merged)
	}

	if !strings.Contains(merged, "node_modules/") {
		t.Errorf("user entry 'node_modules/' was not preserved:\n%s", merged)
	}
}

// TestCheckConfigFile_FixCreates verifies fix=true creates missing config.json
func TestCheckConfigFile_FixCreates(t *testing.T) {
	gitRoot := testGitRepo(t)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	result := checkConfigFile(true)
	if !result.passed {
		t.Errorf("expected passed=true, got false: %s", result.message)
	}
	if result.message != "created" {
		t.Errorf("expected message='created', got %q", result.message)
	}

	configPath := filepath.Join(sageoxDir, "config.json")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Fatal("config.json was not created")
	}

	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.ConfigVersion != config.CurrentConfigVersion {
		t.Errorf("expected version %s, got %s", config.CurrentConfigVersion, cfg.ConfigVersion)
	}
}

// TestCheckConfigFile_FixUpgrades verifies fix=true upgrades outdated config.json
func TestCheckConfigFile_FixUpgrades(t *testing.T) {
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

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	result := checkConfigFile(true)
	if !result.passed {
		t.Errorf("expected passed=true, got false: %s", result.message)
	}
	if !strings.Contains(result.message, "upgraded") {
		t.Errorf("expected message to contain 'upgraded', got %q", result.message)
	}

	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.ConfigVersion != config.CurrentConfigVersion {
		t.Errorf("expected version %s, got %s", config.CurrentConfigVersion, cfg.ConfigVersion)
	}
}

// TestCheckConfigFields_FixInvalid verifies fix=true corrects invalid config fields
func TestCheckConfigFields_FixInvalid(t *testing.T) {
	gitRoot := testGitRepo(t)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	invalidConfig := `{
		"config_version": "2",
		"update_frequency_hours": -1
	}`
	configPath := filepath.Join(sageoxDir, "config.json")
	if err := os.WriteFile(configPath, []byte(invalidConfig), 0644); err != nil {
		t.Fatalf("failed to write invalid config: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	result := checkConfigFields(true)
	if !result.passed {
		t.Errorf("expected passed=true, got false: %s", result.message)
	}
	if result.message != "fixed" {
		t.Errorf("expected message='fixed', got %q", result.message)
	}

	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.UpdateFrequencyHours < 1 {
		t.Errorf("expected update frequency >= 1, got %d", cfg.UpdateFrequencyHours)
	}
}

// TestCheckSageoxDirectory_FixSkips verifies fix=true skips creating .sageox if it doesn't exist
func TestCheckSageoxDirectory_FixSkips(t *testing.T) {
	gitRoot := testGitRepo(t)

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	// checkSageoxDirectory doesn't take a fix parameter - it just checks existence
	result := checkSageoxDirectory()

	// should fail because .sageox doesn't exist
	if result.passed {
		t.Error("expected passed=false when .sageox doesn't exist")
	}
	if result.message != "not found" {
		t.Errorf("expected message='not found', got %q", result.message)
	}

	// verify .sageox was not created
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if _, err := os.Stat(sageoxDir); !os.IsNotExist(err) {
		t.Error(".sageox should not be created by checkSageoxDirectory")
	}
}

// TestCheckConfigFields_CorruptedJSON verifies handling of corrupted JSON in checkConfigFields
func TestCheckConfigFields_CorruptedJSON(t *testing.T) {
	gitRoot := testGitRepo(t)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	configPath := filepath.Join(sageoxDir, "config.json")
	corruptedJSON := `{"config_version": "2", "update_frequency_hours": 24, BROKEN}`
	if err := os.WriteFile(configPath, []byte(corruptedJSON), 0644); err != nil {
		t.Fatalf("failed to write corrupted config: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	result := checkConfigFields(false)

	// should fail with parse error
	if result.passed {
		t.Error("expected passed=false for corrupted JSON")
	}
	if result.message != "parse failed" {
		t.Errorf("expected message='parse failed', got %q", result.message)
	}
}

// TestCheckConfigFile_PreservesCustomSettings verifies fix preserves user customizations in config.json
func TestCheckConfigFile_PreservesCustomSettings(t *testing.T) {
	gitRoot := testGitRepo(t)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	// create config with custom update frequency but old version
	customConfig := `{
		"config_version": "1",
		"update_frequency_hours": 168
	}`
	configPath := filepath.Join(sageoxDir, "config.json")
	if err := os.WriteFile(configPath, []byte(customConfig), 0644); err != nil {
		t.Fatalf("failed to write custom config: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	// fix should upgrade version but preserve custom frequency
	result := checkConfigFile(true)
	if !result.passed {
		t.Errorf("expected passed=true, got false: %s", result.message)
	}
	if !strings.Contains(result.message, "upgraded") {
		t.Errorf("expected message to contain 'upgraded', got %q", result.message)
	}

	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	// verify version was upgraded
	if cfg.ConfigVersion != config.CurrentConfigVersion {
		t.Errorf("expected version %s, got %s", config.CurrentConfigVersion, cfg.ConfigVersion)
	}

	// verify custom frequency was preserved
	if cfg.UpdateFrequencyHours != 168 {
		t.Errorf("expected custom frequency 168 to be preserved, got %d", cfg.UpdateFrequencyHours)
	}
}
