package ledger

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newReadLFSFixture(t *testing.T, handler http.HandlerFunc) *readFixture {
	t.Helper()
	f := newReadFixture(t, handler)
	previous := http.DefaultTransport
	transport := previous.(*http.Transport).Clone()
	roots := x509.NewCertPool()
	roots.AddCert(f.server.Certificate())
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	http.DefaultTransport = transport
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		http.DefaultTransport = previous
	})
	return f
}

func commitReadLFSPointer(t *testing.T, f *readFixture, path string, content []byte) string {
	t.Helper()
	pointer := lfs.FormatPointer("sha256:"+lfs.ComputeOID(content), int64(len(content)))
	commitReadPointer(t, f, path, pointer)
	return pointer
}

func commitReadPointer(t *testing.T, f *readFixture, path, pointer string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(f.source, path)), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(f.source, path), []byte(pointer), 0600))
	readTestGit(t, f.source, "add", "--", path)
	readTestGit(t, f.source, "commit", "-m", "add LFS content")
	readTestGit(t, f.bare, "fetch", f.source, "+refs/heads/main:refs/heads/main")
}

// Failure prevented: batching mixes up out-of-order grants, repeats a shared
// object in the request, or reacquires grants for already verified warm files.
func TestReadSyncLFSBatchHydratesUniqueAndSharedObjects(t *testing.T) {
	first, second := []byte("shared session content\n"), []byte("different plan content\n")
	firstOID, secondOID := lfs.ComputeOID(first), lfs.ComputeOID(second)
	expected := []lfs.BatchObject{{OID: firstOID, Size: int64(len(first))}, {OID: secondOID, Size: int64(len(second))}}
	contents := map[string][]byte{firstOID: first, secondOID: second}
	var batches, downloads atomic.Int32
	f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch") {
			batches.Add(1)
			assert.Equal(t, http.MethodPost, r.Method)
			var request struct {
				Operation string            `json:"operation"`
				Objects   []lfs.BatchObject `json:"objects"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			assert.Equal(t, "download", request.Operation)
			assert.ElementsMatch(t, expected, request.Objects)
			response := lfs.BatchResponse{}
			for i := len(request.Objects) - 1; i >= 0; i-- {
				object := request.Objects[i]
				response.Objects = append(response.Objects, lfs.BatchResponseObject{
					OID: object.OID, Size: object.Size, Actions: &lfs.Actions{Download: &lfs.Action{
						Href: "https://" + r.Host + strings.TrimSuffix(r.URL.Path, "/batch") + "/" + object.OID,
					}},
				})
			}
			json.NewEncoder(w).Encode(response)
			return
		}
		downloads.Add(1)
		assert.Equal(t, http.MethodGet, r.Method)
		content, ok := contents[filepath.Base(r.URL.Path)]
		if !assert.True(t, ok, "only a requested object may be downloaded") {
			http.NotFound(w, r)
			return
		}
		w.Write(content)
	})
	files := map[string][]byte{
		"sessions/a/session.md": first, "sessions/b/session.md": first,
		"data/plans/batched/plan.md": second,
	}
	for path, content := range files {
		commitReadLFSPointer(t, f, path, content)
	}
	result := ReadSync(context.Background(), f.opts)
	require.True(t, result.Ready, "%+v", result)
	require.Equal(t, ReadHydration{State: "complete", Required: len(files), Completed: len(files)}, result.Hydration)
	for path, content := range files {
		actual, err := os.ReadFile(filepath.Join(f.opts.Path, path))
		require.NoError(t, err)
		require.Equal(t, content, actual, path)
	}
	require.Equal(t, int32(1), batches.Load(), "all unique objects share one batch grant")
	coldDownloads := downloads.Load()
	require.Positive(t, coldDownloads)
	warm := ReadSync(context.Background(), f.opts)
	require.True(t, warm.Ready, "%+v", warm)
	require.Equal(t, result.Head, warm.Head)
	require.Equal(t, int32(1), batches.Load())
	require.Equal(t, coldDownloads, downloads.Load(), "warm verified files require no object downloads")
}

// Failure prevented: a zero-byte artifact's pointer (size 0) was reported as
// missing_hydration before any request, so one empty session file failed the
// whole ledger read. An empty object needs no grant: its content is implied.
func TestReadSyncLFSEmptyObjectMaterializesWithoutBatch(t *testing.T) {
	content := []byte("session content\n")
	var batches atomic.Int32
	f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch") {
			batches.Add(1)
			var request struct {
				Objects []lfs.BatchObject `json:"objects"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			for _, object := range request.Objects {
				assert.NotZero(t, object.Size, "an empty object must never be requested")
			}
			response := lfs.BatchResponse{}
			for _, object := range request.Objects {
				response.Objects = append(response.Objects, lfs.BatchResponseObject{
					OID: object.OID, Size: object.Size, Actions: &lfs.Actions{Download: &lfs.Action{
						Href: "https://" + r.Host + strings.TrimSuffix(r.URL.Path, "/batch") + "/" + object.OID,
					}},
				})
			}
			json.NewEncoder(w).Encode(response)
			return
		}
		w.Write(content)
	})
	commitReadLFSPointer(t, f, "sessions/a/session.md", content)
	commitReadLFSPointer(t, f, "sessions/a/context-trace.jsonl", []byte{})
	result := ReadSync(context.Background(), f.opts)
	require.True(t, result.Ready, "%+v", result)
	require.Equal(t, "complete", result.Hydration.State)
	empty, err := os.ReadFile(filepath.Join(f.opts.Path, "sessions/a/context-trace.jsonl"))
	require.NoError(t, err)
	require.Empty(t, empty)
	require.Equal(t, int32(1), batches.Load(), "only the non-empty object needs a grant")
	warm := ReadSync(context.Background(), f.opts)
	require.True(t, warm.Ready, "%+v", warm)
	require.Equal(t, int32(1), batches.Load(), "a verified empty file needs nothing on warm sync")
}

// Failure prevented: a size-0 pointer naming any other object is materialized
// as an empty file, silently replacing content the pointer never described.
func TestReadSyncLFSEmptyObjectRequiresEmptyOID(t *testing.T) {
	var batches atomic.Int32
	f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		batches.Add(1)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	})
	commitReadPointer(t, f, "sessions/a/context-trace.jsonl", lfs.FormatPointer("sha256:"+lfs.ComputeOID([]byte("not empty")), 0))
	result := ReadSync(context.Background(), f.opts)
	require.False(t, result.Ready)
	require.Equal(t, "missing_hydration", result.ErrorClass)
	require.Equal(t, &ReadFailureDetail{Reason: "empty_object_oid_mismatch", Path: "sessions/a/context-trace.jsonl",
		OID: lfs.ComputeOID([]byte("not empty")), ExpectedOID: lfs.ComputeOID(nil)}, result.ErrorDetail)
	require.Zero(t, batches.Load(), "an unhydratable pointer must not reach the server")
}

func TestMaterializeEmptyReadObjectMissingDir(t *testing.T) {
	require.Error(t, materializeEmptyReadObject(filepath.Join(t.TempDir(), "missing", "context-trace.jsonl")))
}

