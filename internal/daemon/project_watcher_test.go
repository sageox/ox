package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ChangeAccumulator tests ---

func TestAccumulator_CollapseWrites(t *testing.T) {
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	acc.AddEvent("src/config.go", fsnotify.Write, false)
	acc.AddEvent("src/config.go", fsnotify.Write, false)
	acc.AddEvent("src/config.go", fsnotify.Write, false)

	var changes []FileChange
	require.Eventually(t, func() bool {
		changes = acc.DrainSettled()
		return len(changes) == 1
	}, 2*time.Second, 10*time.Millisecond)
	require.Len(t, changes, 1)
	assert.Equal(t, "src/config.go", changes[0].Path)
	assert.Equal(t, ChangeModified, changes[0].ChangeType)
}

func TestAccumulator_CreateThenDelete(t *testing.T) {
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	acc.AddEvent("tmp/scratch.txt", fsnotify.Create, false)
	acc.AddEvent("tmp/scratch.txt", fsnotify.Remove, false)

	// wait until pending events are fully settled, then verify suppression
	require.Eventually(t, func() bool {
		return acc.PendingCount() == 0
	}, 2*time.Second, 10*time.Millisecond)
	changes := acc.DrainSettled()
	assert.Nil(t, changes, "create+delete of same file should be suppressed")
}

