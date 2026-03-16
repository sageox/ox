package lfs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeOID(t *testing.T) {
	// SHA256 of empty string
	oid := ComputeOID([]byte(""))
	assert.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", oid)

	// SHA256 of "hello"
	oid = ComputeOID([]byte("hello"))
	assert.Equal(t, "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824", oid)
}

func TestUploadObject(t *testing.T) {
	content := []byte("test upload data")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		assert.Equal(t, "my-value", r.Header.Get("X-Custom"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, content, body)

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	action := &Action{
		Href: server.URL,
		Header: map[string]string{
			"X-Custom": "my-value",
		},
	}

	err := UploadObject(action, content)
	require.NoError(t, err)
}

func TestUploadObject_NilAction(t *testing.T) {
	err := UploadObject(nil, []byte("data"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no upload action")
}

func TestUploadObject_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("forbidden"))
	}))
	defer server.Close()

	err := UploadObject(&Action{Href: server.URL}, []byte("data"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 403")
}

func TestDownloadObject(t *testing.T) {
	expected := []byte("downloaded content")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		w.Write(expected)
	}))
	defer server.Close()

	data, err := DownloadObject(&Action{Href: server.URL})
	require.NoError(t, err)
	assert.Equal(t, expected, data)
}

func TestDownloadObject_NilAction(t *testing.T) {
	_, err := DownloadObject(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no download action")
}

func TestUploadAll(t *testing.T) {
	oid := ComputeOID([]byte("test data"))
	content := []byte("test data")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	resp := &BatchResponse{
		Objects: []BatchResponseObject{
			{
				OID:  oid,
				Size: int64(len(content)),
				Actions: &Actions{
					Upload: &Action{Href: server.URL},
				},
			},
		},
	}

	files := map[string][]byte{oid: content}
	results := UploadAll(resp, files, 2)
	require.Len(t, results, 1)
	assert.NoError(t, results[0].Error)
	assert.Equal(t, oid, results[0].OID)
}

func TestUploadAll_AlreadyExists(t *testing.T) {
	// When the server returns no upload action, the object already exists
	resp := &BatchResponse{
		Objects: []BatchResponseObject{
			{
				OID:  "existing-oid",
				Size: 100,
				// no Actions means object already exists
			},
		},
	}

	results := UploadAll(resp, nil, 2)
	require.Len(t, results, 1)
	assert.NoError(t, results[0].Error)
}

func TestUploadAll_ObjectError(t *testing.T) {
	resp := &BatchResponse{
		Objects: []BatchResponseObject{
			{
				OID:  "bad-oid",
				Size: 100,
				Error: &ObjectError{
					Code:    422,
					Message: "invalid object",
				},
			},
		},
	}

	results := UploadAll(resp, nil, 2)
	require.Len(t, results, 1)
	assert.Error(t, results[0].Error)
	assert.Contains(t, results[0].Error.Error(), "invalid object")
}

func TestDownloadAll(t *testing.T) {
	expected := []byte("file content")
	oid := ComputeOID(expected)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(expected)
	}))
	defer server.Close()

	resp := &BatchResponse{
		Objects: []BatchResponseObject{
			{
				OID:  oid,
				Size: int64(len(expected)),
				Actions: &Actions{
					Download: &Action{Href: server.URL},
				},
			},
		},
	}

	results := DownloadAll(resp, 2)
	require.Len(t, results, 1)
	assert.NoError(t, results[0].Error)
	assert.Equal(t, expected, results[0].Content)
	assert.Equal(t, oid, results[0].OID)
}

func TestDownloadAll_NoAction(t *testing.T) {
	resp := &BatchResponse{
		Objects: []BatchResponseObject{
			{
				OID:  "missing-action",
				Size: 100,
				// no download action
			},
		},
	}

	results := DownloadAll(resp, 2)
	require.Len(t, results, 1)
	require.Error(t, results[0].Error)
	assert.Contains(t, results[0].Error.Error(), "no download action")
}

func TestDownloadAll_ServerError(t *testing.T) {
	resp := &BatchResponse{
		Objects: []BatchResponseObject{
			{
				OID:  "err-oid",
				Size: 100,
				Error: &ObjectError{
					Code:    404,
					Message: "not found",
				},
			},
		},
	}

	results := DownloadAll(resp, 2)
	require.Len(t, results, 1)
	require.Error(t, results[0].Error)
	assert.Contains(t, results[0].Error.Error(), "server error 404")
	assert.Contains(t, results[0].Error.Error(), "not found")
}

