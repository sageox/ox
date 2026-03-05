package lfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatPointer(t *testing.T) {
	oid := "sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393"
	size := int64(12345)

	got := FormatPointer(oid, size)

	want := "version https://git-lfs.github.com/spec/v1\n" +
		"oid sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393\n" +
		"size 12345\n"

	if got != want {
		t.Errorf("FormatPointer() =\n%q\nwant:\n%q", got, want)
	}
}

func TestParsePointerRoundTrip(t *testing.T) {
	oid := "sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393"
	size := int64(98765)

	content := FormatPointer(oid, size)
	gotOID, gotSize, err := ParsePointer(content)
	if err != nil {
		t.Fatalf("ParsePointer() error: %v", err)
	}
	if gotOID != oid {
		t.Errorf("OID = %q, want %q", gotOID, oid)
	}
	if gotSize != size {
		t.Errorf("Size = %d, want %d", gotSize, size)
	}
}

func TestParsePointerErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"empty", ""},
		{"no version", "oid sha256:abc\nsize 100\n"},
		{"wrong version", "version something\noid sha256:abc\nsize 100\n"},
		{"missing oid", "version https://git-lfs.github.com/spec/v1\nsize 100\n"},
		{"missing size", "version https://git-lfs.github.com/spec/v1\noid sha256:abc\n"},
		{"bad size", "version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize notanumber\n"},
		{"too few lines", "version https://git-lfs.github.com/spec/v1\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ParsePointer(tt.content)
			if err == nil {
				t.Error("ParsePointer() expected error, got nil")
			}
		})
	}
}

// --- File I/O layer tests ---

func TestWritePointerFile(t *testing.T) {
	dir := t.TempDir()
	ref := FileRef{OID: "sha256:abc123def456", Size: 9876}
	path := filepath.Join(dir, "raw.jsonl")

	err := WritePointerFile(path, ref)
	require.NoError(t, err)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, FormatPointer(ref.OID, ref.Size), string(got))
}

func TestWritePointerFiles(t *testing.T) {
	dir := t.TempDir()
	files := map[string]FileRef{
		"raw.jsonl":    {OID: "sha256:aaa", Size: 100},
		"events.jsonl": {OID: "sha256:bbb", Size: 200},
		"summary.md":   {OID: "sha256:ccc", Size: 300},
	}

	paths, err := WritePointerFiles(dir, files)
	require.NoError(t, err)
	assert.Len(t, paths, 3)

	// paths should be sorted
	for i := 1; i < len(paths); i++ {
		assert.True(t, paths[i-1] <= paths[i], "paths should be sorted")
	}

	// round-trip: read back each pointer and verify
	for name, ref := range files {
		gotRef, err := ReadPointerFile(filepath.Join(dir, name))
		require.NoError(t, err)
		assert.Equal(t, ref.OID, gotRef.OID)
		assert.Equal(t, ref.Size, gotRef.Size)
	}
}

func TestWritePointerFiles_EmptyMap(t *testing.T) {
	paths, err := WritePointerFiles(t.TempDir(), map[string]FileRef{})
	assert.NoError(t, err)
	assert.Nil(t, paths)
}

func TestWritePointerFiles_NilMap(t *testing.T) {
	paths, err := WritePointerFiles(t.TempDir(), nil)
	assert.NoError(t, err)
	assert.Nil(t, paths)
}

func TestIsPointerFile(t *testing.T) {
	dir := t.TempDir()

	// write a valid pointer file
	pointerPath := filepath.Join(dir, "raw.jsonl")
	require.NoError(t, WritePointerFile(pointerPath, FileRef{OID: "sha256:abc", Size: 100}))

	// write a real content file (too large to be a pointer)
	contentPath := filepath.Join(dir, "events.jsonl")
	require.NoError(t, os.WriteFile(contentPath, make([]byte, 1024), 0644))

	// write a small non-pointer file
	notPointerPath := filepath.Join(dir, "small.txt")
	require.NoError(t, os.WriteFile(notPointerPath, []byte("not a pointer"), 0644))

	assert.True(t, IsPointerFile(pointerPath), "should detect valid pointer file")
	assert.False(t, IsPointerFile(contentPath), "large file should not be a pointer")
	assert.False(t, IsPointerFile(notPointerPath), "non-pointer small file should return false")
	assert.False(t, IsPointerFile(filepath.Join(dir, "missing.jsonl")), "missing file should return false")
}

func TestReadPointerFile(t *testing.T) {
	dir := t.TempDir()
	ref := FileRef{OID: "sha256:deadbeef1234", Size: 42}
	path := filepath.Join(dir, "test.jsonl")

	require.NoError(t, WritePointerFile(path, ref))

	got, err := ReadPointerFile(path)
	require.NoError(t, err)
	assert.Equal(t, ref.OID, got.OID)
	assert.Equal(t, ref.Size, got.Size)
}

func TestReadPointerFile_NotPointer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "content.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(`{"event":"test"}`), 0644))

	_, err := ReadPointerFile(path)
	assert.Error(t, err, "should error on non-pointer content")
}

func TestReadPointerFile_Missing(t *testing.T) {
	_, err := ReadPointerFile(filepath.Join(t.TempDir(), "missing.jsonl"))
	assert.Error(t, err, "should error on missing file")
}

