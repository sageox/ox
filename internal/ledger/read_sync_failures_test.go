package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/require"
)

// Failure prevented: malformed destinations overwrite unrelated local data or
// become a published checkout after validation fails.
func TestReadSyncRejectsUnownedDestinations(t *testing.T) {
	f := newReadFixture(t)
	for _, tc := range []struct {
		name, errorClass string
		prepare          func(*testing.T, *ReadSyncOptions)
	}{
		{"relative path", "invalid_arguments", func(t *testing.T, opts *ReadSyncOptions) { opts.Path = "relative" }},
		{"wrong authority", "invalid_arguments", func(t *testing.T, opts *ReadSyncOptions) { opts.ReadURL = "https://example.invalid/ledger.git" }},
		{"regular file", "interrupted", func(t *testing.T, opts *ReadSyncOptions) {
			require.NoError(t, os.WriteFile(opts.Path, []byte("owned by someone else"), 0600))
		}},
		{"empty directory", "interrupted", func(t *testing.T, opts *ReadSyncOptions) { require.NoError(t, os.Mkdir(opts.Path, 0700)) }},
		{"parent is a file", "interrupted", func(t *testing.T, opts *ReadSyncOptions) {
			require.NoError(t, os.WriteFile(opts.Path, []byte("parent"), 0600))
			opts.Path = filepath.Join(opts.Path, "checkout")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := f.opts
			opts.Path = filepath.Join(t.TempDir(), "checkout")
			tc.prepare(t, &opts)
			result := ReadSync(context.Background(), opts)
			require.False(t, result.Ready)
			require.Equal(t, tc.errorClass, result.ErrorClass)
			require.Nil(t, result.LastSuccessfulSync)
		})
	}
}

// Failure prevented: cancellation while another creator holds the lock poisons
// the destination or prevents a later request from completing.
func TestReadSyncCanceledCreatorCanRetry(t *testing.T) {
	f := newReadFixture(t)
	ctx := context.Background()
	require.NoError(t, gitutil.WithPreCloneLock(ctx, f.opts.Path, func() error {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		result := ReadSync(canceled, f.opts)
		require.False(t, result.Ready)
		require.Equal(t, "interrupted", result.ErrorClass)
		require.NoDirExists(t, f.opts.Path)
		return nil
	}))
	result := ReadSync(ctx, f.opts)
	require.True(t, result.Ready, "%+v", result)
}

// Failure prevented: a damaged receipt or redirected authority grants cached
// content access, or a forged future timestamp makes stale data look fresh.
func TestReadSyncReceiptRecoveryRequiresIdentityAndValidTime(t *testing.T) {
	f := newReadFixture(t)
	ctx := context.Background()
	first := ReadSync(ctx, f.opts)
	require.True(t, first.Ready)
	original := loadReadReceipt(f.opts.Path, f.opts.RepoID, f.opts.Endpoint)
	require.NotNil(t, original)
	for _, tc := range []struct {
		name, errorClass string
		change           func(*readReceipt)
		invalidJSON      bool
	}{
		{name: "corrupt json", errorClass: "interrupted", invalidJSON: true},
		{name: "unsupported schema", errorClass: "interrupted", change: func(r *readReceipt) { r.SchemaVersion++ }},
		{name: "moved checkout", errorClass: "interrupted", change: func(r *readReceipt) { r.Path += "-other" }},
		{name: "different repo", errorClass: "interrupted", change: func(r *readReceipt) { r.RepoID += "-other" }},
		{name: "different endpoint", errorClass: "interrupted", change: func(r *readReceipt) { r.Endpoint += "/other" }},
		{name: "untrusted authority", errorClass: "identity_mismatch", change: func(r *readReceipt) { r.ReadURL = "https://example.invalid/ledger.git" }},
		{name: "future freshness", change: func(r *readReceipt) { future := time.Now().Add(time.Hour); r.LastSuccessfulSync = &future }},
		{name: "zero freshness", change: func(r *readReceipt) { r.LastSuccessfulSync = new(time.Time) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receipt := *original
			if tc.change != nil {
				tc.change(&receipt)
			}
			data, err := json.Marshal(receipt)
			require.NoError(t, err)
			if tc.invalidJSON {
				data = []byte("{interrupted receipt")
			}
			require.NoError(t, os.WriteFile(filepath.Join(f.opts.Path, readReceiptRelative), data, 0600))
			result := CheckReadiness(ctx, f.opts.Path, f.opts.RepoID, f.opts.Endpoint)
			require.Equal(t, tc.errorClass, result.ErrorClass)
			require.Equal(t, tc.errorClass == "", result.Ready)
			require.Nil(t, result.LastSuccessfulSync)
			if tc.errorClass != "" {
				called := false
				err := WithReadCheckout(ctx, f.opts.Path, f.opts.RepoID, f.opts.Endpoint, func(ReadSyncResult) error { called = true; return nil })
				require.EqualError(t, err, tc.errorClass)
				require.False(t, called)
			}
		})
	}
	result := ReadSync(ctx, f.opts)
	require.True(t, result.Ready, "%+v", result)
	require.NotNil(t, result.LastSuccessfulSync)
}

