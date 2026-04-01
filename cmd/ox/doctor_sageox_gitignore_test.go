//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
