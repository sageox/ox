package ledger

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pointerShapedObject is an object whose own content is an LFS pointer: the
// pointer to the empty object, which is what an empty file cleaned a second
// time stores as its object — the same object in every repository.
var pointerShapedObject = []byte(lfs.FormatPointer("sha256:"+lfs.ComputeOID(nil), 0))

// Failure prevented: an object whose content is itself a pointer hydrates to
// bytes shaped like one, and verification rejected them by that shape alone —
// as nested_stub, or as malformed_pointer when this reader cannot parse the
// inner pointer — so no attempt could make the ledger ready, however clean the
// transport (ox #1000). The counter-risk is the opposite: a pointer-shaped file
// whose bytes are not the committed object is a stub, and stays one.
func TestReadSyncLFSPointerShapedObjectVerifiesByContent(t *testing.T) {
	// The fingerprint ox #1000 observed in a real ledger.
	require.Equal(t, "9d530f74b243d7e8f27437991152e4a2f4a581b0fb6873c115aec2ee54aa5867", lfs.ComputeOID(pointerShapedObject))
	// A reader refuses to parse a pointer to an object larger than it accepts,
	// so a pointer naming a 2 MiB object is malformed to this one.
	t.Setenv("OX_LFS_MAX_OBJECT_SIZE", strconv.Itoa(1<<20))
	tooLarge := func(content string) string {
		return lfs.FormatPointer("sha256:"+lfs.ComputeOID([]byte(content)), 2<<20)
	}
	for _, tc := range []struct {
		name   string
		object []byte
		// stale is a stub the size of object that is not object, so only a
		// hash tells the two apart; reason is what it reports.
		stale, reason string
	}{
		{"pointer to the empty object", pointerShapedObject,
			lfs.FormatPointer("sha256:"+lfs.ComputeOID([]byte("some other object")), 0), "nested_stub"},
		{"pointer this reader cannot parse", []byte(tooLarge("a large object")),
			tooLarge("some other large object"), "malformed_pointer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const path = "sessions/doubled/raw.jsonl"
			var transfers atomic.Int32
			f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/batch") {
					grantReadLFSBatch(t, w, r)
					return
				}
				transfers.Add(1)
				_, _ = w.Write(tc.object)
			})
			commitReadLFSPointer(t, f, path, tc.object)

			cold := ReadSync(context.Background(), f.opts)
			require.True(t, cold.Ready, "%+v %+v", cold, cold.ErrorDetail)
			require.Equal(t, ReadHydration{State: "complete", Required: 1, Completed: 1}, cold.Hydration)
			actual, err := os.ReadFile(filepath.Join(f.opts.Path, path))
			require.NoError(t, err)
			require.Equal(t, tc.object, actual, "the file holds the committed object, byte for byte")

			// A refresh inspects the published checkout before it fetches, and
			// must not read the hydrated file as local work or as a stub to
			// transfer again.
			warm := ReadSync(context.Background(), f.opts)
			require.True(t, warm.Ready, "%+v %+v", warm, warm.ErrorDetail)
			require.Equal(t, int32(1), transfers.Load(), "a verified object is never transferred again")
			require.True(t, CheckReadiness(context.Background(), f.opts.Path, f.opts.RepoID, f.opts.Endpoint).Ready)

			require.Len(t, tc.stale, len(tc.object))
			require.NoError(t, os.WriteFile(filepath.Join(f.opts.Path, path), []byte(tc.stale), 0600))
			result := ReadSync(context.Background(), f.opts)
			require.False(t, result.Ready)
			require.Equal(t, "missing_hydration", result.ErrorClass)
			require.Equal(t, &ReadFailureDetail{Reason: tc.reason, Path: path}, result.ErrorDetail)
			kept, err := os.ReadFile(filepath.Join(f.opts.Path, path))
			require.NoError(t, err)
			require.Equal(t, tc.stale, string(kept), "a refused refresh leaves the file as it found it")
		})
	}
}

