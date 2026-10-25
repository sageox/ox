package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	myRawName  = "2026-10-06T18-00-ryan-OxMMMM"
	theirsName = "2026-10-06T19-00-tess-OxSSSS"
)

var (
	myNeverUploaded     = "{\"role\":\"user\",\"who\":\"me, never uploaded\"}\n"
	theirNeverUploaded  = "{\"role\":\"user\",\"who\":\"teammate, never uploaded\"}\n"
	myNeverUploadedPath = "sessions/" + myRawName + "/raw.jsonl"
	theirsPath          = "sessions/" + theirsName + "/raw.jsonl"
)

// lfsStub is a minimal LFS server that records the blobs PUT to it.
type lfsStub struct {
	mu      sync.Mutex
	uploads map[string][]byte // bare OID -> bytes
	url     string
}

func newLFSStub(t *testing.T) *lfsStub {
	t.Helper()
	stub := &lfsStub{uploads: map[string][]byte{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/info/lfs/objects/batch"):
			var request struct {
				Objects []lfs.BatchObject `json:"objects"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			response := lfs.BatchResponse{Transfer: "basic"}
			for _, obj := range request.Objects {
				response.Objects = append(response.Objects, lfs.BatchResponseObject{OID: obj.OID, Size: obj.Size,
					Actions: &lfs.Actions{Upload: &lfs.Action{Href: stub.url + "/upload/" + obj.OID}, Verify: &lfs.Action{Href: stub.url + "/verify"}}})
			}
			w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
			_ = json.NewEncoder(w).Encode(response)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/upload/"):
			body, _ := io.ReadAll(r.Body)
			stub.mu.Lock()
			stub.uploads[strings.TrimPrefix(r.URL.Path, "/upload/")] = body
			stub.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/verify":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	stub.url = server.URL
	return stub
}

func (s *lfsStub) uploaded(content string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.uploads[lfs.ComputeOID([]byte(content))]
	return ok && string(data) == content
}

func (s *lfsStub) uploader(username string) *ownArtifactUploader {
	return &ownArtifactUploader{
		username: username,
		client: func() (*lfs.Client, error) {
			return lfs.NewClient(s.url+"/ledger.git", "tester", "token"), nil
		},
	}
}

// addNeverUploadedSessions commits two sessions whose artifacts were never uploaded: one authored by
// "ryan" and one by "tess". Neither meta.json records an OID, so nothing can vouch for the bytes.
func addNeverUploadedSessions(t *testing.T, ledger string) {
	t.Helper()
	for _, s := range []struct{ name, user, content string }{
		{myRawName, "ryan", myNeverUploaded},
		{theirsName, "tess", theirNeverUploaded},
	} {
		dir := filepath.Join(ledger, "sessions", s.name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(s.content), 0o644))
		require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
			Version: "1.0", SessionName: s.name, Username: s.user, AgentID: "OxAgent", AgentType: "claude-code",
			CreatedAt: time.Date(2026, 10, 6, 18, 0, 0, 0, time.UTC), Title: s.name,
		}))
	}
	mustRunGit(t, ledger, "add", "-A")
	mustRunGit(t, ledger, "commit", "-m", "never uploaded sessions")
}

// TestRestoreUnpushedSessionPointers_UploadsOwnNeverUploadedArtifacts covers the artifacts no OID vouches
// for because their upload never happened. The bytes are the truth, so the repair uploads them, writes the
// pointer and records the OID in meta.json. A teammate's equivalent artifact must stay untouched: uploading
// someone else's recording is not this coworker's call.
func TestRestoreUnpushedSessionPointers_UploadsOwnNeverUploadedArtifacts(t *testing.T) {
	ctx := context.Background()
	ledger := newWedgedLedger(t, false)
	addNeverUploadedSessions(t, ledger)
	stub := newLFSStub(t)

	report, err := restoreUnpushedSessionPointers(ctx, ledger, true, stub.uploader("Ryan"))

	require.NoError(t, err)
	assert.True(t, stub.uploaded(myNeverUploaded), "the own artifact's bytes must reach the LFS store")
	assert.False(t, stub.uploaded(theirNeverUploaded), "a teammate's artifact is never uploaded")
	assert.Contains(t, report.Restored, myNeverUploadedPath)
	require.Len(t, report.Unrepairable, 1)
	assert.Equal(t, theirsPath, report.Unrepairable[0].Path)

	ref := lfs.NewFileRef([]byte(myNeverUploaded))
	assert.Equal(t, lfs.FormatPointer(ref.OID, ref.Size), ledgerFile(t, ledger, "HEAD:"+myNeverUploadedPath))
	assert.Equal(t, theirNeverUploaded, ledgerFile(t, ledger, "HEAD:"+theirsPath), "the teammate's artifact stays as committed")

	meta, err := lfs.ReadSessionMeta(filepath.Join(ledger, "sessions", myRawName))
	require.NoError(t, err)
	assert.Equal(t, ref.OID, meta.Files["raw.jsonl"].OID)
	assert.Equal(t, ref.Size, meta.Files["raw.jsonl"].Size)
	committedMeta, err := runIsolatedGit(t, ledger, "show", "HEAD:sessions/"+myRawName+"/meta.json")
	require.NoError(t, err)
	assert.Contains(t, committedMeta, ref.OID, "the meta.json update is part of the repair commit")
	assert.Empty(t, mustGitStatus(t, ledger, "sessions/"+myRawName), "nothing is left uncommitted for the own session")
}

func mustGitStatus(t *testing.T, ledger, path string) string {
	t.Helper()
	out, err := runIsolatedGit(t, ledger, "status", "--porcelain", "--", path)
	require.NoError(t, err, out)
	return out
}

// TestRestoreUnpushedSessionPointers_NoUploaderNeverUploads covers a doctor run with no way to upload
// (not logged in): own artifacts are reported like any other unrepairable file and nothing is written.
func TestRestoreUnpushedSessionPointers_NoUploaderNeverUploads(t *testing.T) {
	ledger := newWedgedLedger(t, false)
	addNeverUploadedSessions(t, ledger)

	report, err := restoreUnpushedSessionPointers(context.Background(), ledger, true, nil)

	require.NoError(t, err)
	var paths []string
	for _, failure := range report.Unrepairable {
		paths = append(paths, failure.Path)
	}
	assert.ElementsMatch(t, []string{myNeverUploadedPath, theirsPath}, paths)
	assert.Equal(t, myNeverUploaded, ledgerFile(t, ledger, "HEAD:"+myNeverUploadedPath))
}
