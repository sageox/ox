package agentwork

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/require"
)

type localFinalizeLFS struct {
	batchUnavailable atomic.Bool
	blobUnavailable  atomic.Bool
}

// enableLocalFinalizeLFS keeps git-behavior fixtures on the real publication
// path. Skipping LFS would test a raw-content fallback the commit guard forbids.
// Only LFS HTTP is mocked; pointer preparation and git commits remain real.
func enableLocalFinalizeLFS(t *testing.T, handler *SessionFinalizeHandler, ledgerPath string, missingOIDs ...string) *localFinalizeLFS {
	t.Helper()
	service := &localFinalizeLFS{}
	previousConfig := gitserver.TestSetConfigDirOverride(t.TempDir())
	t.Cleanup(func() { gitserver.TestSetConfigDirOverride(previousConfig) })
	previousFileStorage := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() { gitserver.TestSetForceFileStorage(previousFileStorage) })
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/objects/batch"):
			if service.batchUnavailable.Load() {
				http.Error(w, "LFS batch unavailable", http.StatusServiceUnavailable)
				return
			}
			var request struct {
				Objects   []lfs.BatchObject `json:"objects"`
				Operation string            `json:"operation"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			response := lfs.BatchResponse{Transfer: "basic"}
			for _, object := range request.Objects {
				if request.Operation == "download" && slices.Contains(missingOIDs, object.OID) {
					response.Objects = append(response.Objects, lfs.BatchResponseObject{OID: object.OID, Size: object.Size, Error: &lfs.ObjectError{Code: http.StatusNotFound, Message: "missing test blob"}})
					continue
				}
				response.Objects = append(response.Objects, lfs.BatchResponseObject{OID: object.OID, Size: object.Size, Actions: &lfs.Actions{Upload: &lfs.Action{Href: serverURL + "/objects/" + object.OID}}})
			}
			w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
			_ = json.NewEncoder(w).Encode(response)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/objects/"):
			if service.blobUnavailable.Load() {
				http.Error(w, "LFS blob upload unavailable", http.StatusServiceUnavailable)
				return
			}
			data, err := io.ReadAll(r.Body)
			if err != nil || lfs.ComputeOID(data) != filepath.Base(r.URL.Path) {
				http.Error(w, "invalid content", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	serverURL = server.URL
	t.Setenv("SAGEOX_ENDPOINT", serverURL)
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(serverURL, gitserver.GitCredentials{Username: "test", Token: "test-token", ServerURL: serverURL, ExpiresAt: time.Now().Add(time.Hour)}))
	pushURL := gitOutput(t, ledgerPath, "remote", "get-url", "--push", "origin")
	runGitCmd(t, ledgerPath, "remote", "set-url", "origin", serverURL+"/ledger.git")
	runGitCmd(t, ledgerPath, "remote", "set-url", "--push", "origin", pushURL)
	handler.skipLFS = false
	handler.projectRoot = t.TempDir()
	return service
}