// Failure prevented: cached readiness survives missing history or a narrower
// sparse checkout, leaving consumers unable to read promised plans/sessions.
func TestReadSyncIncompleteCheckoutMustRecoverBeforeReady(t *testing.T) {
	for _, tc := range []struct {
		name, errorClass string
		breakCheckout    func(*testing.T, *readFixture)
	}{
		{"shallow history", "incomplete_history", func(t *testing.T, f *readFixture) {
			head := readTestGit(t, f.opts.Path, "rev-parse", "HEAD")
			require.NoError(t, os.WriteFile(filepath.Join(f.opts.Path, ".git/shallow"), []byte(head+"\n"), 0600))
		}},
		{"narrow sparse cone", "incomplete_coverage", func(t *testing.T, f *readFixture) {
			readTestGit(t, f.opts.Path, "sparse-checkout", "set", "--cone", "sessions")
		}},
		{"disabled sparse checkout", "incomplete_coverage", func(t *testing.T, f *readFixture) {
			transport, err := gitserver.NewReadTransport(f.opts.Endpoint, f.opts.RepoID, f.opts.ReadURL)
			require.NoError(t, err)
			_, err = runReadGit(context.Background(), transport, true, f.opts.Path, "sparse-checkout", "disable")
			require.NoError(t, err)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReadFixture(t)
			readTestGit(t, f.source, "commit", "--allow-empty", "-m", "second")
			readTestGit(t, f.source, "push", f.bare, "main")
			ctx := context.Background()
			first := ReadSync(ctx, f.opts)
			require.True(t, first.Ready)
			tc.breakCheckout(t, f)
			require.NotNil(t, loadReadReceipt(f.opts.Path, f.opts.RepoID, f.opts.Endpoint), "damaged checkout must retain its identity receipt")
			broken := CheckReadiness(ctx, f.opts.Path, f.opts.RepoID, f.opts.Endpoint)
			require.False(t, broken.Ready)
			require.Equal(t, tc.errorClass, broken.ErrorClass)
			result := ReadSync(ctx, f.opts)
			require.True(t, result.Ready, "%+v", result)
			require.Equal(t, "full", result.History)
			require.FileExists(t, filepath.Join(f.opts.Path, "data/plans/plan/plan.md"))
			require.Equal(t, "2", readTestGit(t, f.opts.Path, "rev-list", "--count", "HEAD"))
		})
	}
}

