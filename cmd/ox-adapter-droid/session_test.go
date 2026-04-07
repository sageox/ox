package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectDirMatchesRepo_SymlinkResolution(t *testing.T) {
	// create a real repo dir and resolve any intermediate symlinks (e.g., /tmp -> /private/tmp on macOS)
	realRepoDir := t.TempDir()
	realRepoDir, err := filepath.EvalSymlinks(realRepoDir)
	if err != nil {
		t.Fatalf("EvalSymlinks on realRepoDir: %v", err)
	}

	// create a project dir with a session file whose cwd points to the real repo
	projectDir := t.TempDir()
	sessionContent := fmt.Sprintf(`{"type":"session_start","cwd":"%s"}`, realRepoDir)
	if err := os.WriteFile(filepath.Join(projectDir, "test-session.jsonl"), []byte(sessionContent+"\n"), 0644); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	// create a symlink pointing to the real repo dir
	symlinkDir := filepath.Join(t.TempDir(), "symlink-repo")
	if err := os.Symlink(realRepoDir, symlinkDir); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	// symlink path should match the real repo
	if !projectDirMatchesRepo(projectDir, symlinkDir) {
		t.Errorf("expected projectDirMatchesRepo(projectDir, symlinkPath) to be true")
	}

	// real path should also still match
	if !projectDirMatchesRepo(projectDir, realRepoDir) {
		t.Errorf("expected projectDirMatchesRepo(projectDir, realRepoDir) to be true")
	}
}

func TestProjectDirMatchesRepo_ReverseSymlink(t *testing.T) {
	// create a real repo dir and resolve intermediate symlinks
	realRepoDir := t.TempDir()
	realRepoDir, err := filepath.EvalSymlinks(realRepoDir)
	if err != nil {
		t.Fatalf("EvalSymlinks on realRepoDir: %v", err)
	}

	// create a symlink pointing to the real repo dir
	symlinkDir := filepath.Join(t.TempDir(), "symlink-repo")
	if err := os.Symlink(realRepoDir, symlinkDir); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	// write a session file whose cwd is the symlink path (reverse case)
	projectDir := t.TempDir()
	sessionContent := fmt.Sprintf(`{"type":"session_start","cwd":"%s"}`, symlinkDir)
	if err := os.WriteFile(filepath.Join(projectDir, "test-session.jsonl"), []byte(sessionContent+"\n"), 0644); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	// real repo path should match even though session recorded the symlink path
	if !projectDirMatchesRepo(projectDir, realRepoDir) {
		t.Errorf("expected projectDirMatchesRepo(projectDir, realRepoDir) to be true when session cwd is symlink")
	}
}
