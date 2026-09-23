package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: a cold sync that fails after materializing most of the
// ledger reports coverage.files 0 and hydration "unknown", so its result reads
// exactly like an attempt that did nothing, and nothing in it says the next
// attempt resumes the stage instead of cloning again (ox #983). Every way
// hydration can end short must report what the stage holds, leaving ready,
// error_class, and error_detail as they were.
func TestReadSyncColdFailureReportsTheStageItKept(t *testing.T) {
	names := []string{"a", "b", "c"}
	contents, byOID := map[string][]byte{}, map[string]string{}
	for _, name := range names {
		contents[name] = []byte("cold stage object " + name + "\n")
		byOID[lfs.ComputeOID(contents[name])] = name
	}
	refused := func(code int) *ReadFailureDetail {
		return &ReadFailureDetail{Reason: "download_refused", Path: "sessions/cold/a.md",
			OID: lfs.ComputeOID(contents["a"]), ServerCode: code}
	}
	for _, tc := range []struct {
		name, errorClass string
		// status answers a's download; 0 cancels the attempt while a is in flight.
		status int
		detail *ReadFailureDetail
	}{
		{"object refused", "missing_hydration", http.StatusNotFound, refused(http.StatusNotFound)},
		{"access revoked", "denied", http.StatusUnauthorized, refused(http.StatusUnauthorized)},
		{"budget expired", "interrupted", 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transfers := map[string]*atomic.Int32{}
			for _, name := range names {
				transfers[name] = new(atomic.Int32)
			}
			var holding atomic.Bool
			holding.Store(true)
			release := make(chan struct{})
			f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/batch") {
					grantReadLFSBatch(t, w, r)
					return
				}
				name, ok := byOID[filepath.Base(r.URL.Path)]
				if !assert.True(t, ok, "only a requested object may be downloaded") {
					http.NotFound(w, r)
					return
				}
				transfers[name].Add(1)
				if name == "a" && holding.Load() {
					select {
					case <-release:
						w.WriteHeader(tc.status)
					case <-r.Context().Done():
					}
					return
				}
				_, _ = w.Write(contents[name])
			})
			for _, name := range names {
				commitReadLFSPointer(t, f, "sessions/cold/"+name+".md", contents[name])
			}
			stage := readStagePath(f.opts.Path)
			landed := func(name string) bool {
				actual, err := os.ReadFile(filepath.Join(stage, "sessions/cold", name+".md"))
				return err == nil && bytes.Equal(contents[name], actual)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan ReadSyncResult, 1)
			go func() { finished <- ReadSync(ctx, f.opts) }()
			// a sorts first in its batch, so ending the attempt there — after b
			// and c are on disk — leaves progress only an honest result reports.
			require.Eventually(t, func() bool { return landed("b") && landed("c") }, 30*time.Second, 5*time.Millisecond)
			if tc.status == 0 {
				cancel()
			} else {
				close(release)
			}
			result := <-finished

			require.False(t, result.Ready, "%+v", result)
			require.Equal(t, tc.errorClass, result.ErrorClass, "%+v", result)
			require.Equal(t, tc.detail, result.ErrorDetail)
			require.NoDirExists(t, f.opts.Path, "a failed cold clone is never published")
			require.Equal(t, ReadHydration{State: "missing", Required: 3, Completed: 2}, result.Hydration, "%+v", result)
			require.True(t, result.Resumable, "%+v", result)
			transport, err := gitserver.NewReadTransport(f.opts.Endpoint, f.opts.RepoID, f.opts.ReadURL)
			require.NoError(t, err)
			onDisk := verifyReadCheckout(context.Background(), f.opts, transport, stage, result.Coverage.Paths)
			require.Positive(t, onDisk.Coverage.Files)
			require.Equal(t, onDisk.Coverage, result.Coverage, "coverage counts the stage as it is on disk")
			require.Equal(t, onDisk.Hydration, result.Hydration, "hydration counts the stage as it is on disk")
			data, err := json.Marshal(result)
			require.NoError(t, err)
			require.Contains(t, string(data), `"resumable":true`)

			// The claim is only worth making if it holds: the next attempt
			// continues from the stage instead of transferring b and c again.
			holding.Store(false)
			resumed := ReadSync(context.Background(), f.opts)
			require.True(t, resumed.Ready, "%+v", resumed)
			require.False(t, resumed.Resumable, "publishing consumes the stage")
			require.Equal(t, int32(2), transfers["a"].Load())
			require.Equal(t, int32(1), transfers["b"].Load(), "a landed object is never transferred again")
			require.Equal(t, int32(1), transfers["c"].Load())
		})
	}
}

