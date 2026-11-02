package main

import (
	"bytes"
	"encoding/json"
	"errors"
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
// cached secret stayed untouched. On upload failure, or when the cache no
// longer matches the pointer, nothing may change.
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

	stale := original + `{"type":"user","content":"newer ` + secret + `"}` + "\n"

	tests := []struct {
		name      string
		serverURL string
		cache     string
		wantOK    bool
	}{
		{"upload succeeds", stub.url, original, true},
		{"upload fails", down.URL, original, false},
		{"cache does not match pointer", stub.url, stale, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			work := makeLedgerWithCommitAndOrigin(t, map[string]string{
				rel:                        pointer,
				"sessions/leaky/meta.json": string(metaJSON),
			})
			cachePath := filepath.Join(work, ".sageox", "cache", rel)
			require.NoError(t, os.MkdirAll(filepath.Dir(cachePath), 0o755))
			require.NoError(t, os.WriteFile(cachePath, []byte(tt.cache), 0o644))
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
				assert.Equal(t, tt.cache, string(cached), "cached content must stay recoverable")
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

// TestRedactLFSContent_FailuresLeaveSessionUnchanged covers each early refusal
// and the rollback after upload: a failure must leave the pointer, meta.json
// and cached content exactly as they were.
func TestRedactLFSContent_FailuresLeaveSessionUnchanged(t *testing.T) {
	const rel = "sessions/leaky/raw.jsonl"
	original := `{"type":"user","content":"key AKIAIOSFODNN7EXAMPLE"}` + "\n"
	originalRef := lfs.NewFileRef([]byte(original))
	pointer := lfs.FormatPointer(originalRef.OID, originalRef.Size)
	var meta lfs.SessionMeta
	require.NoError(t, json.Unmarshal([]byte(minimalMetaJSON("leaky", "OxLEAK")), &meta))
	meta.Files["raw.jsonl"] = originalRef
	metaJSON, err := json.Marshal(meta)
	require.NoError(t, err)
	stub := newLFSStub(t)

	tests := []struct {
		name      string
		rel       string
		noPointer bool
		noCache   bool
		noMeta    bool
		noFiles   bool
		blockTmp  bool
		client    func(ledger string) (*lfs.Client, error)
		wantErr   string
		wantIs    error
	}{
		{name: "not a session path", rel: "notes/raw.jsonl", wantErr: "not a session artifact"},
		{name: "not JSONL", rel: "sessions/leaky/summary.md", wantErr: "not JSONL"},
		{name: "pointer missing", rel: rel, noPointer: true, wantIs: os.ErrNotExist},
		{name: "cache missing", rel: rel, noCache: true, wantIs: os.ErrNotExist},
		{name: "redacted copy cannot be written", rel: rel, blockTmp: true, wantErr: "redact.tmp"},
		{name: "client unavailable", rel: rel, client: func(string) (*lfs.Client, error) {
			return nil, errors.New("no credentials")
		}, wantErr: "no credentials"},
		{name: "meta.json missing", rel: rel, noMeta: true, wantErr: "meta.json missing"},
		{name: "cache replace fails after upload", rel: rel, client: func(ledger string) (*lfs.Client, error) {
			// Turn the cache path into a non-empty directory so the final
			// rename fails after meta.json and the pointer were switched.
			cachePath := filepath.Join(ledger, ".sageox", "cache", rel)
			if err := os.Remove(cachePath); err != nil {
				return nil, err
			}
			if err := os.MkdirAll(filepath.Join(cachePath, "block"), 0o755); err != nil {
				return nil, err
			}
			return lfs.NewClient(stub.url+"/ledger.git", "tester", "token"), nil
		}, wantErr: "raw.jsonl"},
	}
	blocked := tests[len(tests)-1]
	blocked.name, blocked.noFiles = "cache replace fails, meta.json had no files", true
	tests = append(tests, blocked)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ledger := t.TempDir()
			sessionDir := filepath.Join(ledger, "sessions", "leaky")
			require.NoError(t, os.MkdirAll(sessionDir, 0o755))
			pointerPath := filepath.Join(sessionDir, "raw.jsonl")
			if !tt.noPointer {
				require.NoError(t, os.WriteFile(pointerPath, []byte(pointer), 0o644))
			}
			metaPath := filepath.Join(sessionDir, "meta.json")
			if !tt.noMeta {
				seeded := metaJSON
				if tt.noFiles {
					seeded = []byte(strings.Replace(minimalMetaJSON("leaky", "OxLEAK"), ",\n  \"files\": {}", "", 1))
				}
				require.NoError(t, os.WriteFile(metaPath, seeded, 0o644))
			}
			cachePath := filepath.Join(ledger, ".sageox", "cache", rel)
			if !tt.noCache {
				require.NoError(t, os.MkdirAll(filepath.Dir(cachePath), 0o755))
				require.NoError(t, os.WriteFile(cachePath, []byte(original), 0o644))
			}
			if tt.blockTmp {
				require.NoError(t, os.MkdirAll(filepath.Join(cachePath+".redact.tmp", "block"), 0o755))
			}
			client := tt.client
			if client == nil {
				client = func(string) (*lfs.Client, error) {
					return lfs.NewClient(stub.url+"/ledger.git", "tester", "token"), nil
				}
			}

			err := redactLFSContent(t.Context(), ledger, tt.rel, func() (*lfs.Client, error) { return client(ledger) })
			if tt.wantIs != nil {
				require.ErrorIs(t, err, tt.wantIs)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}

			if tt.noPointer {
				return
			}
			got, err := os.ReadFile(pointerPath)
			require.NoError(t, err)
			assert.Equal(t, pointer, string(got), "pointer must be restored")
			if !tt.noMeta {
				var gotMeta lfs.SessionMeta
				data, err := os.ReadFile(metaPath)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(data, &gotMeta))
				gotRef, ok := gotMeta.Files["raw.jsonl"]
				if tt.noFiles {
					assert.False(t, ok, "an absent meta.json entry must stay absent")
				} else {
					assert.Equal(t, originalRef, gotRef, "meta.json must be restored")
				}
			}
		})
	}
}

func TestRedactFileInPlace_MissingFile(t *testing.T) {
	require.ErrorIs(t, redactFileInPlace(filepath.Join(t.TempDir(), "raw.jsonl"), nil), os.ErrNotExist)
}