// Failure prevented: ledgers with over 100 unique pointers exceed the backend's
// batch limit, or a later failed batch discards previously verified hydration.
func TestReadSyncLFSBoundedBatchesPreserveProgress(t *testing.T) {
	for _, tc := range []struct{ name, errorClass string }{
		{name: "complete"},
		{name: "later batch foreign", errorClass: "missing_hydration"},
		// A batch the server settles on must not stop hydration, only leave its
		// own objects unmaterialized. A transient batch failure is retried
		// instead — TestReadSyncLFSBatchRequestRetriesOnlyTransientFailures.
		{name: "later batch refused", errorClass: "missing_hydration"},
		{name: "later batch denied", errorClass: "denied"},
		{name: "later batch canceled", errorClass: "interrupted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contents := make(map[string][]byte)
			paths := make(map[string]string)
			for i := range 101 {
				content := []byte(fmt.Sprintf("batched object %03d\n", i))
				oid := lfs.ComputeOID(content)
				contents[oid] = content
				paths[fmt.Sprintf("sessions/bounded/object-%03d.md", i)] = oid
			}
			firstOID := paths["sessions/bounded/object-000.md"]
			paths["sessions/bounded/shared.md"] = firstOID
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var batches, downloads atomic.Int32
			f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/batch") {
					downloads.Add(1)
					content, ok := contents[filepath.Base(r.URL.Path)]
					if !assert.True(t, ok) {
						http.NotFound(w, r)
						return
					}
					w.Write(content)
					return
				}
				batch := batches.Add(1)
				body, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				assert.LessOrEqual(t, len(body), 64*1024, "backend request-body limit")
				var request struct {
					Objects []lfs.BatchObject `json:"objects"`
				}
				assert.NoError(t, json.Unmarshal(body, &request))
				if len(request.Objects) > 100 {
					http.Error(w, "batch object limit exceeded", http.StatusRequestEntityTooLarge)
					return
				}
				if batch == 1 {
					assert.Len(t, request.Objects, 100)
				} else {
					assert.Len(t, request.Objects, 1)
					assert.Equal(t, int32(101), downloads.Load(), "the prior batch, including shared files, remains hydrated")
				}
				if batch == 2 {
					switch tc.name {
					case "later batch refused":
						w.WriteHeader(http.StatusUnprocessableEntity)
						return
					case "later batch denied":
						w.WriteHeader(http.StatusForbidden)
						return
					case "later batch canceled":
						cancel()
						<-r.Context().Done()
						return
					case "later batch foreign":
						request.Objects = []lfs.BatchObject{{OID: firstOID, Size: int64(len(contents[firstOID]))}}
					}
				}
				response := lfs.BatchResponse{}
				for i := len(request.Objects) - 1; i >= 0; i-- {
					object := request.Objects[i]
					response.Objects = append(response.Objects, lfs.BatchResponseObject{
						OID: object.OID, Size: object.Size, Actions: &lfs.Actions{Download: &lfs.Action{
							Href: "https://" + r.Host + strings.TrimSuffix(r.URL.Path, "/batch") + "/" + object.OID,
						}},
					})
				}
				json.NewEncoder(w).Encode(response)
			})
			require.True(t, ReadSync(ctx, f.opts).Ready)
			require.NoError(t, os.MkdirAll(filepath.Join(f.source, "sessions/bounded"), 0700))
			for path, oid := range paths {
				pointer := lfs.FormatPointer("sha256:"+oid, int64(len(contents[oid])))
				require.NoError(t, os.WriteFile(filepath.Join(f.source, path), []byte(pointer), 0600))
			}
			readTestGit(t, f.source, "add", "--", "sessions/bounded")
			readTestGit(t, f.source, "commit", "-m", "add more than one LFS batch")
			readTestGit(t, f.bare, "fetch", f.source, "+refs/heads/main:refs/heads/main")
			result := ReadSync(ctx, f.opts)
			require.Equal(t, tc.errorClass, result.ErrorClass, "%+v", result)
			require.Equal(t, int32(2), batches.Load())
			if tc.errorClass != "" {
				require.False(t, result.Ready)
				require.Equal(t, int32(101), downloads.Load())
				receipt := loadReadReceipt(f.opts.Path, f.opts.RepoID, f.opts.Endpoint)
				require.NotNil(t, receipt)
				require.False(t, receipt.Ready)
				for path, oid := range paths {
					actual, err := os.ReadFile(filepath.Join(f.opts.Path, path))
					require.NoError(t, err)
					if path == "sessions/bounded/object-100.md" {
						require.Equal(t, lfs.FormatPointer("sha256:"+oid, int64(len(contents[oid]))), string(actual))
					} else {
						require.Equal(t, contents[oid], actual, path)
					}
				}
				result = ReadSync(context.Background(), f.opts)
				require.Equal(t, int32(3), batches.Load(), "retry requests only the remaining object")
			}
			require.True(t, result.Ready, "%+v", result)
			require.Equal(t, int32(len(paths)), downloads.Load())
			for path, oid := range paths {
				actual, err := os.ReadFile(filepath.Join(f.opts.Path, path))
				require.NoError(t, err)
				require.Equal(t, contents[oid], actual, path)
			}
		})
	}
}

// Failure prevented: a response entry whose identity, size, or action is
// invalid is materialized anyway — or, the other way, it condemns the entries
// beside it, which is how one malformed entry made a whole batch permanently
// unreadable (ox #947). Each entry is judged on its own: the invalid one leaves
// its stub in place and the valid ones in the same response still hydrate.
func TestReadSyncLFSBatchRejectsInvalidResponseObjectsIndividually(t *testing.T) {
	firstPath, secondPath := "sessions/a/session.md", "sessions/b/session.md"
	firstOID, secondOID := lfs.ComputeOID([]byte(firstPath+"\n")), lfs.ComputeOID([]byte(secondPath+"\n"))
	secondSize := int64(len(secondPath + "\n"))
	for _, tc := range []struct {
		name   string
		detail ReadFailureDetail
	}{
		{"missing", ReadFailureDetail{Reason: "batch_response_incomplete"}},
		{"duplicate", ReadFailureDetail{Reason: "batch_object_duplicated", OID: firstOID}},
		{"foreign", ReadFailureDetail{Reason: "batch_object_unrequested", OID: lfs.ComputeOID([]byte("unrequested object"))}},
		// An unrequested OID is arbitrary server text that nothing validates.
		// It is dropped rather than republished; the reason still names the condition.
		{"foreign credential", ReadFailureDetail{Reason: "batch_object_unrequested"}},
		{"wrong size", ReadFailureDetail{Reason: "object_size_mismatch", Path: secondPath, OID: secondOID,
			ExpectedSize: readSize(secondSize), ActualSize: readSize(secondSize + 1)}},
		{"missing action", ReadFailureDetail{Reason: "object_missing_actions", Path: secondPath, OID: secondOID}},
		{"empty actions", ReadFailureDetail{Reason: "object_missing_download_action", Path: secondPath, OID: secondOID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var batches, downloads atomic.Int32
			f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/batch") {
					downloads.Add(1)
					if !assert.Equal(t, firstOID, filepath.Base(r.URL.Path), "only the valid entry may be downloaded") {
						http.NotFound(w, r)
						return
					}
					_, _ = w.Write([]byte(firstPath + "\n"))
					return
				}
				batches.Add(1)
				var request struct {
					Objects []lfs.BatchObject `json:"objects"`
				}
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				if !assert.Len(t, request.Objects, 2) {
					http.Error(w, "expected a single combined batch", http.StatusBadRequest)
					return
				}
				objects := make([]lfs.BatchResponseObject, 0, 2)
				for _, object := range request.Objects {
					objects = append(objects, lfs.BatchResponseObject{
						OID: object.OID, Size: object.Size, Actions: &lfs.Actions{Download: &lfs.Action{
							Href: "https://" + r.Host + strings.TrimSuffix(r.URL.Path, "/batch") + "/" + object.OID,
						}},
					})
				}
				switch tc.name {
				case "missing":
					objects = objects[:1]
				case "duplicate":
					objects[1] = objects[0]
				case "foreign":
					objects[1].OID = lfs.ComputeOID([]byte("unrequested object"))
				case "foreign credential":
					objects[1].OID = "https://ox:" + readTestToken + "@ledger.invalid/repo.git?sig=" + readTestToken
				case "wrong size":
					objects[1].Size++
				case "missing action":
					objects[1].Actions = nil
				case "empty actions":
					objects[1].Actions = &lfs.Actions{}
				}
				json.NewEncoder(w).Encode(lfs.BatchResponse{Objects: objects})
			})
			require.True(t, ReadSync(context.Background(), f.opts).Ready)
			pointers := map[string]string{}
			for _, path := range []string{firstPath, secondPath} {
				pointers[path] = commitReadLFSPointer(t, f, path, []byte(path+"\n"))
			}
			result := ReadSync(context.Background(), f.opts)
			require.False(t, result.Ready)
			require.Equal(t, "missing_hydration", result.ErrorClass)
			require.Equal(t, &tc.detail, result.ErrorDetail)
			rendered, err := json.Marshal(result)
			require.NoError(t, err)
			for _, forbidden := range []string{readTestToken, f.opts.ReadURL, "?sig=", "ledger.invalid"} {
				require.NotContains(t, string(rendered), forbidden, "a malformed grant must not republish server bytes")
			}
			require.Nil(t, result.LastSuccessfulSync)
			require.Equal(t, int32(1), batches.Load())
			require.Equal(t, int32(1), downloads.Load(), "the entry beside the invalid one is still materialized")
			require.Equal(t, ReadHydration{State: "missing", Required: 2, Completed: 1}, result.Hydration)
			first, err := os.ReadFile(filepath.Join(f.opts.Path, firstPath))
			require.NoError(t, err)
			require.Equal(t, firstPath+"\n", string(first))
			second, err := os.ReadFile(filepath.Join(f.opts.Path, secondPath))
			require.NoError(t, err)
			require.Equal(t, pointers[secondPath], string(second), "the object the response got wrong keeps its stub")
			require.False(t, CheckReadiness(context.Background(), f.opts.Path, f.opts.RepoID, f.opts.Endpoint).Ready)
		})
	}
}

