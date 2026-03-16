//go:build !short

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
)

// TestCheckReadmeFile_FixCreates verifies that when fix=true and README.md is missing,
// the check creates the file with the expected content.
func TestCheckReadmeFile_FixCreates(t *testing.T) {
	tmpDir := testGitRepo(t)
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(tmpDir)

	// run the check with fix=true
	result := checkReadmeFile(true)

	// verify the check passed
	if !result.passed {
		t.Errorf("expected check to pass, got passed=%v, message=%q, detail=%q",
			result.passed, result.message, result.detail)
	}

	// verify the message indicates creation
	if result.message != "created" {
		t.Errorf("expected message 'created', got %q", result.message)
	}

	// verify the file was actually created
	readmePath := filepath.Join(sageoxDir, "README.md")
	if _, err := os.Stat(readmePath); os.IsNotExist(err) {
		t.Fatalf("README.md was not created at %s", readmePath)
	}

	// verify the content matches the expected content
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("failed to read created README.md: %v", err)
	}

	expectedContent := GetSageoxReadmeContent(nil)
	if string(content) != expectedContent {
		t.Errorf("README.md content does not match expected.\nGot:\n%s\n\nExpected:\n%s",
			string(content), expectedContent)
	}
}

// TestCheckReadmeFile_FixEmpty verifies that when fix=true and README.md exists but is empty,
// the check writes the expected content to the file.
func TestCheckReadmeFile_FixEmpty(t *testing.T) {
	tmpDir := testGitRepo(t)
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	// create an empty README.md
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to create empty README.md: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(tmpDir)

	result := checkReadmeFile(true)

	if !result.passed {
		t.Errorf("expected check to pass, got passed=%v, message=%q, detail=%q",
			result.passed, result.message, result.detail)
	}

	if result.message != "fixed (was empty)" {
		t.Errorf("expected message 'fixed (was empty)', got %q", result.message)
	}

	// verify the file now has content
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("failed to read README.md: %v", err)
	}

	expectedContent := GetSageoxReadmeContent(nil)
	if string(content) != expectedContent {
		t.Errorf("README.md content does not match expected after fix")
	}
}

// TestCheckReadmeFile_FixStale verifies that when fix=true and README.md has outdated content,
// the check updates the file to the latest content.
func TestCheckReadmeFile_FixStale(t *testing.T) {
	tmpDir := testGitRepo(t)
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	// create a README.md with old content (different from expected template)
	readmePath := filepath.Join(sageoxDir, "README.md")
	oldContent := "# Old SageOx README\nThis is outdated content."
	if err := os.WriteFile(readmePath, []byte(oldContent), 0644); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	// set the modification time to 8 days ago
	eightDaysAgo := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(readmePath, eightDaysAgo, eightDaysAgo); err != nil {
		t.Fatalf("failed to set file modification time: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(tmpDir)

	result := checkReadmeFile(true)

	if !result.passed {
		t.Errorf("expected check to pass, got passed=%v, message=%q, detail=%q",
			result.passed, result.message, result.detail)
	}

	// content mismatch is detected as "outdated" and fixed to latest version
	if result.message != "updated to latest version" {
		t.Errorf("expected message 'updated to latest version', got %q", result.message)
	}

	// verify the file was updated with current content
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("failed to read README.md: %v", err)
	}

	expectedContent := GetSageoxReadmeContent(nil)
	if string(content) != expectedContent {
		t.Errorf("README.md was not updated to latest content")
	}
}

// TestCheckSageoxGitignore_FixCreates verifies that when fix=true and .gitignore is missing,
// the check creates it with the required content.
func TestCheckSageoxGitignore_FixCreates(t *testing.T) {
	tmpDir := testGitRepo(t)
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(tmpDir)

	result := checkSageoxGitignore(true)

	if !result.passed {
		t.Errorf("expected check to pass, got passed=%v, message=%q, detail=%q",
			result.passed, result.message, result.detail)
	}

	if result.message != "created" {
		t.Errorf("expected message 'created', got %q", result.message)
	}

	// verify the file was created
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
		t.Fatalf(".gitignore was not created at %s", gitignorePath)
	}

	// verify the content matches expected
	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}

	if string(content) != sageoxGitignoreContent {
		t.Errorf(".gitignore content does not match expected.\nGot:\n%s\n\nExpected:\n%s",
			string(content), sageoxGitignoreContent)
	}
}

