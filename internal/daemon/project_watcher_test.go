package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
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