func TestAccumulator_DeleteThenCreate(t *testing.T) {
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	acc.AddEvent("src/config.go", fsnotify.Remove, false)
	acc.AddEvent("src/config.go", fsnotify.Create, false)

	var changes []FileChange
	require.Eventually(t, func() bool {
		changes = acc.DrainSettled()
		return len(changes) == 1
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, ChangeModified, changes[0].ChangeType, "delete+create = atomic save = modified")
}

func TestAccumulator_CreateThenModify(t *testing.T) {
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	acc.AddEvent("src/new.go", fsnotify.Create, false)
	acc.AddEvent("src/new.go", fsnotify.Write, false)

	var changes []FileChange
	require.Eventually(t, func() bool {
		changes = acc.DrainSettled()
		return len(changes) == 1
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, ChangeCreated, changes[0].ChangeType, "create+write should stay as created")
}

func TestAccumulator_SettleTimer(t *testing.T) {
	acc := NewChangeAccumulator(100 * time.Millisecond)
	defer acc.Stop()

	acc.AddEvent("src/foo.go", fsnotify.Write, false)

	// before settle period
	changes := acc.DrainSettled()
	assert.Nil(t, changes, "changes should not be available before settle period")

	// wait for settle
	require.Eventually(t, func() bool {
		changes = acc.DrainSettled()
		return len(changes) == 1
	}, 2*time.Second, 10*time.Millisecond)
}

func TestAccumulator_DrainClears(t *testing.T) {
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	acc.AddEvent("src/foo.go", fsnotify.Write, false)

	var changes []FileChange
	require.Eventually(t, func() bool {
		changes = acc.DrainSettled()
		return len(changes) == 1
	}, 2*time.Second, 10*time.Millisecond)

	// second drain should return nil
	changes = acc.DrainSettled()
	assert.Nil(t, changes)
}

func TestAccumulator_MultipleFiles(t *testing.T) {
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	acc.AddEvent("src/a.go", fsnotify.Create, false)
	acc.AddEvent("src/b.go", fsnotify.Write, false)
	acc.AddEvent("src/c.go", fsnotify.Remove, false)

	var changes []FileChange
	require.Eventually(t, func() bool {
		changes = acc.DrainSettled()
		return len(changes) == 3
	}, 2*time.Second, 10*time.Millisecond)

	byPath := make(map[string]ChangeType)
	for _, c := range changes {
		byPath[c.Path] = c.ChangeType
	}
	assert.Equal(t, ChangeCreated, byPath["src/a.go"])
	assert.Equal(t, ChangeModified, byPath["src/b.go"])
	assert.Equal(t, ChangeDeleted, byPath["src/c.go"])
}

func TestAccumulator_SettleResetsOnNewEvent(t *testing.T) {
	acc := NewChangeAccumulator(100 * time.Millisecond)
	defer acc.Stop()

	acc.AddEvent("src/foo.go", fsnotify.Write, false)
	time.Sleep(60 * time.Millisecond)                 // 60ms < 100ms settle
	acc.AddEvent("src/bar.go", fsnotify.Write, false) // resets timer

	// at 60ms after first event, timer was reset — should not have settled yet
	changes := acc.DrainSettled()
	assert.Nil(t, changes)

	// wait for full settle period from last event
	require.Eventually(t, func() bool {
		changes = acc.DrainSettled()
		return len(changes) == 2
	}, 2*time.Second, 10*time.Millisecond)
}

// --- GitTrackedMatcher tests ---

func TestGitTrackedMatcher_RootAlwaysTracked(t *testing.T) {
	m := &GitTrackedMatcher{
		projectRoot:  t.TempDir(),
		trackedDirs:  map[string]struct{}{},
		trackedFiles: map[string]struct{}{},
		logger:       slogDiscard(),
	}
	assert.True(t, m.IsTrackedDir("."))
	assert.True(t, m.IsTrackedDir(""))
}

func TestGitTrackedMatcher_GitDirAlwaysIgnored(t *testing.T) {
	m := &GitTrackedMatcher{
		projectRoot:  t.TempDir(),
		trackedDirs:  map[string]struct{}{".git": {}},
		trackedFiles: map[string]struct{}{},
		logger:       slogDiscard(),
	}
	assert.False(t, m.IsTrackedDir(".git"))
	assert.False(t, m.IsTrackedDir(".git/objects/pack"))
}

func TestGitTrackedMatcher_TrackedAndUntracked(t *testing.T) {
	m := &GitTrackedMatcher{
		projectRoot: t.TempDir(),
		trackedDirs: map[string]struct{}{
			"src":          {},
			"src/internal": {},
		},
		trackedFiles: map[string]struct{}{
			"src/main.go":          {},
			"src/internal/util.go": {},
		},
		logger: slogDiscard(),
	}

	assert.True(t, m.IsTrackedDir("src"))
	assert.True(t, m.IsTrackedDir("src/internal"))
	assert.False(t, m.IsTrackedDir("node_modules"))
	assert.False(t, m.IsTrackedDir("build"))

	assert.True(t, m.IsTrackedFile("src/main.go"))
	assert.True(t, m.IsTrackedFile("src/internal/util.go"))
	assert.False(t, m.IsTrackedFile("untracked.txt"))
}

func TestGitTrackedMatcher_TrackedDirsSorted(t *testing.T) {
	m := &GitTrackedMatcher{
		projectRoot: t.TempDir(),
		trackedDirs: map[string]struct{}{
			"zzz": {},
			"aaa": {},
			"mmm": {},
		},
		trackedFiles: map[string]struct{}{},
		logger:       slogDiscard(),
	}
	dirs := m.TrackedDirs()
	assert.Equal(t, []string{"aaa", "mmm", "zzz"}, dirs)
}

// --- ProjectWatcher tests ---

func TestProjectWatcher_WatchesTrackedDirs(t *testing.T) {
	if testing.Short() {
		t.Skip("short: watcher polling can take up to 2s")
	}
	mockWatcher := NewMockFileSystemWatcher()
	mockFS := NewMockFileSystem()

	dir := t.TempDir()
	sub1 := filepath.Join(dir, "src")
	sub2 := filepath.Join(dir, "src", "internal")
	require.NoError(t, os.MkdirAll(sub2, 0755))

	// mock FS to report these as directories
	mockFS.AddDir(sub1, nil)
	mockFS.AddDir(sub2, nil)

	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	tracker := &GitTrackedMatcher{
		projectRoot: dir,
		trackedDirs: map[string]struct{}{
			"src":          {},
			"src/internal": {},
		},
		trackedFiles: map[string]struct{}{},
		logger:       slogDiscard(),
	}

	pw := NewProjectWatcher(
		dir, slogDiscard(),
		func() (FileSystemWatcher, error) { return mockWatcher, nil },
		mockFS, acc, tracker,
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pw.Start(ctx)
		close(done)
	}()

	require.Eventually(t, func() bool {
		paths := mockWatcher.AddedPaths()
		return len(paths) >= 3
	}, 2*time.Second, 10*time.Millisecond)

	paths := mockWatcher.AddedPaths()
	assert.Contains(t, paths, dir)
	assert.Contains(t, paths, sub1)
	assert.Contains(t, paths, sub2)

	cancel()
	<-done
}

func TestProjectWatcher_UntrackedNotWatched(t *testing.T) {
	if testing.Short() {
		t.Skip("short: watcher polling can take up to 2s")
	}
	mockWatcher := NewMockFileSystemWatcher()
	mockFS := NewMockFileSystem()

	dir := t.TempDir()
	srcDir := filepath.Join(dir, "src")
	nmDir := filepath.Join(dir, "node_modules")
	buildDir := filepath.Join(dir, "build")
	require.NoError(t, os.MkdirAll(srcDir, 0755))
	require.NoError(t, os.MkdirAll(nmDir, 0755))
	require.NoError(t, os.MkdirAll(buildDir, 0755))

	mockFS.AddDir(srcDir, nil)
	mockFS.AddDir(nmDir, nil)
	mockFS.AddDir(buildDir, nil)

	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	// only src is tracked
	tracker := &GitTrackedMatcher{
		projectRoot:  dir,
		trackedDirs:  map[string]struct{}{"src": {}},
		trackedFiles: map[string]struct{}{},
		logger:       slogDiscard(),
	}

	pw := NewProjectWatcher(
		dir, slogDiscard(),
		func() (FileSystemWatcher, error) { return mockWatcher, nil },
		mockFS, acc, tracker,
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pw.Start(ctx)
		close(done)
	}()

	require.Eventually(t, func() bool {
		return len(mockWatcher.AddedPaths()) >= 2
	}, 2*time.Second, 10*time.Millisecond)

	paths := mockWatcher.AddedPaths()
	assert.Contains(t, paths, dir)
	assert.Contains(t, paths, srcDir)
	assert.NotContains(t, paths, nmDir)
	assert.NotContains(t, paths, buildDir)

	cancel()
	<-done
}

func TestProjectWatcher_EventsReachAccumulator(t *testing.T) {
	if testing.Short() {
		t.Skip("short: watcher polling can take up to 2s")
	}
	mockWatcher := NewMockFileSystemWatcher()
	mockFS := NewMockFileSystem()

	dir := t.TempDir()

	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	tracker := &GitTrackedMatcher{
		projectRoot:  dir,
		trackedDirs:  map[string]struct{}{},
		trackedFiles: map[string]struct{}{"main.go": {}},
		logger:       slogDiscard(),
	}

	pw := NewProjectWatcher(
		dir, slogDiscard(),
		func() (FileSystemWatcher, error) { return mockWatcher, nil },
		mockFS, acc, tracker,
	)

	filePath := filepath.Join(dir, "main.go")
	mockFS.AddFile(filePath, 100, 0644)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pw.Start(ctx)
		close(done)
	}()

	require.Eventually(t, func() bool {
		return len(mockWatcher.AddedPaths()) > 0
	}, 2*time.Second, 10*time.Millisecond)

	mockWatcher.SendEvent(fsnotify.Event{
		Name: filePath,
		Op:   fsnotify.Write,
	})

	var changes []FileChange
	require.Eventually(t, func() bool {
		changes = acc.DrainSettled()
		return len(changes) == 1
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, "main.go", changes[0].Path)
	assert.Equal(t, ChangeModified, changes[0].ChangeType)

	cancel()
	<-done
}

func TestProjectWatcher_UntrackedFileEventsFiltered(t *testing.T) {
	if testing.Short() {
		t.Skip("short: watcher polling can take up to 2s")
	}
	mockWatcher := NewMockFileSystemWatcher()
	mockFS := NewMockFileSystem()

	dir := t.TempDir()

	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	tracker := &GitTrackedMatcher{
		projectRoot:  dir,
		trackedDirs:  map[string]struct{}{},
		trackedFiles: map[string]struct{}{"main.go": {}},
		logger:       slogDiscard(),
	}

	pw := NewProjectWatcher(
		dir, slogDiscard(),
		func() (FileSystemWatcher, error) { return mockWatcher, nil },
		mockFS, acc, tracker,
	)

	trackedFile := filepath.Join(dir, "main.go")
	untrackedFile := filepath.Join(dir, "scratch.tmp")
	mockFS.AddFile(trackedFile, 100, 0644)
	mockFS.AddFile(untrackedFile, 50, 0644)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pw.Start(ctx)
		close(done)
	}()

	require.Eventually(t, func() bool {
		return len(mockWatcher.AddedPaths()) > 0
	}, 2*time.Second, 10*time.Millisecond)

	// modify both tracked and untracked files
	mockWatcher.SendEvent(fsnotify.Event{Name: trackedFile, Op: fsnotify.Write})
	mockWatcher.SendEvent(fsnotify.Event{Name: untrackedFile, Op: fsnotify.Write})

	var changes []FileChange
	require.Eventually(t, func() bool {
		changes = acc.DrainSettled()
		return len(changes) == 1
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, "main.go", changes[0].Path)

	cancel()
	<-done
}

func TestProjectWatcher_NewFileCreationPassesThrough(t *testing.T) {
	if testing.Short() {
		t.Skip("short: watcher polling can take up to 2s")
	}
	mockWatcher := NewMockFileSystemWatcher()
	mockFS := NewMockFileSystem()

	dir := t.TempDir()

	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	tracker := &GitTrackedMatcher{
		projectRoot:  dir,
		trackedDirs:  map[string]struct{}{},
		trackedFiles: map[string]struct{}{},
		logger:       slogDiscard(),
	}

	pw := NewProjectWatcher(
		dir, slogDiscard(),
		func() (FileSystemWatcher, error) { return mockWatcher, nil },
		mockFS, acc, tracker,
	)

	newFile := filepath.Join(dir, "new.go")
	mockFS.AddFile(newFile, 100, 0644)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pw.Start(ctx)
		close(done)
	}()

	require.Eventually(t, func() bool {
		return len(mockWatcher.AddedPaths()) > 0
	}, 2*time.Second, 10*time.Millisecond)

	// create event passes through even for untracked files
	mockWatcher.SendEvent(fsnotify.Event{Name: newFile, Op: fsnotify.Create})

	var changes []FileChange
	require.Eventually(t, func() bool {
		changes = acc.DrainSettled()
		return len(changes) == 1
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, "new.go", changes[0].Path)
	assert.Equal(t, ChangeCreated, changes[0].ChangeType)

	cancel()
	<-done
}

// slogDiscard returns a logger that discards output.
func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// --- OnSettled callback tests ---

// TestChangeAccumulator_OnSettledCallback verifies that the onSettled callback
// fires after changes settle.
// Failure prevented: ChangeAccumulator settles but codedb never learns about it.
func TestChangeAccumulator_OnSettledCallback(t *testing.T) {
	t.Parallel()

	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	called := make(chan struct{}, 1)
	acc.SetOnSettled(func() {
		select {
		case called <- struct{}{}:
		default:
		}
	})

	acc.AddEvent("src/main.go", fsnotify.Write, false)

	select {
	case <-called:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("onSettled callback not called after settle")
	}
}

// TestChangeAccumulator_OnSettledNotCalledAfterStop verifies that the callback
// doesn't fire after the accumulator is stopped.
// Failure prevented: stale callback fires during daemon shutdown.
func TestChangeAccumulator_OnSettledNotCalledAfterStop(t *testing.T) {
	t.Parallel()

	acc := NewChangeAccumulator(100 * time.Millisecond)

	var count int64
	acc.SetOnSettled(func() {
		atomic.AddInt64(&count, 1)
	})

	acc.AddEvent("src/main.go", fsnotify.Write, false)
	// stop before settle fires
	time.Sleep(20 * time.Millisecond)
	acc.Stop()

	// wait past settle window
	time.Sleep(200 * time.Millisecond)

	assert.Equal(t, int64(0), atomic.LoadInt64(&count), "callback should not fire after Stop()")
}

// TestChangeAccumulator_OnSettledNotCalledWhenEmpty verifies that the callback
// doesn't fire when there are no pending changes (settle with empty pending).
// Failure prevented: spurious dirty overlay rebuilds when no files changed.
func TestChangeAccumulator_OnSettledNotCalledWhenEmpty(t *testing.T) {
	t.Parallel()

	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	var count int64
	acc.SetOnSettled(func() {
		atomic.AddInt64(&count, 1)
	})

	// add and drain before settle
	acc.AddEvent("src/main.go", fsnotify.Write, false)

	// wait for first settle to fire callback
	require.Eventually(t, func() bool {
		return atomic.LoadInt64(&count) == 1
	}, 2*time.Second, 10*time.Millisecond)

	// no new events — next settle timer shouldn't fire the callback
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int64(1), atomic.LoadInt64(&count), "callback should not fire with no pending changes")
}

// --- FD leak regression tests ---

// TestProjectWatcher_RemoveOnDirectoryDelete verifies that deleting a watched
// directory calls watcher.Remove() to release the kqueue/inotify FD.
// Without this fix, deleted directory watches leak FDs indefinitely.
func TestProjectWatcher_RemoveOnDirectoryDelete(t *testing.T) {
	if testing.Short() {
		t.Skip("short: watcher polling")
	}
	mockWatcher := NewMockFileSystemWatcher()
	mockFS := NewMockFileSystem()

	dir := t.TempDir()
	srcDir := filepath.Join(dir, "src")
	require.NoError(t, os.MkdirAll(srcDir, 0755))
	mockFS.AddDir(srcDir, nil)

	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	tracker := &GitTrackedMatcher{
		projectRoot:  dir,
		trackedDirs:  map[string]struct{}{"src": {}},
		trackedFiles: map[string]struct{}{},
		logger:       slogDiscard(),
	}

	pw := NewProjectWatcher(
		dir, slogDiscard(),
		func() (FileSystemWatcher, error) { return mockWatcher, nil },
		mockFS, acc, tracker,
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pw.Start(ctx)
		close(done)
	}()

	// wait for initial watches
	require.Eventually(t, func() bool {
		return len(mockWatcher.AddedPaths()) >= 2
	}, 2*time.Second, 10*time.Millisecond)

	// simulate directory deletion
	mockFS.SetStatError(srcDir, os.ErrNotExist)
	mockWatcher.SendEvent(fsnotify.Event{
		Name: srcDir,
		Op:   fsnotify.Remove,
	})

	// verify Remove() was called
	require.Eventually(t, func() bool {
		return len(mockWatcher.RemovedPaths()) >= 1
	}, 2*time.Second, 10*time.Millisecond)

	removed := mockWatcher.RemovedPaths()
	assert.Contains(t, removed, srcDir, "watcher.Remove must be called for deleted directory")
	assert.Equal(t, 1, pw.WatchedDirCount(), "only root should remain watched")

	cancel()
	<-done
}

// TestProjectWatcher_PruneStaleWatches verifies that directories which are no
// longer git-tracked are pruned on the next refresh cycle.
// Without this, non-tracked directories (build/, tmp/) accumulate watches.
func TestProjectWatcher_PruneStaleWatches(t *testing.T) {
	mockWatcher := NewMockFileSystemWatcher()
	mockFS := NewMockFileSystem()

	dir := t.TempDir()
	srcDir := filepath.Join(dir, "src")
	buildDir := filepath.Join(dir, "build")
	require.NoError(t, os.MkdirAll(srcDir, 0755))
	require.NoError(t, os.MkdirAll(buildDir, 0755))
	mockFS.AddDir(srcDir, nil)
	mockFS.AddDir(buildDir, nil)

	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	tracker := &GitTrackedMatcher{
		projectRoot:  dir,
		trackedDirs:  map[string]struct{}{"src": {}, "build": {}},
		trackedFiles: map[string]struct{}{},
		logger:       slogDiscard(),
	}

	pw := NewProjectWatcher(
		dir, slogDiscard(),
		func() (FileSystemWatcher, error) { return mockWatcher, nil },
		mockFS, acc, tracker,
	)

	// simulate walkAndWatch to populate watchedDirs
	pw.walkAndWatch(mockWatcher)
	require.Equal(t, 3, pw.WatchedDirCount(), "root + src + build")

	// now remove "build" from tracked dirs (simulating git branch switch)
	tracker.mu.Lock()
	delete(tracker.trackedDirs, "build")
	tracker.mu.Unlock()

	// prune
	pw.pruneStaleWatches(mockWatcher)

	assert.Equal(t, 2, pw.WatchedDirCount(), "root + src should remain")
	removed := mockWatcher.RemovedPaths()
	assert.Contains(t, removed, buildDir, "build dir should be pruned")
}

// TestProjectWatcher_FDStability verifies that Add and Remove counts stay
// balanced across create+delete cycles — the core FD leak invariant.
func TestProjectWatcher_FDStability(t *testing.T) {
	if testing.Short() {
		t.Skip("short: watcher polling")
	}
	mockWatcher := NewMockFileSystemWatcher()
	mockFS := NewMockFileSystem()

	dir := t.TempDir()
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	tracker := &GitTrackedMatcher{
		projectRoot:  dir,
		trackedDirs:  map[string]struct{}{},
		trackedFiles: map[string]struct{}{},
		logger:       slogDiscard(),
	}

	pw := NewProjectWatcher(
		dir, slogDiscard(),
		func() (FileSystemWatcher, error) { return mockWatcher, nil },
		mockFS, acc, tracker,
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pw.Start(ctx)
		close(done)
	}()

	// wait for root watch
	require.Eventually(t, func() bool {
		return len(mockWatcher.AddedPaths()) >= 1
	}, 2*time.Second, 10*time.Millisecond)

	initialAdds := len(mockWatcher.AddedPaths())

	// simulate 10 create+delete directory cycles
	for i := 0; i < 10; i++ {
		tmpDir := filepath.Join(dir, "tmp", time.Now().Format("150405.000"))
		mockFS.AddDir(tmpDir, nil)
		mockWatcher.SendEvent(fsnotify.Event{Name: tmpDir, Op: fsnotify.Create})

		// wait for add
		time.Sleep(20 * time.Millisecond)

		// delete
		mockFS.SetStatError(tmpDir, os.ErrNotExist)
		mockWatcher.SendEvent(fsnotify.Event{Name: tmpDir, Op: fsnotify.Remove})

		// wait for remove
		time.Sleep(20 * time.Millisecond)
	}

	// allow events to settle
	time.Sleep(100 * time.Millisecond)

	adds := len(mockWatcher.AddedPaths()) - initialAdds
	removes := len(mockWatcher.RemovedPaths())
	assert.Equal(t, adds, removes,
		"every Add from a create event must have a matching Remove on delete (adds=%d, removes=%d)", adds, removes)

	cancel()
	<-done
}

// TestProjectWatcher_RemoveUnwatchesChildrenRecursively verifies that when a
// watched directory is removed, every descendant directory we were watching
// is also unwatched. fsnotify v1.9.0's kqueue backend does not recursively
// close children on NOTE_DELETE (see backend_kqueue.go:489 — it calls
// w.remove(name, false), the no-op-children branch). Without our recursive
// cleanup, FDs leak per descendant on every rm-rf, which is exactly the
// 226-FDs-against-one-inode pattern observed in the daemon FD-leak diagnosis.
func TestProjectWatcher_RemoveUnwatchesChildrenRecursively(t *testing.T) {
	mockWatcher := NewMockFileSystemWatcher()
	mockFS := NewMockFileSystem()

	dir := t.TempDir()
	parent := filepath.Join(dir, ".context")
	childA := filepath.Join(parent, "build")
	childB := filepath.Join(parent, "build", "esp32")
	sibling := filepath.Join(dir, "src")

	for _, d := range []string{parent, childA, childB, sibling} {
		require.NoError(t, os.MkdirAll(d, 0o755))
		mockFS.AddDir(d, nil)
	}

	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	tracker := &GitTrackedMatcher{
		projectRoot: dir,
		trackedDirs: map[string]struct{}{
			".context":             {},
			".context/build":       {},
			".context/build/esp32": {},
			"src":                  {},
		},
		trackedFiles: map[string]struct{}{},
		logger:       slogDiscard(),
	}

	pw := NewProjectWatcher(
		dir, slogDiscard(),
		func() (FileSystemWatcher, error) { return mockWatcher, nil },
		mockFS, acc, tracker,
	)

	pw.walkAndWatch(mockWatcher)
	require.Equal(t, 5, pw.WatchedDirCount(), "root + .context + build + esp32 + src")

	// simulate rm -rf .context — fsnotify only emits a single Remove for the
	// top-level dir (the kqueue NOTE_DELETE on the parent), not one per child.
	mockFS.SetStatError(parent, os.ErrNotExist)
	pw.handleEvent(mockWatcher, fsnotify.Event{Name: parent, Op: fsnotify.Remove})

	assert.Equal(t, 2, pw.WatchedDirCount(),
		"only root + sibling should remain after recursive cleanup; got %d", pw.WatchedDirCount())

	removed := mockWatcher.RemovedPaths()
	assert.Contains(t, removed, parent, "parent must be unwatched")
	assert.Contains(t, removed, childA, "child must be unwatched (fsnotify v1.9 leaks this without our help)")
	assert.Contains(t, removed, childB, "grandchild must be unwatched (fsnotify v1.9 leaks this without our help)")
	assert.NotContains(t, removed, sibling, "unrelated sibling must not be unwatched")
}

// TestProjectWatcher_CreateSkipsAlreadyWatched verifies addDir is not called
// twice for the same path. Defensive against the diagnosed re-Create storm
// where a stale Remove leaves an orphan FD and a subsequent Create on the
// same path would otherwise drive a fresh unix.Open() each time.
func TestProjectWatcher_CreateSkipsAlreadyWatched(t *testing.T) {
	mockWatcher := NewMockFileSystemWatcher()
	mockFS := NewMockFileSystem()

	dir := t.TempDir()
	sub := filepath.Join(dir, "pkg")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	mockFS.AddDir(sub, nil)

	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	tracker := &GitTrackedMatcher{
		projectRoot:  dir,
		trackedDirs:  map[string]struct{}{"pkg": {}},
		trackedFiles: map[string]struct{}{},
		logger:       slogDiscard(),
	}

	pw := NewProjectWatcher(
		dir, slogDiscard(),
		func() (FileSystemWatcher, error) { return mockWatcher, nil },
		mockFS, acc, tracker,
	)

	pw.walkAndWatch(mockWatcher)
	beforeAdds := len(mockWatcher.AddedPaths())

	// fire 10 spurious Create events on a path we already watch
	for i := 0; i < 10; i++ {
		pw.handleEvent(mockWatcher, fsnotify.Event{Name: sub, Op: fsnotify.Create})
	}

	addsForPath := 0
	for _, p := range mockWatcher.AddedPaths() {
		if p == sub {
			addsForPath++
		}
	}
	assert.Equal(t, 1, addsForPath, "Add must be called exactly once per path")
	assert.Equal(t, beforeAdds, len(mockWatcher.AddedPaths()),
		"redundant Create events must not call Add again")
}

// --- Per-file FD-leak tests (epic ox-5pwx) ---

// forceChildMirrorForTest enables the kqueue child mirror for the duration
// of the test, regardless of GOOS. The mirror is gated by
// childMirrorEnabled = (runtime.GOOS == "darwin") in production, so without
// this helper the mock-based tests below would silently no-op on Linux CI
// runners and still pass — which would defeat their purpose. Restored in
// t.Cleanup so test order doesn't matter.
func forceChildMirrorForTest(t *testing.T) {
	t.Helper()
	old := childMirrorEnabled
	childMirrorEnabled = true
	t.Cleanup(func() { childMirrorEnabled = old })
}

// TestProjectWatcher_AddDir_SnapshotsChildren verifies that addDir captures
// the per-file child set so Remove/Rename can replay it. fsnotify's kqueue
// backend opens 1+N FDs per watched dir (1 dir + N per file via
// watchDirectoryFiles); without the snapshot we can't tell fsnotify to close
// the per-file FDs on rm-rf.
// Failure prevented: per-file FD leak invisible to userspace because
// fsnotify exposes it only through a package-private map.
func TestProjectWatcher_AddDir_SnapshotsChildren(t *testing.T) {
	forceChildMirrorForTest(t)
	mockWatcher := NewMockFileSystemWatcher()
	mockFS := NewMockFileSystem()

	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "pkg")
	require.NoError(t, os.MkdirAll(pkgDir, 0o755))
	mockFS.AddDir(pkgDir, []string{"a.go", "b.go", "c.go"})

	pw := NewProjectWatcher(
		dir, slogDiscard(),
		func() (FileSystemWatcher, error) { return mockWatcher, nil },
		mockFS, NewChangeAccumulator(50*time.Millisecond),
		&GitTrackedMatcher{
			projectRoot: dir, trackedDirs: map[string]struct{}{}, trackedFiles: map[string]struct{}{},
			logger: slogDiscard(),
		},
	)

	pw.addDir(mockWatcher, pkgDir)

	pw.mu.Lock()
	children := append([]string(nil), pw.dirChildren[pkgDir]...)
	pw.mu.Unlock()

	assert.ElementsMatch(t,
		[]string{
			filepath.Join(pkgDir, "a.go"),
			filepath.Join(pkgDir, "b.go"),
			filepath.Join(pkgDir, "c.go"),
		},
		children,
		"addDir must snapshot all non-dir entries as absolute paths")
}

