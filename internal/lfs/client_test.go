package lfs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient(t *testing.T) {
	c := NewClient("https://git.sageox.io/sageox/ledger.git", "oauth2", "test-token")
	assert.Equal(t, "https://git.sageox.io/sageox/ledger.git/info/lfs/objects/batch", c.batchURL)
	assert.Contains(t, c.authHeader, "Basic ")
	assert.Equal(t, 2*time.Minute, c.httpClient.Timeout, "batch API timeout should be 2 minutes")
}

func TestNewClient_TrailingSlash(t *testing.T) {
	c := NewClient("https://git.sageox.io/sageox/ledger.git/", "oauth2", "test-token")
	assert.Equal(t, "https://git.sageox.io/sageox/ledger.git/info/lfs/objects/batch", c.batchURL)
}

func TestBatchUpload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/vnd.git-lfs+json", r.Header.Get("Content-Type"))
		assert.Contains(t, r.Header.Get("Authorization"), "Basic ")

		var req batchRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, "upload", req.Operation)
		assert.Len(t, req.Objects, 1)
		assert.Equal(t, "abc123", req.Objects[0].OID)

		resp := BatchResponse{
			Transfer: "basic",
			Objects: []BatchResponseObject{
				{
					OID:  "abc123",
					Size: 100,
					Actions: &Actions{
						Upload: &Action{
							Href: "https://storage.example.com/upload/abc123",
							Header: map[string]string{
								"Content-Type": "application/octet-stream",
							},
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := &Client{
		batchURL:   server.URL,
		httpClient: server.Client(),
		authHeader: "Basic dGVzdDp0b2tlbg==",
	}

	objects := []BatchObject{{OID: "abc123", Size: 100}}
	resp, err := c.BatchUpload(objects)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Len(t, resp.Objects, 1)
	assert.Equal(t, "abc123", resp.Objects[0].OID)
	assert.NotNil(t, resp.Objects[0].Actions)
	assert.NotNil(t, resp.Objects[0].Actions.Upload)
}

func TestBatchDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req batchRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, "download", req.Operation)

		resp := BatchResponse{
			Transfer: "basic",
			Objects: []BatchResponseObject{
				{
					OID:  "def456",
					Size: 200,
					Actions: &Actions{
						Download: &Action{
							Href: "https://storage.example.com/download/def456",
						},
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := &Client{
		batchURL:   server.URL,
		httpClient: server.Client(),
		authHeader: "Basic dGVzdDp0b2tlbg==",
	}

	objects := []BatchObject{{OID: "def456", Size: 200}}
	resp, err := c.BatchDownload(objects)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Len(t, resp.Objects, 1)
	assert.NotNil(t, resp.Objects[0].Actions.Download)
}

func TestBatch_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	c := &Client{
		batchURL:   server.URL,
		httpClient: server.Client(),
		authHeader: "Basic dGVzdDp0b2tlbg==",
	}

	_, err := c.BatchUpload([]BatchObject{{OID: "abc", Size: 10}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 500")
}

// TestBatch_RejectsPlaintextHTTP confirms doBatch refuses to send
// Authorization to a non-loopback http URL. Without this guard, a
// misconfigured or attacker-rewritten remote (http://example.com/...)
// would leak the user's PAT in the clear on the very first request,
// before the per-action trusted-host stamping has any chance to run.
func TestBatch_RejectsPlaintextHTTP(t *testing.T) {
	c := &Client{
		batchURL:   "http://example.com/sageox/ledger.git/info/lfs/objects/batch",
		httpClient: &http.Client{Timeout: 5 * time.Second},
		authHeader: "Basic dGVzdDp0b2tlbg==",
	}
	_, err := c.BatchUpload([]BatchObject{{OID: "abc", Size: 10}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plaintext http")
}

// TestBatch_AllowsLoopbackHTTP confirms the guard does not break
// httptest-based tests, which always use 127.0.0.1.
func TestBatch_AllowsLoopbackHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(BatchResponse{Transfer: "basic"})
	}))
	defer server.Close()

	c := &Client{
		batchURL:   server.URL, // http://127.0.0.1:NNNN — must be permitted
		httpClient: server.Client(),
		authHeader: "Basic dGVzdDp0b2tlbg==",
	}
	_, err := c.BatchUpload([]BatchObject{{OID: "abc", Size: 10}})
	require.NoError(t, err)
}

func TestBatch_ObjectError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := BatchResponse{
			Objects: []BatchResponseObject{
				{
					OID:  "abc123",
					Size: 100,
					Error: &ObjectError{
						Code:    404,
						Message: "object not found",
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := &Client{
		batchURL:   server.URL,
		httpClient: server.Client(),
		authHeader: "Basic dGVzdDp0b2tlbg==",
	}

	resp, err := c.BatchDownload([]BatchObject{{OID: "abc123", Size: 100}})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.NotNil(t, resp.Objects[0].Error)
	assert.Equal(t, 404, resp.Objects[0].Error.Code)
}
