package lfs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sageox/ox/internal/sacred"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func recoveryLFSServer(t *testing.T, missing map[string]bool, uploadStatus int) (*Client, map[string][]byte) {
	t.Helper()
	var mu sync.Mutex
	uploaded := make(map[string][]byte)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			oid := strings.TrimPrefix(r.URL.Path, "/objects/")
			content, _ := io.ReadAll(r.Body)
			mu.Lock()
			uploaded[oid] = content
			mu.Unlock()
			if uploadStatus != 0 {
				w.WriteHeader(uploadStatus)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}

		var request batchRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode batch request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		response := BatchResponse{Transfer: "basic"}
		for _, object := range request.Objects {
			item := BatchResponseObject{OID: object.OID, Size: object.Size}
			switch request.Operation {
			case "download":
				if missing[object.OID] {
					item.Error = &ObjectError{Code: http.StatusNotFound, Message: "missing"}
				} else {
					item.Actions = &Actions{Download: &Action{Href: server.URL + "/objects/" + object.OID}}
				}
			case "upload":
				if missing[object.OID] {
					item.Actions = &Actions{Upload: &Action{Href: server.URL + "/objects/" + object.OID}}
				}
			}
			response.Objects = append(response.Objects, item)
		}
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode batch response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return NewClient(server.URL, "oauth2", "token"), uploaded
}

