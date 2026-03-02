//go:build !short

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
)

func TestMergeGitignoreEntries_EmptyContent(t *testing.T) {
	content := ""
	merged, changed := mergeGitignoreEntries(content)

	if !changed {
		t.Error("expected changed=true for empty content")
	}

	// verify all required entries are present
	for _, required := range requiredGitignoreEntries {
		if !strings.Contains(merged, required) {
			t.Errorf("merged content missing required entry: %q", required)
		}
	}

	// verify section header is present
	if !strings.Contains(merged, "# SageOx required entries") {
		t.Error("merged content missing section header")
	}
}

func TestMergeGitignoreEntries_SomeEntriesPresent(t *testing.T) {
	content := `# custom comment
logs/
cache/
# another comment
custom-entry.txt
`
	merged, changed := mergeGitignoreEntries(content)

	if !changed {
		t.Error("expected changed=true when some entries are missing")
	}

	// verify all required entries are present
	for _, required := range requiredGitignoreEntries {
		if !strings.Contains(merged, required) {
			t.Errorf("merged content missing required entry: %q", required)
		}
	}

	// verify custom entries are preserved
	if !strings.Contains(merged, "custom-entry.txt") {
		t.Error("merged content lost custom entry")
	}
	if !strings.Contains(merged, "# custom comment") {
		t.Error("merged content lost custom comment")
	}
}

func TestMergeGitignoreEntries_ConflictingEntries(t *testing.T) {
	// test case where "discovered.jsonl" exists but "!discovered.jsonl" is required
	content := `# ignore everything in .sageox
logs/
cache/
discovered.jsonl
session.jsonl
sessions/
`
	merged, changed := mergeGitignoreEntries(content)

	if !changed {
		t.Error("expected changed=true when conflicting entries exist")
	}

	// verify conflicting entry was removed
	lines := strings.Split(merged, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "discovered.jsonl" {
			t.Error("conflicting entry 'discovered.jsonl' should have been removed")
		}
	}

	// verify negated version is present
	if !strings.Contains(merged, "!discovered.jsonl") {
		t.Error("merged content missing required entry: !discovered.jsonl")
	}

	// verify other entries are still present
	if !strings.Contains(merged, "logs/") {
		t.Error("merged content lost logs/ entry")
	}
}

func TestMergeGitignoreEntries_MultipleConflicts(t *testing.T) {
	// test multiple conflicting entries
	content := `logs/
cache/
session.jsonl
sessions/
README.md
config.json
discovered.jsonl
offline/
`
	merged, changed := mergeGitignoreEntries(content)

	if !changed {
		t.Error("expected changed=true when multiple conflicts exist")
	}

	// verify all conflicting entries were removed
	lines := strings.Split(merged, "\n")
	conflicts := []string{"README.md", "config.json", "discovered.jsonl", "offline/"}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		for _, conflict := range conflicts {
			if trimmed == conflict {
				t.Errorf("conflicting entry %q should have been removed", conflict)
			}
		}
	}

	// verify negated versions are present
	if !strings.Contains(merged, "!README.md") {
		t.Error("merged content missing required entry: !README.md")
	}
	if !strings.Contains(merged, "!config.json") {
		t.Error("merged content missing required entry: !config.json")
	}
	if !strings.Contains(merged, "!discovered.jsonl") {
		t.Error("merged content missing required entry: !discovered.jsonl")
	}
	if !strings.Contains(merged, "!offline/") {
		t.Error("merged content missing required entry: !offline/")
	}
}

func TestMergeGitignoreEntries_AllEntriesPresent(t *testing.T) {
	content := sageoxGitignoreContent
	merged, changed := mergeGitignoreEntries(content)

	if changed {
		t.Error("expected changed=false when all entries are present")
	}

	if merged != content {
		t.Error("content should be unchanged when all entries are present")
	}
}

func TestMergeGitignoreEntries_PreservesBlankLines(t *testing.T) {
	content := `logs/

cache/

session.jsonl
`
	merged, changed := mergeGitignoreEntries(content)

	if !changed {
		t.Error("expected changed=true when some entries are missing")
	}

	// verify structure is maintained (blank lines between entries)
	if !strings.Contains(merged, "logs/") {
		t.Error("merged content missing logs/ entry")
	}
	if !strings.Contains(merged, "cache/") {
		t.Error("merged content missing cache/ entry")
	}
}

// setupTempGitRepo creates a temporary directory and initializes it as a git repo.
// It calls skipIntegration to skip with -short flag.
func setupTempGitRepo(t *testing.T) (string, func()) {
	t.Helper()
	skipIntegration(t)

	tmpDir, err := os.MkdirTemp("", "ox-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	// initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to init git repo: %v", err)
	}

	// configure git user for commits
	userCmd := exec.Command("git", "config", "user.name", "Test User")
	userCmd.Dir = tmpDir
	userCmd.Run()

	emailCmd := exec.Command("git", "config", "user.email", "test@example.com")
	emailCmd.Dir = tmpDir
	emailCmd.Run()

	cleanup := func() {
		os.RemoveAll(tmpDir)
	}

	return tmpDir, cleanup
}

// changeToDir changes the current directory for the duration of the test
func changeToDir(t *testing.T, dir string) func() {
	t.Helper()

	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get current dir: %v", err)
	}

	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to change to dir %s: %v", dir, err)
	}

	return func() {
		os.Chdir(oldDir)
	}
}

func TestCheckReadmeFile_NotFound(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .sageox dir so we test "file not found" not "not initialized"
	requireSageoxDir(t, gitRoot)

	result := checkReadmeFile(false)

	if result.passed {
		t.Error("expected passed=false when README.md not found")
	}
	if result.message != "not found" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckReadmeFile_NotFoundWithFix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .sageox dir so we test "file not found" not "not initialized"
	requireSageoxDir(t, gitRoot)

	result := checkReadmeFile(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}
	if result.message != "created" {
		t.Errorf("unexpected message: %s", result.message)
	}

	// verify file was created
	readmePath := filepath.Join(gitRoot, ".sageox", "README.md")
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Errorf("README.md should exist after fix: %v", err)
	}

	// verify content matches expected
	expectedContent := GetSageoxReadmeContent(nil)
	if string(content) != expectedContent {
		t.Error("README.md content does not match expected")
	}
}

func TestCheckReadmeFile_Empty(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create empty README.md
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to create empty README.md: %v", err)
	}

	result := checkReadmeFile(false)

	if result.passed {
		t.Error("expected passed=false for empty README.md")
	}
	if result.message != "empty" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckReadmeFile_EmptyWithFix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create empty README.md
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to create empty README.md: %v", err)
	}

	result := checkReadmeFile(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}
	if result.message != "fixed (was empty)" {
		t.Errorf("unexpected message: %s", result.message)
	}

	// verify file was updated
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Errorf("README.md should exist after fix: %v", err)
	}
	if len(content) == 0 {
		t.Error("README.md should not be empty after fix")
	}
}

