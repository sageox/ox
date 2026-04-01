//go:build slow

package daemon

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. LFS pointer file survives git push/pull ---

// TestLFS_PointerFile_SurvivesPushPull verifies that an LFS pointer
// file committed and pushed to Gitea is correctly preserved when
// cloned again — the pointer text (not the blob) is what git stores.
// Failure prevented: LFS pointer files corrupted during push/pull,
// breaking session hydration which relies on parsing pointer format.
func TestLFS_PointerFile_SurvivesPushPull(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-lfs-pointer-push")

	client := g.lfsClient(t, "twin-lfs-pointer-push")

	// upload a blob via LFS API
	content := []byte("session content for pointer test\n")
	oid := lfs.ComputeOID(content)
	size := int64(len(content))

	resp, err := client.BatchUpload([]lfs.BatchObject{{OID: oid, Size: size}})
	require.NoError(t, err)
	require.Len(t, resp.Objects, 1)
	if resp.Objects[0].Actions != nil && resp.Objects[0].Actions.Upload != nil {
		require.NoError(t, lfs.UploadObject(resp.Objects[0].Actions.Upload, content))
	}

	// clone repo, write an LFS pointer file, commit and push
	cloneDir := filepath.Join(t.TempDir(), "clone")
	g.cloneRepo(t, cloneURL, cloneDir)

	// construct LFS pointer text (standard format)
	pointerText := fmt.Sprintf("version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n", oid, size)

	sessionsDir := filepath.Join(cloneDir, "sessions", "test-session")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(sessionsDir, "raw.jsonl"),
		[]byte(pointerText), 0o644))

	// also write meta.json referencing the LFS OID
	metaJSON := fmt.Sprintf(`{"files":{"raw.jsonl":{"oid":"sha256:%s","size":%d}}}`, oid, size)
	require.NoError(t, os.WriteFile(
		filepath.Join(sessionsDir, "meta.json"),
		[]byte(metaJSON), 0o644))

	cmd := exec.Command("git", "-C", cloneDir, "add", "sessions/")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "add session with LFS pointer")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git push: %s", string(out))

	// clone fresh and verify pointer file is intact
	verifyDir := filepath.Join(t.TempDir(), "verify")
	g.cloneRepo(t, cloneURL, verifyDir)

	pointerContent, err := os.ReadFile(filepath.Join(verifyDir, "sessions", "test-session", "raw.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, pointerText, string(pointerContent),
		"LFS pointer file should survive push/pull unchanged")

	// verify we can still download the actual content via LFS API
	dlResp, err := client.BatchDownload([]lfs.BatchObject{{OID: oid, Size: size}})
	require.NoError(t, err)
	require.Len(t, dlResp.Objects, 1)
	require.NotNil(t, dlResp.Objects[0].Actions)
	require.NotNil(t, dlResp.Objects[0].Actions.Download)

	downloaded, err := lfs.DownloadObject(dlResp.Objects[0].Actions.Download)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(content, downloaded))
}

// --- B. LFS upload with auth token (not basic auth) ---

// TestLFS_AuthToken_WorksForBatchAPI verifies that the LFS client
// works with Gitea's API token for authentication, not just
// username/password basic auth.
// Failure prevented: LFS batch API rejecting token-based auth,
// causing all session uploads from daemon to fail.
func TestLFS_AuthToken_WorksForBatchAPI(t *testing.T) {
	g := getSharedGitea(t)
	repoName := "twin-lfs-token-auth"
	g.createRepo(t, repoName)

	// create client using admin token (not password)
	client := g.lfsClient(t, repoName)

	content := []byte("authenticated upload test\n")
	oid := lfs.ComputeOID(content)
	size := int64(len(content))

	// batch upload with token auth
	resp, err := client.BatchUpload([]lfs.BatchObject{{OID: oid, Size: size}})
	require.NoError(t, err, "batch upload with token auth should succeed")
	require.Len(t, resp.Objects, 1)
	require.Nil(t, resp.Objects[0].Error)

	if resp.Objects[0].Actions != nil && resp.Objects[0].Actions.Upload != nil {
		err = lfs.UploadObject(resp.Objects[0].Actions.Upload, content)
		require.NoError(t, err, "blob upload with token auth should succeed")
	}

	// verify download also works
	dlResp, err := client.BatchDownload([]lfs.BatchObject{{OID: oid, Size: size}})
	require.NoError(t, err)
	require.Len(t, dlResp.Objects, 1)
	require.NotNil(t, dlResp.Objects[0].Actions)
	require.NotNil(t, dlResp.Objects[0].Actions.Download)

	downloaded, err := lfs.DownloadObject(dlResp.Objects[0].Actions.Download)
	require.NoError(t, err)
	assert.Equal(t, content, downloaded)
}

// --- C. Large blob upload ---

// TestLFS_LargeBlob_RealServer verifies that larger blobs (>64KB)
// upload and download correctly through Gitea's LFS.
// Failure prevented: chunked transfer encoding or content-length
// issues causing large session files (raw.jsonl) to fail upload.
func TestLFS_LargeBlob_RealServer(t *testing.T) {
	g := getSharedGitea(t)
	repoName := "twin-lfs-large"
	g.createRepo(t, repoName)

	client := g.lfsClient(t, repoName)

	// 256KB blob (realistic raw.jsonl size)
	content := make([]byte, 256*1024)
	for i := range content {
		content[i] = byte('A' + (i % 26))
	}

	oid := lfs.ComputeOID(content)
	size := int64(len(content))

	resp, err := client.BatchUpload([]lfs.BatchObject{{OID: oid, Size: size}})
	require.NoError(t, err)
	require.Len(t, resp.Objects, 1)
	require.Nil(t, resp.Objects[0].Error)
	require.NotNil(t, resp.Objects[0].Actions)
	require.NotNil(t, resp.Objects[0].Actions.Upload)

	err = lfs.UploadObject(resp.Objects[0].Actions.Upload, content)
	require.NoError(t, err, "large blob upload should succeed")

	// download and verify byte-for-byte
	dlResp, err := client.BatchDownload([]lfs.BatchObject{{OID: oid, Size: size}})
	require.NoError(t, err)
	require.Len(t, dlResp.Objects, 1)
	require.NotNil(t, dlResp.Objects[0].Actions)
	require.NotNil(t, dlResp.Objects[0].Actions.Download)

	downloaded, err := lfs.DownloadObject(dlResp.Objects[0].Actions.Download)
	require.NoError(t, err)
	assert.Equal(t, len(content), len(downloaded), "downloaded size should match")
	assert.True(t, bytes.Equal(content, downloaded), "downloaded content should match byte-for-byte")
}

// --- D. LFS with bad credentials ---

// TestLFS_BadCredentials_ReturnsError verifies that LFS batch API
// returns a clear error when credentials are invalid.
// Failure prevented: bad LFS credentials causing unclear error
// messages that don't help users diagnose auth issues.
func TestLFS_BadCredentials_ReturnsError(t *testing.T) {
	g := getSharedGitea(t)
	repoName := "twin-lfs-badauth"
	g.createPrivateRepo(t, repoName)

	// create client with bad token
	repoURL := "http://localhost:" + giteaHostPort + "/" + g.adminUser + "/" + repoName + ".git"
	badClient := lfs.NewClient(repoURL, "nobody", "invalid-token")

	content := []byte("should not upload\n")
	oid := lfs.ComputeOID(content)
	size := int64(len(content))

	_, err := badClient.BatchUpload([]lfs.BatchObject{{OID: oid, Size: size}})
	require.Error(t, err, "LFS batch with bad credentials should fail")
}