// Failure prevented: a response holding MORE objects than the batch reports
// batch_response_incomplete — a reason the contract defines as "fewer objects
// than the batch" — and, being recorded before the per-object loop runs, that
// vaguer reason is the one retained, hiding which entry was actually surplus.
func TestReadSyncLFSSurplusBatchResponseNamesTheSurplusEntry(t *testing.T) {
	firstPath, secondPath := "sessions/a/session.md", "sessions/b/session.md"
	firstOID := lfs.ComputeOID([]byte(firstPath + "\n"))
	contents := map[string][]byte{
		firstOID: []byte(firstPath + "\n"), lfs.ComputeOID([]byte(secondPath + "\n")): []byte(secondPath + "\n"),
	}
	var batches, downloads atomic.Int32
	f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/batch") {
			downloads.Add(1)
			content, ok := contents[filepath.Base(r.URL.Path)]
			if !assert.True(t, ok) {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(content)
			return
		}
		batches.Add(1)
		var request struct {
			Objects []lfs.BatchObject `json:"objects"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		if !assert.Len(t, request.Objects, 2) {
			http.Error(w, "expected a single combined batch", http.StatusBadRequest)
			return
		}
		objects := make([]lfs.BatchResponseObject, 0, 3)
		for _, object := range request.Objects {
			objects = append(objects, lfs.BatchResponseObject{
				OID: object.OID, Size: object.Size, Actions: &lfs.Actions{Download: &lfs.Action{
					Href: "https://" + r.Host + strings.TrimSuffix(r.URL.Path, "/batch") + "/" + object.OID,
				}},
			})
		}
		// One entry too many: the first object repeated.
		objects = append(objects, objects[0])
		assert.NoError(t, json.NewEncoder(w).Encode(lfs.BatchResponse{Objects: objects}))
	})
	require.True(t, ReadSync(context.Background(), f.opts).Ready)
	for _, path := range []string{firstPath, secondPath} {
		commitReadLFSPointer(t, f, path, []byte(path+"\n"))
	}

	result := ReadSync(context.Background(), f.opts)
	require.Equal(t, "missing_hydration", result.ErrorClass)
	require.Equal(t, &ReadFailureDetail{Reason: "batch_object_duplicated", OID: firstOID}, result.ErrorDetail,
		"a surplus entry is named, not counted")
	require.Equal(t, int32(1), batches.Load())
	// Both requested objects were described correctly, so both still materialize.
	require.Equal(t, int32(2), downloads.Load())
	for _, path := range []string{firstPath, secondPath} {
		actual, err := os.ReadFile(filepath.Join(f.opts.Path, path))
		require.NoError(t, err)
		require.Equal(t, path+"\n", string(actual), path)
	}
}

// Failure prevented: a deadline that lands after an object failure was raised
// reports "interrupted" while still naming the object, so error_class and
// error_detail describe two different failures. Verification runs Git
// subprocesses between the two, so the window is an ordinary timeout, not a race.
func TestRecordReadFailureDropsDetailWhenTheOperationWasInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var skips readSkips
	for _, path := range []string{"sessions/a/session.md", "sessions/b/session.md"} {
		require.True(t, skips.skip(ctx, missingHydration(ReadFailureDetail{Reason: "object_refused", Path: path})))
	}
	failure := skips.err()

	var live ReadSyncResult
	recordReadFailure(ctx, &live, failure)
	require.Equal(t, "missing_hydration", live.ErrorClass)
	require.NotNil(t, live.ErrorDetail)
	require.NotNil(t, live.Skipped)

	cancel()
	var interrupted ReadSyncResult
	recordReadFailure(ctx, &interrupted, failure)
	require.Equal(t, "interrupted", interrupted.ErrorClass)
	require.Nil(t, interrupted.ErrorDetail, "the class and the detail must describe one failure")
	require.Nil(t, interrupted.Skipped, "the objects the summary names did not fail the operation either")
}

// Failure prevented: a server-controlled or pointer-supplied identifier is
// republished verbatim in error_detail, turning a diagnostic field into a
// channel for credentials and arbitrary bytes.
func TestSafeReadOIDAcceptsOnlyCanonicalIdentifiers(t *testing.T) {
	canonical := lfs.ComputeOID([]byte("canonical object"))
	require.Equal(t, canonical, safeReadOID(canonical))
	for _, rejected := range []string{
		"",
		"sha256:" + canonical,
		strings.ToUpper(canonical),
		strings.Repeat("z", 64),
		canonical[:63],
		canonical + "0",
		"https://ox:oxt_test_1ljPfr@ledger.invalid/repo.git",
	} {
		require.Empty(t, safeReadOID(rejected), "%q must not reach a result", rejected)
	}
}

// Failure prevented: two files naming one object at different sizes are batched
// under a single size, so at most one of them can ever verify — and the failure
// does not say which pointer disagrees. The file whose pointer the bytes match
// hydrates whichever path order it happens to have: picking the winner by path
// order strands the correct pointer whenever it sorts second, which is the ox
// #947 defect one level down.
func TestReadSyncLFSSharedObjectSizeConflictHydratesTheCorrectPointer(t *testing.T) {
	content := []byte("one object claimed at two sizes\n")
	oid := lfs.ComputeOID(content)
	// The truthful size is the content's; the other pointer is the wrong one
	// whichever path it sits on. "a" sorts before "b" in the committed tree.
	const first, second = "sessions/a/session.md", "sessions/b/session.md"
	for _, tc := range []struct{ name, correct, wrong string }{
		{"correct pointer sorts first", first, second},
		{"correct pointer sorts second", second, first},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var batches, downloads atomic.Int32
			f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/batch") {
					downloads.Add(1)
					_, _ = w.Write(content)
					return
				}
				batches.Add(1)
				var request struct {
					Objects []lfs.BatchObject `json:"objects"`
				}
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				assert.Len(t, request.Objects, 1, "one object, however many pointers name it")
				assert.Equal(t, oid, request.Objects[0].OID)
				response := lfs.BatchResponse{}
				for _, object := range request.Objects {
					// Echo the requested size, the least helpful thing a server may
					// do here: the grant then carries no evidence of which pointer
					// is right, so only the download can settle it.
					response.Objects = append(response.Objects, lfs.BatchResponseObject{
						OID: object.OID, Size: object.Size, Actions: &lfs.Actions{Download: &lfs.Action{
							Href: "https://" + r.Host + strings.TrimSuffix(r.URL.Path, "/batch") + "/" + object.OID,
						}},
					})
				}
				assert.NoError(t, json.NewEncoder(w).Encode(response))
			})
			require.True(t, ReadSync(context.Background(), f.opts).Ready)
			wrongPointer := lfs.FormatPointer("sha256:"+oid, int64(len(content))+1)
			pointers := map[string]string{
				tc.correct: lfs.FormatPointer("sha256:"+oid, int64(len(content))),
				tc.wrong:   wrongPointer,
			}
			for _, path := range []string{first, second} {
				commitReadPointer(t, f, path, pointers[path])
			}

			result := ReadSync(context.Background(), f.opts)
			require.False(t, result.Ready)
			require.Equal(t, "missing_hydration", result.ErrorClass)
			require.Equal(t, "shared_object_size_conflict", result.ErrorDetail.Reason)
			require.Equal(t, oid, result.ErrorDetail.OID)
			// Naming a file here would have to choose before the bytes settle
			// which pointer is wrong, and would name the hydrated file in one of
			// these two orderings — sending an operator to a file that is fine.
			require.Empty(t, result.ErrorDetail.Path, "a conflict between two pointers names neither")
			require.NotNil(t, result.ErrorDetail.ExpectedSize)
			require.NotNil(t, result.ErrorDetail.ActualSize)
			require.Equal(t, ReadHydration{State: "missing", Required: 2, Completed: 1}, result.Hydration)
			require.Equal(t, int32(1), batches.Load())

			hydrated, err := os.ReadFile(filepath.Join(f.opts.Path, tc.correct))
			require.NoError(t, err)
			require.Equal(t, content, hydrated, "the pointer the bytes match hydrates regardless of path order")
			stub, err := os.ReadFile(filepath.Join(f.opts.Path, tc.wrong))
			require.NoError(t, err)
			require.Equal(t, wrongPointer, string(stub), "the pointer the bytes contradict keeps its stub")
		})
	}
}

// Failure prevented: a refused object reports only "missing_hydration", so
// establishing WHICH object the server refused needs server request logs
// correlated against object storage by hand (ox #946). The counter-risk is the
// obvious fix leaking the server's reflected response into the result.
func TestReadSyncLFSRefusedObjectNamesItselfWithoutLeakingTheResponse(t *testing.T) {
	const path = "sessions/refused/session.md"
	content := []byte("content the server refuses to grant\n")
	for _, tc := range []struct {
		name, errorClass string
		code             int
	}{
		{"object not found", "missing_hydration", http.StatusNotFound},
		{"object gone", "missing_hydration", http.StatusGone},
		{"object forbidden", "denied", http.StatusForbidden},
		{"object unauthorized", "denied", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var refuse atomic.Bool
			f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/batch") {
					_, _ = w.Write(content)
					return
				}
				var request struct {
					Objects []lfs.BatchObject `json:"objects"`
				}
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				response := lfs.BatchResponse{}
				for _, object := range request.Objects {
					granted := lfs.BatchResponseObject{OID: object.OID, Size: object.Size}
					if refuse.Load() {
						// A hostile or careless server reflects the caller's own
						// credential and a signed URL back in the message it controls.
						granted.Error = &lfs.ObjectError{Code: tc.code,
							Message: "token " + readTestToken + " rejected at https://" + r.Host + r.URL.Path + "?sig=" + readTestToken}
					} else {
						granted.Actions = &lfs.Actions{Download: &lfs.Action{
							Href: "https://" + r.Host + strings.TrimSuffix(r.URL.Path, "/batch") + "/" + object.OID,
						}}
					}
					response.Objects = append(response.Objects, granted)
				}
				assert.NoError(t, json.NewEncoder(w).Encode(response))
			})
			require.True(t, ReadSync(context.Background(), f.opts).Ready)
			refuse.Store(true)
			pointer := commitReadLFSPointer(t, f, path, content)

			result := ReadSync(context.Background(), f.opts)
			require.False(t, result.Ready)
			require.Equal(t, tc.errorClass, result.ErrorClass, "naming the object must not change its category")
			require.Equal(t, &ReadFailureDetail{Reason: "object_refused", Path: path,
				OID: lfs.ComputeOID(content), ServerCode: tc.code}, result.ErrorDetail)

			rendered, err := json.Marshal(result)
			require.NoError(t, err)
			for _, forbidden := range []string{readTestToken, f.opts.ReadURL, "?sig=", "rejected at", http.StatusText(tc.code)} {
				require.NotContains(t, string(rendered), forbidden, "result must carry no credential, URL, or response body")
			}

			actual, err := os.ReadFile(filepath.Join(f.opts.Path, path))
			require.NoError(t, err)
			require.Equal(t, pointer, string(actual), "a refused object leaves its stub in place")

			refuse.Store(false)
			recovered := ReadSync(context.Background(), f.opts)
			require.True(t, recovered.Ready, "%+v", recovered)
			require.Nil(t, recovered.ErrorDetail, "a recovered sync carries no stale detail")
		})
	}
}

// Failure prevented: one object the server will not serve stops hydration for
// every object after it, so a ledger that accumulated thousands of objects over
// years is permanently unreadable through this path the moment one of them
// becomes unservable (ox #947). The counter-risk is walking past a failure that
// makes the remaining work pointless: a denied grant must still stop on the spot.
func TestReadSyncLFSRefusedObjectLeavesTheRestOfTheLedgerHydrated(t *testing.T) {
	const refusedPath = "sessions/bulk/object-002.md"
	paths := make([]string, 0, 5)
	contents := make(map[string][]byte)
	oids := make(map[string]string)
	for i := range 5 {
		path := fmt.Sprintf("sessions/bulk/object-%03d.md", i)
		content := []byte(fmt.Sprintf("bulk object %03d\n", i))
		paths = append(paths, path)
		oids[path] = lfs.ComputeOID(content)
		contents[oids[path]] = content
	}
	for _, tc := range []struct {
		name, errorClass string
		code, hydrated   int
	}{
		{"object not found", "missing_hydration", http.StatusNotFound, 4},
		{"object gone", "missing_hydration", http.StatusGone, 4},
		{"object forbidden", "denied", http.StatusForbidden, 0},
		{"object unauthorized", "denied", http.StatusUnauthorized, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var refuse atomic.Bool
			var batched, downloads atomic.Int32
			f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/batch") {
					downloads.Add(1)
					content, ok := contents[filepath.Base(r.URL.Path)]
					if !assert.True(t, ok, "only a requested object may be downloaded") {
						http.NotFound(w, r)
						return
					}
					_, _ = w.Write(content)
					return
				}
				var request struct {
					Objects []lfs.BatchObject `json:"objects"`
				}
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				batched.Store(int32(len(request.Objects)))
				response := lfs.BatchResponse{}
				for _, object := range request.Objects {
					granted := lfs.BatchResponseObject{OID: object.OID, Size: object.Size}
					if refuse.Load() && object.OID == oids[refusedPath] {
						granted.Error = &lfs.ObjectError{Code: tc.code, Message: "refused"}
					} else {
						granted.Actions = &lfs.Actions{Download: &lfs.Action{
							Href: "https://" + r.Host + strings.TrimSuffix(r.URL.Path, "/batch") + "/" + object.OID,
						}}
					}
					response.Objects = append(response.Objects, granted)
				}
				assert.NoError(t, json.NewEncoder(w).Encode(response))
			})
			require.True(t, ReadSync(context.Background(), f.opts).Ready)
			refuse.Store(true)
			pointers := make(map[string]string, len(paths))
			for _, path := range paths {
				pointers[path] = commitReadLFSPointer(t, f, path, contents[oids[path]])
			}

			result := ReadSync(context.Background(), f.opts)
			require.False(t, result.Ready, "a listed gap is still not coverage")
			require.Equal(t, tc.errorClass, result.ErrorClass, "%+v", result)
			require.Equal(t, &ReadFailureDetail{Reason: "object_refused", Path: refusedPath,
				OID: oids[refusedPath], ServerCode: tc.code}, result.ErrorDetail)
			require.Equal(t, ReadHydration{State: "missing", Required: len(paths), Completed: tc.hydrated}, result.Hydration)
			require.Equal(t, int32(tc.hydrated), downloads.Load())
			require.Equal(t, int32(len(paths)), batched.Load(), "every object is requested in one batch")
			for _, path := range paths {
				actual, err := os.ReadFile(filepath.Join(f.opts.Path, path))
				require.NoError(t, err)
				if path == refusedPath || tc.hydrated == 0 {
					require.Equal(t, pointers[path], string(actual), path)
				} else {
					require.Equal(t, contents[oids[path]], actual, path)
				}
			}

			refuse.Store(false)
			recovered := ReadSync(context.Background(), f.opts)
			require.True(t, recovered.Ready, "%+v", recovered)
			require.Nil(t, recovered.ErrorDetail)
			require.Equal(t, ReadHydration{State: "complete", Required: len(paths), Completed: len(paths)}, recovered.Hydration)
			require.Equal(t, int32(len(paths)-tc.hydrated), batched.Load(), "a retry requests only the objects still missing")
			require.Equal(t, int32(len(paths)), downloads.Load(), "an object materialized before the refusal is not downloaded twice")
			for _, path := range paths {
				actual, err := os.ReadFile(filepath.Join(f.opts.Path, path))
				require.NoError(t, err)
				require.Equal(t, contents[oids[path]], actual, path)
			}
		})
	}
}

// Failure prevented: hydration walks past a failure that makes every remaining
// object unreachable — a canceled operation, an unusable read credential, a
// denied grant — spending the rest of the budget on requests that cannot
// succeed; or it stops at a per-object failure it could have walked past,
// which is the defect ox #947 is about.
func TestReadSkipsStopOnlyWhereContinuingCannotMaterialize(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	refused := func(code int) error {
		return &readFailure{err: &lfs.HTTPError{StatusCode: code},
			detail: ReadFailureDetail{Reason: "object_refused", ServerCode: code}}
	}
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		err   error
		stops bool
	}{
		{"object not found", context.Background(), refused(http.StatusNotFound), false},
		{"object gone", context.Background(), refused(http.StatusGone), false},
		{"batch request failed", context.Background(), &lfs.HTTPError{StatusCode: http.StatusInternalServerError}, false},
		{"grant invalid", context.Background(), missingHydration(ReadFailureDetail{Reason: "object_size_mismatch"}), false},
		{"local write failed", context.Background(), os.ErrPermission, false},
		{"object forbidden", context.Background(), refused(http.StatusForbidden), true},
		{"object unauthorized", context.Background(), refused(http.StatusUnauthorized), true},
		{"credential unusable", context.Background(), fmt.Errorf("download: %w", auth.ErrReadTokenUnavailable), true},
		{"canceled", canceled, refused(http.StatusNotFound), true},
		// lfs.Client carries its own request deadline, so a timed-out request
		// reports a deadline while the caller's context is still live. Walking
		// past it spends that deadline again on every batch that remains.
		{"client deadline", context.Background(), fmt.Errorf("batch request: %w", context.DeadlineExceeded), true},
		{"client canceled", context.Background(), fmt.Errorf("batch request: %w", context.Canceled), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var skips readSkips
			require.Equal(t, !tc.stops, skips.skip(tc.ctx, tc.err))
			if tc.stops {
				require.NoError(t, skips.err(), "a failure hydration stops at is returned directly, never accumulated")
				require.Zero(t, skips.total, "nor counted among the failures walked past")
				return
			}
			require.Equal(t, tc.err, skips.first)
			require.True(t, skips.skip(tc.ctx, errors.New("a later object")))
			require.Equal(t, tc.err, errors.Unwrap(skips.err()), "the first failure walked past is the one reported")
			require.Equal(t, 2, skips.total)
		})
	}
}

// Failure prevented: readers see partial hydration, or slow object delivery
// makes a revision appear remotely observed later than its actual Git fetch.
func TestReadSyncLFSHydrationPublishesAfterVerification(t *testing.T) {
	content := []byte("verified plan content delivered in two chunks\n")
	oid := lfs.ComputeOID(content)
	started := make(chan time.Time, 1)
	release := make(chan struct{})
	var released atomic.Bool
	unblock := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	defer unblock()
	var batches, downloads atomic.Int32
	f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch") {
			batches.Add(1)
			var request struct {
				Operation string            `json:"operation"`
				Objects   []lfs.BatchObject `json:"objects"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			assert.Equal(t, "download", request.Operation)
			assert.Equal(t, []lfs.BatchObject{{OID: oid, Size: int64(len(content))}}, request.Objects)
			json.NewEncoder(w).Encode(lfs.BatchResponse{Objects: []lfs.BatchResponseObject{{
				OID: oid, Size: int64(len(content)), Actions: &lfs.Actions{Download: &lfs.Action{
					Href: "https://" + r.Host + strings.TrimSuffix(r.URL.Path, "/batch") + "/" + oid,
				}},
			}}})
			return
		}
		downloads.Add(1)
		assert.Equal(t, http.MethodGet, r.Method)
		w.Write(content[:8])
		w.(http.Flusher).Flush()
		started <- time.Now().UTC()
		select {
		case <-release:
			w.Write(content[8:])
		case <-r.Context().Done():
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	first := ReadSync(ctx, f.opts)
	require.True(t, first.Ready, "%+v", first)
	path := "data/plans/hydrated/plan.md"
	pointer := commitReadLFSPointer(t, f, path, content)
	finished := make(chan ReadSyncResult, 1)
	go func() {
		defer close(finished)
		finished <- ReadSync(ctx, f.opts)
	}()
	defer func() {
		unblock()
		cancel()
		for range finished {
		}
	}()
	var objectStarted time.Time
	select {
	case objectStarted = <-started:
	case result := <-finished:
		t.Fatalf("sync completed before object delivery: %+v", result)
	case <-ctx.Done():
		t.Fatal("sync never requested the LFS object")
	}
	// Inspect the durable flag without taking the materializer's held lock.
	invalid := loadReadReceipt(f.opts.Path, f.opts.RepoID, f.opts.Endpoint)
	require.NotNil(t, invalid)
	require.False(t, invalid.Ready)
	require.Equal(t, first.Head, invalid.Head)
	require.Equal(t, first.LastSuccessfulSync, invalid.LastSuccessfulSync)
	actual, err := os.ReadFile(filepath.Join(f.opts.Path, path))
	require.NoError(t, err)
	require.Equal(t, pointer, string(actual), "partial object bytes must stay in staging")
	readerCtx, readerCancel := context.WithTimeout(ctx, 25*time.Millisecond)
	defer readerCancel()
	called := false
	err = WithReadCheckout(readerCtx, f.opts.Path, f.opts.RepoID, f.opts.Endpoint, func(ReadSyncResult) error {
		called = true
		return nil
	})
	require.Error(t, err)
	require.False(t, called, "a reader cannot enter while hydration holds the checkout lock")
	unblock()
	result := <-finished
	require.True(t, result.Ready, "%+v", result)
	require.Empty(t, result.ErrorClass)
	require.NotNil(t, result.LastSuccessfulSync)
	require.True(t, result.LastSuccessfulSync.After(*first.LastSuccessfulSync))
	require.True(t, result.LastSuccessfulSync.Before(objectStarted), "freshness retains the preceding Git observation time")
	require.Equal(t, ReadHydration{State: "complete", Required: 1, Completed: 1}, result.Hydration)
	actual, err = os.ReadFile(filepath.Join(f.opts.Path, path))
	require.NoError(t, err)
	require.Equal(t, content, actual)
	require.Equal(t, int32(1), batches.Load())
	require.Equal(t, int32(1), downloads.Load())
	warm := ReadSync(ctx, f.opts)
	require.True(t, warm.Ready, "%+v", warm)
	require.Equal(t, result.Head, warm.Head)
	require.True(t, warm.LastSuccessfulSync.After(*result.LastSuccessfulSync))
	require.Equal(t, int32(1), batches.Load(), "unchanged verified content needs no new LFS grant")
	require.Equal(t, int32(1), downloads.Load(), "unchanged warm fetch must retain hydrated bytes")
}

// Failure prevented: a newly added missing object causes already hydrated
// sessions to be replaced by stubs before the failed refresh can restore them.
func TestReadSyncLFSFailedRefreshPreservesEarlierHydration(t *testing.T) {
	retained := []byte("previously verified session content\n")
	retainedOID := lfs.ComputeOID(retained)
	var oldDownloads, missingBatches atomic.Int32
	f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch") {
			var request struct {
				Objects []lfs.BatchObject `json:"objects"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			if !assert.Len(t, request.Objects, 1) {
				return
			}
			requested := request.Objects[0]
			object := lfs.BatchResponseObject{OID: requested.OID, Size: requested.Size}
			if requested.OID == retainedOID {
				object.Actions = &lfs.Actions{Download: &lfs.Action{Href: "https://" + r.Host + strings.TrimSuffix(r.URL.Path, "/batch") + "/" + retainedOID}}
			} else {
				missingBatches.Add(1)
				object.Error = &lfs.ObjectError{Code: 404, Message: "new object unavailable"}
			}
			json.NewEncoder(w).Encode(lfs.BatchResponse{Objects: []lfs.BatchResponseObject{object}})
			return
		}
		oldDownloads.Add(1)
		w.Write(retained)
	})
	oldPath := "sessions/z-retained/session.md"
	commitReadLFSPointer(t, f, oldPath, retained)
	ctx := context.Background()
	first := ReadSync(ctx, f.opts)
	require.True(t, first.Ready, "%+v", first)
	require.Equal(t, int32(1), oldDownloads.Load())
	actual, err := os.ReadFile(filepath.Join(f.opts.Path, oldPath))
	require.NoError(t, err)
	require.Equal(t, retained, actual)

	// The new failure sorts before the old hydrated object, so there is no
	// successful re-download to conceal premature dehydration of existing data.
	newPath := "sessions/a-missing/session.md"
	pointer := commitReadLFSPointer(t, f, newPath, []byte("missing new session\n"))
	failed := ReadSync(ctx, f.opts)
	require.False(t, failed.Ready)
	require.Equal(t, "missing_hydration", failed.ErrorClass)
	require.NotEqual(t, first.Head, failed.Head)
	require.Equal(t, int32(1), missingBatches.Load())
	require.Equal(t, int32(1), oldDownloads.Load(), "existing verified content must be retained locally")
	actual, err = os.ReadFile(filepath.Join(f.opts.Path, oldPath))
	require.NoError(t, err)
	require.Equal(t, retained, actual, "failed refresh must not discard an earlier successful hydration")
	actual, err = os.ReadFile(filepath.Join(f.opts.Path, newPath))
	require.NoError(t, err)
	require.Equal(t, pointer, string(actual))
	receipt := loadReadReceipt(f.opts.Path, f.opts.RepoID, f.opts.Endpoint)
	require.NotNil(t, receipt)
	require.False(t, receipt.Ready)
}

// Failure prevented: a missing/denied/corrupt object overwrites a pointer,
// destroys existing content, or certifies an incompletely materialized HEAD.
func TestReadSyncLFSFailuresPreserveContentAndRecoverLocally(t *testing.T) {
	for _, tc := range []struct {
		name, errorClass string
		objectStatus     int
		omitAction       bool
	}{
		{name: "missing object", objectStatus: 404, errorClass: "missing_hydration"},
		{name: "missing action", omitAction: true, errorClass: "missing_hydration"},
		{name: "corrupt content", errorClass: "missing_hydration"},
		{name: "denied object", objectStatus: 403, errorClass: "denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := []byte("expected session data\n")
			oid := lfs.ComputeOID(content)
			f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/batch") {
					object := lfs.BatchResponseObject{OID: oid, Size: int64(len(content))}
					if tc.objectStatus != 0 {
						object.Error = &lfs.ObjectError{Code: tc.objectStatus, Message: "unavailable"}
					} else if !tc.omitAction {
						object.Actions = &lfs.Actions{Download: &lfs.Action{Href: "https://" + r.Host + strings.TrimSuffix(r.URL.Path, "/batch") + "/" + oid}}
					}
					json.NewEncoder(w).Encode(lfs.BatchResponse{Objects: []lfs.BatchResponseObject{object}})
					return
				}
				w.Write([]byte("unverified bytes"))
			})
			ctx := context.Background()
			first := ReadSync(ctx, f.opts)
			require.True(t, first.Ready, "%+v", first)
			path := "sessions/new/session.md"
			pointer := commitReadLFSPointer(t, f, path, content)
			result := ReadSync(ctx, f.opts)
			require.False(t, result.Ready)
			require.Equal(t, tc.errorClass, result.ErrorClass)
			require.NotEqual(t, first.Head, result.Head)
			require.Nil(t, result.LastSuccessfulSync, "incomplete new HEAD has no verified freshness")
			require.Equal(t, "missing", result.Hydration.State)
			actual, err := os.ReadFile(filepath.Join(f.opts.Path, path))
			require.NoError(t, err)
			require.Equal(t, pointer, string(actual))
			for _, original := range []string{"sessions/old/session.md", "data/plans/plan/plan.md"} {
				actual, err := os.ReadFile(filepath.Join(f.opts.Path, original))
				require.NoError(t, err)
				require.Equal(t, original+"\n", string(actual))
			}
			staging, err := filepath.Glob(filepath.Join(f.opts.Path, "sessions/new/.ox-read-object-*"))
			require.NoError(t, err)
			require.Empty(t, staging)
			receipt := loadReadReceipt(f.opts.Path, f.opts.RepoID, f.opts.Endpoint)
			require.NotNil(t, receipt)
			require.False(t, receipt.Ready)
			require.False(t, CheckReadiness(ctx, f.opts.Path, f.opts.RepoID, f.opts.Endpoint).Ready)
			// Recovered bytes establish local readiness, never a new remote timestamp.
			require.NoError(t, os.WriteFile(filepath.Join(f.opts.Path, path), content, 0600))
			t.Setenv("SAGEOX_TOKEN", "")
			recovered := CheckReadiness(ctx, f.opts.Path, f.opts.RepoID, f.opts.Endpoint)
			require.True(t, recovered.Ready, "%+v", recovered)
			require.Nil(t, recovered.LastSuccessfulSync)
		})
	}
}

// Failure prevented: a cold clone is published despite a failed LFS hash check.
func TestReadSyncLFSColdFailureNeverPublishesCheckout(t *testing.T) {
	content := []byte("expected cold clone content\n")
	oid := lfs.ComputeOID(content)
	f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch") {
			json.NewEncoder(w).Encode(lfs.BatchResponse{Objects: []lfs.BatchResponseObject{{OID: oid, Size: int64(len(content)), Actions: &lfs.Actions{Download: &lfs.Action{
				Href: "https://" + r.Host + strings.TrimSuffix(r.URL.Path, "/batch") + "/" + oid,
			}}}}})
			return
		}
		w.Write([]byte("bad object"))
	})
	commitReadLFSPointer(t, f, "sessions/cold/session.md", content)
	result := ReadSync(context.Background(), f.opts)
	require.False(t, result.Ready)
	require.Equal(t, "missing_hydration", result.ErrorClass)
	require.Nil(t, result.LastSuccessfulSync)
	require.NoDirExists(t, f.opts.Path)
	// The stage survives so the next attempt resumes from it, and carries only
	// the invalidated receipt this attempt wrote before hydration.
	receipt := loadReadReceiptAt(readStagePath(f.opts.Path), f.opts.Path, f.opts.RepoID, f.opts.Endpoint)
	require.NotNil(t, receipt)
	require.False(t, receipt.Ready)
	require.Empty(t, receipt.Head)
}

// coldReadStageFixture commits three LFS objects under sessions/cold/ and serves
// them, refusing the last one while refuse is set. It reports how many times each
// object was actually transferred and what the most recent batch asked for.
type coldReadStageFixture struct {
	*readFixture
	contents  map[string][]byte
	paths     map[string]string
	refused   string
	refuse    atomic.Bool
	downloads map[string]*atomic.Int32
	mu        sync.Mutex
	lastBatch []string
}

func newColdReadStageFixture(t *testing.T) *coldReadStageFixture {
	t.Helper()
	c := &coldReadStageFixture{contents: map[string][]byte{}, paths: map[string]string{}, downloads: map[string]*atomic.Int32{}}
	c.refuse.Store(true)
	for _, name := range []string{"a", "b", "c"} {
		content := []byte("cold clone object " + name + "\n")
		oid := lfs.ComputeOID(content)
		c.contents[oid], c.paths["sessions/cold/"+name+".md"], c.downloads[oid] = content, oid, &atomic.Int32{}
	}
	// Refusing "c" leaves "a" and "b" transferred: hydration materializes every
	// object it can and reports the one it could not. The refusal is settled so
	// the attempt spends one request on it rather than a retry budget.
	c.refused = c.paths["sessions/cold/c.md"]
	c.readFixture = newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch") {
			var request struct {
				Objects []lfs.BatchObject `json:"objects"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			response := lfs.BatchResponse{}
			oids := make([]string, 0, len(request.Objects))
			for _, object := range request.Objects {
				oids = append(oids, object.OID)
				response.Objects = append(response.Objects, lfs.BatchResponseObject{OID: object.OID, Size: object.Size, Actions: &lfs.Actions{Download: &lfs.Action{
					Href: "https://" + r.Host + strings.TrimSuffix(r.URL.Path, "/batch") + "/" + object.OID,
				}}})
			}
			c.mu.Lock()
			c.lastBatch = oids
			c.mu.Unlock()
			assert.NoError(t, json.NewEncoder(w).Encode(response))
			return
		}
		oid := filepath.Base(r.URL.Path)
		content, ok := c.contents[oid]
		if !assert.True(t, ok) {
			http.NotFound(w, r)
			return
		}
		c.downloads[oid].Add(1)
		if oid == c.refused && c.refuse.Load() {
			http.Error(w, "object refused", http.StatusNotFound)
			return
		}
		_, _ = w.Write(content)
	})
	require.NoError(t, os.MkdirAll(filepath.Join(c.source, "sessions/cold"), 0700))
	for path, oid := range c.paths {
		pointer := lfs.FormatPointer("sha256:"+oid, int64(len(c.contents[oid])))
		require.NoError(t, os.WriteFile(filepath.Join(c.source, path), []byte(pointer), 0600))
	}
	readTestGit(t, c.source, "add", "--", "sessions/cold")
	readTestGit(t, c.source, "commit", "-m", "add cold clone objects")
	readTestGit(t, c.bare, "fetch", c.source, "+refs/heads/main:refs/heads/main")
	return c
}

