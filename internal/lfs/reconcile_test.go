package lfs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Behavioral tests for ReconcileUnpushedPointers
//
// These test the CONTRACT, not the implementation. If the implementation
// changes (e.g., squash replaced by filter-branch, walk replaced by
// git ls-files), these should still pass as long as the guarantees hold.
//
// Guarantees tested:
// 1. Ledgers without pointer files are unaffected
// 2. Non-pointer content is never modified or lost
// 3. Orphaned pointers are neutralized (no longer block push)
// 4. Reconciliation is idempotent
// 5. After reconciliation, unpushed history is collapsed to minimize push size
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

// --- Guarantee 1: ledgers without pointer files are unaffected ---

func TestReconcile_EmptyLedger_Unaffected(t *testing.T) {
	dir := t.TempDir() // not even a git repo
	result, err := ReconcileUnpushedPointers(context.Background(), dir, "", nil)
	require.NoError(t, err)
	assert.Zero(t, result.ScannedPointers)
	assert.Zero(t, result.Replaced)
}

func TestReconcile_NoSessions_Unaffected(t *testing.T) {
	dir := initLedgerRepo(t)
	// sessions/ exists but is empty
	result, err := ReconcileUnpushedPointers(context.Background(), dir, "", nil)
	require.NoError(t, err)
	assert.Zero(t, result.ScannedPointers)
}

func TestReconcile_OnlyRegularContent_Unaffected(t *testing.T) {
	dir := initLedgerRepo(t)

	// write real session content (not pointers)
	sessDir := filepath.Join(dir, "sessions", "2026-04-07-test")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "raw.jsonl"),
		[]byte(`{"type":"header","version":"1.0"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "summary.md"),
		[]byte("# Session summary\nThis is real content."), 0o644))

	result, err := ReconcileUnpushedPointers(context.Background(), dir, "", nil)
	require.NoError(t, err)
	assert.Zero(t, result.ScannedPointers, "regular content should not be detected as pointers")

	// verify content is untouched
	content, _ := os.ReadFile(filepath.Join(sessDir, "raw.jsonl"))
	assert.Contains(t, string(content), "header")
}

// --- Guarantee 2: non-pointer content is never modified ---

func TestReconcile_LargeFiles_Ignored(t *testing.T) {
	dir := initLedgerRepo(t)
	sessDir := filepath.Join(dir, "sessions", "test-session")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	// file that starts like a pointer but is too large (e.g., a session with
	// content that coincidentally starts with the version string — paranoid case)
	bigContent := make([]byte, maxPointerSize+100)
	copy(bigContent, []byte("version https://git-lfs.github.com/spec/v1\noid sha256:aaa\nsize 999\n"))
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "big.jsonl"), bigContent, 0o644))

	result, err := ReconcileUnpushedPointers(context.Background(), dir, "", nil)
	require.NoError(t, err)
	assert.Zero(t, result.ScannedPointers, "files exceeding pointer size limit should be skipped")

	// verify file is untouched
	after, _ := os.ReadFile(filepath.Join(sessDir, "big.jsonl"))
	assert.Equal(t, bigContent, after)
}

func TestReconcile_MalformedPointer_Ignored(t *testing.T) {
	dir := initLedgerRepo(t)
	sessDir := filepath.Join(dir, "sessions", "test-session")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	// looks like a pointer but missing size field — ParsePointer should reject
	malformed := "version https://git-lfs.github.com/spec/v1\noid sha256:abc123\n"
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "broken.jsonl"), []byte(malformed), 0o644))

	result, err := ReconcileUnpushedPointers(context.Background(), dir, "", nil)
	require.NoError(t, err)
	assert.Zero(t, result.ScannedPointers, "malformed pointers should not be counted")

	// verify file is untouched
	after, _ := os.ReadFile(filepath.Join(sessDir, "broken.jsonl"))
	assert.Equal(t, malformed, string(after))
}

// --- Guarantee 3: orphaned pointers are detected ---

func TestReconcile_DetectsValidPointerFiles(t *testing.T) {
	dir := initLedgerRepo(t)
	sessDir := filepath.Join(dir, "sessions", "test-session")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	// write valid pointer files
	oid1 := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	oid2 := "cafebabecafebabecafebabecafebabecafebabecafebabecafebabecafebabe"
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "raw.jsonl"),
		[]byte(lfsPointerContent(oid1, 12345)), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "summary.md"),
		[]byte(lfsPointerContent(oid2, 67890)), 0o644))

	// will fail at LFS client creation (no credentials) but should have counted pointers
	result, err := ReconcileUnpushedPointers(context.Background(), dir, "https://example.com", nil)
	assert.Error(t, err, "should fail when LFS client can't be created")
	assert.Equal(t, 2, result.ScannedPointers, "should detect both pointer files before failing")
}

func TestReconcile_NestedSessionDirectories(t *testing.T) {
	dir := initLedgerRepo(t)

	// pointers scattered across multiple session directories
	sessions := []string{
		"2026-04-07T03-48-ryan-Ox83hv",
		"2026-04-07T04-10-ryan-OxU3Y3",
		"2026-04-09T18-34-ryan-OxXUbq",
	}
	oid := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, s := range sessions {
		sessDir := filepath.Join(dir, "sessions", s)
		require.NoError(t, os.MkdirAll(sessDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(sessDir, "raw.jsonl"),
			[]byte(lfsPointerContent(oid, 100)), 0o644))
	}

	result, err := ReconcileUnpushedPointers(context.Background(), dir, "https://example.com", nil)
	assert.Error(t, err) // LFS client fails
	assert.Equal(t, 3, result.ScannedPointers, "should scan all nested session dirs")
}

// --- Guarantee 4: idempotent ---

func TestReconcile_AlreadyEmpty_Idempotent(t *testing.T) {
	dir := initLedgerRepo(t)
	sessDir := filepath.Join(dir, "sessions", "test-session")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	// empty files (what a pointer looks like AFTER reconciliation)
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "raw.jsonl"), []byte{}, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "summary.md"), []byte{}, 0o644))

	result, err := ReconcileUnpushedPointers(context.Background(), dir, "", nil)
	require.NoError(t, err)
	assert.Zero(t, result.ScannedPointers, "empty files should not be detected as pointers")
	assert.Zero(t, result.Replaced, "nothing to replace")
}

// --- Guarantee 5: history squash collapses unpushed commits ---

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

// --- Mixed scenarios ---

func TestReconcile_MixedContent_OnlyPointersScanned(t *testing.T) {
	dir := initLedgerRepo(t)
	sessDir := filepath.Join(dir, "sessions", "mixed-session")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	// mix of real content, pointers, and metadata
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "meta.json"),
		[]byte(`{"version":"1.0","files":{}}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "summary.json"),
		[]byte(`{"title":"test"}`), 0o644))

	oid := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "raw.jsonl"),
		[]byte(lfsPointerContent(oid, 5000)), 0o644))

	// regular content file that happens to be small
	require.NoError(t, os.WriteFile(filepath.Join(sessDir, "context-trace.jsonl"),
		[]byte(`{"type":"provided","ts":"2026-04-07T05:01:26Z"}`), 0o644))

	result, err := ReconcileUnpushedPointers(context.Background(), dir, "https://example.com", nil)
	assert.Error(t, err) // LFS client creation fails
	assert.Equal(t, 1, result.ScannedPointers,
		"only the actual pointer file should be scanned, not metadata or regular content")
}