// Failure prevented: HEAD exists but its parent object is missing, so a cached
// receipt advertises complete history that Git cannot traverse.
func TestReadSyncRejectsBrokenCommitHistory(t *testing.T) {
	f := newReadFixture(t)
	ctx := context.Background()
	first := ReadSync(ctx, f.opts)
	require.True(t, first.Ready)
	tree := readTestGit(t, f.opts.Path, "rev-parse", "HEAD^{tree}")
	commit := fmt.Sprintf("tree %s\nparent %s\nauthor Test <test@example.invalid> 1 +0000\ncommitter Test <test@example.invalid> 1 +0000\n\nbroken parent\n", tree, strings.Repeat("1", 40))
	path := filepath.Join(t.TempDir(), "broken-commit")
	require.NoError(t, os.WriteFile(path, []byte(commit), 0600))
	head := readTestGit(t, f.opts.Path, "hash-object", "-t", "commit", "-w", path)
	readTestGit(t, f.opts.Path, "update-ref", "HEAD", head)
	result := CheckReadiness(ctx, f.opts.Path, f.opts.RepoID, f.opts.Endpoint)
	require.False(t, result.Ready)
	require.Equal(t, "incomplete_history", result.ErrorClass)
	require.Equal(t, "unknown", result.History)
	require.Nil(t, result.LastSuccessfulSync)
	readTestGit(t, f.opts.Path, "update-ref", "HEAD", first.Head)
	require.True(t, ReadSync(ctx, f.opts).Ready)
}

// Failure prevented: non-regular or unexpectedly missing tracked content is
// silently overwritten instead of preserving local changes for the coworker.
func TestReadSyncRejectsDamagedTrackedContent(t *testing.T) {
	for _, tc := range []struct {
		name, errorClass string
		damage           func(*testing.T, *readFixture, string)
	}{
		{"missing file", "dirty", func(t *testing.T, f *readFixture, path string) { require.NoError(t, os.Remove(path)) }},
		{"directory replaces file", "dirty", func(t *testing.T, f *readFixture, path string) {
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.Mkdir(path, 0700))
		}},
		{"staged work", "dirty", func(t *testing.T, f *readFixture, path string) {
			require.NoError(t, os.WriteFile(path, []byte("staged local work"), 0600))
			readTestGit(t, f.opts.Path, "add", "--", path)
		}},
		{"malformed stub", "missing_hydration", func(t *testing.T, f *readFixture, path string) {
			require.NoError(t, os.WriteFile(path, []byte("version https://git-lfs.github.com/spec/v1\noid sha256:invalid\nsize not-a-number\n"), 0600))
		}},
		{"different stub", "missing_hydration", func(t *testing.T, f *readFixture, path string) {
			require.NoError(t, os.WriteFile(path, []byte(lfs.FormatPointer("sha256:"+lfs.ComputeOID([]byte("new")), 3)), 0600))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReadFixture(t)
			ctx := context.Background()
			require.True(t, ReadSync(ctx, f.opts).Ready)
			path := filepath.Join(f.opts.Path, "sessions/old/session.md")
			tc.damage(t, f, path)
			result := ReadSync(ctx, f.opts)
			require.False(t, result.Ready)
			require.Equal(t, tc.errorClass, result.ErrorClass)
		})
	}
}

// Failure prevented: object write errors publish partial data, or hash-valid
// bytes with the wrong advertised length are accepted as hydrated content.
func TestReadSyncObjectMaterializationFailuresLeaveDestinationUntouched(t *testing.T) {
	content := []byte("verified object bytes")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(content) }))
	t.Cleanup(server.Close)
	action := &lfs.Action{Href: server.URL}
	ref := lfs.FileRef{OID: "sha256:" + lfs.ComputeOID(content), Size: int64(len(content))}
	for _, tc := range []struct {
		name string
		prep func(*testing.T, string, *lfs.FileRef) string
	}{
		{"missing parent", func(t *testing.T, root string, ref *lfs.FileRef) string {
			return filepath.Join(root, "missing", "object")
		}},
		{"destination is directory", func(t *testing.T, root string, ref *lfs.FileRef) string {
			path := filepath.Join(root, "object")
			require.NoError(t, os.Mkdir(path, 0700))
			return path
		}},
		{"size mismatch", func(t *testing.T, root string, ref *lfs.FileRef) string {
			ref.Size++
			path := filepath.Join(root, "object")
			require.NoError(t, os.WriteFile(path, []byte("original"), 0600))
			return path
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			localRef := ref
			path := tc.prep(t, root, &localRef)
			err := materializeReadObject(context.Background(), action, path, localRef)
			require.Error(t, err)
			if tc.name == "size mismatch" {
				require.EqualError(t, err, "missing_hydration")
				actual, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, "original", string(actual))
			}
			staging, err := filepath.Glob(filepath.Join(root, ".ox-read-object-*"))
			require.NoError(t, err)
			require.Empty(t, staging)
		})
	}
}

