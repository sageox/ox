//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
