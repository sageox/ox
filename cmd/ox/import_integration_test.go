package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- commitAndPushDocImport integration tests ---

func TestCommitAndPushDocImport_Success(t *testing.T) {
	barePath, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	docID := "test-doc-001"
	docDir := filepath.Join(clonePath, "data", "docs", "2026", "01", "15", docID)
	require.NoError(t, os.MkdirAll(docDir, 0o755))

	srcContent := []byte("fake pdf content")
	srcRef := lfs.NewFileRef(srcContent)
	srcPointerPath := filepath.Join(docDir, "source.bin")
	require.NoError(t, os.WriteFile(srcPointerPath, []byte(lfs.FormatPointer(srcRef.OID, srcRef.Size)), 0o644))

	meta := docMeta{
		Version:        "1",
		Title:          "Test Document",
		SourceFilename: "test.pdf",
		ContentType:    "application/pdf",
		SourceSize:     srcRef.Size,
		CreatedAt:      "2026-01-15",
		ImportedAt:     time.Now().UTC().Format(time.RFC3339),
		HasTextExtract: false,
		Files:          map[string]lfs.FileRef{"source.bin": srcRef},
	}
	metaData, err := json.MarshalIndent(meta, "", "  ")
	require.NoError(t, err)
	metaPath := filepath.Join(docDir, "metadata.json")
	require.NoError(t, os.WriteFile(metaPath, metaData, 0o644))

	textPointerPath := filepath.Join(docDir, "extracted.md")
	err = commitAndPushDocImport(clonePath, "https://test.invalid", docID, metaPath, srcPointerPath, textPointerPath, false)
	require.NoError(t, err)

	// verify on remote
	verifyClone := cloneBare(t, barePath)
	verifyMeta := filepath.Join(verifyClone, "data", "docs", "2026", "01", "15", docID, "metadata.json")
	_, statErr := os.Stat(verifyMeta)
	assert.NoError(t, statErr, "metadata.json should exist on remote")

	verifySrc := filepath.Join(verifyClone, "data", "docs", "2026", "01", "15", docID, "source.bin")
	_, statErr = os.Stat(verifySrc)
	assert.NoError(t, statErr, "source.bin pointer should exist on remote")

	// verify commit message format
	msg := runGit(t, verifyClone, "log", "-1", "--format=%s")
	assert.Equal(t, "import: doc "+docID, msg)
}

func TestCommitAndPushDocImport_WithTextExtract(t *testing.T) {
	barePath, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	docID := "test-doc-002"
	docDir := filepath.Join(clonePath, "data", "docs", "2026", "02", "14", docID)
	require.NoError(t, os.MkdirAll(docDir, 0o755))

	srcContent := []byte("source document bytes")
	srcRef := lfs.NewFileRef(srcContent)
	srcPointerPath := filepath.Join(docDir, "source.bin")
	require.NoError(t, os.WriteFile(srcPointerPath, []byte(lfs.FormatPointer(srcRef.OID, srcRef.Size)), 0o644))

	textContent := []byte("# Extracted Text\nSome content here")
	textRef := lfs.NewFileRef(textContent)
	textPointerPath := filepath.Join(docDir, "extracted.md")
	require.NoError(t, os.WriteFile(textPointerPath, []byte(lfs.FormatPointer(textRef.OID, textRef.Size)), 0o644))

	meta := docMeta{
		Version:        "1",
		Title:          "Doc With Text",
		SourceFilename: "report.pdf",
		ContentType:    "application/pdf",
		SourceSize:     srcRef.Size,
		CreatedAt:      "2026-02-14",
		ImportedAt:     time.Now().UTC().Format(time.RFC3339),
		HasTextExtract: true,
		Files: map[string]lfs.FileRef{
			"source.bin":   srcRef,
			"extracted.md": textRef,
		},
	}
	metaData, err := json.MarshalIndent(meta, "", "  ")
	require.NoError(t, err)
	metaPath := filepath.Join(docDir, "metadata.json")
	require.NoError(t, os.WriteFile(metaPath, metaData, 0o644))

	err = commitAndPushDocImport(clonePath, "https://test.invalid", docID, metaPath, srcPointerPath, textPointerPath, true)
	require.NoError(t, err)

	// verify both files on remote
	verifyClone := cloneBare(t, barePath)
	verifySrc := filepath.Join(verifyClone, "data", "docs", "2026", "02", "14", docID, "source.bin")
	_, statErr := os.Stat(verifySrc)
	assert.NoError(t, statErr, "source.bin should exist on remote")

	verifyText := filepath.Join(verifyClone, "data", "docs", "2026", "02", "14", docID, "extracted.md")
	_, statErr = os.Stat(verifyText)
	assert.NoError(t, statErr, "extracted.md should exist on remote")
}

