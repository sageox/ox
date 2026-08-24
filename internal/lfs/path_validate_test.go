package lfs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateRelativePath(t *testing.T) {
	t.Parallel()

	valid := []string{
		"raw.jsonl",
		"summary.md",
		"session.html",
		"subdir/file.txt",
	}
	for _, name := range valid {
		t.Run("valid/"+name, func(t *testing.T) {
			assert.NoError(t, ValidateRelativePath(name))
		})
	}

	invalid := []struct {
		name string
		path string
	}{
		{"empty", ""},
		{"absolute unix", "/etc/passwd"},
		{"absolute windows", "C:\\Users\\file"},
		{"parent traversal", "../../../.ssh/authorized_keys"},
		{"mid traversal", "foo/../../../etc/passwd"},
		{"backslash", "foo\\bar"},
		{"dot-dot only", ".."},
		{"leading dot-dot", "../file"},
		{"unclean", "foo//bar"},
		{"trailing slash", "foo/"},
		{"dot current", "./foo"},
	}
	for _, tt := range invalid {
		t.Run("invalid/"+tt.name, func(t *testing.T) {
			assert.Error(t, ValidateRelativePath(tt.path))
		})
	}
}

func TestReadSessionMeta_RejectsPathTraversalFilenames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// write a meta.json with a malicious filename
	meta := &SessionMeta{
		Version:     "1.0",
		SessionName: "test",
		Username:    "attacker",
		AgentID:     "a1b2",
		AgentType:   "claude-code",
		Files: map[string]FileRef{
			"../../../.ssh/authorized_keys": {
				Storage: StorageLFS,
				OID:     "sha256:deadbeef",
				Size:    1024,
			},
		},
	}
	// write directly to bypass validation in WriteSessionMetaOnly
	data, err := json.MarshalIndent(meta, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), data, 0644))

	_, err = ReadSessionMeta(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsafe filename")
}

func TestWritePointerFiles_RejectsPathTraversal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	files := map[string]FileRef{
		"../escape.txt": {Storage: StorageLFS, OID: "sha256:abc", Size: 100},
	}
	_, err := WritePointerFiles(dir, AssertUploadedManifest(files))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsafe pointer filename")
}