// Failure prevented: a retry overwrites a corrupt or redirected retained LFS
// object, losing the only local copy before a changed pointer is checked out.
func TestReadSyncDehydrationRetainsVerifiedObjectsAcrossRetries(t *testing.T) {
	for _, kind := range []string{"verified existing object", "corrupt existing object", "directory object", "blocked cache parent"} {
		t.Run(kind, func(t *testing.T) {
			f := newReadFixture(t)
			ctx := context.Background()
			require.True(t, ReadSync(ctx, f.opts).Ready)
			content := []byte("the previously verified object is irreplaceable")
			oid := lfs.ComputeOID(content)
			pointer := lfs.FormatPointer("sha256:"+oid, int64(len(content)))
			path := filepath.Join(f.opts.Path, "sessions/old/session.md")
			require.NoError(t, os.WriteFile(path, []byte(pointer), 0600))
			readTestGit(t, f.opts.Path, "add", "--", path)
			readTestGit(t, f.opts.Path, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "local pointer")
			require.NoError(t, os.WriteFile(path, content, 0600))
			require.True(t, CheckReadiness(ctx, f.opts.Path, f.opts.RepoID, f.opts.Endpoint).Ready)
			cacheDir := filepath.Join(f.opts.Path, ".sageox/cache/read-sync/objects")
			cachePath := filepath.Join(cacheDir, oid)
			if kind == "blocked cache parent" {
				require.NoError(t, os.WriteFile(cacheDir, []byte("owned file"), 0600))
			} else {
				require.NoError(t, os.Mkdir(cacheDir, 0700))
				switch kind {
				case "verified existing object":
					require.NoError(t, os.WriteFile(cachePath, content, 0600))
				case "corrupt existing object":
					require.NoError(t, os.WriteFile(cachePath, []byte("not the object"), 0600))
				case "directory object":
					require.NoError(t, os.Mkdir(cachePath, 0700))
				}
			}
			transport, err := gitserver.NewReadTransport(f.opts.Endpoint, f.opts.RepoID, f.opts.ReadURL)
			require.NoError(t, err)
			err = dehydrateReadFiles(ctx, transport, f.opts.Path, "", sparseCheckoutDirs())
			actual, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			if kind == "verified existing object" {
				require.NoError(t, err)
				require.Equal(t, pointer, string(actual))
				backup, err := os.ReadFile(cachePath)
				require.NoError(t, err)
				require.Equal(t, content, backup)
			} else {
				require.EqualError(t, err, "interrupted")
				require.Equal(t, content, actual, "failure must preserve the hydrated worktree file")
			}
		})
	}
}

