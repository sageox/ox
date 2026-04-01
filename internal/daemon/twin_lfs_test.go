//go:build slow

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Full round-trip: batch upload then download via LFS API ---

// TestLFS_BatchUploadDownload_RealServer verifies a complete upload/download
// cycle through the LFS batch API against the Gitea digital twin.
// Failure prevented: broken batch API integration silently drops session blobs.
func TestLFS_BatchUploadDownload_RealServer(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	_ = g.createRepo(t, "lfs-roundtrip")
	client := g.lfsClient(t, "lfs-roundtrip")

	content := []byte("hello world from LFS integration test")
	oid := lfs.ComputeOID(content)
	size := int64(len(content))

	// request upload action
	uploadResp, err := client.BatchUpload([]lfs.BatchObject{{OID: oid, Size: size}})
	require.NoError(t, err)
	require.Len(t, uploadResp.Objects, 1, "batch response must contain exactly one object")

	obj := uploadResp.Objects[0]
	require.Nil(t, obj.Error, "server returned error for upload object")
	require.NotNil(t, obj.Actions, "upload response missing actions")
	require.NotNil(t, obj.Actions.Upload, "upload response missing upload action")

	// upload the blob
	err = lfs.UploadObject(obj.Actions.Upload, content)
	require.NoError(t, err)

	// request download action
	downloadResp, err := client.BatchDownload([]lfs.BatchObject{{OID: oid, Size: size}})
	require.NoError(t, err)
	require.Len(t, downloadResp.Objects, 1, "batch response must contain exactly one object")

	dlObj := downloadResp.Objects[0]
	require.Nil(t, dlObj.Error, "server returned error for download object")
	require.NotNil(t, dlObj.Actions, "download response missing actions")
	require.NotNil(t, dlObj.Actions.Download, "download response missing download action")

	// download and verify content matches
	downloaded, err := lfs.DownloadObject(dlObj.Actions.Download)
	require.NoError(t, err)
	assert.Equal(t, content, downloaded, "downloaded content must match original")
}

// --- B. Session upload pipeline end-to-end ---

// TestLFS_UploadSessionFiles_RealServer verifies the full UploadSessionFiles
// pipeline: read local files, compute OIDs, batch upload, and return FileRef map.
// Failure prevented: session upload pipeline fails silently, losing session data.
func TestLFS_UploadSessionFiles_RealServer(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	_ = g.createRepo(t, "lfs-session")
	client := g.lfsClient(t, "lfs-session")

	// create temp session directory with content files
	sessionDir := t.TempDir()

	sessionFiles := map[string][]byte{
		"raw.jsonl":  []byte(`{"seq":1,"type":"user","content":"test"}`),
		"summary.md": []byte("# Summary\nTest session"),
		"session.md": []byte("# Session\nFull transcript"),
	}

	for name, data := range sessionFiles {
		err := os.WriteFile(filepath.Join(sessionDir, name), data, 0o644)
		require.NoError(t, err)
	}

	// run the full upload pipeline
	refs, err := lfs.UploadSessionFiles(client, sessionDir, nil)
	require.NoError(t, err)
	require.Len(t, refs, 3, "must return FileRef for each session file")

	// verify each FileRef OID matches locally computed OID
	for name, data := range sessionFiles {
		ref, ok := refs[name]
		require.True(t, ok, "missing FileRef for %s", name)

		expectedOID := lfs.ComputeOID(data)
		assert.Equal(t, expectedOID, ref.BareOID(),
			"OID mismatch for %s", name)
		assert.Equal(t, int64(len(data)), ref.Size,
			"size mismatch for %s", name)
	}

	// download each blob from the server to confirm it was actually stored
	for name, data := range sessionFiles {
		ref := refs[name]
		dlResp, err := client.BatchDownload([]lfs.BatchObject{
			{OID: ref.BareOID(), Size: ref.Size},
		})
		require.NoError(t, err, "batch download failed for %s", name)
		require.Len(t, dlResp.Objects, 1)

		dlObj := dlResp.Objects[0]
		require.Nil(t, dlObj.Error, "server error downloading %s", name)
		require.NotNil(t, dlObj.Actions, "no actions for download of %s", name)
		require.NotNil(t, dlObj.Actions.Download, "no download action for %s", name)

		downloaded, err := lfs.DownloadObject(dlObj.Actions.Download)
		require.NoError(t, err, "download failed for %s", name)
		assert.Equal(t, data, downloaded,
			"server content mismatch for %s", name)
	}
}

