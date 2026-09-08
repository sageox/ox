package lfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
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

const (
	readTestRepoID = "repo_01936d5a-0000-7abc-8def-0123456789ab"
	readTestToken  = "oxt_test_1ljPfr"
	rotatedToken   = "oxt_rotated_1lKvCA"
)

func readLFSFixture(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	t.Setenv("SAGEOX_TOKEN", readTestToken)
	c, err := NewReadClient(server.URL, readTestRepoID, server.URL+"/api/v1/cli/repos/"+readTestRepoID+"/ledger.git")
	require.NoError(t, err)
	c.httpClient.Transport = server.Client().Transport
	previous := lfsHTTPClient
	local := *previous
	local.Transport = server.Client().Transport
	lfsHTTPClient = &local
	t.Cleanup(func() { lfsHTTPClient = previous })
	return c, server
}

// Failure prevented: a client or saved action retains an old identity after rotation.
func TestReadLFS_RotationAndDownloadOnly(t *testing.T) {
	content := []byte("authorized session content")
	oid := ComputeOID(content)
	wantToken := readTestToken
	var hits atomic.Int32
	c, _ := readLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		username, token, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "ox", username)
		assert.Equal(t, wantToken, token)
		assert.Empty(t, r.Header.Get("X-Upstream-Token"))
		if strings.HasSuffix(r.URL.Path, "/batch") {
			var request batchRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			assert.Equal(t, "download", request.Operation)
			json.NewEncoder(w).Encode(BatchResponse{Objects: []BatchResponseObject{{
				OID: oid, Size: int64(len(content)), Actions: &Actions{
					Download: &Action{Href: "https://" + r.Host + strings.TrimSuffix(r.URL.Path, "/batch") + "/" + oid,
						Header: map[string]string{"Authorization": "Bearer stale-token", "X-Upstream-Token": "secret"}},
					Upload: &Action{Href: "https://" + r.Host + "/upload"}, Verify: &Action{Href: "https://" + r.Host + "/verify"},
				},
			}}})
			return
		}
		assert.Equal(t, http.MethodGet, r.Method)
		w.Write(content)
	})
	resp, err := c.BatchDownload([]BatchObject{{OID: oid, Size: int64(len(content))}})
	require.NoError(t, err)
	actions := resp.Objects[0].Actions
	require.NotNil(t, actions.Download)
	assert.Nil(t, actions.Upload)
	assert.Nil(t, actions.Verify)
	assert.Empty(t, c.authHeader, "read clients must not retain a token")

	wantToken = rotatedToken
	t.Setenv("SAGEOX_TOKEN", rotatedToken)
	var out bytes.Buffer
	require.NoError(t, DownloadToFileContext(context.Background(), actions.Download, &out, true, oid))
	assert.Equal(t, content, out.Bytes())
	_, err = c.BatchDownload([]BatchObject{{OID: oid, Size: int64(len(content))}})
	require.NoError(t, err)

	_, err = c.BatchUpload([]BatchObject{{OID: oid}})
	require.Error(t, err)
	require.Error(t, UploadObject(actions.Download, content))
	require.Error(t, VerifyObject(actions.Download, oid, int64(len(content))))
	assert.Equal(t, int32(3), hits.Load(), "read clients and actions cannot perform upload/verify requests")

	t.Setenv("SAGEOX_TOKEN", "")
	_, err = c.BatchDownload([]BatchObject{{OID: oid}})
	require.ErrorIs(t, err, auth.ErrReadTokenUnavailable)
	_, err = DownloadObject(actions.Download)
	require.ErrorIs(t, err, auth.ErrReadTokenUnavailable)
	assert.Equal(t, int32(3), hits.Load(), "missing current credential must stop remote requests")
}

// Failure prevented: grant denial is treated as an empty ledger or leaks a reflected token.
func TestReadLFS_DeniedAndMissingRemainDistinct(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c, _ := readLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				w.Write([]byte(readTestToken))
			})
			_, err := c.BatchDownload(nil)
			var httpErr *HTTPError
			require.ErrorAs(t, err, &httpErr)
			assert.Equal(t, status, httpErr.StatusCode)
			assert.NotContains(t, err.Error(), readTestToken)
		})
	}
	t.Run("missing object", func(t *testing.T) {
		c, _ := readLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(BatchResponse{Objects: []BatchResponseObject{{OID: "missing", Error: &ObjectError{Code: 404, Message: readTestToken}}}})
		})
		resp, err := c.BatchDownload([]BatchObject{{OID: "missing"}})
		require.NoError(t, err)
		require.Len(t, resp.Objects, 1)
		require.NotNil(t, resp.Objects[0].Error)
		assert.Equal(t, 404, resp.Objects[0].Error.Code)
		assert.NotContains(t, resp.Objects[0].Error.Message, readTestToken)
	})
}