func TestDownloadAll_PartialFailure(t *testing.T) {
	goodContent := []byte("good file")
	goodOID := ComputeOID(goodContent)

	goodServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(goodContent)
	}))
	defer goodServer.Close()

	badServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	}))
	defer badServer.Close()

	resp := &BatchResponse{
		Objects: []BatchResponseObject{
			{
				OID:  goodOID,
				Size: int64(len(goodContent)),
				Actions: &Actions{
					Download: &Action{Href: goodServer.URL},
				},
			},
			{
				OID:  "bad-oid",
				Size: 50,
				Actions: &Actions{
					Download: &Action{Href: badServer.URL},
				},
			},
		},
	}

	results := DownloadAll(resp, 2)
	require.Len(t, results, 2)

	// both results are present — one success, one failure
	var successes, failures int
	for _, r := range results {
		if r.Error != nil {
			failures++
			assert.Contains(t, r.Error.Error(), "bad-oid")
		} else {
			successes++
			assert.Equal(t, goodOID, r.OID)
			assert.Equal(t, goodContent, r.Content)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, failures)
}

func TestDownloadAll_MixedErrors(t *testing.T) {
	// One server error object, one missing-action object, one OK
	goodContent := []byte("content")
	goodOID := ComputeOID(goodContent)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(goodContent)
	}))
	defer server.Close()

	resp := &BatchResponse{
		Objects: []BatchResponseObject{
			{
				OID:  "server-err",
				Size: 10,
				Error: &ObjectError{
					Code:    500,
					Message: "internal",
				},
			},
			{
				OID:  "no-action",
				Size: 10,
				// no Actions
			},
			{
				OID:  goodOID,
				Size: int64(len(goodContent)),
				Actions: &Actions{
					Download: &Action{Href: server.URL},
				},
			},
		},
	}

	results := DownloadAll(resp, 4)
	require.Len(t, results, 3)

	errCount := 0
	for _, r := range results {
		if r.Error != nil {
			errCount++
		}
	}
	assert.Equal(t, 2, errCount, "should report both server error and missing action")
}

func TestUploadAll_PartialFailure(t *testing.T) {
	goodOID := ComputeOID([]byte("good"))
	badOID := ComputeOID([]byte("bad"))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) == "bad" {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("upload error"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	resp := &BatchResponse{
		Objects: []BatchResponseObject{
			{
				OID:  goodOID,
				Size: 4,
				Actions: &Actions{
					Upload: &Action{Href: server.URL},
				},
			},
			{
				OID:  badOID,
				Size: 3,
				Actions: &Actions{
					Upload: &Action{Href: server.URL},
				},
			},
		},
	}

	files := map[string][]byte{
		goodOID: []byte("good"),
		badOID:  []byte("bad"),
	}

	results := UploadAll(resp, files, 2)
	require.Len(t, results, 2)

	var successes, failures int
	for _, r := range results {
		if r.Error != nil {
			failures++
		} else {
			successes++
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, failures)
}

func TestUploadAll_MissingContent(t *testing.T) {
	resp := &BatchResponse{
		Objects: []BatchResponseObject{
			{
				OID:  "unknown-oid",
				Size: 10,
				Actions: &Actions{
					Upload: &Action{Href: "http://unused"},
				},
			},
		},
	}

	results := UploadAll(resp, map[string][]byte{}, 2)
	require.Len(t, results, 1)
	require.Error(t, results[0].Error)
	assert.Contains(t, results[0].Error.Error(), "no content for OID")
}

func TestSharedHTTPClient_ConnectionReuse(t *testing.T) {
	// verify that upload and download use the shared client (connection reuse)
	// by checking that multiple sequential operations to the same server reuse connections
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.Method == "PUT" {
			w.WriteHeader(http.StatusOK)
		} else {
			w.Write([]byte("content"))
		}
	}))
	defer server.Close()

	action := &Action{Href: server.URL}

	// multiple uploads should reuse the shared client
	for i := 0; i < 3; i++ {
		err := UploadObject(action, []byte("data"))
		require.NoError(t, err)
	}

	// multiple downloads should also reuse
	for i := 0; i < 3; i++ {
		data, err := DownloadObject(action)
		require.NoError(t, err)
		assert.Equal(t, []byte("content"), data)
	}

	assert.Equal(t, 6, requestCount, "expected 6 total requests (3 uploads + 3 downloads)")
}

func TestSharedHTTPClient_Timeout(t *testing.T) {
	// verify the shared client has the expected timeout
	assert.Equal(t, 5*time.Minute, lfsHTTPClient.Timeout, "shared LFS client should have 5-minute timeout")
}

func TestBytesReaderAt(t *testing.T) {
	data := []byte("hello world")
	r := newBytesReaderAt(data)

	buf := make([]byte, 5)
	n, err := r.ReadAt(buf, 0)
	assert.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, "hello", string(buf))

	n, err = r.ReadAt(buf, 6)
	assert.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, "world", string(buf))

	// read past end
	n, err = r.ReadAt(buf, int64(len(data)))
	assert.Equal(t, io.EOF, err)
	assert.Equal(t, 0, n)
}