// Failure prevented: a cold sync lands every object and then fails
// verification, and reports coverage.files 0 and hydration "unknown" — as if
// it had transferred nothing. Verification replaces the result with its own,
// and a verification that stops before it counts has no counts to give. A file
// written into the stage while objects transfer stops it here; a budget that
// expires while verification hashes the stage stops it the same way.
func TestReadSyncColdVerificationFailureKeepsTheHydrationCounts(t *testing.T) {
	content := []byte("an object that lands before verification fails\n")
	var f *readFixture
	f = newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch") {
			grantReadLFSBatch(t, w, r)
			return
		}
		// Hydration's walk of the stage is behind it; verification's is not.
		assert.NoError(t, os.WriteFile(filepath.Join(readStagePath(f.opts.Path), "sessions/stray.md"), []byte("not ox's\n"), 0600))
		_, _ = w.Write(content)
	})
	commitReadLFSPointer(t, f, "sessions/cold/session.md", content)
	stage := readStagePath(f.opts.Path)

	result := ReadSync(context.Background(), f.opts)
	require.False(t, result.Ready, "%+v", result)
	require.Equal(t, "dirty", result.ErrorClass, "%+v", result)
	require.True(t, result.Resumable, "%+v", result)
	require.Equal(t, ReadHydration{State: "complete", Required: 1, Completed: 1}, result.Hydration, "%+v", result)

	// Without the stray file, a walk of the stage verifies, and finds what the
	// result reported.
	require.NoError(t, os.Remove(filepath.Join(stage, "sessions/stray.md")))
	transport, err := gitserver.NewReadTransport(f.opts.Endpoint, f.opts.RepoID, f.opts.ReadURL)
	require.NoError(t, err)
	onDisk := verifyReadCheckout(context.Background(), f.opts, transport, stage, result.Coverage.Paths)
	require.True(t, onDisk.Ready, "%+v", onDisk)
	require.Equal(t, onDisk.Coverage, result.Coverage, "coverage counts the stage as it is on disk")
	require.Equal(t, onDisk.Hydration, result.Hydration, "hydration counts the stage as it is on disk")
}

// Failure prevented: an object is renamed into place and only then does its
// directory sync fail, so the attempt reports it as a stub still to transfer
// while a walk of the stage — verification's, or the next attempt's — counts
// it hydrated. The failure is still reported; the count says what is on disk.
func TestReadSyncColdFailureCountsAnObjectWhoseDirectorySyncFailed(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions must reject reads for this failure injection")
	}
	content := []byte("object in place before its directory sync fails\n")
	var f *readFixture
	var transfers atomic.Int32
	f = newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch") {
			grantReadLFSBatch(t, w, r)
			return
		}
		transfers.Add(1)
		// Hydration's walk has read this directory by now. Without read
		// permission the rename into it still succeeds, and the directory sync
		// after it cannot open it.
		assert.NoError(t, os.Chmod(filepath.Join(readStagePath(f.opts.Path), "sessions/cold"), 0300))
		_, _ = w.Write(content)
	})
	commitReadLFSPointer(t, f, "sessions/cold/session.md", content)
	stage := readStagePath(f.opts.Path)
	dir := filepath.Join(stage, "sessions/cold")
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })

	result := ReadSync(context.Background(), f.opts)
	if probe, err := os.Open(dir); err == nil {
		_ = probe.Close()
		t.Fatal("permission injection did not prevent reading the directory")
	}
	require.False(t, result.Ready, "%+v", result)
	require.NotEmpty(t, result.ErrorClass, "the failed sync is still reported")
	require.NoDirExists(t, f.opts.Path)
	require.True(t, result.Resumable)
	require.Equal(t, ReadHydration{State: "complete", Required: 1, Completed: 1}, result.Hydration, "%+v", result)

	require.NoError(t, os.Chmod(dir, 0700))
	transport, err := gitserver.NewReadTransport(f.opts.Endpoint, f.opts.RepoID, f.opts.ReadURL)
	require.NoError(t, err)
	onDisk := verifyReadCheckout(context.Background(), f.opts, transport, stage, result.Coverage.Paths)
	require.Equal(t, onDisk.Hydration, result.Hydration, "the count is what a walk of the stage finds")
	resumed := ReadSync(context.Background(), f.opts)
	require.True(t, resumed.Ready, "%+v", resumed)
	require.Equal(t, int32(1), transfers.Load(), "the object in place is not transferred again")
}

