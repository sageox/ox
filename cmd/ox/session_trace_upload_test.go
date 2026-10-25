package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session/pipeline"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The production CLI adapter must resolve its real ledger/client and confirm
// blob uploads before exposing either trace pointer in the Git worktree.
func TestUploadSessionTraces_ConfirmsPublicationThroughLocalLFS(t *testing.T) {
	for _, mode := range []string{"success", "missing credentials", "upload refused"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newDraftLedgerFixture(t)
			t.Chdir(fixture.projectRoot)
			oldCfg := cfg
			cfg = &config.Config{}
			t.Cleanup(func() { cfg = oldCfg })
			for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
				t.Setenv(key, t.TempDir())
			}
			priorDir := gitserver.TestSetConfigDirOverride(t.TempDir())
			t.Cleanup(func() { gitserver.TestSetConfigDirOverride(priorDir) })
			priorStorage := gitserver.TestSetForceFileStorage(true)
			t.Cleanup(func() { gitserver.TestSetForceFileStorage(priorStorage) })
			cacheDir := filepath.Join(fixture.ledgerPath, ".sageox", "cache", "sessions", "trace-upload")
			sessionDir := filepath.Join(fixture.ledgerPath, "sessions", "trace-upload")
			require.NoError(t, os.MkdirAll(cacheDir, 0o700))
			require.NoError(t, os.MkdirAll(sessionDir, 0o700))
			artifacts := make(map[string][]byte)
			for _, name := range []string{pipeline.LedgerFileTraceSpans, pipeline.LedgerFileTraceEvents} {
				var compressed bytes.Buffer
				writer := gzip.NewWriter(&compressed)
				_, err := writer.Write([]byte(`{"type":"` + name + `"}` + "\n"))
				require.NoError(t, err)
				require.NoError(t, writer.Close())
				artifacts[name] = append([]byte(nil), compressed.Bytes()...)
				require.NoError(t, os.WriteFile(filepath.Join(cacheDir, name), artifacts[name], 0o600))
			}
			var requests atomic.Int64
			var uploaded sync.Map
			var serverURL string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				for name := range artifacts {
					_, err := os.Stat(filepath.Join(sessionDir, name))
					assert.True(t, os.IsNotExist(err), "pointer must wait until both uploads finish")
				}
				switch {
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/objects/batch"):
					var request struct {
						Objects []lfs.BatchObject `json:"objects"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					response := lfs.BatchResponse{Transfer: "basic"}
					for _, obj := range request.Objects {
						response.Objects = append(response.Objects, lfs.BatchResponseObject{
							OID: obj.OID, Size: obj.Size,
							Actions: &lfs.Actions{Upload: &lfs.Action{Href: serverURL + "/upload/" + obj.OID}},
						})
					}
					w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
					_ = json.NewEncoder(w).Encode(response)
				case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/upload/"):
					if mode == "upload refused" {
						http.Error(w, "trace upload refused", http.StatusForbidden)
						return
					}
					data, err := io.ReadAll(r.Body)
					if err != nil || lfs.ComputeOID(data) != filepath.Base(r.URL.Path) {
						http.Error(w, "invalid upload", http.StatusBadRequest)
						return
					}
					uploaded.Store(lfs.ComputeOID(data), data)
					w.WriteHeader(http.StatusOK)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			serverURL = server.URL
			t.Setenv("SAGEOX_ENDPOINT", server.URL)
			runGit(t, fixture.ledgerPath, "remote", "set-url", "origin", server.URL+"/ledger.git")
			if mode != "missing credentials" {
				require.NoError(t, gitserver.SaveCredentialsForEndpoint(server.URL, gitserver.GitCredentials{
					Username: "testuser", Token: "test-token", ServerURL: server.URL, ExpiresAt: time.Now().Add(time.Hour),
				}))
			}

			refs, err := uploadSessionTraces(fixture.projectRoot, cacheDir, sessionDir)
			if mode != "success" {
				require.Error(t, err)
				assert.Empty(t, refs)
			} else {
				require.NoError(t, err)
				require.Len(t, refs, 2)
			}
			if mode == "missing credentials" {
				assert.Zero(t, requests.Load())
			}
			for name, original := range artifacts {
				cached, err := os.ReadFile(filepath.Join(cacheDir, name))
				require.NoError(t, err)
				assert.Equal(t, original, cached, "source cache must survive every outcome")
				if mode != "success" {
					assert.NoFileExists(t, filepath.Join(sessionDir, name))
					continue
				}
				ref, err := lfs.ReadPointerFile(filepath.Join(sessionDir, name))
				require.NoError(t, err)
				assert.Equal(t, lfs.NewFileRef(original), ref)
				assert.Equal(t, ref, refs[name])
				blob, found := uploaded.Load(ref.BareOID())
				require.True(t, found, "published pointer must have a confirmed uploaded blob")
				assert.Equal(t, original, blob)
			}
		})
	}
}
