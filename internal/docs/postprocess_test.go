package docs

import (
	"strings"
	"testing"
)

func TestTransformFile_DirectoryPath(t *testing.T) {
	srcDir := t.TempDir()
	destPath := t.TempDir() + "/out.md"

	err := transformFile(srcDir, destPath, 1)
	if err == nil {
		t.Fatal("expected error for directory path, got nil")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("expected 'not a regular file' error, got: %v", err)
	}
}