func TestCheckReadmeFile_Stale(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create README.md with correct content but old modification time
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte(GetSageoxReadmeContent(nil)), 0644); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	// set modification time to 8 days ago
	eightDaysAgo := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(readmePath, eightDaysAgo, eightDaysAgo); err != nil {
		t.Fatalf("failed to set file time: %v", err)
	}

	result := checkReadmeFile(false)

	if !result.passed {
		t.Error("expected passed=true for stale file (with warning)")
	}
	if !result.warning {
		t.Error("expected warning=true for stale file")
	}
	if result.message != "stale" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckReadmeFile_StaleWithFix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create README.md with correct content but old modification time
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte(GetSageoxReadmeContent(nil)), 0644); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	// set modification time to 8 days ago
	eightDaysAgo := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(readmePath, eightDaysAgo, eightDaysAgo); err != nil {
		t.Fatalf("failed to set file time: %v", err)
	}

	result := checkReadmeFile(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}
	if result.message != "refreshed" {
		t.Errorf("unexpected message: %s", result.message)
	}

	// verify file was updated
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Errorf("README.md should exist after fix: %v", err)
	}

	expectedContent := GetSageoxReadmeContent(nil)
	if string(content) != expectedContent {
		t.Error("README.md should be updated with current content")
	}
}

func TestCheckReadmeFile_Fresh(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create fresh README.md
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte(GetSageoxReadmeContent(nil)), 0644); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	result := checkReadmeFile(false)

	if !result.passed {
		t.Errorf("expected passed=true for fresh file, got: %+v", result)
	}
	if result.warning {
		t.Error("expected warning=false for fresh file")
	}
	if result.message != "ok" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckReadmeFile_Outdated(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create README.md with outdated content (different from expected template)
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Old Template\n\nThis is outdated content."), 0644); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	result := checkReadmeFile(false)

	if !result.passed {
		t.Error("expected passed=true for outdated file (with warning)")
	}
	if !result.warning {
		t.Error("expected warning=true for outdated file")
	}
	if result.message != "outdated" {
		t.Errorf("unexpected message: %s, expected 'outdated'", result.message)
	}
	if !strings.Contains(result.detail, "update to latest version") {
		t.Errorf("expected detail to mention updating, got: %s", result.detail)
	}
}

func TestCheckReadmeFile_OutdatedWithFix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create README.md with outdated content
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Old Template\n\nOutdated content from old ox version."), 0644); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	result := checkReadmeFile(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}
	if result.message != "updated to latest version" {
		t.Errorf("unexpected message: %s, expected 'updated to latest version'", result.message)
	}

	// verify file was updated
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Errorf("README.md should exist after fix: %v", err)
	}
	expectedContent := GetSageoxReadmeContent(nil)
	if string(content) != expectedContent {
		t.Error("README.md should be updated with latest content")
	}
}

func TestCheckSageoxGitignore_NotFound(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .sageox dir so we test "file not found" not "not initialized"
	requireSageoxDir(t, gitRoot)

	result := checkSageoxGitignore(false)

	if result.passed {
		t.Error("expected passed=false when .gitignore not found")
	}
	if result.message != "not found" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckSageoxGitignore_NotFoundWithFix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .sageox dir so we test "file not found" not "not initialized"
	requireSageoxDir(t, gitRoot)

	result := checkSageoxGitignore(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}
	if result.message != "created" {
		t.Errorf("unexpected message: %s", result.message)
	}

	// verify file was created
	gitignorePath := filepath.Join(gitRoot, ".sageox", ".gitignore")
	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Errorf(".gitignore should exist after fix: %v", err)
	}

	// verify all required entries are present
	for _, required := range requiredGitignoreEntries {
		if !strings.Contains(string(content), required) {
			t.Errorf("created .gitignore missing required entry: %q", required)
		}
	}
}

func TestCheckSageoxGitignore_MissingEntries(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitignore with only some entries
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	partialContent := `logs/
cache/
`
	if err := os.WriteFile(gitignorePath, []byte(partialContent), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	result := checkSageoxGitignore(false)

	if !result.passed {
		t.Error("expected passed=true (warning check passes with warning flag)")
	}
	if !result.warning {
		t.Error("expected warning=true when entries are missing")
	}
	if result.message != "missing entries" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckSageoxGitignore_MissingEntriesWithFix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitignore with only some entries
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	partialContent := `# my custom entries
logs/
cache/
custom.txt
`
	if err := os.WriteFile(gitignorePath, []byte(partialContent), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	result := checkSageoxGitignore(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}
	if result.message != "merged missing entries" {
		t.Errorf("unexpected message: %s", result.message)
	}

	// verify file was updated with all required entries
	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Errorf(".gitignore should exist after fix: %v", err)
	}

	for _, required := range requiredGitignoreEntries {
		if !strings.Contains(string(content), required) {
			t.Errorf("updated .gitignore missing required entry: %q", required)
		}
	}

	// verify custom entries are preserved
	if !strings.Contains(string(content), "custom.txt") {
		t.Error("updated .gitignore lost custom entry")
	}
	if !strings.Contains(string(content), "# my custom entries") {
		t.Error("updated .gitignore lost custom comment")
	}
}

func TestCheckSageoxGitignore_ConflictingEntriesWithFix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitignore with conflicting entries
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	conflictingContent := `logs/
cache/
session.jsonl
sessions/
README.md
config.json
discovered.jsonl
offline/
`
	if err := os.WriteFile(gitignorePath, []byte(conflictingContent), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	result := checkSageoxGitignore(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}

	// verify conflicts were resolved
	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Errorf(".gitignore should exist after fix: %v", err)
	}

	lines := strings.Split(string(content), "\n")
	conflicts := []string{"README.md", "config.json", "discovered.jsonl", "offline/"}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		for _, conflict := range conflicts {
			if trimmed == conflict {
				t.Errorf("conflicting entry %q should have been removed", conflict)
			}
		}
	}

	// verify negated versions are present
	if !strings.Contains(string(content), "!README.md") {
		t.Error("updated .gitignore missing: !README.md")
	}
	if !strings.Contains(string(content), "!config.json") {
		t.Error("updated .gitignore missing: !config.json")
	}
	if !strings.Contains(string(content), "!discovered.jsonl") {
		t.Error("updated .gitignore missing: !discovered.jsonl")
	}
	if !strings.Contains(string(content), "!offline/") {
		t.Error("updated .gitignore missing: !offline/")
	}
}

func TestCheckSageoxGitignore_AllEntriesPresent(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitignore with all required entries
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte(sageoxGitignoreContent), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	result := checkSageoxGitignore(false)

	if !result.passed {
		t.Errorf("expected passed=true when all entries present, got: %+v", result)
	}
	if result.warning {
		t.Error("expected warning=false when all entries present")
	}
	if result.message != "ok" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckSageoxGitignore_PreservesUserCustomizations(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitignore with user customizations and required entries
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	customContent := `# My custom settings
logs/
cache/
session.jsonl
sessions/

# Custom entries
*.tmp
.DS_Store

!README.md
!config.json
!discovered.jsonl
!offline/
`
	if err := os.WriteFile(gitignorePath, []byte(customContent), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	result := checkSageoxGitignore(false)

	if !result.passed {
		t.Errorf("expected passed=true, got: %+v", result)
	}

	// run with fix to ensure it doesn't modify anything
	result = checkSageoxGitignore(true)
	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}

	// verify custom entries are still present
	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Errorf(".gitignore should exist: %v", err)
	}

	if !strings.Contains(string(content), "*.tmp") {
		t.Error("custom entry *.tmp was lost")
	}
	if !strings.Contains(string(content), ".DS_Store") {
		t.Error("custom entry .DS_Store was lost")
	}
	if !strings.Contains(string(content), "# My custom settings") {
		t.Error("custom comment was lost")
	}
}