// --- C. Idempotent upload ---

// TestLFS_UploadDuplicate_Idempotent verifies that uploading the same blob
// twice does not produce an error. LFS servers must accept duplicate uploads.
// Failure prevented: retry logic breaks when server rejects duplicate OIDs.
func TestLFS_UploadDuplicate_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	_ = g.createRepo(t, "lfs-dedup")
	client := g.lfsClient(t, "lfs-dedup")

	content := []byte("duplicate upload test content")
	oid := lfs.ComputeOID(content)
	size := int64(len(content))
	obj := lfs.BatchObject{OID: oid, Size: size}

	// first upload
	resp1, err := client.BatchUpload([]lfs.BatchObject{obj})
	require.NoError(t, err)
	require.Len(t, resp1.Objects, 1)
	require.Nil(t, resp1.Objects[0].Error)

	if resp1.Objects[0].Actions != nil && resp1.Objects[0].Actions.Upload != nil {
		err = lfs.UploadObject(resp1.Objects[0].Actions.Upload, content)
		require.NoError(t, err, "first upload must succeed")
	}

	// second upload of same content -- must not error
	resp2, err := client.BatchUpload([]lfs.BatchObject{obj})
	require.NoError(t, err)
	require.Len(t, resp2.Objects, 1)
	require.Nil(t, resp2.Objects[0].Error)

	if resp2.Objects[0].Actions != nil && resp2.Objects[0].Actions.Upload != nil {
		// server still offers an upload action -- upload again, must succeed
		err = lfs.UploadObject(resp2.Objects[0].Actions.Upload, content)
		require.NoError(t, err, "duplicate upload must not error")
	}
	// if server returns no upload action, object already exists -- also fine
}

// --- D. Download of non-existent object ---

// TestLFS_DownloadMissing_ReturnsError verifies that requesting a download
// for a non-existent OID results in an error condition (either a batch-level
// object error or a failed download).
// Failure prevented: missing-blob errors swallowed, causing silent data loss.
func TestLFS_DownloadMissing_ReturnsError(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	_ = g.createRepo(t, "lfs-missing")
	client := g.lfsClient(t, "lfs-missing")

	// valid-format SHA256 hex that doesn't correspond to any uploaded object
	fakeOID := strings.Repeat("a", 64)

	resp, err := client.BatchDownload([]lfs.BatchObject{
		{OID: fakeOID, Size: 42},
	})

	// the batch call itself may succeed but the object should have an error,
	// or the batch call fails outright -- either is acceptable
	if err != nil {
		// batch-level failure is a valid error signal
		return
	}

	require.Len(t, resp.Objects, 1)
	obj := resp.Objects[0]

	if obj.Error != nil {
		// server reported object-level error (e.g., 404) -- expected
		assert.NotEqual(t, 0, obj.Error.Code, "error code should be non-zero")
		return
	}

	// if the server returned a download action anyway, the download itself must fail
	if obj.Actions != nil && obj.Actions.Download != nil {
		_, err := lfs.DownloadObject(obj.Actions.Download)
		assert.Error(t, err, "downloading a non-existent OID must fail")
		return
	}

	// no error, no download action -- also an error condition (object doesn't exist)
	t.Fatal("batch response had no error and no download action for non-existent OID")
}