// Failure prevented: a pointer HEAD commits that this reader cannot parse names
// no object it can hydrate, and read as plain content it would be served as
// the file — the pointer's text published as ready ledger data.
func TestReadSyncLFSUnparseableCommittedPointerIsNeverServed(t *testing.T) {
	const path = "sessions/large/raw.jsonl"
	t.Setenv("OX_LFS_MAX_OBJECT_SIZE", strconv.Itoa(1<<20))
	var requests atomic.Int32
	f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	})
	commitReadPointer(t, f, path, lfs.FormatPointer("sha256:"+lfs.ComputeOID([]byte("a large object")), 2<<20))

	result := ReadSync(context.Background(), f.opts)
	require.False(t, result.Ready, "%+v", result)
	require.Equal(t, "missing_hydration", result.ErrorClass)
	require.Equal(t, &ReadFailureDetail{Reason: "malformed_pointer", Path: path}, result.ErrorDetail)
	require.Zero(t, requests.Load(), "an unparseable pointer names no object to request")
}

// Failure prevented: a warm refresh started Git processes for every hydrated
// file — two object lookups in each verification pass and a pointer lookup
// before dehydration — so its duration grew with the ledger past any freshness
// bound a hosted reader can meet (ox #1022).
func TestReadSyncGitProcessesDoNotGrowWithHydratedFiles(t *testing.T) {
	const total = 12
	contents := make([][]byte, total)
	byOID := make(map[string][]byte, total)
	for i := range contents {
		contents[i] = []byte(fmt.Sprintf("hydrated object %02d\n", i))
		byOID[lfs.ComputeOID(contents[i])] = contents[i]
	}
	f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch") {
			grantReadLFSBatch(t, w, r)
			return
		}
		_, _ = w.Write(byOID[filepath.Base(r.URL.Path)])
	})
	// Count every Git process the sync starts by wrapping the fixture's git.
	fixtureGit, err := exec.LookPath("git")
	require.NoError(t, err)
	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "spawns")
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\necho >> '"+log+"'\nexec '"+fixtureGit+"' \"$@\"\n"), 0700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	// warmSpawns hydrates objects [from, to), then counts the Git processes a
	// warm refresh of the fully hydrated checkout starts.
	warmSpawns := func(from, to int) int {
		t.Helper()
		for i := from; i < to; i++ {
			commitReadLFSPointer(t, f, fmt.Sprintf("sessions/many/object-%02d.md", i), contents[i])
		}
		require.True(t, ReadSync(context.Background(), f.opts).Ready)
		require.NoError(t, os.WriteFile(log, nil, 0600))
		warm := ReadSync(context.Background(), f.opts)
		require.True(t, warm.Ready, "%+v", warm)
		require.Equal(t, ReadHydration{State: "complete", Required: to, Completed: to}, warm.Hydration)
		spawns, err := os.ReadFile(log)
		require.NoError(t, err)
		return strings.Count(string(spawns), "\n")
	}
	few := warmSpawns(0, 2)
	require.Equal(t, few, warmSpawns(2, total), "refreshing %d hydrated files starts as many Git processes as refreshing 2", total)
}

// newLocalReadTransport returns a transport for tests that run only local Git.
func newLocalReadTransport(t *testing.T) *gitserver.ReadTransport {
	t.Helper()
	transport, err := gitserver.NewReadTransport("https://sageox.ai", readRepoID, "https://sageox.ai/api/v1/cli/repos/"+readRepoID+"/ledger.git")
	require.NoError(t, err)
	return transport
}

// Failure prevented: a lookup reads the rest of the previous answer as its own
// — after a blob too large to be a pointer, or an object not present locally —
// so a file is verified against another object's bytes.
func TestReadBlobsKeepEachAnswerWithItsLookup(t *testing.T) {
	dir := t.TempDir()
	readTestGit(t, dir, "init", "-q")
	store := func(content string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "object")
		require.NoError(t, os.WriteFile(path, []byte(content), 0600))
		return readTestGit(t, dir, "hash-object", "-w", "--", path)
	}
	pointer := lfs.FormatPointer("sha256:"+lfs.ComputeOID([]byte("content")), 7)
	pointerOID := store(pointer)
	// Larger than a pipe buffer, so an answer left unread would block Git.
	largeOID := store(strings.Repeat("large plain content\n", 5000))
	missingOID := strings.Repeat("0", len(pointerOID))
	blobs, err := startReadBlobs(context.Background(), newLocalReadTransport(t), dir)
	require.NoError(t, err)
	defer blobs.close()

	blob, err := blobs.small(largeOID, 1024)
	require.NoError(t, err)
	require.Nil(t, blob, "a blob too large to be a pointer is not returned")
	blob, err = blobs.small(pointerOID, 1024)
	require.NoError(t, err)
	require.Equal(t, pointer, string(blob))
	_, err = blobs.small(missingOID, 1024)
	require.Error(t, err, "an object not present locally is an error, not an empty blob")
	blob, err = blobs.small(pointerOID, 1024)
	require.NoError(t, err)
	require.Equal(t, pointer, string(blob))
}