func commitMissingSessionPointer(t *testing.T, ledger, session string, content []byte) (pointerPath, metaPath, cachePath string, ref FileRef) {
	t.Helper()
	ref = NewFileRef(content)
	pointerPath = filepath.Join(ledger, "sessions", session, "raw.jsonl")
	metaPath = filepath.Join(ledger, "sessions", session, "meta.json")
	cachePath = filepath.Join(ledger, ".sageox", "cache", "sessions", session, "raw.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(pointerPath), 0o755))
	require.NoError(t, os.WriteFile(pointerPath, []byte(FormatPointer(ref.OID, ref.Size)), 0o644))
	meta := metaFor(metaRef("raw.jsonl", ref.BareOID(), int(ref.Size)))
	require.NoError(t, os.WriteFile(metaPath, []byte(meta), 0o644))
	git(t, ledger, "add", "--sparse", filepath.ToSlash(filepath.Dir(strings.TrimPrefix(pointerPath, ledger+string(filepath.Separator)))))
	git(t, ledger, "commit", "-m", "missing session pointer", "--no-verify")
	return pointerPath, metaPath, cachePath, ref
}

func TestReconcile_ReuploadsExactRecoveryCacheWithoutLocalMutation(t *testing.T) {
	ledger, _ := initLedgerWithRemote(t)
	content := []byte("exact recovery content\n")
	pointerPath, metaPath, cachePath, ref := commitMissingSessionPointer(t, ledger, "exact", content)
	require.NoError(t, os.MkdirAll(filepath.Dir(cachePath), 0o700))
	require.NoError(t, os.WriteFile(cachePath, content, 0o600))
	pointerBefore, _ := os.ReadFile(pointerPath)
	metaBefore, _ := os.ReadFile(metaPath)
	headBefore := git(t, ledger, "rev-parse", "HEAD")
	indexBefore := git(t, ledger, "write-tree")
	statusBefore := git(t, ledger, "status", "--porcelain")
	client, uploaded := recoveryLFSServer(t, map[string]bool{ref.BareOID(): true}, 0)

	result, err := reconcileUnpushedPointers(context.Background(), ledger, nil, func() (*Client, error) { return client, nil })

	require.NoError(t, err)
	assert.Equal(t, 1, result.RecoveredUploads)
	assert.True(t, result.Changed())
	assert.Equal(t, content, uploaded[ref.BareOID()])
	assert.Equal(t, headBefore, git(t, ledger, "rev-parse", "HEAD"))
	assert.Equal(t, indexBefore, git(t, ledger, "write-tree"))
	assert.Equal(t, statusBefore, git(t, ledger, "status", "--porcelain"))
	assert.Equal(t, pointerBefore, mustReadFile(t, pointerPath))
	assert.Equal(t, metaBefore, mustReadFile(t, metaPath))
	assert.Equal(t, content, mustReadFile(t, cachePath))
}

func TestReconcile_RecoveryCacheFailuresAreAtomic(t *testing.T) {
	for _, test := range []struct {
		name         string
		prepareCache func(t *testing.T, path string, content []byte)
		uploadStatus int
		wantError    string
	}{
		{
			name: "mismatch",
			prepareCache: func(t *testing.T, path string, _ []byte) {
				require.NoError(t, os.WriteFile(path, []byte("different"), 0o600))
			},
			wantError: "does not match pointer OID and size",
		},
		{
			name: "symlink",
			prepareCache: func(t *testing.T, path string, content []byte) {
				target := filepath.Join(t.TempDir(), "target")
				require.NoError(t, os.WriteFile(target, content, 0o600))
				require.NoError(t, os.Symlink(target, path))
			},
			wantError: "not a regular non-symlink file",
		},
		{
			name: "upload failure",
			prepareCache: func(t *testing.T, path string, content []byte) {
				require.NoError(t, os.WriteFile(path, content, 0o600))
			},
			uploadStatus: http.StatusInternalServerError,
			wantError:    "upload returned HTTP 500",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger, _ := initLedgerWithRemote(t)
			content := []byte("recoverable bytes\n")
			pointerPath, metaPath, cachePath, ref := commitMissingSessionPointer(t, ledger, "atomic", content)
			require.NoError(t, os.MkdirAll(filepath.Dir(cachePath), 0o700))
			test.prepareCache(t, cachePath, content)
			headBefore := git(t, ledger, "rev-parse", "HEAD")
			indexBefore := git(t, ledger, "write-tree")
			statusBefore := git(t, ledger, "status", "--porcelain")
			pointerBefore := mustReadFile(t, pointerPath)
			metaBefore := mustReadFile(t, metaPath)
			cacheInfoBefore, cacheErr := os.Lstat(cachePath)
			require.NoError(t, cacheErr)
			var cacheBefore string
			if cacheInfoBefore.Mode()&os.ModeSymlink != 0 {
				cacheBefore, cacheErr = os.Readlink(cachePath)
			} else {
				cacheBefore = string(mustReadFile(t, cachePath))
			}
			require.NoError(t, cacheErr)
			client, _ := recoveryLFSServer(t, map[string]bool{ref.BareOID(): true}, test.uploadStatus)

			result, err := reconcileUnpushedPointers(context.Background(), ledger, nil, func() (*Client, error) { return client, nil })

			require.ErrorContains(t, err, test.wantError)
			assert.Zero(t, result.RecoveredUploads)
			assert.False(t, result.Changed())
			assert.Equal(t, headBefore, git(t, ledger, "rev-parse", "HEAD"))
			assert.Equal(t, indexBefore, git(t, ledger, "write-tree"))
			assert.Equal(t, statusBefore, git(t, ledger, "status", "--porcelain"))
			assert.Equal(t, pointerBefore, mustReadFile(t, pointerPath))
			assert.Equal(t, metaBefore, mustReadFile(t, metaPath))
			cacheInfoAfter, cacheErr := os.Lstat(cachePath)
			require.NoError(t, cacheErr)
			assert.Equal(t, cacheInfoBefore.Mode(), cacheInfoAfter.Mode())
			if cacheInfoAfter.Mode()&os.ModeSymlink != 0 {
				target, readErr := os.Readlink(cachePath)
				require.NoError(t, readErr)
				assert.Equal(t, cacheBefore, target)
			} else {
				assert.Equal(t, cacheBefore, string(mustReadFile(t, cachePath)))
			}
		})
	}
}

func TestReconcile_MissingUpstreamDoesNotFallBackToWholeTree(t *testing.T) {
	ledger := initLedgerRepo(t)
	content := []byte("unpublished\n")
	pointerPath, _, _, _ := commitMissingSessionPointer(t, ledger, "no-upstream", content)
	headBefore := git(t, ledger, "rev-parse", "HEAD")
	indexBefore := git(t, ledger, "write-tree")
	clientCalls := 0

	result, err := reconcileUnpushedPointers(context.Background(), ledger, nil, func() (*Client, error) {
		clientCalls++
		return nil, assert.AnError
	})

	require.ErrorContains(t, err, "no upstream tracking ref")
	assert.Zero(t, clientCalls)
	assert.False(t, result.Changed())
	assert.Equal(t, headBefore, git(t, ledger, "rev-parse", "HEAD"))
	assert.Equal(t, indexBefore, git(t, ledger, "write-tree"))
	assert.FileExists(t, pointerPath)
}