func TestCommitAndPushDocImport_GitattributesIncluded(t *testing.T) {
	barePath, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	// write .gitattributes before import
	gitattrsContent := "data/**/metadata.json !filter !diff !merge text\n"
	require.NoError(t, os.WriteFile(filepath.Join(clonePath, ".gitattributes"), []byte(gitattrsContent), 0o644))

	docID := "test-doc-003"
	docDir := filepath.Join(clonePath, "data", "docs", "2026", "03", "01", docID)
	require.NoError(t, os.MkdirAll(docDir, 0o755))

	srcContent := []byte("gitattributes test content")
	srcRef := lfs.NewFileRef(srcContent)
	srcPointerPath := filepath.Join(docDir, "source.bin")
	require.NoError(t, os.WriteFile(srcPointerPath, []byte(lfs.FormatPointer(srcRef.OID, srcRef.Size)), 0o644))

	meta := docMeta{
		Version:        "1",
		Title:          "Gitattributes Test",
		SourceFilename: "test.txt",
		ContentType:    "text/plain",
		SourceSize:     srcRef.Size,
		CreatedAt:      "2026-03-01",
		ImportedAt:     time.Now().UTC().Format(time.RFC3339),
		HasTextExtract: false,
		Files:          map[string]lfs.FileRef{"source.bin": srcRef},
	}
	metaData, err := json.MarshalIndent(meta, "", "  ")
	require.NoError(t, err)
	metaPath := filepath.Join(docDir, "metadata.json")
	require.NoError(t, os.WriteFile(metaPath, metaData, 0o644))

	textPointerPath := filepath.Join(docDir, "extracted.md")
	err = commitAndPushDocImport(clonePath, "https://test.invalid", docID, metaPath, srcPointerPath, textPointerPath, false)
	require.NoError(t, err)

	// verify .gitattributes on remote
	verifyClone := cloneBare(t, barePath)
	verifyAttrs := filepath.Join(verifyClone, ".gitattributes")
	data, readErr := os.ReadFile(verifyAttrs)
	require.NoError(t, readErr, ".gitattributes should exist on remote")
	assert.Contains(t, string(data), "data/**/metadata.json !filter !diff !merge text")
}

func TestCommitAndPushDocImport_NothingToCommit(t *testing.T) {
	_, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	docID := "test-doc-004"
	docDir := filepath.Join(clonePath, "data", "docs", "2026", "01", "01", docID)
	require.NoError(t, os.MkdirAll(docDir, 0o755))

	srcContent := []byte("already committed content")
	srcRef := lfs.NewFileRef(srcContent)
	srcPointerPath := filepath.Join(docDir, "source.bin")
	require.NoError(t, os.WriteFile(srcPointerPath, []byte(lfs.FormatPointer(srcRef.OID, srcRef.Size)), 0o644))

	meta := docMeta{
		Version:        "1",
		Title:          "Already Committed",
		SourceFilename: "test.txt",
		ContentType:    "text/plain",
		SourceSize:     srcRef.Size,
		CreatedAt:      "2026-01-01",
		ImportedAt:     time.Now().UTC().Format(time.RFC3339),
		HasTextExtract: false,
		Files:          map[string]lfs.FileRef{"source.bin": srcRef},
	}
	metaData, err := json.MarshalIndent(meta, "", "  ")
	require.NoError(t, err)
	metaPath := filepath.Join(docDir, "metadata.json")
	require.NoError(t, os.WriteFile(metaPath, metaData, 0o644))

	textPointerPath := filepath.Join(docDir, "extracted.md")

	// first commit succeeds
	err = commitAndPushDocImport(clonePath, "https://test.invalid", docID, metaPath, srcPointerPath, textPointerPath, false)
	require.NoError(t, err)

	// second commit with same files: "nothing to commit" should not error
	err = commitAndPushDocImport(clonePath, "https://test.invalid", docID, metaPath, srcPointerPath, textPointerPath, false)
	assert.NoError(t, err, "re-committing same files should succeed (nothing to commit)")
}

