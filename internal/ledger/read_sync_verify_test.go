package ledger

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

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