// Failure prevented: a consumer that builds the code index before its first
// read sync leaves ox's own cache at the checkout path, and every sync after
// that refuses the path in about a second as "interrupted", with nothing in
// the result saying why (ox #1045). A path holding only that cache is cloned
// like an absent one, and the cache survives into the checkout.
func TestReadSyncAdoptsAPathHoldingOnlyItsOwnCache(t *testing.T) {
	f := newReadFixture(t)
	ctx := context.Background()
	index := map[string]string{
		".sageox/cache/codedb/metadata.db":      "sqlite-data",
		".sageox/cache/codedb/bleve/code/store": "bleve-store",
	}
	for _, tc := range []struct {
		name  string
		files map[string]string
		// seed adds what files cannot express, once the path exists.
		seed func(t *testing.T, opts ReadSyncOptions)
	}{
		{name: "code index", files: index},
		{name: "empty directory"},
		{name: "empty .sageox", seed: func(t *testing.T, opts ReadSyncOptions) {
			require.NoError(t, os.Mkdir(filepath.Join(opts.Path, ".sageox"), 0700))
		}},
		{name: "receipt of a checkout no longer there", files: index, seed: func(t *testing.T, opts ReadSyncOptions) {
			// A receipt describes the checkout beside it. Carried in with the
			// cache, it must not stand in for the one this sync publishes.
			stale := readReceipt{ReadSyncResult: newReadResult(opts), ReadURL: opts.ReadURL}
			observed := time.Now().Add(-24 * time.Hour).UTC()
			stale.Ready, stale.Head, stale.LastSuccessfulSync = true, strings.Repeat("0", 40), &observed
			data, err := json.Marshal(stale)
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Join(opts.Path, ".sageox/cache/read-sync"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(opts.Path, readReceiptRelative), data, 0600))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := f.opts
			opts.Path = filepath.Join(t.TempDir(), "checkout")
			require.NoError(t, os.Mkdir(opts.Path, 0700))
			for name, content := range tc.files {
				require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(opts.Path, name)), 0700))
				require.NoError(t, os.WriteFile(filepath.Join(opts.Path, name), []byte(content), 0600))
			}
			if tc.seed != nil {
				tc.seed(t, opts)
			}

			result := ReadSync(ctx, opts)
			require.True(t, result.Ready, "%+v", result)
			require.Empty(t, result.ErrorClass)
			require.NotNil(t, result.LastSuccessfulSync)
			require.False(t, result.Resumable, "publishing consumes the stage")
			require.NoDirExists(t, readStagePath(opts.Path))
			require.FileExists(t, filepath.Join(opts.Path, "data/plans/plan/plan.md"))
			assertReadCache := func(t *testing.T) {
				t.Helper()
				for name, content := range tc.files {
					actual, err := os.ReadFile(filepath.Join(opts.Path, name))
					require.NoError(t, err)
					require.Equal(t, content, string(actual), "the cache survives the sync")
				}
			}
			assertReadCache(t)
			receipt := loadReadReceipt(opts.Path, opts.RepoID, opts.Endpoint)
			require.NotNil(t, receipt)
			require.Equal(t, result.Head, receipt.Head)
			require.Equal(t, result.LastSuccessfulSync, receipt.LastSuccessfulSync)

			// The published checkout verifies with the cache inside it, and a
			// warm refresh keeps the cache where the code index expects it.
			require.True(t, CheckReadiness(ctx, opts.Path, opts.RepoID, opts.Endpoint).Ready)
			warm := ReadSync(ctx, opts)
			require.True(t, warm.Ready, "%+v", warm)
			assertReadCache(t)
		})
	}
}