// --- pushTeamContext integration tests ---

func TestPushTeamContext_ConflictRetry(t *testing.T) {
	barePath, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	// local: commit a file but don't push
	docFile := filepath.Join(clonePath, "local-doc.txt")
	require.NoError(t, os.WriteFile(docFile, []byte("local change"), 0o644))
	runGit(t, clonePath, "add", "local-doc.txt")
	runGit(t, clonePath, "commit", "--no-verify", "-m", "local doc")

	// remote: push a different commit from another clone to create divergence
	otherClone := cloneBare(t, barePath)
	otherFile := filepath.Join(otherClone, "remote-doc.txt")
	require.NoError(t, os.WriteFile(otherFile, []byte("remote change"), 0o644))
	runGit(t, otherClone, "add", "remote-doc.txt")
	runGit(t, otherClone, "commit", "--no-verify", "-m", "remote doc")
	runGit(t, otherClone, "push")

	// pushTeamContext should handle non-fast-forward with rebase retry
	err := pushTeamContext(context.Background(), clonePath, "https://test.invalid")
	require.NoError(t, err, "pushTeamContext should succeed after rebase retry")

	// verify both commits on remote
	verifyClone := cloneBare(t, barePath)
	count := commitCount(t, verifyClone)
	// initial + remote doc + local doc = 3
	assert.Equal(t, 3, count, "remote should have all 3 commits after rebase")

	_, statErr := os.Stat(filepath.Join(verifyClone, "local-doc.txt"))
	assert.NoError(t, statErr, "local-doc.txt should be on remote after rebase+push")
	_, statErr = os.Stat(filepath.Join(verifyClone, "remote-doc.txt"))
	assert.NoError(t, statErr, "remote-doc.txt should be on remote")
}

func TestPushTeamContext_AuthFailure(t *testing.T) {
	_, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	// commit something to push
	docFile := filepath.Join(clonePath, "auth-test.txt")
	require.NoError(t, os.WriteFile(docFile, []byte("test"), 0o644))
	runGit(t, clonePath, "add", "auth-test.txt")
	runGit(t, clonePath, "commit", "--no-verify", "-m", "auth test")

	// point to nonexistent local path — fails fast with "does not appear to be a
	// git repository" which exhausts retries quickly (no network timeout)
	runGit(t, clonePath, "remote", "set-url", "origin", "/nonexistent/bare/repo.git")

	err := pushTeamContext(context.Background(), clonePath, "https://test.invalid")
	require.Error(t, err, "should fail when remote is unreachable")

	// local commit preserved
	log := runGit(t, clonePath, "log", "--oneline")
	assert.Contains(t, log, "auth test", "local commit should survive failed push")
}

// --- findExistingDocByOID edge case tests ---

