package fileutil

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAtomicWriteJSON_NonexistentDirectory(t *testing.T) {
	t.Parallel()
	// writing to a path where the parent directory doesn't exist should fail
	path := filepath.Join(t.TempDir(), "does", "not", "exist", "test.json")
	err := AtomicWriteJSON(path, "data", 0600)
	assert.Error(t, err, "expected error when parent dir doesn't exist")
	assert.Contains(t, err.Error(), "create temp file")
}

func TestAtomicWriteJSON_OverwriteExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "overwrite.json")

	// write initial data
	require.NoError(t, AtomicWriteJSON(path, map[string]string{"v": "1"}, 0600))

	// overwrite with new data
	require.NoError(t, AtomicWriteJSON(path, map[string]string{"v": "2"}, 0600))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var got map[string]string
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, "2", got["v"])
}

func TestAtomicWriteJSON_IndentedOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "indent.json")

	data := map[string]int{"a": 1, "b": 2}
	require.NoError(t, AtomicWriteJSON(path, data, 0644))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	// json.Encoder with SetIndent should produce indented output
	assert.Contains(t, string(raw), "\n")
	assert.Contains(t, string(raw), "  ")
}

func TestAtomicWriteJSON_ComplexTypes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	tests := []struct {
		name string
		data any
	}{
		{"nil_value", nil},
		{"empty_string", ""},
		{"integer", 42},
		{"float", 3.14},
		{"bool", true},
		{"nested_struct", struct {
			Name  string `json:"name"`
			Items []int  `json:"items"`
		}{Name: "test", Items: []int{1, 2, 3}}},
		{"empty_slice", []string{}},
		{"empty_map", map[string]string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(dir, tt.name+".json")
			err := AtomicWriteJSON(path, tt.data, 0644)
			assert.NoError(t, err, "failed to write %s", tt.name)

			// verify we can read it back
			raw, err := os.ReadFile(path)
			assert.NoError(t, err)
			assert.NotEmpty(t, raw)
		})
	}
}

func TestAtomicWriteJSON_DifferentPermissions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	perms := []os.FileMode{0644, 0600, 0400, 0666}
	for _, perm := range perms {
		path := filepath.Join(dir, "perm_test.json")
		require.NoError(t, AtomicWriteJSON(path, "test", perm))

		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, perm, info.Mode().Perm(), "expected perm %o", perm)

		// cleanup for next iteration
		os.Remove(path)
	}
}

func TestAtomicWriteJSON_NoLeftoverTempOnSuccess(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "clean.json")

	require.NoError(t, AtomicWriteJSON(path, "test", 0600))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	// should only have the target file, no temp files
	assert.Len(t, entries, 1)
	assert.Equal(t, "clean.json", entries[0].Name())
}