// Failure prevented: Git's stat cache or the pointer-size optimization hides
// edits in larger tracked files, including files at the repository root.
func TestReadSyncVerifiesLargePlainFilesAndRootContent(t *testing.T) {
	f := newReadFixture(t)
	content := []byte(strings.Repeat("large session contents\n", 4000))
	require.NoError(t, os.WriteFile(filepath.Join(f.source, "sessions/old/session.md"), content, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(f.source, "README.md"), []byte("ledger root content"), 0600))
	readTestGit(t, f.source, "add", "--all")
	readTestGit(t, f.source, "commit", "-m", "large content and root metadata")
	readTestGit(t, f.source, "push", f.bare, "main")
	ctx := context.Background()
	first := ReadSync(ctx, f.opts)
	require.True(t, first.Ready, "%+v", first)
	require.FileExists(t, filepath.Join(f.opts.Path, "README.md"))
	path := filepath.Join(f.opts.Path, "sessions/old/session.md")
	readTestGit(t, f.opts.Path, "update-index", "--assume-unchanged", "sessions/old/session.md")
	content[0] = '!'
	require.NoError(t, os.WriteFile(path, content, 0600))
	result := ReadSync(ctx, f.opts)
	require.False(t, result.Ready)
	require.Equal(t, "dirty", result.ErrorClass)
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, content, actual)
}

// Failure prevented: unreadable files, canceled hashing, or SHA-256 Git object
// IDs are mistaken for verified content during a local readiness check.
func TestReadSyncHashingRequiresCompleteReadableBytes(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "object")
	require.NoError(t, os.WriteFile(path, []byte("object bytes\n"), 0600))
	readTestGit(t, root, "init", "--object-format=sha256")
	gitOID, lfsOID, size, _, err := hashReadFile(context.Background(), path, 64)
	require.NoError(t, err)
	require.Equal(t, readTestGit(t, root, "hash-object", "--", path), gitOID)
	require.Equal(t, lfs.ComputeOID([]byte("object bytes\n")), lfsOID)
	require.EqualValues(t, len("object bytes\n"), size)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _, _, err = hashReadFile(ctx, path, 64)
	require.ErrorIs(t, err, context.Canceled)
	_, _, _, _, err = hashReadFile(context.Background(), path+"-missing", 40)
	require.Error(t, err)
	_, _, _, _, err = hashReadFile(context.Background(), root, 40)
	require.Error(t, err)
}

// Failure prevented: a checkout is reported ready after its durable receipt
// could not be replaced, or failed publication destroys the last valid receipt.
func TestReadSyncReceiptPublicationFailurePreservesEvidence(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions must reject writes for this failure injection")
	}
	f := newReadFixture(t)
	ctx := context.Background()
	first := ReadSync(ctx, f.opts)
	require.True(t, first.Ready)
	dir := filepath.Dir(filepath.Join(f.opts.Path, readReceiptRelative))
	require.NoError(t, os.Chmod(dir, 0500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	probe, err := os.CreateTemp(dir, "must-fail-*")
	if err == nil {
		_ = probe.Close()
		_ = os.Remove(probe.Name())
		t.Fatal("permission injection did not prevent writing")
	}
	result := CheckReadiness(ctx, f.opts.Path, f.opts.RepoID, f.opts.Endpoint)
	require.False(t, result.Ready)
	require.Equal(t, "interrupted", result.ErrorClass)
	receipt := loadReadReceipt(f.opts.Path, f.opts.RepoID, f.opts.Endpoint)
	require.NotNil(t, receipt)
	require.Equal(t, first.Head, receipt.Head)
	require.Equal(t, first.LastSuccessfulSync, receipt.LastSuccessfulSync)
	require.NoError(t, os.Chmod(dir, 0700))
	require.True(t, ReadSync(ctx, f.opts).Ready)
}

// Failure prevented: canceling a partial LFS response leaves temporary bytes
// exposed, holds the checkout lock forever, or makes the next sync unrecoverable.
func TestReadSyncCanceledHydrationCanRetry(t *testing.T) {
	content := []byte("session bytes arrive after a canceled first attempt\n")
	oid := lfs.ComputeOID(content)
	started := make(chan struct{}, 1)
	var stall atomic.Bool
	stall.Store(true)
	f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch") {
			_ = json.NewEncoder(w).Encode(lfs.BatchResponse{Objects: []lfs.BatchResponseObject{{
				OID: oid, Size: int64(len(content)), Actions: &lfs.Actions{Download: &lfs.Action{
					Href: "https://" + r.Host + strings.TrimSuffix(r.URL.Path, "/batch") + "/" + oid,
				}},
			}}})
			return
		}
		if stall.Load() {
			_, _ = w.Write(content[:8])
			w.(http.Flusher).Flush()
			started <- struct{}{}
			<-r.Context().Done()
			return
		}
		_, _ = w.Write(content)
	})
	require.True(t, ReadSync(context.Background(), f.opts).Ready)
	path := "sessions/cancel/session.md"
	pointer := commitReadLFSPointer(t, f, path, content)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	finished := make(chan ReadSyncResult, 1)
	go func() { finished <- ReadSync(ctx, f.opts) }()
	select {
	case <-started:
	case result := <-finished:
		t.Fatalf("sync completed before response cancellation: %+v", result)
	case <-ctx.Done():
		t.Fatal("sync did not request the object")
	}
	cancel()
	result := <-finished
	require.False(t, result.Ready)
	require.Equal(t, "interrupted", result.ErrorClass)
	actual, err := os.ReadFile(filepath.Join(f.opts.Path, path))
	require.NoError(t, err)
	require.Equal(t, pointer, string(actual))
	staging, err := filepath.Glob(filepath.Join(f.opts.Path, "sessions/cancel/.ox-read-object-*"))
	require.NoError(t, err)
	require.Empty(t, staging)
	stall.Store(false)
	result = ReadSync(context.Background(), f.opts)
	require.True(t, result.Ready, "%+v", result)
	require.NotNil(t, result.LastSuccessfulSync)
	actual, err = os.ReadFile(filepath.Join(f.opts.Path, path))
	require.NoError(t, err)
	require.Equal(t, content, actual)
}