func TestCheckSageoxGitignore_ReadError(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .sageox directory
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")

	// create .gitignore as a directory instead of a file to trigger read error
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	if err := os.Mkdir(gitignorePath, 0755); err != nil {
		t.Fatalf("failed to create .gitignore dir: %v", err)
	}

	result := checkSageoxGitignore(false)

	if result.passed {
		t.Error("expected passed=false when read error occurs")
	}
	if result.message != "read error" {
		t.Errorf("unexpected message: %s", result.message)
	}
	if result.detail == "" {
		t.Error("expected detail to contain error information")
	}
}

// edge case tests for checkSageoxGitignore

func TestCheckSageoxGitignore_EmptyFile(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to create empty .gitignore: %v", err)
	}

	result := checkSageoxGitignore(false)

	if !result.passed {
		t.Error("expected passed=true (warning check)")
	}
	if !result.warning {
		t.Error("expected warning=true for empty .gitignore")
	}
	if result.message != "missing entries" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckSageoxGitignore_WhitespaceOnly(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	// file with only whitespace and blank lines
	whitespaceContent := "   \n\n\t\t\n  \n"
	if err := os.WriteFile(gitignorePath, []byte(whitespaceContent), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	result := checkSageoxGitignore(false)

	if !result.passed {
		t.Error("expected passed=true (warning check)")
	}
	if !result.warning {
		t.Error("expected warning=true for whitespace-only .gitignore")
	}
}

func TestCheckSageoxGitignore_WhitespaceOnlyWithFix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	whitespaceContent := "   \n\n\t\t\n  \n"
	if err := os.WriteFile(gitignorePath, []byte(whitespaceContent), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	result := checkSageoxGitignore(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}

	// verify all required entries are present
	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Errorf(".gitignore should exist after fix: %v", err)
	}

	for _, required := range requiredGitignoreEntries {
		if !strings.Contains(string(content), required) {
			t.Errorf("updated .gitignore missing required entry: %q", required)
		}
	}
}

