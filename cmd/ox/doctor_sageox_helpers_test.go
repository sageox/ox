//go:build !short

package main

import (
	"os"
	"os/exec"
	"testing"
)

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
