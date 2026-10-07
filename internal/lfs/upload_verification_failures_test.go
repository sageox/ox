package lfs

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// accepting a PUT is insufficient: an unverifiable object must never yield an
// UploadedRef that allows callers to replace their original bytes with a stub.
func TestUploadBlob_RefusesUnverifiableStoreAcknowledgment(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		wrongOID  bool
		wantError string
	}{
		{name: "probe unavailable", status: http.StatusServiceUnavailable, wantError: "verification probe"},
		{name: "object omitted", status: http.StatusOK, wantError: "not reported by the store"},
		{name: "different object reported", status: http.StatusOK, wrongOID: true, wantError: "not reported by the store"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := []byte("original large plan render retained by the caller\n")
			var mu sync.Mutex
			var stored []byte
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					mu.Lock()
					stored = body
					mu.Unlock()
					w.WriteHeader(http.StatusOK)
					return
				}
				var request batchRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
				if request.Operation == "download" {
					if tc.status != http.StatusOK {
						w.WriteHeader(tc.status)
						return
					}
					objects := []BatchResponseObject{}
					if tc.wrongOID {
						objects = append(objects, BatchResponseObject{OID: ComputeOID([]byte("different render")), Size: 16})
					}
					_ = json.NewEncoder(w).Encode(BatchResponse{Objects: objects})
					return
				}
				object := request.Objects[0]
				_ = json.NewEncoder(w).Encode(BatchResponse{Objects: []BatchResponseObject{{OID: object.OID, Size: object.Size, Actions: &Actions{Upload: &Action{Href: server.URL + "/objects/" + object.OID}}}}})
			}))
			defer server.Close()
			uploaded, err := UploadBlob(NewClient(server.URL, "oauth2", "token"), content)
			require.ErrorContains(t, err, tc.wantError)
			assert.Empty(t, uploaded.BareOID(), "an unverified upload cannot authorize a pointer")
			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, content, stored, "the original complete bytes reached the PUT despite verification failure")
			assert.Equal(t, "original large plan render retained by the caller\n", string(content))
		})
	}
}