func (c *coldReadStageFixture) transferred(name string) int32 {
	return c.downloads[c.paths["sessions/cold/"+name+".md"]].Load()
}

func (c *coldReadStageFixture) requested() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastBatch
}

// Failure prevented: a cold clone that fails late in hydration takes its staging
// directory with it, discarding every object already transferred. A ledger whose
// hydration cannot finish in one attempt then never converges — each retry pays
// the whole download again and still ends with an empty directory.
func TestReadSyncColdHydrationFailureResumesTransferredObjects(t *testing.T) {
	c := newColdReadStageFixture(t)
	stage := readStagePath(c.opts.Path)

	result := ReadSync(context.Background(), c.opts)
	require.False(t, result.Ready)
	require.Equal(t, "missing_hydration", result.ErrorClass)
	require.NoDirExists(t, c.opts.Path, "an interrupted cold clone is never published")
	for _, name := range []string{"a", "b"} {
		actual, err := os.ReadFile(filepath.Join(stage, "sessions/cold/"+name+".md"))
		require.NoError(t, err)
		require.Equal(t, c.contents[c.paths["sessions/cold/"+name+".md"]], actual, name)
	}
	stub, err := os.ReadFile(filepath.Join(stage, "sessions/cold/c.md"))
	require.NoError(t, err)
	require.Equal(t, lfs.FormatPointer("sha256:"+c.refused, int64(len(c.contents[c.refused]))), string(stub))
	receipt := loadReadReceiptAt(stage, c.opts.Path, c.opts.RepoID, c.opts.Endpoint)
	require.NotNil(t, receipt, "the stage records the identity a resume must match")
	require.False(t, receipt.Ready, "an interrupted stage is never reported ready")

	result = ReadSync(context.Background(), c.opts)
	require.Equal(t, "missing_hydration", result.ErrorClass, "%+v", result)
	require.Equal(t, []string{c.refused}, c.requested(), "the retry requests only the object still missing")
	require.Equal(t, int32(1), c.transferred("a"), "a verified object is never transferred again")
	require.Equal(t, int32(1), c.transferred("b"))
	require.Equal(t, int32(2), c.transferred("c"))

	c.refuse.Store(false)
	result = ReadSync(context.Background(), c.opts)
	require.True(t, result.Ready, "%+v", result)
	require.NotNil(t, result.LastSuccessfulSync)
	require.NoDirExists(t, stage, "publishing consumes the stage")
	for path, oid := range c.paths {
		actual, err := os.ReadFile(filepath.Join(c.opts.Path, path))
		require.NoError(t, err)
		require.Equal(t, c.contents[oid], actual, path)
	}
	require.Equal(t, int32(1), c.transferred("a"))
	require.Equal(t, int32(3), c.transferred("c"))
}