// Failure prevented: HTTP authorization failures are treated as ordinary Git
// failures, partial LFS objects are exposed, or a transient denial prevents retry.
func TestReadSyncLFSHTTPFailuresPreserveStubAndRetry(t *testing.T) {
	for _, tc := range []struct {
		name, errorClass string
		batch            bool
		status           int
	}{
		{"batch denied", "denied", true, http.StatusUnauthorized},
		{"download denied", "denied", false, http.StatusForbidden},
		{"download missing", "missing_hydration", false, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := []byte("session body available after a transient failure")
			oid := lfs.ComputeOID(content)
			var fail atomic.Bool
			fail.Store(true)
			f := newReadLFSFixture(t, func(w http.ResponseWriter, r *http.Request) {
				batch := strings.HasSuffix(r.URL.Path, "/batch")
				if fail.Load() && batch == tc.batch {
					w.WriteHeader(tc.status)
					return
				}
				if batch {
					_ = json.NewEncoder(w).Encode(lfs.BatchResponse{Objects: []lfs.BatchResponseObject{{
						OID: oid, Size: int64(len(content)), Actions: &lfs.Actions{Download: &lfs.Action{
							Href: "https://" + r.Host + strings.TrimSuffix(r.URL.Path, "/batch") + "/" + oid,
						}},
					}}})
					return
				}
				_, _ = w.Write(content)
			})
			ctx := context.Background()
			require.True(t, ReadSync(ctx, f.opts).Ready)
			path := "sessions/failure/session.md"
			pointer := commitReadLFSPointer(t, f, path, content)
			failed := ReadSync(ctx, f.opts)
			require.False(t, failed.Ready)
			require.Equal(t, tc.errorClass, failed.ErrorClass)
			require.Nil(t, failed.LastSuccessfulSync)
			actual, err := os.ReadFile(filepath.Join(f.opts.Path, path))
			require.NoError(t, err)
			require.Equal(t, pointer, string(actual))
			fail.Store(false)
			result := ReadSync(ctx, f.opts)
			require.True(t, result.Ready, "%+v", result)
			require.Equal(t, failed.Head, result.Head)
			require.NotNil(t, result.LastSuccessfulSync)
			actual, err = os.ReadFile(filepath.Join(f.opts.Path, path))
			require.NoError(t, err)
			require.Equal(t, content, actual)
		})
	}
}

