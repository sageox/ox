package lfs

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Behavioral tests for ReconcileUnpushedPointers
//
// These test the CONTRACT, not the implementation. If the implementation
// changes (e.g., squash replaced by filter-branch), these should still pass as
// long as the guarantees hold.
//
// Guarantees tested:
// 1. Content is never modified or lost, even when reconciliation refuses
// 2. After reconciliation, unpushed history is collapsed to minimize push size
// ---------------------------------------------------------------------------

// lfsPointerContent creates a valid LFS pointer string for test use.
func lfsPointerContent(oid string, size int64) string {
	return FormatPointer("sha256:"+oid, size)
}

// initLedgerRepo creates a minimal git repo that looks like an ox ledger:
// has an initial commit, sessions/ directory, and optionally a remote+tracking.
func initLedgerRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init")
	git(t, dir, "config", "user.email", "test@test.local")
	git(t, dir, "config", "user.name", "Test")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sessions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitkeep"), []byte{}, 0o644))
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "init", "--no-verify")
	return dir
}

// initLedgerWithRemote creates a ledger repo backed by a bare remote so
// we can test push behavior and upstream tracking.
func initLedgerWithRemote(t *testing.T) (ledger string, bare string) {
	t.Helper()
	bare = t.TempDir()
	git(t, bare, "init", "--bare")

	ledger = t.TempDir()
	cmd := exec.Command("git", "clone", "--quiet", bare, ledger)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "clone failed: %s", string(out))

	git(t, ledger, "config", "user.email", "test@test.local")
	git(t, ledger, "config", "user.name", "Test")

	// initial commit so main exists
	require.NoError(t, os.MkdirAll(filepath.Join(ledger, "sessions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ledger, ".gitkeep"), []byte{}, 0o644))
	git(t, ledger, "add", ".")
	git(t, ledger, "commit", "-m", "init", "--no-verify")
	git(t, ledger, "push", "--quiet")
	return
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	fullArgs := args
	if dir != "" {
		fullArgs = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", fullArgs...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), string(out))
	return strings.TrimSpace(string(out))
}

func unpushedCount(t *testing.T, dir string) int {
	t.Helper()
	out := git(t, dir, "rev-list", "--count", "@{upstream}..HEAD")
	var n int
	_, _ = strings.NewReader(out), 0
	for _, c := range out {
		n = n*10 + int(c-'0')
	}
	return n
}

func TestReconcile_WaitsForRepoWriter(t *testing.T) {
	dir := initLedgerRepo(t)
	locked := make(chan struct{})
	release := make(chan struct{})
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- gitutil.WithRepoLock(context.Background(), dir, func() error {
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	result, err := ReconcileUnpushedPointers(ctx, dir, "", nil)
	close(release)
	require.NoError(t, <-lockDone)
	require.True(t, gitutil.IsRepoLockBusy(err), "expected repo-lock contention, got %v", err)
	assert.NotNil(t, result)
	assert.Zero(t, result.ScannedPointers)
}

// --- Guarantee 1: content is never lost ---

// TestReconcileUnpushedPointers_FailsClosed: a reconcile that runs mid-merge,
// cannot scan the push range (no upstream), or cannot reach the LFS store
// returns an error and leaves HEAD and the pointer as they were. Failure
// prevented: a push-triggered repair that cannot safely see what the push
// carries changes the Ledger anyway.
func TestReconcileUnpushedPointers_FailsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repos")
	}
	// no stored or env credentials, so the real client constructor fails
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(auth.EnvVarToken, "")
	t.Setenv(endpoint.EnvVar, "")

	pointer := lfsPointerContent(strings.Repeat("e", 64), 500)
	for _, tc := range []struct {
		name        string
		remote      bool
		midMerge    bool
		wantErr     string
		wantScanned int
	}{
		{name: "merge in progress", remote: true, midMerge: true, wantErr: "preflight Ledger for LFS reconcile"},
		{name: "no upstream", wantErr: "no upstream tracking ref"},
		{name: "no LFS client", remote: true, wantErr: "lfs client", wantScanned: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ledger string
			if tc.remote {
				ledger, _ = initLedgerWithRemote(t)
			} else {
				ledger = initLedgerRepo(t)
			}
			path := filepath.Join(ledger, "data", "plans", "p", "plan.html")
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, []byte(pointer), 0o644))
			git(t, ledger, "add", ".")
			git(t, ledger, "commit", "-m", "plan", "--no-verify")
			head := git(t, ledger, "rev-parse", "HEAD")
			if tc.midMerge {
				// after the commit: committing with MERGE_HEAD present concludes the merge
				require.NoError(t, os.WriteFile(filepath.Join(ledger, ".git", "MERGE_HEAD"), []byte(head+"\n"), 0o644))
			}

			result, err := ReconcileUnpushedPointers(context.Background(), ledger, "https://example.com", nil)

			require.ErrorContains(t, err, tc.wantErr)
			assert.Equal(t, tc.wantScanned, result.ScannedPointers)
			assert.Equal(t, head, git(t, ledger, "rev-parse", "HEAD"), "no commit, no squash")
			got, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			assert.Equal(t, pointer, string(got), "the pointer file is untouched")
		})
	}
}

