package lfs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveRestorableRef_OnlyVouchedContent covers the rule that hydrated bytes become a pointer only
// when an independent ref names their exact sha256 and size. Without it, a stale manifest could pointerize
// the only local copy of a recording.
func TestResolveRestorableRef_OnlyVouchedContent(t *testing.T) {
	content := []byte("{\"role\":\"user\"}\n")
	good := NewFileRef(content)
	otherSize := FileRef{Storage: StorageLFS, OID: good.OID, Size: good.Size + 1}
	otherOID := NewFileRef([]byte("something else\n"))
	gitStored := FileRef{Storage: StorageGit, Size: good.Size}

	tests := []struct {
		name       string
		meta       *SessionMeta
		committed  *FileRef
		wantOK     bool
		wantSource RestoreSource
	}{
		{"committed pointer matches", nil, &good, true, RestoreSourceCommitted},
		{"manifest matches", &SessionMeta{Files: map[string]FileRef{"raw.jsonl": good}}, nil, true, RestoreSourceManifest},
		{"committed pointer wins over manifest", &SessionMeta{Files: map[string]FileRef{"raw.jsonl": good}}, &good, true, RestoreSourceCommitted},
		{"nothing known", nil, nil, false, ""},
		{"manifest names other content", &SessionMeta{Files: map[string]FileRef{"raw.jsonl": otherOID}}, nil, false, ""},
		{"manifest size disagrees", &SessionMeta{Files: map[string]FileRef{"raw.jsonl": otherSize}}, nil, false, ""},
		{"committed pointer names other content", nil, &otherOID, false, ""},
		{"manifest entry is storage git", &SessionMeta{Files: map[string]FileRef{"raw.jsonl": gitStored}}, nil, false, ""},
		{"manifest has no entry for the file", &SessionMeta{Files: map[string]FileRef{"summary.md": good}}, nil, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, source, ok := ResolveRestorableRef(content, "raw.jsonl", tt.meta, tt.committed)

			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantSource, source)
			if tt.wantOK {
				assert.Equal(t, good, ref)
			}
		})
	}
}

func TestIsContentArtifactAndStoredInGit(t *testing.T) {
	assert.True(t, IsContentArtifact("raw.jsonl"))
	assert.True(t, IsContentArtifact("nested/context-trace.jsonl"))
	assert.False(t, IsContentArtifact("meta.json"))
	assert.False(t, IsContentArtifact("notes.txt"))

	var nilMeta *SessionMeta
	assert.False(t, nilMeta.StoredInGit("summary.md"))
	meta := &SessionMeta{Files: map[string]FileRef{"summary.md": NewGitFileRef(3), "raw.jsonl": NewFileRef([]byte("x"))}}
	assert.True(t, meta.StoredInGit("summary.md"))
	assert.False(t, meta.StoredInGit("raw.jsonl"))
	assert.False(t, meta.StoredInGit("missing.md"))
}

// TestRestorePointer_KeepsLocalCopy covers the no-data-loss contract: pointerizing a hydrated file keeps its
// bytes in the cache, never overwrites a cache copy that already exists, and refuses a ref that does not
// describe the file.
func TestRestorePointer_KeepsLocalCopy(t *testing.T) {
	content := []byte("hydrated session bytes\n")
	ref := NewFileRef(content)

	t.Run("writes pointer and preserves content in cache", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "sessions", "s1", "raw.jsonl")
		cache := filepath.Join(dir, "cache", "sessions", "s1", "raw.jsonl")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, content, 0o644))

		require.NoError(t, RestorePointer(path, cache, content, ref))

		assert.True(t, IsPointerFile(path))
		cached, err := os.ReadFile(cache)
		require.NoError(t, err)
		assert.Equal(t, content, cached)
	})

	t.Run("an identical cache copy is kept", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "raw.jsonl")
		cache := filepath.Join(dir, "cache", "raw.jsonl")
		require.NoError(t, os.WriteFile(path, content, 0o644))
		require.NoError(t, os.MkdirAll(filepath.Dir(cache), 0o755))
		require.NoError(t, os.WriteFile(cache, content, 0o600))

		require.NoError(t, RestorePointer(path, cache, content, ref))

		assert.True(t, IsPointerFile(path))
	})

	t.Run("a mismatched cache copy fails closed and the file is untouched", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "raw.jsonl")
		cache := filepath.Join(dir, "cache", "raw.jsonl")
		require.NoError(t, os.WriteFile(path, content, 0o644))
		require.NoError(t, os.MkdirAll(filepath.Dir(cache), 0o755))
		require.NoError(t, os.WriteFile(cache, []byte("already hydrated\n"), 0o600))

		err := RestorePointer(path, cache, content, ref)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "differs")
		onDisk, _ := os.ReadFile(path)
		assert.Equal(t, content, onDisk, "no copy of these bytes exists outside the file, so it must stay")
		cached, _ := os.ReadFile(cache)
		assert.Equal(t, "already hydrated\n", string(cached))
	})

	t.Run("a ref that does not match the bytes is refused and the file is untouched", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "raw.jsonl")
		require.NoError(t, os.WriteFile(path, content, 0o644))

		err := RestorePointer(path, "", content, NewFileRef([]byte("different\n")))

		require.Error(t, err)
		onDisk, _ := os.ReadFile(path)
		assert.Equal(t, content, onDisk)
	})

	t.Run("an unwritable cache location fails before the file changes", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "raw.jsonl")
		require.NoError(t, os.WriteFile(path, content, 0o644))
		blocker := filepath.Join(dir, "blocker")
		require.NoError(t, os.WriteFile(blocker, []byte("a file where a directory is needed"), 0o644))

		err := RestorePointer(path, filepath.Join(blocker, "raw.jsonl"), content, ref)

		require.Error(t, err)
		onDisk, _ := os.ReadFile(path)
		assert.Equal(t, content, onDisk, "the hydrated file must stay when its copy could not be preserved")
	})
}