// Failure prevented: adopting a cache-only path changes what a canceled sync
// reports, or removes the cache before a checkout holding its copy is
// published, so a canceled attempt loses the code index it was carrying.
func TestReadSyncAdoptionCanceledMidHydrationResumes(t *testing.T) {
	content := []byte("an object the canceled attempt never receives\n")
	var holding atomic.Bool
	holding.Store(true)
	requested := make(chan struct{}, 1)
	f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch") {
			grantReadLFSBatch(t, w, r)
			return
		}
		if holding.Load() {
			select {
			case requested <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return
		}
		_, _ = w.Write(content)
	})
	commitReadLFSPointer(t, f, "sessions/cold/session.md", content)
	index := filepath.Join(f.opts.Path, ".sageox/cache/codedb/metadata.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(index), 0700))
	require.NoError(t, os.WriteFile(index, []byte("index built before the first sync"), 0600))
	before := readTestTree(t, f.opts.Path)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan ReadSyncResult, 1)
	go func() { finished <- ReadSync(ctx, f.opts) }()
	select {
	case <-requested:
	case result := <-finished:
		t.Fatalf("the sync ended before hydration requested its object: %+v", result)
	case <-time.After(30 * time.Second):
		t.Fatal("hydration never requested its object")
	}
	cancel()
	result := <-finished
	require.False(t, result.Ready)
	require.Equal(t, "interrupted", result.ErrorClass)
	require.True(t, result.Resumable, "%+v", result)
	require.Equal(t, before, readTestTree(t, f.opts.Path), "a canceled attempt leaves the adopted path as it found it")

	holding.Store(false)
	resumed := ReadSync(context.Background(), f.opts)
	require.True(t, resumed.Ready, "%+v", resumed)
	actual, err := os.ReadFile(index)
	require.NoError(t, err)
	require.Equal(t, "index built before the first sync", string(actual))
	hydrated, err := os.ReadFile(filepath.Join(f.opts.Path, "sessions/cold/session.md"))
	require.NoError(t, err)
	require.Equal(t, content, hydrated)
}

// Failure prevented: adoption is decided before a clone that can run for many
// minutes. Deleting the path at publication on the strength of that decision
// destroys whatever was written there meanwhile.
func TestReadSyncAdoptionRefusesAPathThatGainedContent(t *testing.T) {
	content := []byte("an object that lands while the path gains a file\n")
	var f *readFixture
	f = newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch") {
			grantReadLFSBatch(t, w, r)
			return
		}
		assert.NoError(t, os.WriteFile(filepath.Join(f.opts.Path, "notes.md"), []byte("written mid-sync"), 0600))
		_, _ = w.Write(content)
	})
	commitReadLFSPointer(t, f, "sessions/cold/session.md", content)
	index := filepath.Join(f.opts.Path, ".sageox/cache/codedb/metadata.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(index), 0700))
	require.NoError(t, os.WriteFile(index, []byte("index built before the first sync"), 0600))

	result := ReadSync(context.Background(), f.opts)
	require.False(t, result.Ready, "%+v", result)
	require.Equal(t, "path_occupied", result.ErrorClass)
	require.True(t, result.Resumable, "the verified stage is kept for when the path is cleared")
	notes, err := os.ReadFile(filepath.Join(f.opts.Path, "notes.md"))
	require.NoError(t, err)
	require.Equal(t, "written mid-sync", string(notes))
	actual, err := os.ReadFile(index)
	require.NoError(t, err)
	require.Equal(t, "index built before the first sync", string(actual))
}

// Failure prevented: a cache file ox cannot read passes adoption and then
// fails the copy at publication — after the whole clone, and as
// "interrupted", the class a consumer retries — so every attempt repeats the
// clone and fails the same way.
func TestReadSyncRefusesACacheItCannotRead(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("file permissions must reject reads for this failure injection")
	}
	f := newReadFixture(t)
	index := filepath.Join(f.opts.Path, ".sageox/cache/codedb/metadata.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(index), 0700))
	require.NoError(t, os.WriteFile(index, []byte("index built before the first sync"), 0000))

	result := ReadSync(context.Background(), f.opts)
	require.False(t, result.Ready)
	require.Equal(t, "path_occupied", result.ErrorClass)
	require.NoDirExists(t, readStagePath(f.opts.Path), "refused before cloning")
	info, err := os.Lstat(index)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0), info.Mode().Perm(), "the file is left as it was")
}