// TestProjectWatcher_RemoveCallsUnwatchOnChildFiles verifies the actual leak
// fix: on Remove/Rename of a watched directory, watcher.Remove must be called
// for every child file we knew about, not just the directory itself. This is
// what forces fsnotify's kqueue backend down its file-Remove path
// (backend_kqueue.go:283 → unix.Close at :302). Without per-file Remove calls,
// fsnotify v1.9.0 leaks one FD per child on every dir Remove/Rename.
// Failure prevented: long-lived daemon FD growth from build-output churn,
// the reproducible host symptom that made `lsof` itself hang on the daemon PID.
func TestProjectWatcher_RemoveCallsUnwatchOnChildFiles(t *testing.T) {
	forceChildMirrorForTest(t)
	mockWatcher := NewMockFileSystemWatcher()
	mockFS := NewMockFileSystem()

	dir := t.TempDir()
	pkg := filepath.Join(dir, "pkg")
	subPkg := filepath.Join(pkg, "internal")
	require.NoError(t, os.MkdirAll(subPkg, 0o755))
	mockFS.AddDir(pkg, []string{"a.go", "b.go"})
	mockFS.AddDir(subPkg, []string{"x.go"})

	tracker := &GitTrackedMatcher{
		projectRoot: dir,
		trackedDirs: map[string]struct{}{
			"pkg":          {},
			"pkg/internal": {},
		},
		trackedFiles: map[string]struct{}{},
		logger:       slogDiscard(),
	}

	pw := NewProjectWatcher(
		dir, slogDiscard(),
		func() (FileSystemWatcher, error) { return mockWatcher, nil },
		mockFS, NewChangeAccumulator(50*time.Millisecond), tracker,
	)

	pw.walkAndWatch(mockWatcher)
	require.Equal(t, 3, pw.WatchedDirCount(), "root + pkg + pkg/internal")

	// simulate rm -rf pkg — single Remove event for pkg
	mockFS.SetStatError(pkg, os.ErrNotExist)
	pw.handleEvent(mockWatcher, fsnotify.Event{Name: pkg, Op: fsnotify.Remove})

	removed := mockWatcher.RemovedPaths()

	// Dir-level removes: pkg and pkg/internal
	assert.Contains(t, removed, pkg, "pkg dir must be unwatched")
	assert.Contains(t, removed, subPkg, "descendant dir must be unwatched")

	// THE NEW INVARIANT: per-file Remove calls. fsnotify v1.9.0 won't close
	// these FDs on its own — we must ask explicitly.
	assert.Contains(t, removed, filepath.Join(pkg, "a.go"),
		"pkg/a.go must be Remove'd to close fsnotify's per-file FD")
	assert.Contains(t, removed, filepath.Join(pkg, "b.go"),
		"pkg/b.go must be Remove'd to close fsnotify's per-file FD")
	assert.Contains(t, removed, filepath.Join(subPkg, "x.go"),
		"descendant child files must be Remove'd too (descendant snapshots flushed)")

	// And the maps are cleaned up.
	pw.mu.Lock()
	_, dirStillTracked := pw.watchedDirs[pkg]
	_, childrenStillTracked := pw.dirChildren[pkg]
	_, descendantStillTracked := pw.dirChildren[subPkg]
	pw.mu.Unlock()
	assert.False(t, dirStillTracked, "watchedDirs entry must be cleared")
	assert.False(t, childrenStillTracked, "dirChildren entry must be cleared")
	assert.False(t, descendantStillTracked, "descendant dirChildren must be cleared")
}

