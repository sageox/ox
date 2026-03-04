package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectContentType(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{"report.pdf", "application/pdf"},
		{"notes.md", "text/markdown"},
		{"notes.markdown", "text/markdown"},
		{"data.json", "application/json"},
		{"config.yaml", "application/x-yaml"},
		{"config.yml", "application/x-yaml"},
		{"readme.txt", "text/plain"},
		{"page.html", "text/html"},
		{"page.htm", "text/html"},
		{"data.csv", "text/csv"},
		{"photo.png", "image/png"},
		{"photo.jpg", "image/jpeg"},
		{"photo.jpeg", "image/jpeg"},
		{"doc.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := detectContentType(tt.filename, []byte("dummy content"))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDetectContentTypeSniffFallback(t *testing.T) {
	// unknown extension falls back to http.DetectContentType
	got := detectContentType("unknown.xyz", []byte("<html><body>hello</body></html>"))
	assert.Equal(t, "text/html; charset=utf-8", got)
}

func TestFindExistingDocByOID(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		dir := t.TempDir()
		docDir := filepath.Join(dir, "2026", "01", "15", "q1-report")
		require.NoError(t, os.MkdirAll(docDir, 0o755))

		meta := map[string]any{
			"source_oid": "sha256:deadbeef",
		}
		data, _ := json.Marshal(meta)
		require.NoError(t, os.WriteFile(filepath.Join(docDir, "metadata.json"), data, 0o644))

		docID, found := findExistingDocByOID(dir, "sha256:deadbeef")
		assert.True(t, found)
		assert.Equal(t, "q1-report", docID)
	})

	t.Run("not found", func(t *testing.T) {
		dir := t.TempDir()
		docDir := filepath.Join(dir, "2026", "01", "15", "q1-report")
		require.NoError(t, os.MkdirAll(docDir, 0o755))

		meta := map[string]any{
			"source_oid": "sha256:different",
		}
		data, _ := json.Marshal(meta)
		require.NoError(t, os.WriteFile(filepath.Join(docDir, "metadata.json"), data, 0o644))

		_, found := findExistingDocByOID(dir, "sha256:deadbeef")
		assert.False(t, found)
	})

	t.Run("empty dir", func(t *testing.T) {
		dir := t.TempDir()
		_, found := findExistingDocByOID(dir, "sha256:deadbeef")
		assert.False(t, found)
	})

	t.Run("malformed json", func(t *testing.T) {
		dir := t.TempDir()
		docDir := filepath.Join(dir, "2026", "01", "15", "abc-123")
		require.NoError(t, os.MkdirAll(docDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(docDir, "metadata.json"), []byte("not json"), 0o644))

		_, found := findExistingDocByOID(dir, "sha256:deadbeef")
		assert.False(t, found)
	})
}

func TestEnsureMetadataGitattributes(t *testing.T) {
	t.Run("creates new file", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, ensureMetadataGitattributes(dir))

		content, err := os.ReadFile(filepath.Join(dir, ".gitattributes"))
		require.NoError(t, err)
		assert.Contains(t, string(content), "data/**/metadata.json !filter !diff !merge text")
	})

	t.Run("idempotent", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, ensureMetadataGitattributes(dir))
		require.NoError(t, ensureMetadataGitattributes(dir))

		content, err := os.ReadFile(filepath.Join(dir, ".gitattributes"))
		require.NoError(t, err)

		// should appear exactly once
		count := strings.Count(string(content), "data/**/metadata.json !filter !diff !merge text")
		assert.Equal(t, 1, count)
	})

	t.Run("appends to existing", func(t *testing.T) {
		dir := t.TempDir()
		existing := "data/** filter=lfs diff=lfs merge=lfs -text\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte(existing), 0o644))

		require.NoError(t, ensureMetadataGitattributes(dir))

		content, err := os.ReadFile(filepath.Join(dir, ".gitattributes"))
		require.NoError(t, err)
		assert.Contains(t, string(content), "data/** filter=lfs")
		assert.Contains(t, string(content), "data/**/metadata.json !filter !diff !merge text")
	})
}

func TestInferTitle(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"architecture.pdf", "architecture"},
		{"my-architecture_plan.pdf", "my architecture plan"},
		{"Q1-2026_report.docx", "Q1 2026 report"},
		{"notes.md", "notes"},
		{"no-extension", "no extension"},
		{"/some/path/deep-doc.txt", "deep doc"},
		{"multiple...dots.pdf", "multiple...dots"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			assert.Equal(t, tt.want, inferTitle(tt.path))
		})
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Q1 2026 Report", "q1-2026-report"},
		{"my_architecture_plan", "my-architecture-plan"},
		{"Hello World", "hello-world"},
		{"  multiple   spaces  ", "multiple-spaces"},
		{"special!@#chars", "specialchars"},
		{"already-slugified", "already-slugified"},
		{"MiXeD CaSe", "mixed-case"},
		{"trailing-", "trailing"},
		{"-leading", "leading"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, slugify(tt.input))
		})
	}
}

func TestDocMetaSerialization(t *testing.T) {
	srcContent := []byte("hello world")
	textContent := []byte("# Extracted\nSome text")

	srcRef := lfs.NewFileRef(srcContent)
	textRef := lfs.NewFileRef(textContent)

	now := time.Now().UTC().Format(time.RFC3339)
	meta := docMeta{
		Version:        "1",
		Title:          "Test Doc",
		SourceFilename: "test.pdf",
		ContentType:    "application/pdf",
		SourceSize:     srcRef.Size,
		SourceOID:      srcRef.OID,
		CreatedAt:      time.Date(2026, 2, 14, 10, 30, 0, 0, time.UTC).Format(time.RFC3339),
		ImportedAt:     now,
		Path: "data/docs/2026/02/14/test-doc",
		Sidecars: map[string]sidecar{
			"text-extract": {
				Filename:  "extracted.md",
				OID:       textRef.OID,
				Size:      textRef.Size,
				CreatedAt: now,
			},
		},
	}

	data, err := json.MarshalIndent(meta, "", "  ")
	require.NoError(t, err)

	// round-trip: unmarshal into a generic map to verify structure
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(data, &parsed))

	assert.Equal(t, "1", parsed["version"])
	assert.Equal(t, "Test Doc", parsed["title"])
	assert.Equal(t, "test.pdf", parsed["source_filename"])
	assert.Equal(t, "application/pdf", parsed["content_type"])
	assert.Equal(t, float64(srcRef.Size), parsed["source_size"])
	assert.True(t, strings.HasPrefix(parsed["source_oid"].(string), "sha256:"))
	assert.Equal(t, "2026-02-14T10:30:00Z", parsed["created_at"])
	assert.Equal(t, "data/docs/2026/02/14/test-doc", parsed["path"])

	// verify sidecars keyed by type with filename inside
	sidecars := parsed["sidecars"].(map[string]any)
	assert.Len(t, sidecars, 1)
	textExtract := sidecars["text-extract"].(map[string]any)
	assert.Equal(t, "extracted.md", textExtract["filename"])
	assert.True(t, strings.HasPrefix(textExtract["oid"].(string), "sha256:"))
	assert.Equal(t, float64(len(textContent)), textExtract["size"])
	assert.NotEmpty(t, textExtract["created_at"])
}