// Failure prevented: the stage path is derived from the checkout name, so a
// directory ox never created can already sit there. Deleting it because the
// name matched would destroy content this command does not own.
func TestReadSyncColdStageRefusesForeignDirectory(t *testing.T) {
	f := newReadFixture(t)
	stage := readStagePath(f.opts.Path)
	require.NoError(t, os.MkdirAll(filepath.Join(stage, "notes"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(stage, "notes/keep.txt"), []byte("not ox's\n"), 0600))

	result := ReadSync(context.Background(), f.opts)
	require.False(t, result.Ready)
	require.Equal(t, "dirty", result.ErrorClass)
	require.NoDirExists(t, f.opts.Path, "a refusal publishes nothing")
	kept, err := os.ReadFile(filepath.Join(stage, "notes/keep.txt"))
	require.NoError(t, err)
	require.Equal(t, "not ox's\n", string(kept))

	// A Git checkout of something else is someone's repository, not ox's stage.
	require.NoError(t, os.RemoveAll(stage))
	require.NoError(t, os.MkdirAll(stage, 0700))
	readTestGit(t, stage, "init", "-b", "main")
	readTestGit(t, stage, "remote", "add", "origin", "https://elsewhere.invalid/mine.git")
	require.NoError(t, os.WriteFile(filepath.Join(stage, "work.txt"), []byte("my commit\n"), 0600))
	readTestGit(t, stage, "add", "--", "work.txt")
	readTestGit(t, stage, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "local work")
	head := readTestGit(t, stage, "rev-parse", "HEAD")

	result = ReadSync(context.Background(), f.opts)
	require.False(t, result.Ready)
	require.Equal(t, "dirty", result.ErrorClass)
	require.NoDirExists(t, f.opts.Path)
	require.Equal(t, head, readTestGit(t, stage, "rev-parse", "HEAD"), "the local commit survives")

	// An empty directory holds nothing to lose, so a cold clone claims it.
	require.NoError(t, os.RemoveAll(stage))
	require.NoError(t, os.MkdirAll(stage, 0700))
	require.True(t, ReadSync(context.Background(), f.opts).Ready)
	require.NoDirExists(t, stage, "publishing consumes the stage")
}

// Failure prevented: a budget that expires mid-attempt is read as evidence
// against the stage, so the objects it holds are deleted — the loss this whole
// mechanism exists to prevent. Ownership is what protects it: reading the stage's
// origin needs a Git subprocess, and a canceled context cannot run one, so the
// stage is refused rather than replaced. The checkout lock selects randomly
// between a free lock and a canceled context, so this drives the locked body
// directly to keep the cancellation deterministic.
func TestReadSyncColdStageSurvivesCanceledInspection(t *testing.T) {
	c := newColdReadStageFixture(t)
	stage := readStagePath(c.opts.Path)
	require.Equal(t, "missing_hydration", ReadSync(context.Background(), c.opts).ErrorClass)
	require.DirExists(t, stage)

	transport, err := gitserver.NewReadTransport(c.opts.Endpoint, c.opts.RepoID, c.opts.ReadURL)
	require.NoError(t, err)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	var result ReadSyncResult
	require.NoError(t, gitutil.WithRepoLock(context.Background(), c.opts.Path, func() error {
		result = readSyncLocked(canceled, c.opts, transport)
		return nil
	}))
	require.False(t, result.Ready)
	require.Equal(t, "interrupted", result.ErrorClass)
	require.NoDirExists(t, c.opts.Path)
	kept, err := os.ReadFile(filepath.Join(stage, "sessions/cold/a.md"))
	require.NoError(t, err)
	require.Equal(t, c.contents[c.paths["sessions/cold/a.md"]], kept, "cancellation is not evidence against the stage")
	require.Equal(t, int32(1), c.transferred("a"), "nothing was transferred again")
}

func tamperReadStageReceipt(t *testing.T, stage string, mutate func(*readReceipt)) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stage, readReceiptRelative))
	require.NoError(t, err)
	var receipt readReceipt
	require.NoError(t, json.Unmarshal(data, &receipt))
	mutate(&receipt)
	data, err = json.Marshal(receipt)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(stage, readReceiptRelative), data, 0600))
}