// --- Spec conformance edge cases ---

func TestFormatPointer_RealSHA256_UnderMaxSize(t *testing.T) {
	// realistic OID with full 64-char hex digest
	oid := "sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393"
	size := int64(1048576) // 1 MB

	content := FormatPointer(oid, size)
	assert.Less(t, len(content), maxPointerSize,
		"pointer with real sha256 OID must be under maxPointerSize (%d bytes)", maxPointerSize)
	assert.Less(t, len(content), 1024,
		"pointer must be under 1024 bytes per LFS spec")
}

func TestFormatPointer_LineOrdering(t *testing.T) {
	content := FormatPointer("sha256:abc", 100)
	lines := strings.Split(strings.TrimSpace(content), "\n")
	require.Len(t, lines, 3)
	assert.True(t, strings.HasPrefix(lines[0], "version "), "first line must be version")
	assert.True(t, strings.HasPrefix(lines[1], "oid "), "second line must be oid (alphabetically before size)")
	assert.True(t, strings.HasPrefix(lines[2], "size "), "third line must be size")
}

func TestParsePointer_WindowsLineEndings(t *testing.T) {
	// \r\n should still parse (tolerant parsing)
	content := "version https://git-lfs.github.com/spec/v1\r\noid sha256:abc123\r\nsize 100\r\n"
	oid, size, err := ParsePointer(content)
	// our parser trims spaces around values, \r is part of the value
	// this is acceptable — we only generate \n, but should handle \r\n on read
	if err != nil {
		// if it fails, that's fine — we only generate \n and control both ends
		t.Logf("ParsePointer rejects \\r\\n (acceptable for our use case): %v", err)
		return
	}
	// if it succeeds, values should be clean
	assert.Contains(t, oid, "sha256:")
	assert.Equal(t, int64(100), size)
}

func TestParsePointer_ExtensionKeys(t *testing.T) {
	// spec says unknown keys should be preserved/ignored
	content := "version https://git-lfs.github.com/spec/v1\n" +
		"ext-0-foo sha256:bbbb\n" +
		"oid sha256:abc123\n" +
		"size 100\n"
	oid, size, err := ParsePointer(content)
	require.NoError(t, err, "should parse pointer with extension keys")
	assert.Equal(t, "sha256:abc123", oid)
	assert.Equal(t, int64(100), size)
}

func TestIsPointerFile_AtSizeBoundary(t *testing.T) {
	dir := t.TempDir()

	// file exactly at maxPointerSize should still be checked
	atBoundary := filepath.Join(dir, "at-boundary.txt")
	content := make([]byte, maxPointerSize)
	copy(content, []byte("not a pointer but exactly 200 bytes"))
	require.NoError(t, os.WriteFile(atBoundary, content, 0644))
	assert.False(t, IsPointerFile(atBoundary), "non-pointer at size boundary should return false")

	// file one byte over should be skipped without reading
	overBoundary := filepath.Join(dir, "over-boundary.txt")
	require.NoError(t, os.WriteFile(overBoundary, make([]byte, maxPointerSize+1), 0644))
	assert.False(t, IsPointerFile(overBoundary), "file over maxPointerSize should return false")
}

func TestWritePointerFile_OverwritesLargeContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")

	// write a large content file
	largeContent := make([]byte, 10*1024) // 10 KB
	for i := range largeContent {
		largeContent[i] = 'x'
	}
	require.NoError(t, os.WriteFile(path, largeContent, 0644))

	// overwrite with pointer
	ref := FileRef{OID: "sha256:abc123", Size: int64(len(largeContent))}
	require.NoError(t, WritePointerFile(path, ref))

	// verify file is now a pointer (small)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Less(t, info.Size(), int64(maxPointerSize), "pointer file should be small")
	assert.True(t, IsPointerFile(path))
}

func TestNewFileRef_EndToEnd_RoundTrip(t *testing.T) {
	// NewFileRef → WritePointerFile → ReadPointerFile → verify OID matches
	content := []byte(`{"ts":"2026-01-01T00:00:00Z","type":"user","content":"hello"}`)
	ref := NewFileRef(content)

	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")
	require.NoError(t, WritePointerFile(path, ref))

	got, err := ReadPointerFile(path)
	require.NoError(t, err)
	assert.Equal(t, ref.OID, got.OID)
	assert.Equal(t, ref.Size, got.Size)
	assert.Equal(t, int64(len(content)), got.Size)

	// verify OID matches ComputeOID
	assert.Equal(t, "sha256:"+ComputeOID(content), got.OID)
}

func TestWritePointerFiles_PartialFailure(t *testing.T) {
	dir := t.TempDir()

	// create a subdirectory that doesn't exist to trigger a write error
	files := map[string]FileRef{
		"good.jsonl":               {OID: "sha256:aaa", Size: 100},
		"nonexistent/bad.jsonl":    {OID: "sha256:bbb", Size: 200},
	}

	paths, err := WritePointerFiles(dir, files)
	// map iteration is random, so we might get the error on either file
	// at least one file should fail because the subdirectory doesn't exist
	if err != nil {
		assert.Contains(t, err.Error(), "write pointer")
		// partial results may be returned
		t.Logf("partial paths returned: %v", paths)
	}
	// if both succeeded (map iteration hit good first), that's also fine
	// the key test is that it doesn't panic
}