// TestProjectWatcher_PruneStaleWatches_RemovesChildFiles verifies the same
// invariant via the periodic-prune path: a dir falling out of git-tracked set
// must drop its per-file FDs too, not just the dir's own FD.
// Failure prevented: gitignore changes / branch switches accumulate per-file
// FDs over the lifetime of the daemon.
func TestProjectWatcher_PruneStaleWatches_RemovesChildFiles(t *testing.T) {
	forceChildMirrorForTest(t)
	mockWatcher := NewMockFileSystemWatcher()
	mockFS := NewMockFileSystem()

	dir := t.TempDir()
	buildDir := filepath.Join(dir, "build")
	require.NoError(t, os.MkdirAll(buildDir, 0o755))
	mockFS.AddDir(buildDir, []string{"out1.o", "out2.o", "out3.o"})

	tracker := &GitTrackedMatcher{
		projectRoot:  dir,
		trackedDirs:  map[string]struct{}{"build": {}},
		trackedFiles: map[string]struct{}{},
		logger:       slogDiscard(),
	}

	pw := NewProjectWatcher(
		dir, slogDiscard(),
		func() (FileSystemWatcher, error) { return mockWatcher, nil },
		mockFS, NewChangeAccumulator(50*time.Millisecond), tracker,
	)

	pw.walkAndWatch(mockWatcher)
	require.Equal(t, 2, pw.WatchedDirCount(), "root + build")

	// build/ is no longer tracked (e.g., added to .gitignore)
	tracker.mu.Lock()
	delete(tracker.trackedDirs, "build")
	tracker.mu.Unlock()

	pw.pruneStaleWatches(mockWatcher)

	removed := mockWatcher.RemovedPaths()
	assert.Contains(t, removed, buildDir, "build dir must be unwatched")
	assert.Contains(t, removed, filepath.Join(buildDir, "out1.o"),
		"per-file FD must be released on prune")
	assert.Contains(t, removed, filepath.Join(buildDir, "out2.o"),
		"per-file FD must be released on prune")
	assert.Contains(t, removed, filepath.Join(buildDir, "out3.o"),
		"per-file FD must be released on prune")

	pw.mu.Lock()
	_, stillInChildren := pw.dirChildren[buildDir]
	pw.mu.Unlock()
	assert.False(t, stillInChildren, "dirChildren entry must be cleared on prune")
}

