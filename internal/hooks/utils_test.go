package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureDir_ExistingDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, EnsureDir(dir), "existing dir should be a noop")
}

func TestEnsureDir_NestedCreation(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "a", "b", "c")
	require.NoError(t, EnsureDir(dir))
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestReadJSON_NonexistentFile(t *testing.T) {
	t.Parallel()
	var target map[string]string
	err := ReadJSON(filepath.Join(t.TempDir(), "missing.json"), &target)
	require.NoError(t, err, "nonexistent file should not error")
	assert.Nil(t, target, "target should remain unchanged")
}

func TestReadJSON_EmptyFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "empty.json")
	require.NoError(t, os.WriteFile(path, []byte{}, 0644))

	var target map[string]string
	err := ReadJSON(path, &target)
	require.NoError(t, err, "empty file should not error")
	assert.Nil(t, target, "target should remain unchanged")
}

func TestReadJSON_MalformedJSON(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(path, []byte(`{invalid`), 0644))

	var target map[string]string
	err := ReadJSON(path, &target)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse JSON")
}

func TestReadJSON_ValidJSON(t *testing.T) {
	t.Parallel()
	type cfg struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	path := filepath.Join(t.TempDir(), "valid.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"name":"test","count":42}`), 0644))

	var target cfg
	require.NoError(t, ReadJSON(path, &target))
	assert.Equal(t, "test", target.Name)
	assert.Equal(t, 42, target.Count)
}

func TestWriteJSON_RoundTrip(t *testing.T) {
	t.Parallel()
	type cfg struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	path := filepath.Join(t.TempDir(), "roundtrip.json")
	original := cfg{Name: "roundtrip", Count: 7}
	require.NoError(t, WriteJSON(path, original, 0644))

	var loaded cfg
	require.NoError(t, ReadJSON(path, &loaded))
	assert.Equal(t, original, loaded)
}

func TestWriteJSON_ReadOnlyDir(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "readonly", "file.json")
	// parent doesn't exist and we don't create it — WriteJSON doesn't call MkdirAll
	err := WriteJSON(path, map[string]string{"k": "v"}, 0644)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to write file")
}

func TestFileExists_File(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "exists.txt")
	require.NoError(t, os.WriteFile(path, []byte("hi"), 0644))
	assert.True(t, FileExists(path))
}

func TestFileExists_Directory(t *testing.T) {
	t.Parallel()
	assert.True(t, FileExists(t.TempDir()))
}

func TestFileExists_Missing(t *testing.T) {
	t.Parallel()
	assert.False(t, FileExists(filepath.Join(t.TempDir(), "nope")))
}