// Failure prevented: a resumed cold clone adopts a stage it cannot prove it
// produced — content left by another repo identity, endpoint, or read URL, or a
// worktree that no longer matches HEAD — and publishes it as this checkout.
func TestReadSyncColdStageResumesOnlyProvenIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		damage func(*testing.T, string)
	}{
		{name: "foreign repo", damage: func(t *testing.T, stage string) {
			tamperReadStageReceipt(t, stage, func(r *readReceipt) { r.RepoID = "repo_01936d5a-0001-7abc-8def-0123456789ab" })
		}},
		{name: "foreign endpoint", damage: func(t *testing.T, stage string) {
			tamperReadStageReceipt(t, stage, func(r *readReceipt) { r.Endpoint = "https://elsewhere.invalid" })
		}},
		{name: "foreign read url", damage: func(t *testing.T, stage string) {
			tamperReadStageReceipt(t, stage, func(r *readReceipt) { r.ReadURL = "https://elsewhere.invalid/ledger.git" })
		}},
		{name: "foreign destination", damage: func(t *testing.T, stage string) {
			tamperReadStageReceipt(t, stage, func(r *readReceipt) { r.Path = filepath.Join(filepath.Dir(r.Path), "elsewhere") })
		}},
		{name: "no receipt", damage: func(t *testing.T, stage string) {
			require.NoError(t, os.Remove(filepath.Join(stage, readReceiptRelative)))
		}},
		{name: "symlinked receipt directory", damage: func(t *testing.T, stage string) {
			dir := filepath.Join(stage, ".sageox/cache/read-sync")
			require.NoError(t, os.RemoveAll(dir))
			require.NoError(t, os.Symlink(t.TempDir(), dir))
		}},
		{name: "damaged worktree", damage: func(t *testing.T, stage string) {
			require.NoError(t, os.WriteFile(filepath.Join(stage, "sessions/cold/a.md"), []byte("not the object\n"), 0600))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newColdReadStageFixture(t)
			stage := readStagePath(c.opts.Path)
			require.Equal(t, "missing_hydration", ReadSync(context.Background(), c.opts).ErrorClass)
			require.Equal(t, int32(1), c.transferred("a"))
			tc.damage(t, stage)

			require.Equal(t, "missing_hydration", ReadSync(context.Background(), c.opts).ErrorClass)
			require.Equal(t, int32(2), c.transferred("a"), "an unproven stage is discarded, not resumed")
			receipt := loadReadReceiptAt(stage, c.opts.Path, c.opts.RepoID, c.opts.Endpoint)
			require.NotNil(t, receipt, "the replacement stage is bound to this identity")
			require.False(t, receipt.Ready)
		})
	}
}

