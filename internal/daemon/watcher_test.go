package daemon

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The ledger Watcher polls its directory (non-recursive) and fires a debounced
// callback when the direct children change. These tests drive pollOnce()
// directly for determinism and exercise Start() once for the full lifecycle.

// newLedgerWatcher builds a ledger watcher wired to count callback fires, with a
// short debounce so tests don't wait long. pollInterval is irrelevant when
// driving pollOnce() directly, so it's set large.
func newLedgerWatcher(t *testing.T, dir string, fired *int32) *Watcher {
	t.Helper()
	w := NewWatcherWithFS(dir, 15*time.Millisecond, time.Second, slogDiscard(), &RealFileSystem{})
	w.callback = func() { atomic.AddInt32(fired, 1) }
	return w
}

func TestNewWatcher(t *testing.T) {
	w := NewWatcher("/tmp/ledger", 100*time.Millisecond, slogDiscard())
	assert.Equal(t, "/tmp/ledger", w.path)
	assert.Equal(t, 100*time.Millisecond, w.debounceWindow)
	assert.Equal(t, defaultLedgerPollInterval, w.pollInterval)
}

func TestNewWatcherWithFS(t *testing.T) {
	mfs := NewMockFileSystem()
	w := NewWatcherWithFS("/x", 50*time.Millisecond, 0, slogDiscard(), mfs)
	assert.Equal(t, "/x", w.path)
	assert.Equal(t, 50*time.Millisecond, w.debounceWindow)
	// pollInterval <= 0 falls back to the default.
	assert.Equal(t, defaultLedgerPollInterval, w.pollInterval)
	assert.Same(t, mfs, w.fs.(*MockFileSystem))
}

// --- ledgerWatchIncludes: which direct children matter ---

// TestLedgerWatchIncludes documents the inclusion filter that mirrors the prior
// fsnotify handler.
// Failure prevented: ledger content (.sageox) gets ignored, or .git churn
// triggers spurious sync callbacks.
func TestLedgerWatchIncludes(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{".sageox", true},
		{"session.json", true},
		{"sessions", true},
		{".git", false},
		{"foo.gitsomething", false}, // contains ".git" → excluded (matches prior Contains filter)
		{".hidden", false},
		{".DS_Store", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ledgerWatchIncludes(tc.name))
		})
	}
}

// --- sameFingerprint: change detection primitive ---

func TestSameFingerprint(t *testing.T) {
	base := map[string]dirEntrySig{
		"a": {mtime: time.Unix(100, 0), size: 10, isDir: false},
		"b": {mtime: time.Unix(200, 0), size: 20, isDir: true},
	}
	clone := map[string]dirEntrySig{
		"a": {mtime: time.Unix(100, 0), size: 10, isDir: false},
		"b": {mtime: time.Unix(200, 0), size: 20, isDir: true},
	}
	assert.True(t, sameFingerprint(base, clone), "identical fingerprints are equal")

	tests := map[string]map[string]dirEntrySig{
		"added entry":   {"a": base["a"], "b": base["b"], "c": {size: 1}},
		"removed entry": {"a": base["a"]},
		"size changed":  {"a": {mtime: time.Unix(100, 0), size: 11}, "b": base["b"]},
		"mtime changed": {"a": {mtime: time.Unix(101, 0), size: 10}, "b": base["b"]},
		"isdir changed": {"a": {mtime: time.Unix(100, 0), size: 10, isDir: true}, "b": base["b"]},
	}
	for name, other := range tests {
		t.Run(name, func(t *testing.T) {
			assert.False(t, sameFingerprint(base, other), "fingerprints should differ: %s", name)
		})
	}
}

// --- pollOnce: detection against a real directory ---

// firesWithin asserts the callback fired (count reaches 1) within the window.
func firesWithin(t *testing.T, fired *int32) {
	t.Helper()
	require.Eventually(t, func() bool { return atomic.LoadInt32(fired) >= 1 },
		time.Second, 5*time.Millisecond, "expected ledger change callback to fire")
}

// neverFires asserts the callback does not fire within a short settle window.
func neverFires(t *testing.T, fired *int32) {
	t.Helper()
	assert.Never(t, func() bool { return atomic.LoadInt32(fired) >= 1 },
		150*time.Millisecond, 10*time.Millisecond, "callback should not fire")
}

// TestWatcher_DetectsNewFile verifies a newly created ledger file triggers the
// debounced callback.
// Failure prevented: a new session file lands but no sync is triggered.
func TestWatcher_DetectsNewFile(t *testing.T) {
	dir := t.TempDir()
	var fired int32
	w := newLedgerWatcher(t, dir, &fired)

	w.prev = w.snapshot() // baseline (empty dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "session.json"), []byte("{}"), 0o644))
	w.pollOnce()

	firesWithin(t, &fired)
}

