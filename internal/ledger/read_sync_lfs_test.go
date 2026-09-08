package ledger

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(f.source, path)), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(f.source, path), []byte(pointer), 0600))
	readTestGit(t, f.source, "add", "--", path)
	readTestGit(t, f.source, "commit", "-m", "add LFS content")
	readTestGit(t, f.bare, "fetch", f.source, "+refs/heads/main:refs/heads/main")
	return pointer
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
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
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
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			require.Len(t, request.Objects, 1)
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
	staging, err := filepath.Glob(filepath.Join(filepath.Dir(f.opts.Path), ".ox-read-clone-*"))
	require.NoError(t, err)
	require.Empty(t, staging, "only the unpublished failed clone should be removed")
}