// TestProjectWatcher_SnapshotChildren_BoundedAtCap verifies the
// maxFilesPerWatchedDir cap. A pathological dir (millions of generated files)
// would otherwise dominate startup memory and snapshot cost. The cap is a
// safe-degradation: files beyond the cap lose the leak guarantee, but the
// ungranted FDs are bounded by file count regardless.
// Failure prevented: cold-start memory/CPU spike on monorepos with one
// pathological generated-files directory.
func TestProjectWatcher_SnapshotChildren_BoundedAtCap(t *testing.T) {
	forceChildMirrorForTest(t)
	mockWatcher := NewMockFileSystemWatcher()
	mockFS := NewMockFileSystem()

	dir := t.TempDir()
	bigDir := filepath.Join(dir, "generated")
	require.NoError(t, os.MkdirAll(bigDir, 0o755))

	// Create a dir with maxFilesPerWatchedDir + 100 entries. ReadDir returns
	// all of them; the cap should kick in inside snapshotDirFiles.
	overflow := 100
	names := make([]string, 0, maxFilesPerWatchedDir+overflow)
	for i := 0; i < maxFilesPerWatchedDir+overflow; i++ {
		names = append(names, fmt.Sprintf("f%06d.go", i))
	}
	mockFS.AddDir(bigDir, names)

	pw := NewProjectWatcher(
		dir, slogDiscard(),
		func() (FileSystemWatcher, error) { return mockWatcher, nil },
		mockFS, NewChangeAccumulator(50*time.Millisecond),
		&GitTrackedMatcher{
			projectRoot: dir, trackedDirs: map[string]struct{}{}, trackedFiles: map[string]struct{}{},
			logger: slogDiscard(),
		},
	)

	pw.addDir(mockWatcher, bigDir)

	pw.mu.Lock()
	got := len(pw.dirChildren[bigDir])
	pw.mu.Unlock()
	assert.Equal(t, maxFilesPerWatchedDir, got,
		"snapshotDirFiles must cap at maxFilesPerWatchedDir; got %d", got)
}