// TestWatcher_DetectsModify verifies a content change to an existing ledger file
// triggers the callback.
func TestWatcher_DetectsModify(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "notes.md")
	require.NoError(t, os.WriteFile(f, []byte("a"), 0o644))

	var fired int32
	w := newLedgerWatcher(t, dir, &fired)
	w.prev = w.snapshot() // baseline includes notes.md

	require.NoError(t, os.WriteFile(f, []byte("a much longer body"), 0o644)) // size changes
	w.pollOnce()

	firesWithin(t, &fired)
}

// TestWatcher_DetectsRemove verifies deleting a watched file triggers the callback.
func TestWatcher_DetectsRemove(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "old.json")
	require.NoError(t, os.WriteFile(f, []byte("{}"), 0o644))

	var fired int32
	w := newLedgerWatcher(t, dir, &fired)
	w.prev = w.snapshot()

	require.NoError(t, os.Remove(f))
	w.pollOnce()

	firesWithin(t, &fired)
}

// TestWatcher_AllowsSageox verifies the .sageox entry (ledger content) is watched.
// Failure prevented: .sageox is wrongly treated as a hidden file and ignored.
func TestWatcher_AllowsSageox(t *testing.T) {
	dir := t.TempDir()
	var fired int32
	w := newLedgerWatcher(t, dir, &fired)
	w.prev = w.snapshot()

	require.NoError(t, os.Mkdir(filepath.Join(dir, ".sageox"), 0o755))
	w.pollOnce()

	firesWithin(t, &fired)
}

// TestWatcher_IgnoresGitAndHidden verifies .git internals and hidden files do
// not trigger the callback.
// Failure prevented: routine .git or editor-state churn spams sync.
func TestWatcher_IgnoresGitAndHidden(t *testing.T) {
	dir := t.TempDir()
	var fired int32
	w := newLedgerWatcher(t, dir, &fired)
	w.prev = w.snapshot()

	require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".DS_Store"), []byte("x"), 0o644))
	w.pollOnce()

	neverFires(t, &fired)
}

// TestWatcher_BaselineNoFireOnPreexisting verifies pre-existing content present
// at the first snapshot does not trigger a callback.
// Failure prevented: every daemon start triggers a spurious sync.
func TestWatcher_BaselineNoFireOnPreexisting(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "existing.json"), []byte("{}"), 0o644))

	var fired int32
	w := newLedgerWatcher(t, dir, &fired)
	w.prev = w.snapshot() // baseline captures existing.json
	w.pollOnce()          // nothing changed since baseline

	neverFires(t, &fired)
}

// TestWatcher_DebounceCoalesces verifies multiple changes within the debounce
// window produce a single callback.
// Failure prevented: a burst of ledger writes fans out into N syncs.
func TestWatcher_DebounceCoalesces(t *testing.T) {
	dir := t.TempDir()
	w := NewWatcherWithFS(dir, 60*time.Millisecond, time.Second, slogDiscard(), &RealFileSystem{})
	var fired int32
	w.callback = func() { atomic.AddInt32(&fired, 1) }
	w.prev = w.snapshot()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.json"), []byte("1"), 0o644))
	w.pollOnce()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.json"), []byte("2"), 0o644))
	w.pollOnce() // resets the debounce timer

	// exactly one fire after the window elapses
	require.Eventually(t, func() bool { return atomic.LoadInt32(&fired) == 1 },
		time.Second, 5*time.Millisecond)
	time.Sleep(80 * time.Millisecond)
	assert.Equal(t, int32(1), atomic.LoadInt32(&fired), "burst should coalesce to one callback")
}

// --- Start lifecycle ---

// TestWatcher_Start_DetectsAndStops runs the full poll loop against a real
// directory, then verifies clean shutdown on context cancel.
func TestWatcher_Start_DetectsAndStops(t *testing.T) {
	dir := t.TempDir()
	w := NewWatcherWithFS(dir, 10*time.Millisecond, 10*time.Millisecond, slogDiscard(), &RealFileSystem{})

	var fired int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Start(ctx, func() { atomic.AddInt32(&fired, 1) })
		close(done)
	}()

	// let the baseline snapshot settle, then create a file
	time.Sleep(40 * time.Millisecond)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "session.json"), []byte("{}"), 0o644))

	require.Eventually(t, func() bool { return atomic.LoadInt32(&fired) >= 1 },
		2*time.Second, 10*time.Millisecond, "Start did not deliver a change")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not exit after context cancel")
	}
}

func TestWatcher_Start_Context(t *testing.T) {
	w := NewWatcher(t.TempDir(), 10*time.Millisecond, slogDiscard())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Start(ctx, func() {})
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after cancel")
	}
}

// --- Stop semantics ---

func TestWatcher_Stop(t *testing.T) {
	w := NewWatcher(t.TempDir(), 10*time.Millisecond, slogDiscard())
	var fired int32
	w.callback = func() { atomic.AddInt32(&fired, 1) }

	w.Stop()
	w.debounce() // must be a no-op after Stop

	time.Sleep(60 * time.Millisecond)
	assert.Equal(t, int32(0), atomic.LoadInt32(&fired), "callback must not fire after Stop")
}