// Failure prevented: a cache copy that fails at publication removes the
// adopted path anyway, or leaves no stage to continue from, so the code index
// or the clone is lost.
func TestReadSyncAdoptionCopyFailureKeepsThePathAndTheStage(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions must reject writes for this failure injection")
	}
	content := []byte("an object that lands before the cache copy fails\n")
	var f *readFixture
	f = newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch") {
			grantReadLFSBatch(t, w, r)
			return
		}
		// The stage's receipt already sits in its cache directory. Without write
		// permission there, copying the adopted cache in fails.
		assert.NoError(t, os.Chmod(filepath.Join(readStagePath(f.opts.Path), ".sageox/cache"), 0500))
		_, _ = w.Write(content)
	})
	commitReadLFSPointer(t, f, "sessions/cold/session.md", content)
	stageCache := filepath.Join(readStagePath(f.opts.Path), ".sageox/cache")
	t.Cleanup(func() { _ = os.Chmod(stageCache, 0700) })
	index := filepath.Join(f.opts.Path, ".sageox/cache/codedb/metadata.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(index), 0700))
	require.NoError(t, os.WriteFile(index, []byte("index built before the first sync"), 0600))
	before := readTestTree(t, f.opts.Path)

	result := ReadSync(context.Background(), f.opts)
	require.False(t, result.Ready, "%+v", result)
	require.Equal(t, "interrupted", result.ErrorClass)
	require.True(t, result.Resumable, "%+v", result)
	require.Equal(t, before, readTestTree(t, f.opts.Path), "the adopted path is left as it was")

	require.NoError(t, os.Chmod(stageCache, 0700))
	resumed := ReadSync(context.Background(), f.opts)
	require.True(t, resumed.Ready, "%+v", resumed)
	actual, err := os.ReadFile(index)
	require.NoError(t, err)
	require.Equal(t, "index built before the first sync", string(actual))
}

// Failure prevented: an adopted path ox cannot clear is reported as
// "interrupted", the class a consumer retries, though no retry can clear it.
func TestReadSyncAdoptedPathItCannotClearIsOccupied(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions must reject removal for this failure injection")
	}
	for _, tc := range []struct {
		name string
		// locked is the directory, relative to the checkout path, whose entries
		// cannot be removed. It stays readable, so the cache is adopted and copied.
		locked string
	}{
		{"files in the cache", ".sageox/cache/codedb"},
		{"the path itself", "."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReadFixture(t)
			index := filepath.Join(f.opts.Path, ".sageox/cache/codedb/metadata.db")
			require.NoError(t, os.MkdirAll(filepath.Dir(index), 0700))
			require.NoError(t, os.WriteFile(index, []byte("index built before the first sync"), 0600))
			locked := filepath.Join(f.opts.Path, tc.locked)
			require.NoError(t, os.Chmod(locked, 0500))
			t.Cleanup(func() { _ = os.Chmod(locked, 0700) })

			result := ReadSync(context.Background(), f.opts)
			require.False(t, result.Ready, "%+v", result)
			require.Equal(t, "path_occupied", result.ErrorClass)
			require.True(t, result.Resumable, "the verified stage is kept for when the path is cleared")

			require.NoError(t, os.Chmod(locked, 0700))
			resumed := ReadSync(context.Background(), f.opts)
			require.True(t, resumed.Ready, "%+v", resumed)
			actual, err := os.ReadFile(index)
			require.NoError(t, err)
			require.Equal(t, "index built before the first sync", string(actual), "the stage's copy of the cache is what gets published")
		})
	}
}

// Failure prevented: an adopted path someone removes mid-sync is reported as
// occupied at publication, a condition that sends a consumer to clear a path
// that is already clear.
func TestReadSyncPublishesWhenTheAdoptedPathIsRemovedMidSync(t *testing.T) {
	content := []byte("an object that lands after the adopted path is gone\n")
	var f *readFixture
	f = newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch") {
			grantReadLFSBatch(t, w, r)
			return
		}
		assert.NoError(t, os.RemoveAll(f.opts.Path))
		_, _ = w.Write(content)
	})
	commitReadLFSPointer(t, f, "sessions/cold/session.md", content)
	index := filepath.Join(f.opts.Path, ".sageox/cache/codedb/metadata.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(index), 0700))
	require.NoError(t, os.WriteFile(index, []byte("removed before publication"), 0600))

	result := ReadSync(context.Background(), f.opts)
	require.True(t, result.Ready, "%+v", result)
	require.Empty(t, result.ErrorClass)
	require.NoFileExists(t, index, "nothing was left to carry over")
	hydrated, err := os.ReadFile(filepath.Join(f.opts.Path, "sessions/cold/session.md"))
	require.NoError(t, err)
	require.Equal(t, content, hydrated)
}