// TestProjectWatcher_AcquireRelease_TypedHandle verifies the watchedDirHandle
// discipline helper: acquireDir+Release must produce the same effect as
// addDir+Remove-with-children, with no extra leaks and no extra dirs left in
// the maps. Not RAII (Go has no RAII for resources outliving function scope),
// but enforces register-and-release pairing through the type system.
// Failure prevented: future caller adds a watch via the handle but forgets
// the children-cleanup half of the contract.
func TestProjectWatcher_AcquireRelease_TypedHandle(t *testing.T) {
	forceChildMirrorForTest(t)
	mockWatcher := NewMockFileSystemWatcher()
	mockFS := NewMockFileSystem()

	dir := t.TempDir()
	pkg := filepath.Join(dir, "pkg")
	require.NoError(t, os.MkdirAll(pkg, 0o755))
	mockFS.AddDir(pkg, []string{"main.go", "main_test.go"})

	pw := NewProjectWatcher(
		dir, slogDiscard(),
		func() (FileSystemWatcher, error) { return mockWatcher, nil },
		mockFS, NewChangeAccumulator(50*time.Millisecond),
		&GitTrackedMatcher{
			projectRoot: dir, trackedDirs: map[string]struct{}{}, trackedFiles: map[string]struct{}{},
			logger: slogDiscard(),
		},
	)

	h, err := pw.acquireDir(mockWatcher, pkg)
	require.NoError(t, err)
	require.NotNil(t, h)

	pw.mu.Lock()
	_, watched := pw.watchedDirs[pkg]
	childCount := len(pw.dirChildren[pkg])
	pw.mu.Unlock()
	assert.True(t, watched, "acquireDir must register the dir")
	assert.Equal(t, 2, childCount, "acquireDir must snapshot children")

	h.Release()

	pw.mu.Lock()
	_, stillWatched := pw.watchedDirs[pkg]
	_, stillChildren := pw.dirChildren[pkg]
	pw.mu.Unlock()
	assert.False(t, stillWatched, "Release must clear watchedDirs entry")
	assert.False(t, stillChildren, "Release must clear dirChildren entry")

	removed := mockWatcher.RemovedPaths()
	assert.Contains(t, removed, pkg, "Release must Remove the dir")
	assert.Contains(t, removed, filepath.Join(pkg, "main.go"),
		"Release must Remove each tracked child file")
	assert.Contains(t, removed, filepath.Join(pkg, "main_test.go"),
		"Release must Remove each tracked child file")

	// Idempotent: second Release is a no-op (children list now empty).
	preCount := len(mockWatcher.RemovedPaths())
	h.Release()
	postCount := len(mockWatcher.RemovedPaths())
	// Second Release issues a single Remove for the dir itself (harmless,
	// fsnotify returns ErrNonExistentWatch which we ignore). What matters
	// is that no new per-file Removes happen.
	assert.LessOrEqual(t, postCount-preCount, 1, "Release must be idempotent for child files")
}