// grantReadLFSBatch answers a download batch request by granting every object
// it names, so a test's handler can spend its attention on the object route.
func grantReadLFSBatch(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	var request struct {
		Objects []lfs.BatchObject `json:"objects"`
	}
	assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
	response := lfs.BatchResponse{}
	for _, object := range request.Objects {
		response.Objects = append(response.Objects, lfs.BatchResponseObject{
			OID: object.OID, Size: object.Size, Actions: &lfs.Actions{Download: &lfs.Action{
				Href: "https://" + r.Host + strings.TrimSuffix(r.URL.Path, "/batch") + "/" + object.OID,
			}},
		})
	}
	assert.NoError(t, json.NewEncoder(w).Encode(response))
}

// Failure prevented: hydration transfers objects strictly one at a time, so a
// cold clone of a real ledger — measured at ~3.2 objects/s against ~15,500
// objects — needs over an hour of wall clock, cannot finish in any budget a
// headless consumer can schedule, and holds the checkout lock against every
// reader for that whole time (ox #948). The counter-risk is an unbounded
// fan-out: one client must not open a request per object at one origin.
func TestReadSyncLFSDownloadsObjectsConcurrentlyWithinTheBound(t *testing.T) {
	paths := make([]string, 0, 2*readHydrationConcurrency)
	contents := make(map[string][]byte)
	oids := make(map[string]string)
	for i := range 2 * readHydrationConcurrency {
		path := fmt.Sprintf("sessions/parallel/object-%03d.md", i)
		content := []byte(fmt.Sprintf("parallel object %03d\n", i))
		paths = append(paths, path)
		oids[path] = lfs.ComputeOID(content)
		contents[oids[path]] = content
	}
	var inFlight, peak atomic.Int32
	f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch") {
			grantReadLFSBatch(t, w, r)
			return
		}
		current := inFlight.Add(1)
		defer inFlight.Add(-1)
		for observed := peak.Load(); current > observed; observed = peak.Load() {
			if peak.CompareAndSwap(observed, current) {
				break
			}
		}
		// Hold the response open long enough that concurrent transfers overlap
		// observably. Serial hydration never sees a second request in flight.
		time.Sleep(20 * time.Millisecond)
		content, ok := contents[filepath.Base(r.URL.Path)]
		if !assert.True(t, ok, "only a requested object may be downloaded") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(content)
	})
	for _, path := range paths {
		commitReadLFSPointer(t, f, path, contents[oids[path]])
	}

	result := ReadSync(context.Background(), f.opts)
	require.True(t, result.Ready, "%+v", result)
	require.Equal(t, ReadHydration{State: "complete", Required: len(paths), Completed: len(paths)}, result.Hydration)
	require.Greater(t, peak.Load(), int32(1), "objects must not transfer one at a time")
	require.LessOrEqual(t, peak.Load(), int32(readHydrationConcurrency), "concurrency must stay bounded at one origin")
	for _, path := range paths {
		actual, err := os.ReadFile(filepath.Join(f.opts.Path, path))
		require.NoError(t, err)
		require.Equal(t, contents[oids[path]], actual, path)
	}
}

