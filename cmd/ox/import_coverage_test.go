package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectContentType_EmptyContent(t *testing.T) {
	t.Parallel()

	// unknown extension with empty content should still return something
	got := detectContentType("file.unknown", []byte{})
	if got == "" {
		t.Error("detectContentType with empty content returned empty string")
	}
}

func TestDetectContentType_CaseInsensitiveExtension(t *testing.T) {
	t.Parallel()

	tests := []struct {
		filename string
		want     string
	}{
		{"REPORT.PDF", "application/pdf"},
		{"Notes.MD", "text/markdown"},
		{"data.JSON", "application/json"},
		{"config.YML", "application/x-yaml"},
		{"photo.PNG", "image/png"},
		{"photo.JPG", "image/jpeg"},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			t.Parallel()
			got := detectContentType(tt.filename, []byte("dummy"))
			if got != tt.want {
				t.Errorf("detectContentType(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}

func TestDetectContentType_BinarySniffing(t *testing.T) {
	t.Parallel()

	// binary content with unknown extension should be detected via sniffing
	binaryContent := make([]byte, 100)
	binaryContent[0] = 0x00
	binaryContent[1] = 0x00
	got := detectContentType("file.bin", binaryContent)
	if got == "" {
		t.Error("detectContentType with binary content returned empty string")
	}
}

func TestInferTitle_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want string
	}{
		{"empty path", "", ""},
		{"just extension", ".pdf", ""},
		{"hidden file", ".gitignore", ""},
		{"no extension", "README", "README"},
		{"deep path with dashes and underscores", "/a/b/c/my-doc_v2.pdf", "my doc v2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := inferTitle(tt.path)
			if got != tt.want {
				t.Errorf("inferTitle(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestSlugify_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty string", "", ""},
		{"only special chars", "!@#$%^&*()", ""},
		{"only hyphens", "---", ""},
		{"unicode letters", "cafe\u0301", "cafe"},
		{"numbers only", "12345", "12345"},
		{"mixed unicode and ascii", "hello world!", "hello-world"},
		{"consecutive separators", "a   b---c___d", "a-b-c-d"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := slugify(tt.input)
			if got != tt.want {
				t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFindExistingDocByOID_NonexistentBaseDir(t *testing.T) {
	t.Parallel()

	_, found := findExistingDocByOID("/nonexistent/path/docs", "sha256:abc")
	if found {
		t.Error("expected found=false for nonexistent directory")
	}
}

func TestDetectContentType_AllExtensions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		filename string
		want     string
	}{
		{"doc.pdf", "application/pdf"},
		{"doc.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{"doc.md", "text/markdown"},
		{"doc.markdown", "text/markdown"},
		{"doc.txt", "text/plain"},
		{"doc.html", "text/html"},
		{"doc.htm", "text/html"},
		{"doc.json", "application/json"},
		{"doc.yaml", "application/x-yaml"},
		{"doc.yml", "application/x-yaml"},
		{"doc.csv", "text/csv"},
		{"doc.png", "image/png"},
		{"doc.jpg", "image/jpeg"},
		{"doc.jpeg", "image/jpeg"},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			t.Parallel()
			got := detectContentType(tt.filename, []byte("dummy content"))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDetectContentType_LargeContent(t *testing.T) {
	t.Parallel()

	// content > 512 bytes should be sniffed using only first 512 bytes
	content := make([]byte, 1024)
	for i := range content {
		content[i] = 'A'
	}
	got := detectContentType("file.unknown", content)
	assert.NotEmpty(t, got, "should detect content type via sniffing")
}

func TestSlugify_MoreCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", "Hello World", "hello-world"},
		{"uppercase", "HELLO", "hello"},
		{"leading trailing spaces", "  hello  ", "hello"},
		{"leading trailing hyphens after transform", "---hello---", "hello"},
		{"mixed case with numbers", "My Doc 2024", "my-doc-2024"},
		{"accented chars", "Caf\u00e9 Mocha", "caf\u00e9-mocha"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := slugify(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestInferTitle_MoreCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want string
	}{
		{"simple file", "document.pdf", "document"},
		{"with dashes", "my-document.pdf", "my document"},
		{"with underscores", "my_document.txt", "my document"},
		{"multiple extensions", "file.test.go", "file.test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := inferTitle(tt.path)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFindExistingDocByOID_FindsMatch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	docDir := filepath.Join(dir, "my-doc")
	assert.NoError(t, os.MkdirAll(docDir, 0o755))

	meta := map[string]string{"source_oid": "sha256:abc123"}
	data, _ := json.Marshal(meta)
	assert.NoError(t, os.WriteFile(filepath.Join(docDir, "metadata.json"), data, 0o644))

	docID, found := findExistingDocByOID(dir, "sha256:abc123")
	assert.True(t, found)
	assert.Equal(t, "my-doc", docID)
}

func TestFindExistingDocByOID_NoMatch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	docDir := filepath.Join(dir, "my-doc")
	assert.NoError(t, os.MkdirAll(docDir, 0o755))

	meta := map[string]string{"source_oid": "sha256:different"}
	data, _ := json.Marshal(meta)
	assert.NoError(t, os.WriteFile(filepath.Join(docDir, "metadata.json"), data, 0o644))

	_, found := findExistingDocByOID(dir, "sha256:abc123")
	assert.False(t, found)
}

func TestFindExistingDocByOID_EmptyDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	_, found := findExistingDocByOID(dir, "sha256:abc123")
	assert.False(t, found)
}

func TestFindExistingDocByOID_InvalidMetadataJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	docDir := filepath.Join(dir, "bad-doc")
	assert.NoError(t, os.MkdirAll(docDir, 0o755))
	assert.NoError(t, os.WriteFile(filepath.Join(docDir, "metadata.json"), []byte("not json"), 0o644))

	_, found := findExistingDocByOID(dir, "sha256:abc123")
	assert.False(t, found)
}

func TestEnsureMetadataGitattributes_CreatesNew(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	err := ensureMetadataGitattributes(dir)
	assert.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(dir, ".gitattributes"))
	assert.NoError(t, err)
	assert.Contains(t, string(content), "data/**/metadata.json !filter !diff !merge text")
}

func TestEnsureMetadataGitattributes_AlreadyPresent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	existing := "data/**/metadata.json !filter !diff !merge text\n"
	assert.NoError(t, os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte(existing), 0o644))

	err := ensureMetadataGitattributes(dir)
	assert.NoError(t, err)

	// content should be unchanged (idempotent)
	content, err := os.ReadFile(filepath.Join(dir, ".gitattributes"))
	assert.NoError(t, err)
	assert.Equal(t, existing, string(content))
}

func TestEnsureMetadataGitattributes_ExistingContentPreserved(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	existing := "*.bin filter=lfs diff=lfs merge=lfs -text\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ensureMetadataGitattributes(dir); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(filepath.Join(dir, ".gitattributes"))
	if err != nil {
		t.Fatal(err)
	}

	// original content should be preserved
	s := string(content)
	if !strings.Contains(s, "*.bin filter=lfs") {
		t.Error("original .gitattributes content was lost")
	}
	if !strings.Contains(s, "data/**/metadata.json !filter !diff !merge text") {
		t.Error("metadata rule was not appended")
	}
}

// TestEmitImportJSON_Shape verifies the --json import payload includes the
// recording ID and omits empty optional fields. Failure prevented: agents
// parsing import output can't find the recording_id to pass to `--status --watch`.
func TestEmitImportJSON_Shape(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	err := emitImportJSON(cmd, importResult{
		Status:      "imported",
		Title:       "Standup",
		Path:        "data/docs/2026/06/20/standup",
		TeamID:      "team_abc",
		SourceOID:   "sha256:deadbeef",
		ContentType: "video/mp4",
		RecordingID: "rec_xyz789",
	})
	assert.NoError(t, err)

	var got importResult
	require.NoError(t, json.Unmarshal([]byte(buf.String()), &got))
	assert.Equal(t, "imported", got.Status)
	assert.Equal(t, "rec_xyz789", got.RecordingID)
	assert.Empty(t, got.ID, "id should be omitted on a fresh import")
}

// TestEmitImportJSON_AlreadyImported verifies the dedup path emits a structured
// already_imported result instead of plain text when --json is set.
func TestEmitImportJSON_AlreadyImported(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	err := emitImportJSON(cmd, importResult{Status: "already_imported", ID: "standup"})
	assert.NoError(t, err)

	var got importResult
	require.NoError(t, json.Unmarshal([]byte(buf.String()), &got))
	assert.Equal(t, "already_imported", got.Status)
	assert.Equal(t, "standup", got.ID)
	assert.Empty(t, got.RecordingID)
}