func TestWatcher_Stop_Idempotent(t *testing.T) {
	w := NewWatcher(t.TempDir(), 10*time.Millisecond, slogDiscard())
	assert.NotPanics(t, func() {
		w.Stop()
		w.Stop()
		w.Stop()
	})
}

func TestWatcher_Stop_PreventsNewTimers(t *testing.T) {
	w := NewWatcher(t.TempDir(), 50*time.Millisecond, slogDiscard())
	var fired int32
	w.callback = func() { atomic.AddInt32(&fired, 1) }

	// schedule a callback, then stop before it fires
	w.debounce()
	w.Stop()

	time.Sleep(120 * time.Millisecond)
	assert.Equal(t, int32(0), atomic.LoadInt32(&fired), "pending timer must not fire after Stop")
}

// --- Deterministic detection via injected FileSystem ---

// TestWatcher_MockFS_DetectsChange exercises pollOnce against a mock filesystem,
// proving change detection without touching disk.
func TestWatcher_MockFS_DetectsChange(t *testing.T) {
	mfs := NewMockFileSystem()
	mfs.AddDir("/ledger", []string{"a.json"})

	var fired int32
	w := NewWatcherWithFS("/ledger", 10*time.Millisecond, time.Second, slogDiscard(), mfs)
	w.callback = func() { atomic.AddInt32(&fired, 1) }
	w.prev = w.snapshot() // baseline: a.json

	mfs.AddDir("/ledger", []string{"a.json", "b.json"}) // a new entry appears
	w.pollOnce()

	require.Eventually(t, func() bool { return atomic.LoadInt32(&fired) >= 1 },
		time.Second, 5*time.Millisecond)
}

// TestWatcher_MockFS_ReadDirErrorIsSafe verifies a ReadDir error yields an empty
// snapshot rather than a panic.
func TestWatcher_MockFS_ReadDirErrorIsSafe(t *testing.T) {
	mfs := NewMockFileSystem()
	mfs.AddDir("/ledger", []string{"a.json"})
	w := NewWatcherWithFS("/ledger", time.Millisecond, time.Second, slogDiscard(), mfs)
	w.prev = w.snapshot()

	mfs.SetReadDirError("/ledger", errors.New("boom"))
	assert.NotPanics(t, func() { _ = w.snapshot() })
	assert.Empty(t, w.snapshot(), "error snapshot is empty")
}

// --- FileSystem abstraction tests (retained) ---

func TestMockFileSystem_Stat(t *testing.T) {
	mfs := NewMockFileSystem()
	mfs.AddFile("/x/a.txt", 42, 0o644)

	info, err := mfs.Stat("/x/a.txt")
	require.NoError(t, err)
	assert.Equal(t, "a.txt", info.Name())
	assert.Equal(t, int64(42), info.Size())
	assert.False(t, info.IsDir())

	_, err = mfs.Stat("/missing")
	assert.ErrorIs(t, err, os.ErrNotExist)

	mfs.SetStatError("/boom", errors.New("stat failed"))
	_, err = mfs.Stat("/boom")
	assert.Error(t, err)
}

func TestMockFileSystem_ReadDir(t *testing.T) {
	mfs := NewMockFileSystem()
	mfs.AddDir("/d", []string{"one", "two"})

	entries, err := mfs.ReadDir("/d")
	require.NoError(t, err)
	assert.Len(t, entries, 2)

	n, err := mfs.ReadDirN("/d", 1)
	require.NoError(t, err)
	assert.Len(t, n, 1)

	_, err = mfs.ReadDir("/missing")
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestMockFileInfo(t *testing.T) {
	mt := time.Unix(123, 0)
	fi := mockFileInfo{name: "f", size: 5, mode: 0o644, modTime: mt, isDir: false}
	assert.Equal(t, "f", fi.Name())
	assert.Equal(t, int64(5), fi.Size())
	assert.Equal(t, fs.FileMode(0o644), fi.Mode())
	assert.Equal(t, mt, fi.ModTime())
	assert.False(t, fi.IsDir())
	assert.Nil(t, fi.Sys())
}

func TestMockDirEntry(t *testing.T) {
	de := mockDirEntry{name: "d", isDir: true, mode: fs.ModeDir, info: mockFileInfo{name: "d", isDir: true}}
	assert.Equal(t, "d", de.Name())
	assert.True(t, de.IsDir())
	info, err := de.Info()
	require.NoError(t, err)
	assert.Equal(t, "d", info.Name())
}

func TestRealFileSystem(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("yo"), 0o644))

	rfs := &RealFileSystem{}

	info, err := rfs.Stat(filepath.Join(dir, "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, int64(2), info.Size())

	all, err := rfs.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, all, 2)

	one, err := rfs.ReadDirN(dir, 1)
	require.NoError(t, err)
	assert.Len(t, one, 1)

	allViaN, err := rfs.ReadDirN(dir, 0) // n<=0 returns all
	require.NoError(t, err)
	assert.Len(t, allViaN, 2)
}
