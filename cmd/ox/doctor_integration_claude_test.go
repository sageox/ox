//go:build !short

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckClaudeCodeIntegration_NoProjectHooks(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	result := checkClaudeCodeHooks(false)

	if result.passed {
		t.Errorf("expected failed when no project hooks, got: %+v", result)
	}
	if result.name != "Claude Code hooks" {
		t.Errorf("unexpected name: %s", result.name)
	}
}

func TestCheckClaudeCodeIntegration_ProjectHooksInstalled(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// install project-level hooks
	if err := InstallProjectClaudeHooks(gitRoot); err != nil {
		t.Fatalf("failed to install project hooks: %v", err)
	}

	result := checkClaudeCodeHooks(false)

	if !result.passed {
		t.Errorf("expected passed=true when project hooks installed, got: %+v", result)
	}
	if result.message != "installed (shared)" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

// TestDetectClaudeCode tests detectClaudeCode function
func TestDetectClaudeCode_ProjectClaudeDir(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	claudeDir := filepath.Join(gitRoot, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatalf("failed to create .claude: %v", err)
	}

	detected := detectClaudeCode()

	if !detected {
		t.Error("expected detectClaudeCode()=true when .claude directory exists")
	}
}

func TestDetectClaudeCode_ProjectCLAUDEMd(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	claudePath := filepath.Join(gitRoot, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte("# Instructions\n"), 0644); err != nil {
		t.Fatalf("failed to create CLAUDE.md: %v", err)
	}

	detected := detectClaudeCode()

	if !detected {
		t.Error("expected detectClaudeCode()=true when CLAUDE.md exists")
	}
}

// TestDetectClaudeCode_NotDetected pins the negative case by controlling both
// inputs the detector reads: the working directory and HOME.
//
// Failure prevented: the test asserted nothing because the real HOME decided
// the answer — on any machine that has ever run Claude Code, the user-level
// config makes detection true no matter what the project contains, so a
// detector that always returned true would have passed.
func TestDetectClaudeCode_NotDetected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	restoreCwd := changeToDir(t, t.TempDir())
	defer restoreCwd()

	if detectClaudeCode() {
		t.Error("detectClaudeCode() = true with no project markers and an empty home, want false")
	}
}