// Failure prevented: upstream action headers or TAT escape to signed object storage.
func TestReadLFS_SignedStorageIsCredentialFreeAndVerified(t *testing.T) {
	content := []byte("plan from signed storage")
	oid := ComputeOID(content)
	corrupt := false
	var hits atomic.Int32
	storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		for _, header := range []string{"Authorization", "Proxy-Authorization", "Cookie", "X-Upstream-Token"} {
			assert.Empty(t, r.Header.Get(header), header)
		}
		assert.Equal(t, "storage-signature", r.URL.Query().Get("signature"))
		if corrupt {
			w.Write([]byte("corrupt"))
			return
		}
		w.Write(content)
	}))
	defer storage.Close()
	c, _ := readLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(BatchResponse{Objects: []BatchResponseObject{{OID: oid, Actions: &Actions{Download: &Action{
			Href:   storage.URL + "/object?signature=storage-signature",
			Header: map[string]string{"Authorization": "Bearer " + readTestToken, "Cookie": "session=secret", "X-Upstream-Token": "secret"},
		}}}}})
	})
	resp, err := c.BatchDownload([]BatchObject{{OID: oid}})
	require.NoError(t, err)
	action := resp.Objects[0].Actions.Download
	data, err := DownloadObject(action)
	require.NoError(t, err)
	assert.Equal(t, content, data)
	corrupt = true
	_, err = DownloadObject(action)
	require.ErrorContains(t, err, "OID mismatch")
	var out bytes.Buffer
	err = DownloadToFileContext(context.Background(), action, &out, false, "")
	require.ErrorContains(t, err, "OID mismatch", "verification cannot be disabled for read actions")
	assert.Equal(t, int32(3), hits.Load())
}

// Failure prevented: a malicious action broadens same-origin TAT authority or leaks it in URLs.
func TestReadLFS_UnsafeActionsNeverReachNetwork(t *testing.T) {
	var actionHref string
	var hits atomic.Int32
	oid := ComputeOID([]byte("content"))
	c, server := readLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		json.NewEncoder(w).Encode(BatchResponse{Objects: []BatchResponseObject{{OID: oid, Actions: &Actions{Download: &Action{Href: actionHref}}}}})
	})
	for _, href := range []string{
		server.URL + "/api/v1/cli/repos/foreign/ledger.git/info/lfs/objects/" + oid,
		c.readURL + "/info/lfs/objects/" + strings.Repeat("0", 64),
		c.readURL + "/git-upload-pack",
		strings.Replace(server.URL, "https://", "http://", 1) + "/object",
		"https://username:password@storage.example/object",
		"https://storage.example/object?token=" + readTestToken,
		"https://storage.example/object?token=" + strings.ReplaceAll(readTestToken, "_", "%5f"),
		"https://storage.example/object?token=" + readTestToken + "&invalid=%zz",
	} {
		t.Run(href, func(t *testing.T) {
			actionHref = href
			before := hits.Load()
			resp, err := c.BatchDownload([]BatchObject{{OID: oid}})
			require.NoError(t, err)
			_, err = DownloadObject(resp.Objects[0].Actions.Download)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), readTestToken)
			assert.Equal(t, before+1, hits.Load(), "only batch discovery should reach network")
		})
	}
}

// Failure prevented: redirects replay same-origin Authorization on unrelated routes.
func TestReadLFS_BatchAndObjectRedirectsRefused(t *testing.T) {
	for _, redirectBatch := range []bool{true, false} {
		t.Run(map[bool]string{true: "batch", false: "object"}[redirectBatch], func(t *testing.T) {
			var hits atomic.Int32
			oid := ComputeOID([]byte("content"))
			c, _ := readLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if !redirectBatch && strings.HasSuffix(r.URL.Path, "/batch") {
					json.NewEncoder(w).Encode(BatchResponse{Objects: []BatchResponseObject{{OID: oid, Actions: &Actions{Download: &Action{
						Href: "https://" + r.Host + strings.TrimSuffix(r.URL.Path, "/batch") + "/" + oid,
					}}}}})
					return
				}
				if strings.HasSuffix(r.URL.Path, "/leak") {
					t.Error("redirect must never be followed")
					return
				}
				http.Redirect(w, r, "/leak", http.StatusTemporaryRedirect)
			})
			resp, err := c.BatchDownload([]BatchObject{{OID: oid}})
			wantHits := int32(1)
			if !redirectBatch {
				require.NoError(t, err)
				_, err = DownloadObject(resp.Objects[0].Actions.Download)
				wantHits++
			}
			var httpErr *HTTPError
			require.ErrorAs(t, err, &httpErr)
			assert.Equal(t, http.StatusTemporaryRedirect, httpErr.StatusCode)
			assert.Equal(t, wantHits, hits.Load())
		})
	}
}

// Failure prevented: a canceled refresh keeps downloading LFS content past its budget.
func TestReadLFS_ContextCancellation(t *testing.T) {
	for _, cancelBatch := range []bool{true, false} {
		t.Run(map[bool]string{true: "batch", false: "object"}[cancelBatch], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			release := make(chan struct{})
			defer close(release)
			oid := ComputeOID([]byte("content"))
			c, _ := readLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if !cancelBatch && strings.HasSuffix(r.URL.Path, "/batch") {
					json.NewEncoder(w).Encode(BatchResponse{Objects: []BatchResponseObject{{OID: oid, Actions: &Actions{Download: &Action{
						Href: "https://" + r.Host + strings.TrimSuffix(r.URL.Path, "/batch") + "/" + oid,
					}}}}})
					return
				}
				cancel()
				<-release
			})
			resp, err := c.BatchDownloadContext(ctx, nil)
			if !cancelBatch {
				require.NoError(t, err)
				var out bytes.Buffer
				err = DownloadToFileContext(ctx, resp.Objects[0].Actions.Download, &out, true, oid)
			}
			require.True(t, errors.Is(err, context.Canceled), "%v", err)
		})
	}
}