func TestReconcile_DivergedBranchDoesNotMutate(t *testing.T) {
	ledger, bare := initLedgerWithRemote(t)
	content := []byte("local content\n")
	pointerPath, _, _, _ := commitMissingSessionPointer(t, ledger, "diverged", content)
	other := t.TempDir()
	require.NoError(t, exec.Command("git", "clone", "--quiet", bare, other).Run())
	git(t, other, "config", "user.email", "other@test.local")
	git(t, other, "config", "user.name", "Other")
	writeAndCommit(t, other, "remote change", map[string]string{"remote.txt": "remote"})
	git(t, other, "push", "--quiet")
	git(t, ledger, "fetch", "--quiet")
	headBefore := git(t, ledger, "rev-parse", "HEAD")
	indexBefore := git(t, ledger, "write-tree")

	result, err := reconcileUnpushedPointers(context.Background(), ledger, nil, func() (*Client, error) {
		return nil, assert.AnError
	})

	require.ErrorContains(t, err, "branch diverged")
	assert.False(t, result.Changed())
	assert.Equal(t, headBefore, git(t, ledger, "rev-parse", "HEAD"))
	assert.Equal(t, indexBefore, git(t, ledger, "write-tree"))
	assert.FileExists(t, pointerPath)
}

func TestReconcile_ManyUnrecoverablePointersDoNotMutate(t *testing.T) {
	ledger, _ := initLedgerWithRemote(t)
	missing := make(map[string]bool)
	files := make(map[string]string)
	for i := 0; i <= sacred.MassDeleteThreshold; i++ {
		oid := strings.Repeat(string(rune('a'+i)), 64)
		missing[oid] = true
		files[filepath.ToSlash(filepath.Join("data", "plans", "p"+string(rune('a'+i)), "plan.html"))] = lfsPointerContent(oid, 10)
	}
	writeAndCommit(t, ledger, "many missing plans", files)
	headBefore := git(t, ledger, "rev-parse", "HEAD")
	indexBefore := git(t, ledger, "write-tree")
	statusBefore := git(t, ledger, "status", "--porcelain")
	client, _ := recoveryLFSServer(t, missing, 0)

	result, err := reconcileUnpushedPointers(context.Background(), ledger, nil, func() (*Client, error) { return client, nil })

	require.ErrorContains(t, err, "will not be removed")
	assert.False(t, result.Changed())
	assert.Equal(t, headBefore, git(t, ledger, "rev-parse", "HEAD"))
	assert.Equal(t, indexBefore, git(t, ledger, "write-tree"))
	assert.Equal(t, statusBefore, git(t, ledger, "status", "--porcelain"))
	for path := range files {
		assert.FileExists(t, filepath.Join(ledger, path))
	}
}

func TestReconcile_MixedReuploadAndUnrecoverableKeepsPushPaused(t *testing.T) {
	ledger, _ := initLedgerWithRemote(t)
	content := []byte("keep the recording\n")
	pointerPath, metaPath, cachePath, sessionRef := commitMissingSessionPointer(t, ledger, "mixed", content)
	require.NoError(t, os.MkdirAll(filepath.Dir(cachePath), 0o700))
	require.NoError(t, os.WriteFile(cachePath, content, 0o600))
	planOID := strings.Repeat("f", 64)
	planPath := filepath.Join(ledger, "data", "plans", "mixed", "plan.html")
	writeAndCommit(t, ledger, "missing plan", map[string]string{filepath.ToSlash(strings.TrimPrefix(planPath, ledger+string(filepath.Separator))): lfsPointerContent(planOID, 10)})
	pointerBefore := mustReadFile(t, pointerPath)
	metaBefore := mustReadFile(t, metaPath)
	client, uploaded := recoveryLFSServer(t, map[string]bool{sessionRef.BareOID(): true, planOID: true}, 0)

	result, err := reconcileUnpushedPointers(context.Background(), ledger, nil, func() (*Client, error) { return client, nil })

	var unrecoverable *UnrecoverablePointersError
	require.ErrorAs(t, err, &unrecoverable)
	assert.Equal(t, 1, unrecoverable.Uploaded)
	assert.Equal(t, 1, result.RecoveredUploads)
	assert.True(t, result.Changed())
	assert.Equal(t, content, uploaded[sessionRef.BareOID()])
	assert.Equal(t, pointerBefore, mustReadFile(t, pointerPath))
	assert.Equal(t, metaBefore, mustReadFile(t, metaPath))
	assert.Equal(t, content, mustReadFile(t, cachePath))
	assert.FileExists(t, planPath, "the unrecoverable plan pointer is never removed")
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	return content
}

