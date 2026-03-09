package notification

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckForUpdates_EmptyPath(t *testing.T) {
	latestMtime, updatedFiles := CheckForUpdates("", time.Now())

	if !latestMtime.IsZero() {
		t.Errorf("latestMtime should be zero for empty path, got %v", latestMtime)
	}
	if len(updatedFiles) != 0 {
		t.Errorf("updatedFiles should be empty for empty path, got %v", updatedFiles)
	}
}

func TestCheckForUpdates_NonexistentPath(t *testing.T) {
	tmpDir := t.TempDir()
	nonexistentPath := filepath.Join(tmpDir, "does-not-exist")

	latestMtime, updatedFiles := CheckForUpdates(nonexistentPath, time.Now())

	// should return zero values since no files exist
	if !latestMtime.IsZero() {
		t.Errorf("latestMtime should be zero for non-existent path, got %v", latestMtime)
	}
	if len(updatedFiles) != 0 {
		t.Errorf("updatedFiles should be empty for non-existent path, got %v", updatedFiles)
	}
}

func TestCheckForUpdates_FirstRun_ZeroTime(t *testing.T) {
	tmpDir := t.TempDir()

	// create a watched file
	fileDir := filepath.Join(tmpDir, "agent-context")
	if err := os.MkdirAll(fileDir, 0755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}
	filePath := filepath.Join(fileDir, "distilled-discussions.md")
	if err := os.WriteFile(filePath, []byte("test content"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	// first run with zero time - should NOT return files as updated (avoid spam)
	latestMtime, updatedFiles := CheckForUpdates(tmpDir, time.Time{})

	if latestMtime.IsZero() {
		t.Error("latestMtime should be set even on first run")
	}
	if len(updatedFiles) != 0 {
		t.Errorf("first run should NOT report files as updated (avoid spam), got %v", updatedFiles)
	}
}

func TestCheckForUpdates_DetectsNewChanges(t *testing.T) {
	tmpDir := t.TempDir()

	// create a watched file
	fileDir := filepath.Join(tmpDir, "agent-context")
	if err := os.MkdirAll(fileDir, 0755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}
	filePath := filepath.Join(fileDir, "distilled-discussions.md")
	if err := os.WriteFile(filePath, []byte("initial content"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	// first check to initialize
	latestMtime, _ := CheckForUpdates(tmpDir, time.Time{})

	// wait a bit to ensure mtime is different
	time.Sleep(50 * time.Millisecond)

	// modify the file
	if err := os.WriteFile(filePath, []byte("updated content"), 0644); err != nil {
		t.Fatalf("failed to update file: %v", err)
	}

	// second check with old lastNotified
	_, updatedFiles := CheckForUpdates(tmpDir, latestMtime)

	if len(updatedFiles) != 1 {
		t.Errorf("expected 1 updated file, got %d", len(updatedFiles))
	}
	if len(updatedFiles) > 0 && updatedFiles[0] != filePath {
		t.Errorf("expected %s, got %s", filePath, updatedFiles[0])
	}
}

func TestCheckForUpdates_ClockSkewTolerance(t *testing.T) {
	tmpDir := t.TempDir()

	// create a watched file
	fileDir := filepath.Join(tmpDir, "agent-context")
	if err := os.MkdirAll(fileDir, 0755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}
	filePath := filepath.Join(fileDir, "distilled-discussions.md")
	if err := os.WriteFile(filePath, []byte("test content"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	// get the file's mtime
	info, _ := os.Stat(filePath)
	fileMtime := info.ModTime()

	// set lastNotified slightly AFTER file mtime (within tolerance)
	// this simulates clock skew where CLI clock is ahead
	lastNotified := fileMtime.Add(2 * time.Second)

	_, updatedFiles := CheckForUpdates(tmpDir, lastNotified)

	// should still detect as updated due to tolerance
	if len(updatedFiles) != 1 {
		t.Errorf("expected file to be detected within clock skew tolerance, got %d files", len(updatedFiles))
	}
}

func TestCheckForUpdates_MissingFileOK(t *testing.T) {
	tmpDir := t.TempDir()

	// don't create any files - should not error
	lastNotified := time.Now().Add(-1 * time.Hour)

	latestMtime, updatedFiles := CheckForUpdates(tmpDir, lastNotified)

	// should return zero values but no error
	if !latestMtime.IsZero() {
		t.Errorf("latestMtime should be zero when no files exist, got %v", latestMtime)
	}
	if len(updatedFiles) != 0 {
		t.Errorf("should not report missing files as updated, got %v", updatedFiles)
	}
}

func TestCheckForUpdates_MultipleFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// create multiple watched files
	files := []struct {
		dir  string
		name string
	}{
		{"agent-context", "distilled-discussions.md"},
		{"coworkers", "AGENTS.md"},
		{"coworkers", "CLAUDE.md"},
	}

	for _, f := range files {
		dir := filepath.Join(tmpDir, f.dir)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("failed to create dir %s: %v", dir, err)
		}
		filePath := filepath.Join(dir, f.name)
		if err := os.WriteFile(filePath, []byte("content"), 0644); err != nil {
			t.Fatalf("failed to write file %s: %v", filePath, err)
		}
	}

	// check with old lastNotified
	lastNotified := time.Now().Add(-1 * time.Hour)
	latestMtime, updatedFiles := CheckForUpdates(tmpDir, lastNotified)

	if latestMtime.IsZero() {
		t.Error("latestMtime should not be zero with files present")
	}
	if len(updatedFiles) != 3 {
		t.Errorf("expected 3 updated files, got %d", len(updatedFiles))
	}
}