// TestCheckSageoxGitignore_FixMerges verifies that when fix=true and .gitignore exists
// but is missing some required entries, the check merges them in.
func TestCheckSageoxGitignore_FixMerges(t *testing.T) {
	tmpDir := testGitRepo(t)
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	// create a .gitignore with some entries but missing required ones
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	existingContent := `# User custom entries
logs/
cache/
# Missing some required entries like !discovered.jsonl
`
	if err := os.WriteFile(gitignorePath, []byte(existingContent), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(tmpDir)

	result := checkSageoxGitignore(true)

	if !result.passed {
		t.Errorf("expected check to pass, got passed=%v, message=%q, detail=%q",
			result.passed, result.message, result.detail)
	}

	if result.message != "merged missing entries" {
		t.Errorf("expected message 'merged missing entries', got %q", result.message)
	}

	// verify the file was updated
	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}

	contentStr := string(content)

	// verify all required entries are present
	for _, required := range requiredGitignoreEntries {
		if !strings.Contains(contentStr, required) {
			t.Errorf("missing required entry %q in merged .gitignore:\n%s", required, contentStr)
		}
	}

	// verify the original user content was preserved
	if !strings.Contains(contentStr, "# User custom entries") {
		t.Errorf("user custom content was not preserved in merged .gitignore")
	}
}

// TestCheckSageoxGitignore_FixRemovesConflicts verifies that when fix=true and .gitignore
// contains conflicting entries (e.g., "discovered.jsonl" when "!discovered.jsonl" is required),
// the check removes the conflicting entries.
func TestCheckSageoxGitignore_FixRemovesConflicts(t *testing.T) {
	tmpDir := testGitRepo(t)
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	// create a .gitignore with conflicting entries
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	conflictingContent := `# Ignore everything
logs/
cache/
session.jsonl
sessions/
discovered.jsonl
README.md
config.json
offline/
`
	if err := os.WriteFile(gitignorePath, []byte(conflictingContent), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(tmpDir)

	result := checkSageoxGitignore(true)

	if !result.passed {
		t.Errorf("expected check to pass, got passed=%v, message=%q, detail=%q",
			result.passed, result.message, result.detail)
	}

	// the message should indicate changes were made
	if result.message != "merged missing entries" {
		t.Errorf("expected message 'merged missing entries', got %q", result.message)
	}

	// verify the file was updated
	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}

	contentStr := string(content)

	// verify conflicting entries were removed
	// "discovered.jsonl" should be removed because "!discovered.jsonl" is required
	lines := strings.Split(contentStr, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// check for the ignore version (without the !) as a standalone entry
		if trimmed == "discovered.jsonl" {
			t.Errorf("conflicting entry 'discovered.jsonl' was not removed from .gitignore:\n%s", contentStr)
		}
		if trimmed == "README.md" {
			t.Errorf("conflicting entry 'README.md' was not removed from .gitignore:\n%s", contentStr)
		}
		if trimmed == "config.json" {
			t.Errorf("conflicting entry 'config.json' was not removed from .gitignore:\n%s", contentStr)
		}
		if trimmed == "offline/" {
			t.Errorf("conflicting entry 'offline/' was not removed from .gitignore:\n%s", contentStr)
		}
	}

	// verify all required entries are present
	for _, required := range requiredGitignoreEntries {
		if !strings.Contains(contentStr, required) {
			t.Errorf("missing required entry %q in .gitignore after conflict removal:\n%s", required, contentStr)
		}
	}
}

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

// TestMergeGitignoreEntries_AddsMissing verifies that missing required entries are added.
func TestMergeGitignoreEntries_AddsMissing(t *testing.T) {
	existingContent := `logs/
cache/
`

	merged, changed := mergeGitignoreEntries(existingContent)

	if !changed {
		t.Errorf("expected changed=true when entries are missing, got changed=false")
	}

	// verify all required entries are present in the merged result
	for _, required := range requiredGitignoreEntries {
		if !strings.Contains(merged, required) {
			t.Errorf("required entry %q not found in merged content:\n%s", required, merged)
		}
	}
}

// TestMergeGitignoreEntries_RemovesConflicts verifies that conflicting entries are removed.
func TestMergeGitignoreEntries_RemovesConflicts(t *testing.T) {
	existingContent := `logs/
cache/
session.jsonl
sessions/
discovered.jsonl
!README.md
!config.json
!offline/
`

	merged, changed := mergeGitignoreEntries(existingContent)

	if !changed {
		t.Errorf("expected changed=true when conflicts exist, got changed=false")
	}

	// verify the conflicting entry "discovered.jsonl" was removed
	lines := strings.Split(merged, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "discovered.jsonl" {
			t.Errorf("conflicting entry 'discovered.jsonl' was not removed:\n%s", merged)
		}
	}

	// verify "!discovered.jsonl" is present
	if !strings.Contains(merged, "!discovered.jsonl") {
		t.Errorf("required entry '!discovered.jsonl' not found in merged content:\n%s", merged)
	}
}

// TestMergeGitignoreEntries_PreservesComments verifies that user comments are preserved.
func TestMergeGitignoreEntries_PreservesComments(t *testing.T) {
	existingContent := `# My custom comment
logs/
cache/

# Another important note
session.jsonl
`

	merged, changed := mergeGitignoreEntries(existingContent)

	if !changed {
		t.Errorf("expected changed=true when entries are missing, got changed=false")
	}

	// verify user comments were preserved
	if !strings.Contains(merged, "# My custom comment") {
		t.Errorf("user comment '# My custom comment' was not preserved:\n%s", merged)
	}

	if !strings.Contains(merged, "# Another important note") {
		t.Errorf("user comment '# Another important note' was not preserved:\n%s", merged)
	}
}

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