// A 404 after upload must not turn a recoverable session into an empty commit
// whose successful push lets the caller prune the only surviving source cache.
func TestReconcile_PreservesRecoverableSessionCache(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git history and mocked LFS batch API")
	}
	raw := []byte("{\"type\":\"user\",\"content\":\"keep this recording\"}\n")
	pointer := []byte(FormatPointer("sha256:"+ComputeOID(raw), int64(len(raw))))
	for _, tc := range []struct {
		name        string
		cache       []byte
		protect     bool
		recover     bool
		statError   bool
		wantProtect string
	}{
		{name: "recoverable content", cache: raw, recover: true},
		{name: "cache lookup error", protect: true, statError: true, wantProtect: "inspect session recovery cache"},
		{name: "missing cache"},
		{name: "empty cache", cache: []byte{}, protect: true, wantProtect: "does not match pointer OID and size"},
		{name: "pointer stub cache", cache: pointer, protect: true, wantProtect: "contains an LFS pointer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger, _ := initLedgerWithRemote(t)
			rawPath := filepath.Join(ledger, "sessions", "test-session", "raw.jsonl")
			planPath := filepath.Join(ledger, "data", "plans", "test-plan", "plan.html")
			planOID := strings.Repeat("a", 64)
			planPointer := []byte(lfsPointerContent(planOID, 100))
			for path, data := range map[string][]byte{rawPath: pointer, planPath: planPointer} {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, data, 0o644))
			}
			git(t, ledger, "add", "sessions", "data/plans")
			git(t, ledger, "commit", "-m", "uploaded pointers awaiting push", "--no-verify")

			cachePath := filepath.Join(ledger, ".sageox", "cache", "sessions", "test-session", "raw.jsonl")
			if tc.cache != nil {
				require.NoError(t, os.MkdirAll(filepath.Dir(cachePath), 0o755))
				require.NoError(t, os.WriteFile(cachePath, tc.cache, 0o600))
			}
			if tc.statError {
				// A file blocking a cache directory is an inspection failure,
				// not proof that the source is absent and safe to discard.
				require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Dir(cachePath)), 0o700))
				require.NoError(t, os.WriteFile(filepath.Dir(cachePath), []byte("blocked cache directory"), 0o600))
			}
			headBefore := git(t, ledger, "rev-parse", "HEAD")
			indexBefore := git(t, ledger, "write-tree")
			statusBefore := git(t, ledger, "status", "--porcelain")
			client := fakeLFSDownloadServer(t, map[string]int{
				ComputeOID(raw): http.StatusNotFound,
				planOID:         http.StatusNotFound,
			})
			result, err := reconcileUnpushedPointers(context.Background(), ledger, nil,
				func() (*Client, error) { return client, nil })

			assert.Equal(t, 2, result.MissingOnRemote)
			if tc.protect {
				require.ErrorContains(t, err, tc.wantProtect)
				if tc.statError {
					var pathErr *os.PathError
					require.ErrorAs(t, err, &pathErr)
					assert.Equal(t, cachePath, pathErr.Path)
				}
				assert.False(t, result.Squashed)
				assert.Equal(t, headBefore, git(t, ledger, "rev-parse", "HEAD"), "history must remain intact")
				assert.Equal(t, indexBefore, git(t, ledger, "write-tree"), "nothing may be staged")
				assert.Equal(t, statusBefore, git(t, ledger, "status", "--porcelain"))
				preserved := map[string][]byte{rawPath: pointer, planPath: planPointer}
				if tc.cache != nil {
					preserved[cachePath] = tc.cache
				}
				if tc.statError {
					preserved[filepath.Dir(cachePath)] = []byte("blocked cache directory")
				}
				for path, expected := range preserved {
					content, readErr := os.ReadFile(path)
					require.NoError(t, readErr)
					assert.Equal(t, expected, content, "abort before modifying any pointer or cache")
				}
			} else {
				// the plan pointer has no bytes anywhere, so reconcile refuses; it never removes it
				var unrecoverable *UnrecoverablePointersError
				require.ErrorAs(t, err, &unrecoverable)
				wantUploaded, wantListed := 0, 2
				if tc.recover {
					wantUploaded, wantListed = 1, 1
				}
				assert.Equal(t, wantUploaded, unrecoverable.Uploaded)
				assert.Len(t, unrecoverable.Pointers, wantListed)
				assert.Equal(t, wantUploaded, result.RecoveredUploads)
				assert.False(t, result.Squashed)
				assert.Equal(t, headBefore, git(t, ledger, "rev-parse", "HEAD"), "no commit, no squash")
				assert.Equal(t, pointer, mustReadFile(t, rawPath))
				assert.Equal(t, planPointer, mustReadFile(t, planPath))
				if tc.recover {
					assert.Equal(t, raw, mustReadFile(t, cachePath))
				}
			}
		})
	}
}

