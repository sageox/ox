package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: a credential in meta.json or summary.json, which are
// indented and so never decode line by line, committed past the residual
// scan; and an artifact that cannot be decoded published unscanned.
func TestContainsSecretReadsWholeDocuments(t *testing.T) {
	const key = "AKIAIOSFODNN7EXAMPLE" // AWS's published example key
	r := session.NewRedactor()
	tests := []struct {
		name, file, data string
		want             bool
		wantErr          string
	}{
		{"an indented JSON document", "summary.json", "{\n  \"title\": \"Deploy\",\n  \"score_reason\": \"used " + key + "\"\n}\n", true, ""},
		{"a clean indented JSON document", "meta.json", "{\n  \"title\": \"Deploy\"\n}\n", false, ""},
		{"a JSONL record after a blank line", "raw.jsonl", "{\"type\":\"header\"}\n\n{\"content\":\"" + key + "\"}\n", true, ""},
		{"markdown", "summary.md", "# Summary\n\nused " + key + "\n", true, ""},
		{"a JSON document that does not decode", "summary.json", "{\n  \"title\": \"Deploy\",\n", false, "cannot scan summary.json"},
		{"a JSONL record that does not decode", "raw.jsonl", "{\"type\":\"header\"}\n{\"content\":\n", false, "cannot scan raw.jsonl line 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found, err := containsSecret(r, tt.file, []byte(tt.data))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, found)
		})
	}
}

// Failure prevented: a journal, lock or other machine-local file, or a
// transcript that is not the pointer for what was just uploaded, reaching the
// shared Ledger with an imported session.
func TestCheckImportStagingRefusesAnythingButArtifacts(t *testing.T) {
	const oid = "sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393"
	ref := map[string]lfs.FileRef{"raw.jsonl": {OID: oid, Size: 12345}}
	staged := func(t *testing.T, raw string, extra ...string) string {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte("{}"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(raw), 0o644))
		for _, name := range extra {
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644))
		}
		return dir
	}
	pointer := lfs.FormatPointer(oid, 12345)

	require.NoError(t, checkImportStaging(staged(t, pointer), ref), "the known artifacts pass")
	assert.ErrorContains(t, checkImportStaging(staged(t, pointer, "raw.jsonl.append.json"), ref), "not a session artifact")
	assert.ErrorContains(t, checkImportStaging(staged(t, "{\"type\":\"user\"}\n"), ref), "is not an LFS pointer")
	assert.ErrorContains(t, checkImportStaging(staged(t, lfs.FormatPointer(oid, 99)), ref), "does not point at the uploaded object")
}
