package lfs

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUploadSessionFilesStreamsAbove64MiB(t *testing.T) {
	dir := t.TempDir()
	file, err := os.Create(filepath.Join(dir, "raw.jsonl"))
	require.NoError(t, err)
	// Generate the fixture without first allocating it in process memory.
	block := []byte(strings.Repeat("native-redacted-conversation\n", 1024))
	hash := sha256.New()
	const size = int64(65 * 1024 * 1024)
	written := int64(0)
	for written < size {
		part := block
		if int64(len(part)) > size-written {
			part = part[:size-written]
		}
		n, err := file.Write(part)
		require.NoError(t, err)
		_, err = hash.Write(part[:n])
		require.NoError(t, err)
		written += int64(n)
	}
	require.NoError(t, file.Close())
	expected := fmt.Sprintf("%x", hash.Sum(nil))
	var received atomic.Int64
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/objects/batch") {
			var request struct {
				Objects []BatchObject `json:"objects"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			if len(request.Objects) != 1 || request.Objects[0].OID != expected || request.Objects[0].Size != size {
				http.Error(w, "incorrect object", 500)
				return
			}
			_ = json.NewEncoder(w).Encode(BatchResponse{Objects: []BatchResponseObject{{OID: expected, Size: size, Actions: &Actions{Upload: &Action{Href: server.URL + "/blob"}}}}})
			return
		}
		if r.ContentLength != size {
			http.Error(w, "incorrect length", 500)
			return
		}
		hash := sha256.New()
		n, err := io.CopyBuffer(hash, r.Body, make([]byte, 32*1024))
		if err != nil || n != size || fmt.Sprintf("%x", hash.Sum(nil)) != expected {
			http.Error(w, "incorrect content", 500)
			return
		}
		received.Store(n)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := NewClient(server.URL, "test", "test")
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	refs, err := UploadSessionFilesContext(context.Background(), client, dir, nil)
	runtime.ReadMemStats(&after)
	require.NoError(t, err)
	require.Equal(t, size, received.Load())
	require.Equal(t, "sha256:"+expected, refs["raw.jsonl"].OID)
	// HTTP setup plus fixed transfer buffers needs far less than the old 65MiB
	// ReadFile allocation. TotalAlloc catches a transient whole-file buffer too.
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("streamed bytes=%d total allocated bytes=%d", size, allocated)
	require.Less(t, allocated, uint64(16*1024*1024))
}

func TestUploadSnapshotRejectsReplacementAndMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raw.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("original\n"), 0600))
	snapshot, err := openUploadSnapshot(context.Background(), path)
	require.NoError(t, err)
	defer snapshot.file.Close()
	replacement := path + ".replacement"
	require.NoError(t, os.WriteFile(replacement, []byte("original\n"), 0600))
	require.NoError(t, os.Rename(replacement, path))
	require.ErrorContains(t, snapshot.checkUnchanged(), "changed")
}

func TestUploadSessionFilesRejectsMissingBatchReceipt(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte("test"), 0600))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"objects":[]}`)) }))
	defer server.Close()
	_, err := UploadSessionFiles(NewClient(server.URL, "test", "test"), dir, nil)
	require.ErrorContains(t, err, "omitted")
}

func TestUploadSnapshotCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raw.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("test"), 0600))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := openUploadSnapshot(ctx, path)
	require.ErrorIs(t, err, context.Canceled)
}