// LFS reconciliation used to run an unscoped commit, so a session conflict
// staged by another writer could ride along with an unrelated pointer repair.
// Refuse before changing either history or the pointer working copy.
func TestReconcile_RefusesPreexistingStagedConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git history and mocked LFS batch API")
	}
	ledger, _ := initLedgerWithRemote(t)
	oid := strings.Repeat("a", 64)
	pointer := []byte(lfsPointerContent(oid, 100))
	pointerPath := filepath.Join(ledger, "sessions", "pointer-session", "raw.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(pointerPath), 0o755))
	require.NoError(t, os.WriteFile(pointerPath, pointer, 0o644))
	git(t, ledger, "add", "--sparse", "sessions/pointer-session/raw.jsonl")
	git(t, ledger, "commit", "-m", "pointer awaiting push", "--no-verify")

	metaPath := filepath.Join(ledger, "sessions", "conflicted", "meta.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(metaPath), 0o755))
	markers := []byte("<<<<<<< Updated upstream\n{}\n=======\n{}\n>>>>>>> Stashed changes\n")
	require.NoError(t, os.WriteFile(metaPath, markers, 0o644))
	git(t, ledger, "add", "--sparse", "sessions/conflicted/meta.json")
	headBefore := git(t, ledger, "rev-parse", "HEAD")

	client := fakeLFSDownloadServer(t, map[string]int{oid: http.StatusNotFound})
	result, err := reconcileUnpushedPointers(context.Background(), ledger, nil,
		func() (*Client, error) { return client, nil })
	require.ErrorContains(t, err, "unresolved conflict")
	assert.Equal(t, 1, result.MissingOnRemote)
	assert.False(t, result.Squashed)
	assert.Equal(t, headBefore, git(t, ledger, "rev-parse", "HEAD"))
	content, readErr := os.ReadFile(pointerPath)
	require.NoError(t, readErr)
	assert.Equal(t, pointer, content, "validation must run before destructive pointer replacement")
}

func TestReconcile_RefusesConflictInUnpushedHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git history and mocked LFS batch API")
	}
	ledger, _ := initLedgerWithRemote(t)
	oid := strings.Repeat("b", 64)
	pointerPath := filepath.Join(ledger, "sessions", "pointer-session", "raw.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(pointerPath), 0o755))
	require.NoError(t, os.WriteFile(pointerPath, []byte(lfsPointerContent(oid, 100)), 0o644))
	git(t, ledger, "add", "--sparse", "sessions/pointer-session/raw.jsonl")
	git(t, ledger, "commit", "-m", "pointer awaiting push", "--no-verify")

	metaPath := filepath.Join(ledger, "sessions", "conflicted", "meta.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(metaPath), 0o755))
	markers := []byte("<<<<<<< Updated upstream\n{}\n=======\n{}\n>>>>>>> Stashed changes\n")
	require.NoError(t, os.WriteFile(metaPath, markers, 0o644))
	git(t, ledger, "add", "--sparse", "sessions/conflicted/meta.json")
	git(t, ledger, "commit", "-m", "accidentally published conflict locally", "--no-verify")

	client := fakeLFSDownloadServer(t, map[string]int{oid: http.StatusNotFound})
	result, err := reconcileUnpushedPointers(context.Background(), ledger, nil,
		func() (*Client, error) { return client, nil })
	require.ErrorContains(t, err, "validate unpushed Ledger")
	assert.False(t, result.Squashed)
	assert.Equal(t, 2, unpushedCount(t, ledger), "preflight validation must preserve the original commit chain")
	content, readErr := os.ReadFile(metaPath)
	require.NoError(t, readErr)
	assert.Equal(t, markers, content, "failed squash must preserve corrupt metadata for doctor recovery")
}

// --- Guarantee 2: history squash collapses unpushed commits ---

func TestReconcile_Squash_CollapsesMultipleCommits(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	ledger, _ := initLedgerWithRemote(t)

	// simulate the real scenario: murmur + session finalize + another murmur
	require.NoError(t, os.MkdirAll(filepath.Join(ledger, "data", "murmurs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ledger, "data", "murmurs", "m1.json"),
		[]byte(`{"content":"murmur 1"}`), 0o644))
	git(t, ledger, "add", "data/murmurs/m1.json")
	git(t, ledger, "commit", "-m", "murmur: first", "--no-verify")

	require.NoError(t, os.MkdirAll(filepath.Join(ledger, "sessions", "test-session"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ledger, "sessions", "test-session", "meta.json"),
		[]byte(`{"version":"1.0"}`), 0o644))
	git(t, ledger, "add", "sessions/test-session/meta.json")
	git(t, ledger, "commit", "-m", "finalize session", "--no-verify")

	require.NoError(t, os.WriteFile(filepath.Join(ledger, "data", "murmurs", "m2.json"),
		[]byte(`{"content":"murmur 2"}`), 0o644))
	git(t, ledger, "add", "data/murmurs/m2.json")
	git(t, ledger, "commit", "-m", "murmur: second", "--no-verify")

	require.Equal(t, 3, unpushedCount(t, ledger))

	err := squashUnpushed(context.Background(), ledger, "reconciled")
	require.NoError(t, err)

	assert.Equal(t, 1, unpushedCount(t, ledger), "all unpushed commits should collapse to 1")

	// all files survive the squash
	assert.FileExists(t, filepath.Join(ledger, "data", "murmurs", "m1.json"))
	assert.FileExists(t, filepath.Join(ledger, "data", "murmurs", "m2.json"))
	assert.FileExists(t, filepath.Join(ledger, "sessions", "test-session", "meta.json"))
}

func TestReconcile_Squash_PushSucceedsAfter(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	ledger, bare := initLedgerWithRemote(t)

	// add commits
	require.NoError(t, os.WriteFile(filepath.Join(ledger, "data.json"),
		[]byte(`{"test":true}`), 0o644))
	git(t, ledger, "add", "data.json")
	git(t, ledger, "commit", "-m", "commit 1", "--no-verify")

	require.NoError(t, os.WriteFile(filepath.Join(ledger, "data2.json"),
		[]byte(`{"test":2}`), 0o644))
	git(t, ledger, "add", "data2.json")
	git(t, ledger, "commit", "-m", "commit 2", "--no-verify")

	err := squashUnpushed(context.Background(), ledger, "squashed")
	require.NoError(t, err)

	// push should succeed
	git(t, ledger, "push", "--quiet")

	// verify content landed on remote
	verifyDir := t.TempDir()
	cmd := exec.Command("git", "clone", "--quiet", bare, verifyDir)
	out, cloneErr := cmd.CombinedOutput()
	require.NoError(t, cloneErr, "verify clone: %s", string(out))
	assert.FileExists(t, filepath.Join(verifyDir, "data.json"))
	assert.FileExists(t, filepath.Join(verifyDir, "data2.json"))
}

func TestReconcile_Squash_NoUpstream_FailsGracefully(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	dir := initLedgerRepo(t) // no remote, no upstream

	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.txt"), []byte("x"), 0o644))
	git(t, dir, "add", "test.txt")
	git(t, dir, "commit", "-m", "local only", "--no-verify")

	err := squashUnpushed(context.Background(), dir, "should fail")
	assert.Error(t, err, "squash without upstream should fail, not crash")

	// repo should not be corrupted — HEAD still valid
	git(t, dir, "rev-parse", "HEAD")
}

func TestReconcile_Squash_SingleCommit_NoOp(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	ledger, _ := initLedgerWithRemote(t)

	require.NoError(t, os.WriteFile(filepath.Join(ledger, "file.txt"), []byte("x"), 0o644))
	git(t, ledger, "add", "file.txt")
	git(t, ledger, "commit", "-m", "only commit", "--no-verify")

	before := git(t, ledger, "rev-parse", "HEAD")

	err := squashUnpushed(context.Background(), ledger, "should not change")
	require.NoError(t, err)

	after := git(t, ledger, "rev-parse", "HEAD")
	assert.Equal(t, before, after, "single unpushed commit should not be rewritten")
}
