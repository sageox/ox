package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunRedactHistoryWorkflow_RedactsLFSCachedContent covers a session whose
// raw.jsonl is committed as an LFS pointer with the real content in the Ledger
// cache. Redaction must rewrite the cached content, upload it, and amend the
// pointer and meta.json to the new OID. Failure prevented (#1666): the pointer
// text itself was "redacted" into _raw_line records and published, while the
// cached secret stayed untouched. On upload failure, nothing may change.
func TestRunRedactHistoryWorkflow_RedactsLFSCachedContent(t *testing.T) {
	const secret = "AKIAIOSFODNN7EXAMPLE"
	original := `{"type":"user","content":"key ` + secret + `"}` + "\n"
	originalRef := lfs.NewFileRef([]byte(original))
	pointer := lfs.FormatPointer(originalRef.OID, originalRef.Size)
	const rel = "sessions/leaky/raw.jsonl"

	var meta lfs.SessionMeta
	require.NoError(t, json.Unmarshal([]byte(minimalMetaJSON("leaky", "OxLEAK")), &meta))
	meta.Files["raw.jsonl"] = originalRef
	metaJSON, err := json.Marshal(meta)
	require.NoError(t, err)

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	t.Cleanup(down.Close)
	stub := newLFSStub(t)

	tests := []struct {
		name      string
		serverURL string
		wantOK    bool
	}{
		{"upload succeeds", stub.url, true},
		{"upload fails", down.URL, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			work := makeLedgerWithCommitAndOrigin(t, map[string]string{
				rel:                        pointer,
				"sessions/leaky/meta.json": string(metaJSON),
			})
			cachePath := filepath.Join(work, ".sageox", "cache", rel)
			require.NoError(t, os.MkdirAll(filepath.Dir(cachePath), 0o755))
			require.NoError(t, os.WriteFile(cachePath, []byte(original), 0o644))
			out := &bytes.Buffer{}

			err := runRedactHistoryWorkflow(redactHistoryOptions{
				LedgerPath: work,
				BackupDir:  t.TempDir(),
				Scope:      &sessionScope{AllowAll: true},
				Stdin:      strings.NewReader("y\n"),
				Stdout:     out,
				LFSClient: func() (*lfs.Client, error) {
					return lfs.NewClient(tt.serverURL+"/ledger.git", "tester", "token"), nil
				},
			})
			require.NoError(t, err)
			assert.NotContains(t, out.String(), secret)

			cached, err := os.ReadFile(cachePath)
			require.NoError(t, err)
			headPointer := mustGitOutput(t, work, "show", "HEAD:"+rel)
			var headMeta lfs.SessionMeta
			require.NoError(t, json.Unmarshal([]byte(mustGitCapture(t, work, "show", "HEAD:sessions/leaky/meta.json")), &headMeta))

			if !tt.wantOK {
				assert.Contains(t, out.String(), "Skipped")
				assert.Equal(t, original, string(cached), "cached content must stay recoverable")
				assert.Equal(t, pointer, headPointer)
				assert.Equal(t, originalRef.OID, headMeta.Files["raw.jsonl"].OID)
				assert.Empty(t, headMeta.Redactions, "a failed redaction must not be recorded")
				assert.Empty(t, mustGitCapture(t, work, "status", "--porcelain", "--", "sessions"), "nothing may be left modified or staged")
				return
			}

			assert.NotContains(t, string(cached), secret)
			assert.Contains(t, string(cached), "[REDACTED_AWS_KEY]")
			assert.NotContains(t, string(cached), "_raw_line")
			redactedRef := lfs.NewFileRef(cached)
			assert.True(t, stub.uploaded(string(cached)), "redacted content must reach the LFS store")
			assert.Equal(t, lfs.FormatPointer(redactedRef.OID, redactedRef.Size), headPointer)
			assert.Equal(t, redactedRef.OID, headMeta.Files["raw.jsonl"].OID)
			assert.Equal(t, redactedRef.Size, headMeta.Files["raw.jsonl"].Size)
			assert.Len(t, headMeta.Redactions, 1)

			mustGit(t, work, "push", "origin", "main")
			bare := filepath.Join(filepath.Dir(work), "bare.git")
			assert.Equal(t, headPointer, mustGitOutput(t, bare, "show", "main:"+rel), "origin must hold the new pointer")
		})
	}
}