// A plan whose LFS object vanished from the store is restored from the Ledger
// recovery cache exactly like a session artifact: the cache bytes must match
// the pointer's OID and size, and nothing local is mutated. Failure prevented:
// the cache was consulted only for sessions/, so a plan with its exact bytes
// sitting in .sageox/cache was still reported as "no blob locally" and kept
// every push rejected.
func TestReconcile_ReuploadsExactRecoveryCacheForPlan(t *testing.T) {
	ledger, _ := initLedgerWithRemote(t)
	content := []byte("<!doctype html><html><head><meta name=\"sageox:plan\" content=\"p\"></head><body>plan</body></html>\n")
	ref := NewFileRef(content)
	pointerPath := filepath.Join(ledger, "data", "plans", "2026-10-01-keys", "plan.html")
	cachePath := filepath.Join(ledger, ".sageox", "cache", "data", "plans", "2026-10-01-keys", "plan.html")
	require.NoError(t, os.MkdirAll(filepath.Dir(pointerPath), 0o755))
	require.NoError(t, os.WriteFile(pointerPath, []byte(FormatPointer(ref.OID, ref.Size)), 0o644))
	git(t, ledger, "add", "--sparse", "data/plans/2026-10-01-keys")
	git(t, ledger, "commit", "-m", "plan: keys", "--no-verify")
	require.NoError(t, os.MkdirAll(filepath.Dir(cachePath), 0o700))
	require.NoError(t, os.WriteFile(cachePath, content, 0o600))
	headBefore := git(t, ledger, "rev-parse", "HEAD")
	client, uploaded := recoveryLFSServer(t, map[string]bool{ref.BareOID(): true}, 0)

	result, err := reconcileUnpushedPointers(context.Background(), ledger, nil, func() (*Client, error) { return client, nil })

	require.NoError(t, err)
	assert.Equal(t, 1, result.RecoveredUploads)
	assert.Equal(t, content, uploaded[ref.BareOID()])
	assert.Equal(t, headBefore, git(t, ledger, "rev-parse", "HEAD"))
	assert.Equal(t, []byte(FormatPointer(ref.OID, ref.Size)), mustReadFile(t, pointerPath), "the pointer is untouched")
}

// A session's recovery cache that cannot be inspected fails closed: finalize
// promised those bytes are there, so an unreadable cache is an error, never
// permission to call the pointer unrecoverable.
func TestReconcile_SessionCacheUnreadableFailsClosed(t *testing.T) {
	ledger, _ := initLedgerWithRemote(t)
	content := []byte("recording whose cache is obstructed\n")
	_, _, _, ref := commitMissingSessionPointer(t, ledger, "obstructed", content)
	require.NoError(t, os.MkdirAll(filepath.Join(ledger, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ledger, ".sageox", "cache"), []byte("not a directory"), 0o644))
	client, uploaded := recoveryLFSServer(t, map[string]bool{ref.BareOID(): true}, 0)

	_, err := reconcileUnpushedPointers(context.Background(), ledger, nil, func() (*Client, error) { return client, nil })

	require.ErrorContains(t, err, "inspect session recovery cache for sessions/obstructed/raw.jsonl")
	assert.Empty(t, uploaded, "nothing is uploaded or removed on an unreadable cache")
}
