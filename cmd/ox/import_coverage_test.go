package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