// Failure prevented: a single transient failure anywhere in a transfer that
// runs for many minutes fails the whole sync, so one network blip costs every
// object the attempt had already moved (ox #948). The partial-body case also
// guards the retry itself: an attempt that wrote a prefix must be replaced, not
// appended to, or the retry verifies bytes no server ever sent.
func TestReadSyncLFSRetriesTransientDownloadFailure(t *testing.T) {
	const path = "sessions/flaky/session.md"
	content := []byte("content that arrives on the second attempt\n")
	for _, tc := range []struct {
		name string
		fail func(http.ResponseWriter)
	}{
		{"server is briefly unavailable", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}},
		{"connection is lost mid-object", func(w http.ResponseWriter) {
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			_, _ = w.Write(content[:8])
			// Flush before aborting. Go's own transport transparently retries a
			// request whose connection dies before any response arrives, so an
			// unflushed abort would prove nothing about ox's retry.
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/batch") {
					grantReadLFSBatch(t, w, r)
					return
				}
				if attempts.Add(1) == 1 {
					tc.fail(w)
					return
				}
				_, _ = w.Write(content)
			})
			commitReadLFSPointer(t, f, path, content)

			result := ReadSync(context.Background(), f.opts)
			require.True(t, result.Ready, "%+v", result)
			require.Nil(t, result.ErrorDetail)
			require.Equal(t, ReadHydration{State: "complete", Required: 1, Completed: 1}, result.Hydration)
			require.Equal(t, int32(2), attempts.Load(), "one blip costs one retry, not the sync")
			actual, err := os.ReadFile(filepath.Join(f.opts.Path, path))
			require.NoError(t, err)
			require.Equal(t, content, actual, "the retry replaces the failed attempt's bytes")
		})
	}
}

// Failure prevented: retrying an answer the server has settled spends the
// budget the objects still waiting need — three requests each for every object
// a ledger holds that will never be served, and three for a grant that was
// refused once.
func TestReadSyncLFSSettledDownloadFailuresAreNotRetried(t *testing.T) {
	const path = "sessions/settled/session.md"
	content := []byte("content the server never serves\n")
	for _, tc := range []struct {
		name, errorClass, reason string
		serve                    func(http.ResponseWriter)
	}{
		{"unauthorized", "denied", "download_refused", func(w http.ResponseWriter) { w.WriteHeader(http.StatusUnauthorized) }},
		{"forbidden", "denied", "download_refused", func(w http.ResponseWriter) { w.WriteHeader(http.StatusForbidden) }},
		{"object not found", "missing_hydration", "download_refused", func(w http.ResponseWriter) { w.WriteHeader(http.StatusNotFound) }},
		{"content is not the object", "missing_hydration", "download_failed", func(w http.ResponseWriter) {
			_, _ = w.Write([]byte("bytes that are not the object\n"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/batch") {
					grantReadLFSBatch(t, w, r)
					return
				}
				attempts.Add(1)
				tc.serve(w)
			})
			// Publish a checkout first: a cold clone that fails hydration keeps
			// its stage unpublished, and the stub under test would not exist.
			require.True(t, ReadSync(context.Background(), f.opts).Ready)
			pointer := commitReadLFSPointer(t, f, path, content)

			result := ReadSync(context.Background(), f.opts)
			require.False(t, result.Ready, "%+v", result)
			require.Equal(t, tc.errorClass, result.ErrorClass, "%+v", result)
			require.NotNil(t, result.ErrorDetail)
			require.Equal(t, tc.reason, result.ErrorDetail.Reason)
			require.Equal(t, int32(1), attempts.Load(), "a settled answer is asked for once")
			actual, err := os.ReadFile(filepath.Join(f.opts.Path, path))
			require.NoError(t, err)
			require.Equal(t, pointer, string(actual), "an unserved object keeps its stub")
		})
	}
}

// Failure prevented: a batch request that fails transiently costs the grants
// for every object in it — up to 100 — and there is no smaller unit of the
// request to fall back to. A refused batch must still be answered once.
func TestReadSyncLFSBatchRequestRetriesOnlyTransientFailures(t *testing.T) {
	const path = "sessions/batch-retry/session.md"
	content := []byte("content behind one failed batch request\n")
	for _, tc := range []struct {
		name, errorClass string
		code             int
		ready            bool
		batches          int32
	}{
		{"gateway is briefly unavailable", "", http.StatusBadGateway, true, 2},
		{"batch is refused", "denied", http.StatusUnauthorized, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var batches atomic.Int32
			f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/batch") {
					_, _ = w.Write(content)
					return
				}
				if batches.Add(1) == 1 {
					w.WriteHeader(tc.code)
					return
				}
				grantReadLFSBatch(t, w, r)
			})
			require.True(t, ReadSync(context.Background(), f.opts).Ready)
			commitReadLFSPointer(t, f, path, content)

			result := ReadSync(context.Background(), f.opts)
			require.Equal(t, tc.ready, result.Ready, "%+v", result)
			require.Equal(t, tc.errorClass, result.ErrorClass, "%+v", result)
			require.Nil(t, result.ErrorDetail, "a batch failure names no object")
			require.Equal(t, tc.batches, batches.Load())
		})
	}
}

// Failure prevented: a grant the server stops honoring mid-batch keeps the
// other concurrent transfers running behind it, so objects go on arriving and
// committing after access was denied and the batch spends its remaining
// requests on downloads that cannot succeed. Transferring one object at a time
// stopped at the denial; transferring several at once must too.
func TestReadSyncLFSDeniedDownloadStopsTheBatchsOtherTransfers(t *testing.T) {
	const slowPath, deniedPath = "sessions/denial/a-slow.md", "sessions/denial/b-denied.md"
	slow, denied := []byte("content of the transfer already in flight\n"), []byte("content the grant no longer covers\n")
	var served atomic.Int32
	var stopped atomic.Bool
	// Both downloads are dispatched at once, so the denial could otherwise be
	// answered before the slow transfer has begun — and canceling a transfer
	// that never started is not what this test is about. Holding the denial
	// until the slow handler is running makes every run exercise the same thing.
	inFlight := make(chan struct{})
	var begin sync.Once
	f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch") {
			grantReadLFSBatch(t, w, r)
			return
		}
		if filepath.Base(r.URL.Path) == lfs.ComputeOID(denied) {
			select {
			case <-inFlight:
			case <-time.After(10 * time.Second):
				t.Error("the transfer this denial must stop never started")
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		begin.Do(func() { close(inFlight) })
		select {
		case <-r.Context().Done():
			// The denial reached this transfer and stopped it.
			stopped.Store(true)
		case <-time.After(10 * time.Second):
			// Nothing canceled this download, so it delivers its object — which
			// is the behavior under test failing, not the test timing out.
			served.Add(1)
			_, _ = w.Write(slow)
		}
	})
	require.True(t, ReadSync(context.Background(), f.opts).Ready)
	slowPointer := commitReadLFSPointer(t, f, slowPath, slow)
	deniedPointer := commitReadLFSPointer(t, f, deniedPath, denied)

	result := ReadSync(context.Background(), f.opts)
	require.False(t, result.Ready, "%+v", result)
	require.Equal(t, "denied", result.ErrorClass, "%+v", result)
	require.Equal(t, int32(0), served.Load(), "a denial must stop the transfers beside it")
	require.True(t, stopped.Load(), "the transfer already in flight is the one that must be stopped")
	for path, pointer := range map[string]string{slowPath: slowPointer, deniedPath: deniedPointer} {
		actual, err := os.ReadFile(filepath.Join(f.opts.Path, path))
		require.NoError(t, err)
		require.Equal(t, pointer, string(actual), path)
	}
	require.Equal(t, ReadHydration{State: "missing", Required: 2, Completed: 0}, result.Hydration)
}

// Failure prevented: a batch response the server delivered in full but that
// cannot be decoded is asked for twice more, adding two identical requests and
// 600ms of backoff to a sync that already knows the answer. A body cut short by
// the transport fails while it is read, so nothing reaching the decoder is
// transient.
func TestReadSyncLFSUnusableBatchResponseIsNotRetried(t *testing.T) {
	const path = "sessions/malformed/session.md"
	content := []byte("content whose grant never decodes\n")
	var batches atomic.Int32
	f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/batch") {
			_, _ = w.Write(content)
			return
		}
		batches.Add(1)
		_, _ = w.Write([]byte(`{"objects": [`))
	})
	require.True(t, ReadSync(context.Background(), f.opts).Ready)
	pointer := commitReadLFSPointer(t, f, path, content)

	result := ReadSync(context.Background(), f.opts)
	require.False(t, result.Ready, "%+v", result)
	require.Equal(t, int32(1), batches.Load(), "a response the server completed is its settled answer")
	actual, err := os.ReadFile(filepath.Join(f.opts.Path, path))
	require.NoError(t, err)
	require.Equal(t, pointer, string(actual), "an ungranted object keeps its stub")
}