func TestFindExistingDocByOID_CorruptedMetadata(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"empty file", ""},
		{"partial json", `{"files": {`},
		{"missing files field", `{"version": "1", "title": "test"}`},
		{"files not a map", `{"files": "not a map"}`},
		{"null files", `{"files": null}`},
		{"source.bin missing oid", `{"files": {"source.bin": {"size": 100}}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			docDir := filepath.Join(dir, "2026", "01", "01", "corrupted-doc")
			require.NoError(t, os.MkdirAll(docDir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(docDir, "metadata.json"), []byte(tt.content), 0o644))

			_, found := findExistingDocByOID(dir, "sha256:anything")
			assert.False(t, found, "corrupted metadata should not match")
		})
	}
}

func TestImportDedup_SameContentDifferentFilename(t *testing.T) {
	dir := t.TempDir()

	content := []byte("identical content across imports")
	ref := lfs.NewFileRef(content)

	// set up an existing doc with this OID
	existingID := "existing-doc-aaa"
	docDir := filepath.Join(dir, "2026", "01", "10", existingID)
	require.NoError(t, os.MkdirAll(docDir, 0o755))

	meta := map[string]any{
		"files": map[string]any{
			"source.bin": map[string]any{
				"oid":  ref.OID,
				"size": ref.Size,
			},
		},
	}
	metaData, err := json.Marshal(meta)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(docDir, "metadata.json"), metaData, 0o644))

	// dedup should find existing doc by OID regardless of original filename
	docID, found := findExistingDocByOID(dir, ref.OID)
	assert.True(t, found, "should find existing doc by OID")
	assert.Equal(t, existingID, docID)
}

// --- LFS FileRef tests ---

func TestImportEmptyFile(t *testing.T) {
	// empty content should produce valid OID and size=0
	ref := lfs.NewFileRef([]byte{})
	assert.NotEmpty(t, ref.OID, "empty content should produce valid OID")
	assert.True(t, len(ref.OID) > 7, "OID should have sha256: prefix + hex")
	assert.Equal(t, int64(0), ref.Size, "empty content should have size 0")

	// metadata serialization works for size=0
	meta := docMeta{
		Version:        "1",
		Title:          "Empty Doc",
		SourceFilename: "empty.txt",
		ContentType:    "text/plain",
		SourceSize:     ref.Size,
		CreatedAt:      "2026-01-01",
		ImportedAt:     time.Now().UTC().Format(time.RFC3339),
		HasTextExtract: false,
		Files:          map[string]lfs.FileRef{"source.bin": ref},
	}
	data, err := json.Marshal(meta)
	require.NoError(t, err)

	var parsed docMeta
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, int64(0), parsed.SourceSize)
	assert.Equal(t, ref.OID, parsed.Files["source.bin"].OID)
}

// --- date validation tests ---

func TestImportDateValidation(t *testing.T) {
	valid := []struct {
		name  string
		input string
	}{
		{"standard date", "2026-01-15"},
		{"end of year", "2026-12-31"},
		{"leap year feb", "2024-02-29"},
		{"first of month", "2026-01-01"},
	}

	for _, tt := range valid {
		t.Run("valid/"+tt.name, func(t *testing.T) {
			_, err := time.Parse("2006-01-02", tt.input)
			assert.NoError(t, err, "should parse valid date: %s", tt.input)
		})
	}

	invalid := []struct {
		name  string
		input string
	}{
		{"reversed format", "15-01-2026"},
		{"month 13", "2026-13-01"},
		{"month 00", "2026-00-15"},
		{"not a date", "not-a-date"},
		{"missing day", "2026-01"},
		{"slash separator", "2026/01/15"},
		{"no separator", "20260115"},
		{"non-leap feb 29", "2026-02-29"},
	}

	for _, tt := range invalid {
		t.Run("invalid/"+tt.name, func(t *testing.T) {
			_, err := time.Parse("2006-01-02", tt.input)
			assert.Error(t, err, "should reject invalid date: %s", tt.input)
		})
	}
}

// --- inferTitle edge cases ---

func TestImportSpecialCharFilenames(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"report (v2) [final].pdf", "report (v2) [final]"},
		{"file with spaces.txt", "file with spaces"},
		{"already clean", "already clean"},
		{"MiXeD-CaSe_File.docx", "MiXeD CaSe File"},
		{".hidden-file.txt", ".hidden file"},
		{"dots.in.name.pdf", "dots.in.name"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := inferTitle(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- pointer file format tests ---

func TestImportPointerFileRoundTrip(t *testing.T) {
	content := []byte("pointer round-trip test content")
	ref := lfs.NewFileRef(content)

	pointer := lfs.FormatPointer(ref.OID, ref.Size)

	// pointer should contain the oid and size
	assert.Contains(t, pointer, ref.OID)
	assert.Contains(t, pointer, "size")

	// parse it back
	parsedOID, parsedSize, err := lfs.ParsePointer(pointer)
	require.NoError(t, err)
	assert.Equal(t, ref.OID, parsedOID)
	assert.Equal(t, ref.Size, parsedSize)
}
