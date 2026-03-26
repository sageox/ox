package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyFile_Success(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "source.txt")
	dstPath := filepath.Join(tmpDir, "dest.txt")

	content := "hello world\nline two\n"
	os.WriteFile(srcPath, []byte(content), 0644)

	err := copyFile(srcPath, dstPath)
	if err != nil {
		t.Fatalf("copyFile: %v", err)
	}

	got, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != content {
		t.Errorf("content mismatch: got %q, want %q", string(got), content)
	}
}

func TestCopyFile_SourceNotFound(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	err := copyFile(filepath.Join(tmpDir, "nonexistent"), filepath.Join(tmpDir, "dest"))
	if err == nil {
		t.Error("expected error for nonexistent source")
	}
}

func TestCopyFile_EmptyFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "empty.txt")
	dstPath := filepath.Join(tmpDir, "dest.txt")

	os.WriteFile(srcPath, []byte(""), 0644)

	err := copyFile(srcPath, dstPath)
	if err != nil {
		t.Fatalf("copyFile: %v", err)
	}

	got, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty dest, got %d bytes", len(got))
	}
}

func TestValidateRawJSONLHeader_ValidHeader(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, "raw.jsonl")
	header := `{"metadata":{"agent_type":"claude-code"}}`
	os.WriteFile(rawPath, []byte(header+"\n"), 0644)

	err := validateRawJSONLHeader(rawPath)
	if err != nil {
		t.Errorf("expected no error for valid header, got %v", err)
	}
}

func TestValidateRawJSONLHeader_EmptyFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, "raw.jsonl")
	os.WriteFile(rawPath, []byte(""), 0644)

	err := validateRawJSONLHeader(rawPath)
	if err == nil {
		t.Error("expected error for empty file")
	}
}

func TestValidateRawJSONLHeader_InvalidJSON(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, "raw.jsonl")
	os.WriteFile(rawPath, []byte("not json\n"), 0644)

	err := validateRawJSONLHeader(rawPath)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestValidateRawJSONLHeader_MissingMetadataKey(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, "raw.jsonl")
	os.WriteFile(rawPath, []byte(`{"type":"header"}`+"\n"), 0644)

	err := validateRawJSONLHeader(rawPath)
	if err == nil {
		t.Error("expected error when metadata key is missing")
	}
}

func TestValidateRawJSONLHeader_NonexistentFile(t *testing.T) {
	t.Parallel()

	err := validateRawJSONLHeader("/nonexistent/path/raw.jsonl")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestReadCacheSessionMeta_MetadataNotMap(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, "raw.jsonl")
	// metadata is a string instead of a map
	os.WriteFile(rawPath, []byte(`{"metadata":"not-a-map"}`+"\n"), 0644)

	_, _, err := readCacheSessionMeta(rawPath)
	if err == nil {
		t.Error("expected error when metadata is not a map")
	}
}

func TestReadCacheSessionMeta_WithFooterEntryCount(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, "raw.jsonl")
	header := `{"metadata":{"agent_type":"claude-code","username":"testuser","agent_id":"agent1"}}`
	entry := `{"type":"user","content":"hello"}`
	footer := `{"entry_count":42}`
	os.WriteFile(rawPath, []byte(header+"\n"+entry+"\n"+footer+"\n"), 0644)

	meta, count, err := readCacheSessionMeta(rawPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta == nil {
		t.Fatal("expected non-nil meta")
	}
	if count != 42 {
		t.Errorf("expected entry_count=42, got %d", count)
	}
}

func TestReadCacheSessionMeta_FooterWithoutEntryCount(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, "raw.jsonl")
	header := `{"metadata":{"agent_type":"claude-code"}}`
	footer := `{"some_other_field":"value"}`
	os.WriteFile(rawPath, []byte(header+"\n"+footer+"\n"), 0644)

	_, count, err := readCacheSessionMeta(rawPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected entry_count=0 when footer lacks it, got %d", count)
	}
}

func TestReadCacheSessionMeta_HeaderOnly(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, "raw.jsonl")
	header := `{"metadata":{"agent_type":"claude-code"}}`
	os.WriteFile(rawPath, []byte(header+"\n"), 0644)

	_, count, err := readCacheSessionMeta(rawPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// no footer means entry_count should be 0
	if count != 0 {
		t.Errorf("expected entry_count=0 for header-only file, got %d", count)
	}
}