// Failure prevented: the lookup process starts outside the transport's policy
// — under a checkout's unsafe config, where Git runs configured commands — or a
// Git that cannot start is reported as a working lookup process.
func TestStartReadBlobsRefusesWhatTheTransportRefuses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  error
	}{
		{"unsafe config", func(t *testing.T, dir string) {
			require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[core]\n\tfsmonitor = evil-command\n"), 0600))
		}, gitserver.ErrUnsafeReadTransport},
		{"git is not installed", func(t *testing.T, _ string) { t.Setenv("PATH", t.TempDir()) }, exec.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			blobs, err := startReadBlobs(context.Background(), newLocalReadTransport(t), dir)
			require.ErrorIs(t, err, tc.want)
			require.Nil(t, blobs)
		})
	}
}

// Failure prevented: Git dying mid-answer hands verification an empty or
// truncated blob as if it were the object, or leaves the lookup waiting.
func TestReadBlobsFailWhenGitDoesNotAnswerInFull(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stands in for cat-file with a POSIX shell script on PATH")
	}
	for _, tc := range []struct{ name, answer string }{
		{"no answer", `read oid`},
		{"size that is not a number", `read oid; printf '%s blob many\n' "$oid"`},
		{"content cut short", `read oid; printf '%s blob 10\nshort' "$oid"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n"+tc.answer+"\n"), 0700))
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			// Without a .git directory the transport inspects no config, so the
			// script stands in for cat-file alone.
			blobs, err := startReadBlobs(context.Background(), newLocalReadTransport(t), t.TempDir())
			require.NoError(t, err)
			defer blobs.close()
			blob, err := blobs.small(strings.Repeat("a", 40), 1024)
			require.Error(t, err)
			require.Nil(t, blob)
		})
	}
}

// Failure prevented: a cold clone that stops short keeps a stage holding a
// pointer-shaped object, and the next attempt, rejecting that file as a nested
// stub, replaces the stage instead of resuming it — transferring every object
// again only to fail verification the same way (ox #1000).
func TestReadSyncColdStageHoldingAPointerShapedObjectResumes(t *testing.T) {
	const doubledPath, refusedPath = "sessions/cold/raw.jsonl", "sessions/cold/session.md"
	refusedContent := []byte("an object the server refuses until it does not\n")
	doubledOID := lfs.ComputeOID(pointerShapedObject)
	contents := map[string][]byte{doubledOID: pointerShapedObject, lfs.ComputeOID(refusedContent): refusedContent}
	var refuse atomic.Bool
	refuse.Store(true)
	var doubledTransfers atomic.Int32
	f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch") {
			grantReadLFSBatch(t, w, r)
			return
		}
		oid := filepath.Base(r.URL.Path)
		content, ok := contents[oid]
		if !assert.True(t, ok, "only a requested object may be downloaded") {
			http.NotFound(w, r)
			return
		}
		if oid == doubledOID {
			doubledTransfers.Add(1)
		} else if refuse.Load() {
			http.Error(w, "object refused", http.StatusNotFound)
			return
		}
		_, _ = w.Write(content)
	})
	commitReadLFSPointer(t, f, doubledPath, pointerShapedObject)
	commitReadLFSPointer(t, f, refusedPath, refusedContent)

	first := ReadSync(context.Background(), f.opts)
	require.Equal(t, "missing_hydration", first.ErrorClass, "%+v", first)
	require.True(t, first.Resumable, "%+v", first)
	held, err := os.ReadFile(filepath.Join(readStagePath(f.opts.Path), doubledPath))
	require.NoError(t, err)
	require.Equal(t, pointerShapedObject, held, "the stage holds the pointer-shaped object")

	refuse.Store(false)
	resumed := ReadSync(context.Background(), f.opts)
	require.True(t, resumed.Ready, "%+v %+v", resumed, resumed.ErrorDetail)
	require.Equal(t, int32(1), doubledTransfers.Load(), "the stage is resumed, not replaced by a fresh clone")
}