// Failure prevented: crossing an hour boundary invalidates an otherwise ready
// checkout, or local verification silently advances its promised coverage.
func TestReadSyncChecksCoverageFromPreviousHourWithoutRenewingFreshness(t *testing.T) {
	f := newReadFixture(t)
	ctx := context.Background()
	first := ReadSync(ctx, f.opts)
	require.True(t, first.Ready)
	previous := loadReadReceipt(f.opts.Path, f.opts.RepoID, f.opts.Endpoint)
	require.NotNil(t, previous)
	previous.Coverage.Paths = append([]string{}, first.Coverage.Paths...)
	var oldest string
	for i, dir := range previous.Coverage.Paths {
		if strings.HasPrefix(dir, "data/murmurs/") {
			hour, err := time.Parse("2006-01-02/15", strings.TrimSuffix(strings.TrimPrefix(dir, "data/murmurs/"), "/"))
			require.NoError(t, err)
			previous.Coverage.Paths[i] = filepath.ToSlash(MurmurDateHourDir(hour.Add(-time.Hour))) + "/"
			oldest = previous.Coverage.Paths[i]
		}
	}
	require.NotEmpty(t, oldest)
	transport, err := gitserver.NewReadTransport(f.opts.Endpoint, f.opts.RepoID, f.opts.ReadURL)
	require.NoError(t, err)
	args := append([]string{"sparse-checkout", "set", "--cone", "--"}, previous.Coverage.Paths...)
	_, err = runReadGit(ctx, transport, true, f.opts.Path, args...)
	require.NoError(t, err)
	require.NoError(t, publishReadReceipt(f.opts.Path, *previous, nil))
	checked := CheckReadiness(ctx, f.opts.Path, f.opts.RepoID, f.opts.Endpoint)
	require.True(t, checked.Ready, "%+v", checked)
	require.Equal(t, previous.Coverage.Paths, checked.Coverage.Paths)
	require.Equal(t, first.Head, checked.Head)
	require.Equal(t, first.LastSuccessfulSync, checked.LastSuccessfulSync)
	refreshed := ReadSync(ctx, f.opts)
	require.True(t, refreshed.Ready, "%+v", refreshed)
	require.NotContains(t, refreshed.Coverage.Paths, oldest)
	require.True(t, refreshed.LastSuccessfulSync.After(*first.LastSuccessfulSync))
	actual := strings.Split(readTestGit(t, f.opts.Path, "sparse-checkout", "list"), "\n")
	for _, dir := range refreshed.Coverage.Paths {
		require.Contains(t, actual, strings.TrimSuffix(dir, "/"))
	}
}

// Failure prevented: a truncated or tampered coverage receipt bypasses the
// always-required session, plan, and metadata directories during local checks.
func TestReadSyncRejectsReceiptCoverageMissingRequiredDirectories(t *testing.T) {
	f := newReadFixture(t)
	ctx := context.Background()
	for _, missing := range []string{"all", "data/plans"} {
		t.Run(missing, func(t *testing.T) {
			require.True(t, ReadSync(ctx, f.opts).Ready)
			receipt := loadReadReceipt(f.opts.Path, f.opts.RepoID, f.opts.Endpoint)
			require.NotNil(t, receipt)
			paths := receipt.Coverage.Paths
			receipt.Coverage.Paths = nil
			if missing != "all" {
				for _, path := range paths {
					if path != missing {
						receipt.Coverage.Paths = append(receipt.Coverage.Paths, path)
					}
				}
			}
			require.NoError(t, publishReadReceipt(f.opts.Path, *receipt, nil))
			result := CheckReadiness(ctx, f.opts.Path, f.opts.RepoID, f.opts.Endpoint)
			require.False(t, result.Ready)
			require.Equal(t, "incomplete_coverage", result.ErrorClass)
			require.True(t, ReadSync(ctx, f.opts).Ready)
		})
	}
}