func TestCheckSageoxGitignore_MultipleConflictingEntries(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	// contains all conflicting entries without negation
	conflictingContent := `# user entries
README.md
config.json
discovered.jsonl
offline/
# more entries
custom.txt
`
	if err := os.WriteFile(gitignorePath, []byte(conflictingContent), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	result := checkSageoxGitignore(false)

	if !result.passed {
		t.Error("expected passed=true (warning check)")
	}
	if !result.warning {
		t.Error("expected warning=true for conflicting entries")
	}
}

func TestCheckReadmeFile_SymLink(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")

	// create a target file with proper content
	targetPath := filepath.Join(gitRoot, "readme_target.md")
	if err := os.WriteFile(targetPath, []byte(GetSageoxReadmeContent(nil)), 0644); err != nil {
		t.Fatalf("failed to create target file: %v", err)
	}

	// create symlink to target
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.Symlink(targetPath, readmePath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	result := checkReadmeFile(false)

	// symlink should work - it follows the link and reads the content
	if !result.passed {
		t.Errorf("expected passed=true for symlink to valid file, got: %+v", result)
	}
}

func TestCheckReadmeFile_VeryOld(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	readmePath := filepath.Join(sageoxDir, "README.md")
	// use correct content to test pure age-based staleness
	if err := os.WriteFile(readmePath, []byte(GetSageoxReadmeContent(nil)), 0644); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	// set modification time to 40 days ago (very old)
	fortyDaysAgo := time.Now().Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(readmePath, fortyDaysAgo, fortyDaysAgo); err != nil {
		t.Fatalf("failed to set file time: %v", err)
	}

	result := checkReadmeFile(false)

	if !result.passed {
		t.Error("expected passed=true for very old file (with warning)")
	}
	if !result.warning {
		t.Error("expected warning=true for very old file")
	}
	if result.message != "stale" {
		t.Errorf("unexpected message: %s", result.message)
	}
	if !strings.Contains(result.detail, "40 days old") {
		t.Errorf("expected detail to mention 40 days, got: %s", result.detail)
	}
}

func TestCheckReadmeFile_JustBarelyStale(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	readmePath := filepath.Join(sageoxDir, "README.md")
	// use correct content to test pure age-based staleness
	if err := os.WriteFile(readmePath, []byte(GetSageoxReadmeContent(nil)), 0644); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	// set modification time to exactly 7 days + 1 hour ago
	sevenDaysOneHour := time.Now().Add(-7*24*time.Hour - 1*time.Hour)
	if err := os.Chtimes(readmePath, sevenDaysOneHour, sevenDaysOneHour); err != nil {
		t.Fatalf("failed to set file time: %v", err)
	}

	result := checkReadmeFile(false)

	if !result.passed {
		t.Error("expected passed=true for barely stale file (with warning)")
	}
	if !result.warning {
		t.Error("expected warning=true for barely stale file")
	}
	if result.message != "stale" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckReadmeFile_JustBelowStaleThreshold(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte(GetSageoxReadmeContent(nil)), 0644); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	// set modification time to 6 days ago (just below 7 day threshold)
	sixDaysAgo := time.Now().Add(-6 * 24 * time.Hour)
	if err := os.Chtimes(readmePath, sixDaysAgo, sixDaysAgo); err != nil {
		t.Fatalf("failed to set file time: %v", err)
	}

	result := checkReadmeFile(false)

	if !result.passed {
		t.Errorf("expected passed=true for file below stale threshold, got: %+v", result)
	}
	if result.warning {
		t.Error("expected warning=false for file below stale threshold")
	}
	if result.message != "ok" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

// edge case tests for mergeGitignoreEntries

func TestMergeGitignoreEntries_WindowsLineEndings(t *testing.T) {
	// test with CRLF line endings
	content := "logs/\r\ncache/\r\nsession.jsonl\r\n"
	merged, changed := mergeGitignoreEntries(content)

	if !changed {
		t.Error("expected changed=true when some entries are missing")
	}

	// verify all required entries are present
	for _, required := range requiredGitignoreEntries {
		if !strings.Contains(merged, required) {
			t.Errorf("merged content missing required entry: %q", required)
		}
	}
}

func TestMergeGitignoreEntries_TabsAndSpaces(t *testing.T) {
	// test with tabs and mixed whitespace
	content := "\tlogs/\n  cache/\n\t  session.jsonl\n"
	merged, changed := mergeGitignoreEntries(content)

	if !changed {
		t.Error("expected changed=true when some entries are missing")
	}

	// verify existing entries with tabs/spaces are preserved
	if !strings.Contains(merged, "logs/") {
		t.Error("merged content missing logs/ entry")
	}
	if !strings.Contains(merged, "cache/") {
		t.Error("merged content missing cache/ entry")
	}
}

func TestMergeGitignoreEntries_MultipleBlankLines(t *testing.T) {
	content := `logs/


cache/



session.jsonl
`
	merged, changed := mergeGitignoreEntries(content)

	if !changed {
		t.Error("expected changed=true when some entries are missing")
	}

	// verify structure is maintained
	if !strings.Contains(merged, "logs/") {
		t.Error("merged content missing logs/ entry")
	}
	if !strings.Contains(merged, "cache/") {
		t.Error("merged content missing cache/ entry")
	}
}

func TestMergeGitignoreEntries_TrailingWhitespace(t *testing.T) {
	// test entries with trailing spaces/tabs
	content := "logs/   \ncache/\t\t\nsession.jsonl \t \n"
	merged, changed := mergeGitignoreEntries(content)

	if !changed {
		t.Error("expected changed=true when some entries are missing")
	}

	// verify entries are recognized despite trailing whitespace
	for _, required := range requiredGitignoreEntries {
		if !strings.Contains(merged, required) {
			t.Errorf("merged content missing required entry: %q", required)
		}
	}
}

func TestMergeGitignoreEntries_CommentsOnly(t *testing.T) {
	content := `# this is a comment
# another comment
# yet another comment
`
	merged, changed := mergeGitignoreEntries(content)

	if !changed {
		t.Error("expected changed=true when all entries are missing")
	}

	// verify comments are preserved
	if !strings.Contains(merged, "# this is a comment") {
		t.Error("merged content lost comment")
	}

	// verify all required entries are added
	for _, required := range requiredGitignoreEntries {
		if !strings.Contains(merged, required) {
			t.Errorf("merged content missing required entry: %q", required)
		}
	}
}

func TestMergeGitignoreEntries_MixedConflictsAndMissing(t *testing.T) {
	// has some conflicts and some missing entries
	content := `logs/
cache/
README.md
discovered.jsonl
custom-entry.txt
`
	merged, changed := mergeGitignoreEntries(content)

	if !changed {
		t.Error("expected changed=true when conflicts exist and entries are missing")
	}

	// verify conflicts were removed
	lines := strings.Split(merged, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "README.md" || trimmed == "discovered.jsonl" {
			t.Errorf("conflicting entry %q should have been removed", trimmed)
		}
	}

	// verify negated versions are present
	if !strings.Contains(merged, "!README.md") {
		t.Error("merged content missing: !README.md")
	}
	if !strings.Contains(merged, "!discovered.jsonl") {
		t.Error("merged content missing: !discovered.jsonl")
	}

	// verify all required entries are present
	for _, required := range requiredGitignoreEntries {
		if !strings.Contains(merged, required) {
			t.Errorf("merged content missing required entry: %q", required)
		}
	}

	// verify custom entries are preserved
	if !strings.Contains(merged, "custom-entry.txt") {
		t.Error("merged content lost custom entry")
	}
}

func TestMergeGitignoreEntries_OnlyNegatedEntries(t *testing.T) {
	// file only has the negated entries
	content := `!README.md
!config.json
!discovered.jsonl
!offline/
`
	merged, changed := mergeGitignoreEntries(content)

	if !changed {
		t.Error("expected changed=true when ignore entries are missing")
	}

	// verify all required entries are present
	for _, required := range requiredGitignoreEntries {
		if !strings.Contains(merged, required) {
			t.Errorf("merged content missing required entry: %q", required)
		}
	}
}

func TestMergeGitignoreEntries_DuplicateEntries(t *testing.T) {
	content := `logs/
logs/
cache/
cache/
session.jsonl
`
	merged, changed := mergeGitignoreEntries(content)

	if !changed {
		t.Error("expected changed=true when some entries are missing")
	}

	// verify duplicates are preserved (mergeGitignoreEntries doesn't dedupe)
	if !strings.Contains(merged, "logs/") {
		t.Error("merged content missing logs/ entry")
	}
}

// symlink edge case for .sageox/.gitignore

func TestCheckSageoxGitignore_SymlinkToValidFile(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")

	// create target file with proper content
	targetPath := filepath.Join(gitRoot, "gitignore_target")
	if err := os.WriteFile(targetPath, []byte(sageoxGitignoreContent), 0644); err != nil {
		t.Fatalf("failed to create target file: %v", err)
	}

	// create symlink
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	if err := os.Symlink(targetPath, gitignorePath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	result := checkSageoxGitignore(false)

	// symlink should work - follows link and reads content
	if !result.passed {
		t.Errorf("expected passed=true for symlink to valid file, got: %+v", result)
	}
	if result.message != "ok" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckSageoxGitignore_SymlinkToInvalidFile(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")

	// create target file with missing entries
	targetPath := filepath.Join(gitRoot, "gitignore_target")
	if err := os.WriteFile(targetPath, []byte("logs/\ncache/\n"), 0644); err != nil {
		t.Fatalf("failed to create target file: %v", err)
	}

	// create symlink
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	if err := os.Symlink(targetPath, gitignorePath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	result := checkSageoxGitignore(false)

	// symlink follows to file with missing entries
	if !result.passed {
		t.Error("expected passed=true (warning check)")
	}
	if !result.warning {
		t.Error("expected warning=true for symlink to file with missing entries")
	}
}

// ============================================================================
// checkCloudDoctor Tests (Cloud API Integration)
// ============================================================================

func TestCheckCloudDoctor_Success_ReturnsIssues(t *testing.T) {
	// setup mock server that returns doctor issues
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/doctor") {
			w.WriteHeader(http.StatusOK)
			resp := api.DoctorResponse{
				Issues: []api.DoctorIssue{
					{
						Type:        "merge_pending",
						Severity:    "warning",
						Title:       "Merge pending",
						Description: "Multiple teams working on this repo",
						ActionURL:   "https://app.sageox.ai/merge/123",
						ActionLabel: "Resolve merge",
					},
				},
				CheckedAt: "2025-01-01T00:00:00Z",
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	// set endpoint to mock server
	t.Setenv("SAGEOX_ENDPOINT", mockServer.URL)

	// setup temp git repo with config
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create config with repo_id
	requireSageoxDir(t, gitRoot)

	cfg := &config.ProjectConfig{
		RepoID:        "repo_test123",
		ConfigVersion: config.CurrentConfigVersion,
	}
	if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	results := checkCloudDoctor()

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	result := results[0]
	if result.name != "Merge pending" {
		t.Errorf("expected name 'Merge pending', got %q", result.name)
	}
	if !result.warning {
		t.Error("expected warning=true for warning severity")
	}
}

func TestCheckCloudDoctor_HTTP500_ReturnsWarning(t *testing.T) {
	// setup mock server that returns 500
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal error"}`))
	}))
	defer mockServer.Close()

	t.Setenv("SAGEOX_ENDPOINT", mockServer.URL)

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	cfg := &config.ProjectConfig{
		RepoID:        "repo_test123",
		ConfigVersion: config.CurrentConfigVersion,
	}
	if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	results := checkCloudDoctor()

	// should return a warning about cloud doctor being unavailable
	if len(results) != 1 {
		t.Fatalf("expected 1 result (warning), got %d", len(results))
	}

	result := results[0]
	if result.name != "Cloud doctor" {
		t.Errorf("expected name 'Cloud doctor', got %q", result.name)
	}
	if !result.warning {
		t.Error("expected warning=true for unavailable cloud doctor")
	}
	if !strings.Contains(result.message, "skipped") {
		t.Errorf("expected message to mention 'skipped', got %q", result.message)
	}
}

func TestCheckCloudDoctor_NetworkError_ReturnsWarning(t *testing.T) {
	// point to invalid endpoint
	t.Setenv("SAGEOX_ENDPOINT", "http://localhost:99999")

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	cfg := &config.ProjectConfig{
		RepoID:        "repo_test123",
		ConfigVersion: config.CurrentConfigVersion,
	}
	if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	results := checkCloudDoctor()

	// should return a warning about cloud doctor being unavailable
	if len(results) != 1 {
		t.Fatalf("expected 1 result (warning), got %d", len(results))
	}

	result := results[0]
	if result.name != "Cloud doctor" {
		t.Errorf("expected name 'Cloud doctor', got %q", result.name)
	}
	if !result.warning {
		t.Error("expected warning=true for network error")
	}
}

func TestCheckCloudDoctor_NoConfig_ReturnsNil(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// no config file created

	results := checkCloudDoctor()

	// should silently skip when no config
	if results != nil {
		t.Errorf("expected nil results when no config, got %d results", len(results))
	}
}

func TestCheckCloudDoctor_EmptyIssues_ReturnsNil(t *testing.T) {
	// setup mock server that returns empty issues
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/doctor") {
			w.WriteHeader(http.StatusOK)
			resp := api.DoctorResponse{
				Issues:    []api.DoctorIssue{},
				CheckedAt: "2025-01-01T00:00:00Z",
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	t.Setenv("SAGEOX_ENDPOINT", mockServer.URL)

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	cfg := &config.ProjectConfig{
		RepoID:        "repo_test123",
		ConfigVersion: config.CurrentConfigVersion,
	}
	if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	results := checkCloudDoctor()

	// should silently return nil when no issues
	if results != nil {
		t.Errorf("expected nil results when no issues, got %d results", len(results))
	}
}

func TestCheckCloudDoctor_MultipleSeverities(t *testing.T) {
	// setup mock server that returns issues with different severities
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/doctor") {
			w.WriteHeader(http.StatusOK)
			resp := api.DoctorResponse{
				Issues: []api.DoctorIssue{
					{
						Type:        "critical_issue",
						Severity:    "error",
						Title:       "Critical Issue",
						Description: "This is critical",
					},
					{
						Type:        "minor_issue",
						Severity:    "warning",
						Title:       "Minor Issue",
						Description: "This is a warning",
					},
					{
						Type:        "info_issue",
						Severity:    "info",
						Title:       "Info Issue",
						Description: "This is informational",
					},
				},
				CheckedAt: "2025-01-01T00:00:00Z",
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	t.Setenv("SAGEOX_ENDPOINT", mockServer.URL)

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	cfg := &config.ProjectConfig{
		RepoID:        "repo_test123",
		ConfigVersion: config.CurrentConfigVersion,
	}
	if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	results := checkCloudDoctor()

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// verify error severity
	if results[0].passed {
		t.Error("error severity should have passed=false")
	}

	// verify warning severity
	if !results[1].passed || !results[1].warning {
		t.Error("warning severity should have passed=true, warning=true")
	}

	// verify info severity
	if !results[2].passed || results[2].warning {
		t.Error("info severity should have passed=true, warning=false")
	}
}

// ============================================================================
// checkGitattributes Tests
// ============================================================================

func TestCheckGitattributes_NoFile_SkipsCheck(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	result := checkGitattributes(false)

	if !result.skipped {
		t.Error("expected skipped=true when .gitattributes doesn't exist")
	}
	if result.message != "no file" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckGitattributes_HasEntries_Passes(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitattributes with SageOx entries
	content := `# existing entries
*.txt text
# SageOx infrastructure guidance
.sageox/** linguist-language=SageOx
*.ox linguist-language=SageOx
`
	if err := os.WriteFile(filepath.Join(gitRoot, ".gitattributes"), []byte(content), 0644); err != nil {
		t.Fatalf("failed to create .gitattributes: %v", err)
	}

	result := checkGitattributes(false)

	if !result.passed {
		t.Errorf("expected passed=true when SageOx entries present, got: %+v", result)
	}
	if result.message != "SageOx entries present" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckGitattributes_MissingEntries_Warning(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitattributes without SageOx entries
	content := `# existing entries
*.txt text
*.md text
`
	if err := os.WriteFile(filepath.Join(gitRoot, ".gitattributes"), []byte(content), 0644); err != nil {
		t.Fatalf("failed to create .gitattributes: %v", err)
	}

	result := checkGitattributes(false)

	if !result.passed {
		t.Error("expected passed=true (warning check)")
	}
	if !result.warning {
		t.Error("expected warning=true when SageOx entries missing")
	}
	if result.message != "missing SageOx entries" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckGitattributes_MissingEntries_FixAddsEntries(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitattributes without SageOx entries
	originalContent := `# existing entries
*.txt text
*.md text
`
	gitattrsPath := filepath.Join(gitRoot, ".gitattributes")
	if err := os.WriteFile(gitattrsPath, []byte(originalContent), 0644); err != nil {
		t.Fatalf("failed to create .gitattributes: %v", err)
	}

	result := checkGitattributes(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}
	if result.message != "added SageOx entries" {
		t.Errorf("unexpected message: %s", result.message)
	}

	// verify entries were added
	content, err := os.ReadFile(gitattrsPath)
	if err != nil {
		t.Fatalf("failed to read .gitattributes: %v", err)
	}

	for _, entry := range sageoxGitattributesEntries {
		if !strings.Contains(string(content), entry) {
			t.Errorf("fixed .gitattributes missing entry: %q", entry)
		}
	}

	// verify original content preserved
	if !strings.Contains(string(content), "*.txt text") {
		t.Error("original content was lost")
	}
	if !strings.Contains(string(content), sageoxGitattributesComment) {
		t.Error("SageOx comment not added")
	}
}

func TestCheckGitattributes_EmptyFile_FixAddsEntries(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create empty .gitattributes
	gitattrsPath := filepath.Join(gitRoot, ".gitattributes")
	if err := os.WriteFile(gitattrsPath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to create .gitattributes: %v", err)
	}

	result := checkGitattributes(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}

	// verify entries were added
	content, err := os.ReadFile(gitattrsPath)
	if err != nil {
		t.Fatalf("failed to read .gitattributes: %v", err)
	}

	for _, entry := range sageoxGitattributesEntries {
		if !strings.Contains(string(content), entry) {
			t.Errorf("fixed .gitattributes missing entry: %q", entry)
		}
	}
}

func TestCheckGitattributes_PartialEntries_FixAddsRemaining(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitattributes with only one of the two entries
	originalContent := `.sageox/** linguist-language=SageOx
`
	gitattrsPath := filepath.Join(gitRoot, ".gitattributes")
	if err := os.WriteFile(gitattrsPath, []byte(originalContent), 0644); err != nil {
		t.Fatalf("failed to create .gitattributes: %v", err)
	}

	result := checkGitattributes(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}

	// verify both entries are now present
	content, err := os.ReadFile(gitattrsPath)
	if err != nil {
		t.Fatalf("failed to read .gitattributes: %v", err)
	}

	for _, entry := range sageoxGitattributesEntries {
		if !strings.Contains(string(content), entry) {
			t.Errorf("fixed .gitattributes missing entry: %q", entry)
		}
	}
}

func TestEnsureGitattributes_NoFile_CreatesFile(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	added, err := EnsureGitattributes(gitRoot)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !added {
		t.Error("expected added=true when file is created")
	}

	// verify file was created with SageOx entries
	gitattrsPath := filepath.Join(gitRoot, ".gitattributes")
	content, err := os.ReadFile(gitattrsPath)
	if err != nil {
		t.Fatalf("failed to read created .gitattributes: %v", err)
	}

	// verify it contains SageOx entries
	if !strings.Contains(string(content), sageoxGitattributesComment) {
		t.Error(".gitattributes should contain SageOx comment")
	}
	if !strings.Contains(string(content), ".sageox/**") {
		t.Error(".gitattributes should contain .sageox/** entry")
	}
}

func TestEnsureGitattributes_FileExists_AddsEntries(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	// create existing .gitattributes
	originalContent := `*.txt text
`
	gitattrsPath := filepath.Join(gitRoot, ".gitattributes")
	if err := os.WriteFile(gitattrsPath, []byte(originalContent), 0644); err != nil {
		t.Fatalf("failed to create .gitattributes: %v", err)
	}

	added, err := EnsureGitattributes(gitRoot)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !added {
		t.Error("expected added=true when entries were added")
	}

	// verify entries were added
	content, err := os.ReadFile(gitattrsPath)
	if err != nil {
		t.Fatalf("failed to read .gitattributes: %v", err)
	}

	for _, entry := range sageoxGitattributesEntries {
		if !strings.Contains(string(content), entry) {
			t.Errorf("fixed .gitattributes missing entry: %q", entry)
		}
	}
}

func TestEnsureGitattributes_EntriesAlreadyPresent_NoChange(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	// create .gitattributes with entries already present
	originalContent := `*.txt text
# SageOx infrastructure guidance
.sageox/** linguist-language=SageOx
*.ox linguist-language=SageOx
`
	gitattrsPath := filepath.Join(gitRoot, ".gitattributes")
	if err := os.WriteFile(gitattrsPath, []byte(originalContent), 0644); err != nil {
		t.Fatalf("failed to create .gitattributes: %v", err)
	}

	added, err := EnsureGitattributes(gitRoot)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if added {
		t.Error("expected added=false when entries already present")
	}
}

func TestGetMissingSageoxGitattributes_AllPresent(t *testing.T) {
	content := `.sageox/** linguist-language=SageOx
*.ox linguist-language=SageOx
`
	missing := getMissingSageoxGitattributes(content)

	if len(missing) != 0 {
		t.Errorf("expected no missing entries, got: %v", missing)
	}
}

func TestGetMissingSageoxGitattributes_AllMissing(t *testing.T) {
	content := `*.txt text
`
	missing := getMissingSageoxGitattributes(content)

	if len(missing) != 2 {
		t.Errorf("expected 2 missing entries, got: %d", len(missing))
	}
}

func TestGetMissingSageoxGitattributes_PartiallyPresent(t *testing.T) {
	content := `.sageox/** linguist-language=SageOx
`
	missing := getMissingSageoxGitattributes(content)

	if len(missing) != 1 {
		t.Errorf("expected 1 missing entry, got: %d", len(missing))
	}
	if missing[0] != "*.ox linguist-language=SageOx" {
		t.Errorf("unexpected missing entry: %s", missing[0])
	}
}

func TestAddSageoxGitattributesEntries_EmptyContent(t *testing.T) {
	content := ""
	missing := []string{".sageox/** linguist-language=SageOx", "*.ox linguist-language=SageOx"}

	result := addSageoxGitattributesEntries(content, missing)

	if !strings.Contains(result, sageoxGitattributesComment) {
		t.Error("result should contain SageOx comment")
	}
	for _, entry := range missing {
		if !strings.Contains(result, entry) {
			t.Errorf("result missing entry: %q", entry)
		}
	}
}

func TestAddSageoxGitattributesEntries_WithExistingContent(t *testing.T) {
	content := "*.txt text\n*.md text"
	missing := []string{".sageox/** linguist-language=SageOx"}

	result := addSageoxGitattributesEntries(content, missing)

	// should preserve existing content
	if !strings.Contains(result, "*.txt text") {
		t.Error("result should preserve existing content")
	}
	// should add new entries
	if !strings.Contains(result, ".sageox/** linguist-language=SageOx") {
		t.Error("result should contain new entry")
	}
	// should have blank line separator
	if !strings.Contains(result, "\n\n# SageOx") {
		t.Error("result should have blank line before SageOx section")
	}
}

func TestAddSageoxGitattributesEntries_NoMissing(t *testing.T) {
	content := "*.txt text"
	missing := []string{}

	result := addSageoxGitattributesEntries(content, missing)

	if result != content {
		t.Error("result should be unchanged when no missing entries")
	}
}

// ============================================================================
// checkEndpointNormalization Tests
// ============================================================================

func TestCheckEndpointNormalization_AllClean(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create config with normalized endpoint
	requireSageoxDir(t, gitRoot)
	cfg := &config.ProjectConfig{
		Endpoint:      "https://sageox.ai",
		ConfigVersion: config.CurrentConfigVersion,
	}
	if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// create marker with normalized endpoint
	markerData := map[string]string{
		"repo_id":  "repo_abc123",
		"endpoint": "https://sageox.ai",
	}
	markerJSON, _ := json.MarshalIndent(markerData, "", "  ")
	markerPath := filepath.Join(gitRoot, ".sageox", ".repo_abc123")
	if err := os.WriteFile(markerPath, markerJSON, 0644); err != nil {
		t.Fatalf("failed to write marker: %v", err)
	}

	result := checkEndpointNormalization(false)

	if !result.passed {
		t.Errorf("expected passed=true when all endpoints normalized, got: %+v", result)
	}
	if result.warning {
		t.Error("expected warning=false when all clean")
	}
	if result.message != "all endpoints normalized" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckEndpointNormalization_DetectsConfigPrefix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// write config with prefixed endpoint directly (bypass SaveProjectConfig normalization)
	requireSageoxDir(t, gitRoot)
	rawConfig := map[string]any{
		"config_version":         config.CurrentConfigVersion,
		"endpoint":               "https://api.sageox.ai",
		"update_frequency_hours": 24,
	}
	data, _ := json.MarshalIndent(rawConfig, "", "  ")
	configPath := filepath.Join(gitRoot, ".sageox", "config.json")
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	result := checkEndpointNormalization(false)

	if result.passed && !result.warning {
		t.Errorf("expected warning when config has prefixed endpoint, got: %+v", result)
	}
	if !strings.Contains(result.detail, "config.json") {
		t.Errorf("expected detail to mention config.json, got: %s", result.detail)
	}
}

func TestCheckEndpointNormalization_FixesConfigPrefix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// write config with prefixed endpoint directly
	requireSageoxDir(t, gitRoot)
	rawConfig := map[string]any{
		"config_version":         config.CurrentConfigVersion,
		"endpoint":               "https://api.sageox.ai",
		"update_frequency_hours": 24,
	}
	data, _ := json.MarshalIndent(rawConfig, "", "  ")
	configPath := filepath.Join(gitRoot, ".sageox", "config.json")
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	result := checkEndpointNormalization(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}

	// verify config was rewritten
	reloaded, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if reloaded.Endpoint != "https://sageox.ai" {
		t.Errorf("config endpoint not normalized after fix: %s", reloaded.Endpoint)
	}
}

func TestCheckEndpointNormalization_DetectsMarkerPrefix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	// create marker with prefixed endpoint
	markerData := map[string]string{
		"repo_id":  "repo_abc123",
		"endpoint": "https://www.sageox.ai",
	}
	markerJSON, _ := json.MarshalIndent(markerData, "", "  ")
	markerPath := filepath.Join(gitRoot, ".sageox", ".repo_abc123")
	if err := os.WriteFile(markerPath, markerJSON, 0644); err != nil {
		t.Fatalf("failed to write marker: %v", err)
	}

	result := checkEndpointNormalization(false)

	if result.passed && !result.warning {
		t.Errorf("expected warning for prefixed marker, got: %+v", result)
	}
	if !strings.Contains(result.detail, ".repo_abc123") {
		t.Errorf("expected detail to mention marker file, got: %s", result.detail)
	}
}

func TestCheckEndpointNormalization_FixesMarkerPrefix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	// create marker with prefixed endpoint and extra fields to verify preservation
	markerData := map[string]any{
		"repo_id":  "repo_abc123",
		"endpoint": "https://api.sageox.ai",
		"extra":    "preserved",
	}
	markerJSON, _ := json.MarshalIndent(markerData, "", "  ")
	markerPath := filepath.Join(gitRoot, ".sageox", ".repo_abc123")
	if err := os.WriteFile(markerPath, markerJSON, 0644); err != nil {
		t.Fatalf("failed to write marker: %v", err)
	}

	result := checkEndpointNormalization(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}

	// verify marker was rewritten with normalized endpoint
	reloadedData, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("failed to read marker: %v", err)
	}
	var reloaded map[string]any
	if err := json.Unmarshal(reloadedData, &reloaded); err != nil {
		t.Fatalf("failed to parse marker: %v", err)
	}
	if reloaded["endpoint"] != "https://sageox.ai" {
		t.Errorf("marker endpoint not normalized: %v", reloaded["endpoint"])
	}
	if reloaded["extra"] != "preserved" {
		t.Errorf("marker extra field lost after fix: %v", reloaded["extra"])
	}
}

func TestCheckEndpointNormalization_FixesMarkerLegacyApiEndpoint(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	// create marker with prefixed endpoint in both fields
	markerData := map[string]string{
		"repo_id":      "repo_abc123",
		"endpoint":     "https://api.sageox.ai",
		"api_endpoint": "https://api.sageox.ai",
	}
	markerJSON, _ := json.MarshalIndent(markerData, "", "  ")
	markerPath := filepath.Join(gitRoot, ".sageox", ".repo_abc123")
	if err := os.WriteFile(markerPath, markerJSON, 0644); err != nil {
		t.Fatalf("failed to write marker: %v", err)
	}

	result := checkEndpointNormalization(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}

	// verify both fields were normalized
	reloadedData, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("failed to read marker: %v", err)
	}
	var reloaded map[string]any
	if err := json.Unmarshal(reloadedData, &reloaded); err != nil {
		t.Fatalf("failed to parse marker: %v", err)
	}
	if reloaded["endpoint"] != "https://sageox.ai" {
		t.Errorf("marker endpoint not normalized: %v", reloaded["endpoint"])
	}
	if reloaded["api_endpoint"] != "https://sageox.ai" {
		t.Errorf("marker api_endpoint not normalized: %v", reloaded["api_endpoint"])
	}
}

func TestCheckEndpointNormalization_MultipleIssues(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	// prefixed config
	rawConfig := map[string]any{
		"config_version":         config.CurrentConfigVersion,
		"endpoint":               "https://www.sageox.ai",
		"update_frequency_hours": 24,
	}
	configData, _ := json.MarshalIndent(rawConfig, "", "  ")
	configPath := filepath.Join(gitRoot, ".sageox", "config.json")
	if err := os.WriteFile(configPath, configData, 0600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// prefixed marker
	markerData := map[string]string{
		"repo_id":  "repo_abc123",
		"endpoint": "https://app.sageox.ai",
	}
	markerJSON, _ := json.MarshalIndent(markerData, "", "  ")
	markerPath := filepath.Join(gitRoot, ".sageox", ".repo_abc123")
	if err := os.WriteFile(markerPath, markerJSON, 0644); err != nil {
		t.Fatalf("failed to write marker: %v", err)
	}

	// fix all
	result := checkEndpointNormalization(true)

	if !result.passed {
		t.Errorf("expected passed=true after fixing multiple issues, got: %+v", result)
	}
	if !strings.Contains(result.message, "normalized") {
		t.Errorf("expected message to mention 'normalized', got: %s", result.message)
	}
}

// --- Duplicate repo markers tests ---

// writeMarkerFile creates a .repo_* marker JSON file in the .sageox/ directory.
func writeMarkerFile(t *testing.T, sageoxDir, filename string, data map[string]string) {
	t.Helper()
	markerJSON, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sageoxDir, filename), markerJSON, 0644); err != nil {
		t.Fatalf("failed to write marker %s: %v", filename, err)
	}
}

func TestCheckDuplicateRepoMarkers_SingleMarker(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")

	writeMarkerFile(t, sageoxDir, ".repo_abc123", map[string]string{
		"repo_id":       "repo_abc123",
		"endpoint":      "https://sageox.ai",
		"init_at":       "2026-02-20T10:00:00Z",
		"init_by_email": "alice@example.com",
	})

	result := checkDuplicateRepoMarkers(false)

	if !result.skipped {
		t.Errorf("expected skipped for single marker, got: passed=%v warning=%v message=%s",
			result.passed, result.warning, result.message)
	}
}

func TestCheckDuplicateRepoMarkers_TwoSameEndpoint_Detected(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")

	// config.json points to repo A (the "current" one)
	cfg := &config.ProjectConfig{
		RepoID:        "repo_aaa111",
		Endpoint:      "https://sageox.ai",
		ConfigVersion: config.CurrentConfigVersion,
	}
	if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// marker A: the current user's registration
	writeMarkerFile(t, sageoxDir, ".repo_aaa111", map[string]string{
		"repo_id":       "repo_aaa111",
		"endpoint":      "https://sageox.ai",
		"init_at":       "2026-02-20T10:00:00Z",
		"init_by_email": "alice@example.com",
		"init_by_name":  "Person A",
	})

	// marker B: a teammate's registration
	writeMarkerFile(t, sageoxDir, ".repo_bbb222", map[string]string{
		"repo_id":       "repo_bbb222",
		"endpoint":      "https://sageox.ai",
		"init_at":       "2026-02-22T14:00:00Z",
		"init_by_email": "bob@example.com",
		"init_by_name":  "Person B",
	})

	result := checkDuplicateRepoMarkers(false)

	if result.passed || result.skipped {
		t.Fatalf("expected failed check, got: passed=%v skipped=%v", result.passed, result.skipped)
	}
	if !strings.Contains(result.message, "2 registrations") {
		t.Errorf("expected message to mention '2 registrations', got: %s", result.message)
	}
	if !strings.Contains(result.detail, "repo_aaa111") {
		t.Errorf("expected detail to contain repo_aaa111, got: %s", result.detail)
	}
	if !strings.Contains(result.detail, "repo_bbb222") {
		t.Errorf("expected detail to contain repo_bbb222, got: %s", result.detail)
	}
	if !strings.Contains(result.detail, "current") {
		t.Errorf("expected detail to mark current registration, got: %s", result.detail)
	}
	if !strings.Contains(result.detail, "Person A") {
		t.Errorf("expected detail to show creator name, got: %s", result.detail)
	}
}

func TestCheckDuplicateRepoMarkers_TwoDifferentEndpoints_Skipped(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")

	writeMarkerFile(t, sageoxDir, ".repo_aaa111", map[string]string{
		"repo_id":  "repo_aaa111",
		"endpoint": "https://sageox.ai",
	})

	writeMarkerFile(t, sageoxDir, ".repo_bbb222", map[string]string{
		"repo_id":  "repo_bbb222",
		"endpoint": "https://staging.sageox.ai",
	})

	result := checkDuplicateRepoMarkers(false)

	// different endpoints = not a duplicate (handled by checkMultipleEndpoints)
	if !result.skipped {
		t.Errorf("expected skipped for different endpoints, got: passed=%v message=%s",
			result.passed, result.message)
	}
}

func TestCleanupDuplicateMarkers_KeepCurrentRepo(t *testing.T) {
	// scenario: user picks THEIR repo (the one in config.json) as primary
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")

	// config initially points to repo A
	cfg := &config.ProjectConfig{
		RepoID:        "repo_aaa111",
		Endpoint:      "https://sageox.ai",
		ConfigVersion: config.CurrentConfigVersion,
	}
	if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	writeMarkerFile(t, sageoxDir, ".repo_aaa111", map[string]string{
		"repo_id":  "repo_aaa111",
		"endpoint": "https://sageox.ai",
		"init_at":  "2026-02-20T10:00:00Z",
	})
	writeMarkerFile(t, sageoxDir, ".repo_bbb222", map[string]string{
		"repo_id":  "repo_bbb222",
		"endpoint": "https://sageox.ai",
		"init_at":  "2026-02-22T14:00:00Z",
	})

	// user picks their own repo (A) as primary
	selected := duplicateMarker{
		filename: ".repo_aaa111",
		data:     repoMarkerFullData{RepoID: "repo_aaa111", Endpoint: "https://sageox.ai"},
	}
	all := []duplicateMarker{
		{filename: ".repo_aaa111", data: repoMarkerFullData{RepoID: "repo_aaa111"}},
		{filename: ".repo_bbb222", data: repoMarkerFullData{RepoID: "repo_bbb222"}},
	}

	cleanupDuplicateMarkers(gitRoot, sageoxDir, cfg, selected, all)

	// config.json should still have repo A (unchanged)
	updatedCfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		t.Fatalf("failed to load config after cleanup: %v", err)
	}
	if updatedCfg.RepoID != "repo_aaa111" {
		t.Errorf("expected config repo_id=repo_aaa111, got: %s", updatedCfg.RepoID)
	}

	// marker A should still exist
	if _, err := os.Stat(filepath.Join(sageoxDir, ".repo_aaa111")); os.IsNotExist(err) {
		t.Error("expected .repo_aaa111 to still exist after cleanup")
	}

	// marker B should be removed
	if _, err := os.Stat(filepath.Join(sageoxDir, ".repo_bbb222")); !os.IsNotExist(err) {
		t.Error("expected .repo_bbb222 to be removed after cleanup")
	}
}

func TestCleanupDuplicateMarkers_KeepOtherRepo(t *testing.T) {
	// scenario: user picks the OTHER person's repo as primary
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")

	// config initially points to repo A (our repo)
	cfg := &config.ProjectConfig{
		RepoID:        "repo_aaa111",
		Endpoint:      "https://sageox.ai",
		ConfigVersion: config.CurrentConfigVersion,
	}
	if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	writeMarkerFile(t, sageoxDir, ".repo_aaa111", map[string]string{
		"repo_id":  "repo_aaa111",
		"endpoint": "https://sageox.ai",
		"init_at":  "2026-02-20T10:00:00Z",
	})
	writeMarkerFile(t, sageoxDir, ".repo_bbb222", map[string]string{
		"repo_id":  "repo_bbb222",
		"endpoint": "https://sageox.ai",
		"init_at":  "2026-02-22T14:00:00Z",
	})

	// user picks the OTHER repo (B) as primary
	selected := duplicateMarker{
		filename: ".repo_bbb222",
		data:     repoMarkerFullData{RepoID: "repo_bbb222", Endpoint: "https://sageox.ai"},
	}
	all := []duplicateMarker{
		{filename: ".repo_aaa111", data: repoMarkerFullData{RepoID: "repo_aaa111"}},
		{filename: ".repo_bbb222", data: repoMarkerFullData{RepoID: "repo_bbb222"}},
	}

	cleanupDuplicateMarkers(gitRoot, sageoxDir, cfg, selected, all)

	// config.json should now point to repo B
	updatedCfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		t.Fatalf("failed to load config after cleanup: %v", err)
	}
	if updatedCfg.RepoID != "repo_bbb222" {
		t.Errorf("expected config repo_id=repo_bbb222 after picking other repo, got: %s", updatedCfg.RepoID)
	}

	// marker B should still exist
	if _, err := os.Stat(filepath.Join(sageoxDir, ".repo_bbb222")); os.IsNotExist(err) {
		t.Error("expected .repo_bbb222 to still exist after cleanup")
	}

	// marker A should be removed
	if _, err := os.Stat(filepath.Join(sageoxDir, ".repo_aaa111")); !os.IsNotExist(err) {
		t.Error("expected .repo_aaa111 to be removed after cleanup")
	}
}