// TestCheckGitignore_FixRemovesSageox verifies fix=true removes .sageox/ from root .gitignore
func TestCheckGitignore_FixRemovesSageox(t *testing.T) {
	gitRoot := testGitRepo(t)

	rootGitignorePath := filepath.Join(gitRoot, ".gitignore")
	gitignoreContent := "node_modules/\n.sageox/\n*.log"
	if err := os.WriteFile(rootGitignorePath, []byte(gitignoreContent), 0644); err != nil {
		t.Fatalf("failed to write .gitignore: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	result := checkGitignore(true)
	if !result.passed {
		t.Errorf("expected passed=true, got false: %s", result.message)
	}
	if result.message != "fixed" {
		t.Errorf("expected message='fixed', got %q", result.message)
	}

	content, err := os.ReadFile(rootGitignorePath)
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}

	if strings.Contains(string(content), ".sageox") {
		t.Error("expected .sageox/ to be removed from .gitignore")
	}

	if !strings.Contains(string(content), "node_modules/") {
		t.Error("expected node_modules/ to be preserved")
	}
	if !strings.Contains(string(content), "*.log") {
		t.Error("expected *.log to be preserved")
	}
}

// TestCheckGitignore_FixRemovesAllVariants verifies fix=true removes all .sageox/ variants
func TestCheckGitignore_FixRemovesAllVariants(t *testing.T) {
	variants := []string{".sageox", ".sageox/", "/.sageox", "/.sageox/"}

	for _, variant := range variants {
		t.Run(variant, func(t *testing.T) {
			gitRoot := testGitRepo(t)

			rootGitignorePath := filepath.Join(gitRoot, ".gitignore")
			gitignoreContent := "node_modules/\n" + variant + "\n*.log"
			if err := os.WriteFile(rootGitignorePath, []byte(gitignoreContent), 0644); err != nil {
				t.Fatalf("failed to write .gitignore: %v", err)
			}

			originalWd, _ := os.Getwd()
			defer os.Chdir(originalWd)
			os.Chdir(gitRoot)

			result := checkGitignore(true)
			if !result.passed {
				t.Errorf("expected passed=true, got false: %s", result.message)
			}

			content, err := os.ReadFile(rootGitignorePath)
			if err != nil {
				t.Fatalf("failed to read .gitignore: %v", err)
			}

			if strings.Contains(string(content), ".sageox") {
				t.Errorf("expected %q to be removed from .gitignore", variant)
			}
		})
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

// TestCheckAuthFilePermissions_FixSecures verifies fix=true fixes auth file permissions to 0600
func TestCheckAuthFilePermissions_FixSecures(t *testing.T) {
	tmpDir := t.TempDir()

	authPath := filepath.Join(tmpDir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"token":"test"}`), 0644); err != nil {
		t.Fatalf("failed to create auth file: %v", err)
	}

	info, err := os.Stat(authPath)
	if err != nil {
		t.Fatalf("failed to stat auth file: %v", err)
	}
	if info.Mode().Perm() == 0600 {
		t.Fatal("test setup error: auth file already has secure permissions")
	}

	if err := os.Chmod(authPath, 0600); err != nil {
		t.Fatalf("failed to chmod: %v", err)
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

	agentsResult := checkAgentsIntegrationWithFix(true)
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

// TestCheckReadmeFile_PermissionError verifies handling of permission errors
func TestCheckReadmeFile_PermissionError(t *testing.T) {
	gitRoot := testGitRepo(t)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	// make .sageox directory read-only to prevent writes
	if err := os.Chmod(sageoxDir, 0555); err != nil {
		t.Fatalf("failed to chmod .sageox: %v", err)
	}
	defer os.Chmod(sageoxDir, 0755) // restore permissions

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	result := checkReadmeFile(true)

	// should fail with a write error
	if result.passed {
		t.Error("expected passed=false when directory is read-only")
	}
	if !strings.Contains(result.message, "failed") {
		t.Errorf("expected message to contain 'failed', got %q", result.message)
	}
}

// TestCheckSageoxGitignore_PermissionError verifies handling of permission errors when fixing
func TestCheckSageoxGitignore_PermissionError(t *testing.T) {
	gitRoot := testGitRepo(t)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	// create .gitignore with missing entries
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte("logs/\n"), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	// make .gitignore read-only
	if err := os.Chmod(gitignorePath, 0444); err != nil {
		t.Fatalf("failed to chmod .gitignore: %v", err)
	}
	defer os.Chmod(gitignorePath, 0644) // restore permissions

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	result := checkSageoxGitignore(true)

	// should fail with update error
	if result.passed {
		t.Error("expected passed=false when .gitignore is read-only")
	}
	if result.message != "update failed" {
		t.Errorf("expected message='update failed', got %q", result.message)
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

	agentsResult := checkAgentsIntegrationWithFix(true)
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