// TestProjectWatcher_NoFDLeak_RealWatcher_Darwin is the production-grade
// regression test. It uses a REAL fsnotify watcher, real os.MkdirAll +
// os.RemoveAll, and counts open FDs on the test process via /dev/fd. This
// is the test that would have caught the original leak — the mock-based
// tests can't, because the mock has no internal per-file FDs to leak.
//
// macOS-only: fsnotify's kqueue backend is the source of the leak. Linux's
// inotify uses one FD per watch (not per file), so the leak doesn't exist
// there.
//
// Failure prevented: regression of the FD leak that, in production, made
// the daemon's FD table grow unbounded and `lsof` hang on the daemon PID.
func TestProjectWatcher_NoFDLeak_RealWatcher_Darwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("kqueue per-file FD leak is macOS-specific")
	}
	if testing.Short() {
		t.Skip("short: real-watcher FD test runs many churn cycles")
	}

	dir := t.TempDir()
	logger := slogDiscard()

	// Create a tracked parent so churn subdirectories pass the
	// isAncestorTracked check and actually get watched.
	srcDir := filepath.Join(dir, "src")
	require.NoError(t, os.MkdirAll(srcDir, 0o755))

	tracker := &GitTrackedMatcher{
		projectRoot:  dir,
		trackedDirs:  map[string]struct{}{"src": {}},
		trackedFiles: map[string]struct{}{"src/main.go": {}},
		logger:       logger,
	}
	pw := NewProjectWatcher(
		dir, logger,
		DefaultWatcherFactory,
		&RealFileSystem{},
		NewChangeAccumulator(20*time.Millisecond),
		tracker,
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pw.Start(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	// Wait until root + src are being watched.
	require.Eventually(t, func() bool {
		return pw.WatchedDirCount() >= 2
	}, 2*time.Second, 10*time.Millisecond)

	// Settle and capture baseline. Run a few warmup cycles first because the
	// fsnotify goroutine and kqueue itself open FDs on first use.
	for i := 0; i < 3; i++ {
		warmDir := filepath.Join(srcDir, fmt.Sprintf("warmup-%d", i))
		require.NoError(t, os.MkdirAll(warmDir, 0o755))
		require.NoError(t, os.RemoveAll(warmDir))
	}
	time.Sleep(150 * time.Millisecond)

	baseline := countOpenFDs(t)

	// Churn: 50 iterations of mkdir + N files + rm-rf under the tracked src/
	// parent. Each iteration pre-fix would open (1 + 5) kqueue FDs and only
	// release 1 — net +5/round.
	//
	// Wait for an observable readiness signal between mkdir and rm-rf
	// (the watcher actually registers the new dir) instead of a fixed sleep.
	// Fixed sleeps were racy on slow Darwin runners — if rm-rf fires before
	// fsnotify attaches the per-file watches, the test becomes a false
	// negative. require.Eventually polls cheaply.
	const iterations = 50
	const filesPerDir = 5
	const churnReadyTimeout = 500 * time.Millisecond
	const churnReadyPoll = 1 * time.Millisecond
	for i := 0; i < iterations; i++ {
		sub := filepath.Join(srcDir, fmt.Sprintf("churn-%04d", i))
		preCount := pw.WatchedDirCount()
		require.NoError(t, os.MkdirAll(sub, 0o755))
		for j := 0; j < filesPerDir; j++ {
			f := filepath.Join(sub, fmt.Sprintf("f%d.go", j))
			require.NoError(t, os.WriteFile(f, []byte("package x\n"), 0o644))
		}
		// Wait until the daemon has actually added the new dir to its watch
		// set. WatchedDirCount() advancing past preCount is the observable
		// proxy for "fsnotify Add() returned and watchDirectoryFiles ran."
		// Falling back to a short sleep if the count never advances would
		// silently turn this into a no-op test, so we require strictly.
		require.Eventually(t, func() bool {
			return pw.WatchedDirCount() > preCount
		}, churnReadyTimeout, churnReadyPoll, "watcher should register churn dir %d (preCount=%d)", i, preCount)

		require.NoError(t, os.RemoveAll(sub))

		// Wait for the Remove handler to drain back to the pre-mkdir count
		// (give or take 1 for any stragglers from prior iterations).
		require.Eventually(t, func() bool {
			return pw.WatchedDirCount() <= preCount
		}, churnReadyTimeout, churnReadyPoll, "watcher should drain churn dir %d (preCount=%d, current=%d)", i, preCount, pw.WatchedDirCount())
	}

	// Allow handler to drain any residual events.
	require.Eventually(t, func() bool {
		return pw.WatchedDirCount() <= 5
	}, 5*time.Second, 50*time.Millisecond, "watcher should drain churn dirs")
	time.Sleep(200 * time.Millisecond)

	final := countOpenFDs(t)
	delta := final - baseline

	// Pre-fix: delta ≈ iterations*filesPerDir = 250 leaked FDs (and growing
	// unbounded across runs). Post-fix: delta should be near zero — the
	// gating in handleEvent stops untracked dirs from being watched at all,
	// and tracked-dir churn is cleaned by the Remove handler. Tolerance of
	// 10 catches a regression of even one leaked FD per churn iteration
	// while leaving headroom for goroutine startup and timer noise.
	//
	// The earlier tolerance of 30 was loose enough to swallow a smaller
	// regression of the same shape — precisely why the original bug shipped.
	const tolerance = 10
	assert.Less(t, delta, tolerance,
		"FD count grew by %d (baseline %d, final %d). Pre-fix this is ~%d. Tolerance: <%d.",
		delta, baseline, final, iterations*filesPerDir, tolerance)
}

// --- Bug fix: handleEvent must skip untracked dirs (issue #594) ---

// TestProjectWatcher_CreateSkipsUntrackedDir verifies that handleEvent does NOT
// add a watch when a directory is created under an untracked parent (e.g.,
// node_modules/.pnpm/foo). This is the primary fix for the FD leak: previously
// handleEvent watched every new directory unconditionally, opening 1+N kqueue
// FDs that only got pruned 30s later — by which time per-file FDs had already
// leaked as revoked-but-unclosed handles.
func TestProjectWatcher_CreateSkipsUntrackedDir(t *testing.T) {
	mockWatcher := NewMockFileSystemWatcher()
	mockFS := NewMockFileSystem()

	dir := t.TempDir()
	srcDir := filepath.Join(dir, "src")
	nmDir := filepath.Join(dir, "node_modules")
	pnpmDir := filepath.Join(dir, "node_modules", ".pnpm")
	deepDir := filepath.Join(dir, "node_modules", ".pnpm", "@babel+core@7.28.6")
	require.NoError(t, os.MkdirAll(srcDir, 0o755))
	require.NoError(t, os.MkdirAll(deepDir, 0o755))

	mockFS.AddDir(nmDir, nil)
	mockFS.AddDir(pnpmDir, nil)
	mockFS.AddDir(deepDir, nil)

	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	tracker := &GitTrackedMatcher{
		projectRoot:  dir,
		trackedDirs:  map[string]struct{}{"src": {}},
		trackedFiles: map[string]struct{}{"src/main.go": {}},
		logger:       slogDiscard(),
	}

	pw := NewProjectWatcher(
		dir, slogDiscard(),
		func() (FileSystemWatcher, error) { return mockWatcher, nil },
		mockFS, acc, tracker,
	)
	pw.walkAndWatch(mockWatcher)
	beforeAdds := len(mockWatcher.AddedPaths())

	pw.handleEvent(mockWatcher, fsnotify.Event{Name: nmDir, Op: fsnotify.Create})
	pw.handleEvent(mockWatcher, fsnotify.Event{Name: pnpmDir, Op: fsnotify.Create})
	pw.handleEvent(mockWatcher, fsnotify.Event{Name: deepDir, Op: fsnotify.Create})

	assert.Equal(t, beforeAdds, len(mockWatcher.AddedPaths()),
		"untracked directory Create events must NOT trigger watcher.Add")

	for _, p := range mockWatcher.AddedPaths() {
		assert.NotContains(t, p, "node_modules",
			"no node_modules path should appear in watched dirs")
	}
}

// TestProjectWatcher_CreateAllowsNewTrackedSubdir verifies that handleEvent
// DOES add a watch for a new subdirectory created under a tracked parent.
func TestProjectWatcher_CreateAllowsNewTrackedSubdir(t *testing.T) {
	mockWatcher := NewMockFileSystemWatcher()
	mockFS := NewMockFileSystem()

	dir := t.TempDir()
	srcDir := filepath.Join(dir, "src")
	newPkgDir := filepath.Join(dir, "src", "newpkg")
	require.NoError(t, os.MkdirAll(newPkgDir, 0o755))

	mockFS.AddDir(srcDir, nil)
	mockFS.AddDir(newPkgDir, nil)

	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	tracker := &GitTrackedMatcher{
		projectRoot:  dir,
		trackedDirs:  map[string]struct{}{"src": {}},
		trackedFiles: map[string]struct{}{"src/main.go": {}},
		logger:       slogDiscard(),
	}

	pw := NewProjectWatcher(
		dir, slogDiscard(),
		func() (FileSystemWatcher, error) { return mockWatcher, nil },
		mockFS, acc, tracker,
	)
	pw.walkAndWatch(mockWatcher)

	pw.handleEvent(mockWatcher, fsnotify.Event{Name: newPkgDir, Op: fsnotify.Create})

	paths := mockWatcher.AddedPaths()
	assert.Contains(t, paths, newPkgDir,
		"new subdirectory under tracked parent should be watched")
}

// TestProjectWatcher_IsAncestorTracked verifies the ancestor-walk logic.
func TestProjectWatcher_IsAncestorTracked(t *testing.T) {
	dir := t.TempDir()
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	tracker := &GitTrackedMatcher{
		projectRoot: dir,
		trackedDirs: map[string]struct{}{
			"src":          {},
			"src/internal": {},
			"pkg":          {},
		},
		trackedFiles: map[string]struct{}{},
		logger:       slogDiscard(),
	}

	pw := NewProjectWatcher(
		dir, slogDiscard(),
		func() (FileSystemWatcher, error) { return NewMockFileSystemWatcher(), nil },
		NewMockFileSystem(), acc, tracker,
	)

	tests := []struct {
		relPath string
		want    bool
	}{
		{"src/newpkg", true},
		{"src/internal/deep/nested", true},
		{"pkg/subpkg", true},
		{"node_modules", false},
		{"node_modules/.pnpm", false},
		{"node_modules/.pnpm/@babel+core", false},
		{"build", false},
		{"dist/assets", false},
	}

	for _, tt := range tests {
		t.Run(tt.relPath, func(t *testing.T) {
			got := pw.isAncestorTracked(tt.relPath)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- FD pressure breaker: threshold computation + offender ranking ---

// TestComputeFDPressureThreshold verifies the breaker self-tunes to the
// platform's soft RLIMIT_NOFILE.
//
// Failure prevented: a static threshold (the original 4096) is dead code on
// hosts where the OS kills the process before the breaker fires, and fires
// only after a severe leak on hosts with raised ulimits. Self-tuning keeps
// the breaker meaningful everywhere.
func TestComputeFDPressureThreshold(t *testing.T) {
	tests := []struct {
		name  string
		limit uint64
		want  int
	}{
		{"unknown soft limit falls back", 0, fdPressureFallback},
		{"macOS default 256 → floor", 256, fdPressureFloor},
		{"macOS small raised 512 → floor (half=256)", 512, fdPressureFloor},
		{"just above floor doubles", 1024, 512},
		{"linux default 1024 → 512", 1024, 512},
		{"macOS dev 10240 → 5120", 10240, 5120},
		{"linux server 65536 → 32768", 65536, 32768},
		{"very small limit floored", 100, fdPressureFloor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeFDPressureThreshold(tt.limit)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestTopWatchedDirsByChildCount verifies offender ranking returns the
// largest watched dirs by per-file FD footprint, descending.
//
// Failure prevented: when the breaker fires, users need to know *which*
// dir is leaking. Without this, the only signal is a global FD count and
// the user has to reach for lsof — which most users won't.
func TestTopWatchedDirsByChildCount(t *testing.T) {
	dir := t.TempDir()
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()
	tracker := &GitTrackedMatcher{
		projectRoot:  dir,
		trackedDirs:  map[string]struct{}{},
		trackedFiles: map[string]struct{}{},
		logger:       slogDiscard(),
	}
	pw := NewProjectWatcher(
		dir, slogDiscard(),
		func() (FileSystemWatcher, error) { return NewMockFileSystemWatcher(), nil },
		NewMockFileSystem(), acc, tracker,
	)

	// Prime watchedDirs + dirChildren with three offenders of varying size.
	// Use absolute paths because dirChildren is keyed by absolute dir.
	pw.mu.Lock()
	huge := filepath.Join(dir, "node_modules/.pnpm")
	medium := filepath.Join(dir, "vendor")
	small := filepath.Join(dir, "src")
	empty := filepath.Join(dir, "docs")
	pw.watchedDirs[huge] = struct{}{}
	pw.watchedDirs[medium] = struct{}{}
	pw.watchedDirs[small] = struct{}{}
	pw.watchedDirs[empty] = struct{}{}
	pw.dirChildren[huge] = makeFakeChildren(4200)
	pw.dirChildren[medium] = makeFakeChildren(120)
	pw.dirChildren[small] = makeFakeChildren(8)
	// empty has no entry → should be excluded
	pw.mu.Unlock()

	t.Run("top 2 are largest descending", func(t *testing.T) {
		got := pw.topWatchedDirsByChildCount(2)
		require.Len(t, got, 2)
		assert.Equal(t, "node_modules/.pnpm", got[0].Dir)
		assert.Equal(t, 4200, got[0].Files)
		assert.Equal(t, "vendor", got[1].Dir)
		assert.Equal(t, 120, got[1].Files)
	})

	t.Run("excludes dirs with zero children", func(t *testing.T) {
		got := pw.topWatchedDirsByChildCount(10)
		for _, o := range got {
			assert.NotEqual(t, "docs", o.Dir, "empty dir should be filtered out")
		}
		assert.Len(t, got, 3, "exactly the three non-empty dirs")
	})

	t.Run("n=0 returns all sorted", func(t *testing.T) {
		got := pw.topWatchedDirsByChildCount(0)
		require.Len(t, got, 3)
		assert.Equal(t, 4200, got[0].Files)
		assert.Equal(t, 120, got[1].Files)
		assert.Equal(t, 8, got[2].Files)
	})
}

// TestFormatOffenders verifies the log-line formatter is grep-friendly and
// stable. The breaker log is the user's primary signal that *which* dir is
// leaking — drift in this format would silently break parsers that key on it.
func TestFormatOffenders(t *testing.T) {
	t.Run("empty input → empty string", func(t *testing.T) {
		assert.Equal(t, "", formatOffenders(nil))
		assert.Equal(t, "", formatOffenders([]dirOffender{}))
	})
	t.Run("single offender", func(t *testing.T) {
		got := formatOffenders([]dirOffender{{Dir: "node_modules/.pnpm", Files: 4200}})
		assert.Equal(t, "node_modules/.pnpm(4200)", got)
	})
	t.Run("multiple offenders comma-joined, no spaces", func(t *testing.T) {
		got := formatOffenders([]dirOffender{
			{Dir: "node_modules/.pnpm", Files: 4200},
			{Dir: "vendor", Files: 120},
			{Dir: "src", Files: 8},
		})
		assert.Equal(t, "node_modules/.pnpm(4200),vendor(120),src(8)", got)
	})
}

func makeFakeChildren(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("/abs/file-%d.go", i)
	}
	return out
}

// countOpenFDs returns the number of FDs open on the current process.
// Uses /dev/fd via Readdirnames (not os.ReadDir) — on macOS, ReadDir's
// per-entry fstatat fails on some kernel-only FD types with "bad file
// descriptor". Readdirnames just enumerates names from the directory
// stream, no per-entry stat. We subtract 1 because the readdir itself
// holds a transient FD on /dev/fd.
func countOpenFDs(t *testing.T) int {
	t.Helper()
	d, err := os.Open("/dev/fd")
	require.NoError(t, err, "failed to open /dev/fd for FD count")
	defer d.Close()
	names, err := d.Readdirnames(-1)
	require.NoError(t, err, "failed to enumerate /dev/fd")
	// Subtract 1 for the FD held by 'd' itself, which is closed before
	// the caller checks the next sample.
	n := len(names) - 1
	if n < 0 {
		n = 0
	}
	return n
}
