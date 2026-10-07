package lfs

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeUploadStore is an LFS server that behaves like a strict object store
// behind a proxy: it rejects chunked PUT bodies, can drop the first PUT's
// connection, and can acknowledge a PUT without persisting the object.
type fakeUploadStore struct {
	mu            sync.Mutex
	resetFirstPut bool
	acceptButDrop bool // answers 200 but never stores the object
	stored        map[string][]byte
	putAttempts   int
	chunkedPuts   int
	contentLength []int64
}

func newFakeUploadStore(t *testing.T, resetFirstPut, acceptButDrop bool) (*Client, *fakeUploadStore) {
	t.Helper()
	store := &fakeUploadStore{resetFirstPut: resetFirstPut, acceptButDrop: acceptButDrop, stored: map[string][]byte{}}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			store.handlePut(w, r)
			return
		}
		var request batchRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		response := BatchResponse{Transfer: "basic"}
		store.mu.Lock()
		for _, object := range request.Objects {
			item := BatchResponseObject{OID: object.OID, Size: object.Size}
			_, have := store.stored[object.OID]
			switch request.Operation {
			case "download":
				if have {
					item.Actions = &Actions{Download: &Action{Href: server.URL + "/objects/" + object.OID}}
				} else {
					item.Error = &ObjectError{Code: http.StatusNotFound, Message: "Object does not exist on the server"}
				}
			case "upload":
				if !have {
					item.Actions = &Actions{Upload: &Action{Href: server.URL + "/objects/" + object.OID}}
				}
			}
			response.Objects = append(response.Objects, item)
		}
		store.mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(server.Close)
	return NewClient(server.URL, "oauth2", "token"), store
}

func (s *fakeUploadStore) handlePut(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.putAttempts++
	attempt := s.putAttempts
	s.contentLength = append(s.contentLength, r.ContentLength)
	chunked := r.ContentLength < 0 || len(r.TransferEncoding) > 0
	if chunked {
		s.chunkedPuts++
	}
	s.mu.Unlock()

	if chunked {
		http.Error(w, "chunked bodies are not accepted", http.StatusBadRequest)
		return
	}
	if s.resetFirstPut && attempt == 1 {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return
	}
	body, _ := io.ReadAll(r.Body)
	if !s.acceptButDrop {
		s.mu.Lock()
		s.stored[strings.TrimPrefix(r.URL.Path, "/objects/")] = body
		s.mu.Unlock()
	}
	w.WriteHeader(http.StatusOK)
}

// A 5 MB plan.html upload must carry Content-Length, and one connection reset
// must not lose the object: the second attempt is a fresh request.
func TestUploadBlob_SendsContentLengthAndRetriesOnceOnReset(t *testing.T) {
	client, store := newFakeUploadStore(t, true, false)
	content := []byte(strings.Repeat("<html>plan</html>\n", 300000))

	ref, err := UploadBlob(client, content)

	require.NoError(t, err)
	assert.Equal(t, NewFileRef(content).BareOID(), ref.BareOID())
	assert.Zero(t, store.chunkedPuts, "the PUT must never be chunked")
	assert.GreaterOrEqual(t, store.putAttempts, 2, "the reset PUT is retried")
	for _, length := range store.contentLength {
		assert.Equal(t, int64(len(content)), length)
	}
	assert.Equal(t, content, store.stored[ref.BareOID()])
}

// A PUT the store answers 200 for but never persists must fail the upload: the
// Batch download probe reports the object missing, so no pointer may be minted.
func TestUploadBlob_FailsWhenStoreDoesNotHoldObjectAfterUpload(t *testing.T) {
	client, store := newFakeUploadStore(t, false, true)

	_, err := UploadBlob(client, []byte("<html>plan that vanishes</html>\n"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found on the store after upload")
	assert.Equal(t, 1, store.putAttempts)
}
