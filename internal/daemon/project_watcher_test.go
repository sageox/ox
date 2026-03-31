package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
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

	// wait for settle
	time.Sleep(100 * time.Millisecond)

	changes := acc.DrainSettled()
	require.Len(t, changes, 1)
	assert.Equal(t, "src/config.go", changes[0].Path)
	assert.Equal(t, ChangeModified, changes[0].ChangeType)
}

func TestAccumulator_CreateThenDelete(t *testing.T) {
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	acc.AddEvent("tmp/scratch.txt", fsnotify.Create, false)
	acc.AddEvent("tmp/scratch.txt", fsnotify.Remove, false)

	time.Sleep(100 * time.Millisecond)

	changes := acc.DrainSettled()
	assert.Nil(t, changes, "create+delete of same file should be suppressed")
}

func TestAccumulator_DeleteThenCreate(t *testing.T) {
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	acc.AddEvent("src/config.go", fsnotify.Remove, false)
	acc.AddEvent("src/config.go", fsnotify.Create, false)

	time.Sleep(100 * time.Millisecond)

	changes := acc.DrainSettled()
	require.Len(t, changes, 1)
	assert.Equal(t, ChangeModified, changes[0].ChangeType, "delete+create = atomic save = modified")
}

func TestAccumulator_CreateThenModify(t *testing.T) {
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	acc.AddEvent("src/new.go", fsnotify.Create, false)
	acc.AddEvent("src/new.go", fsnotify.Write, false)

	time.Sleep(100 * time.Millisecond)

	changes := acc.DrainSettled()
	require.Len(t, changes, 1)
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
	time.Sleep(150 * time.Millisecond)

	changes = acc.DrainSettled()
	require.Len(t, changes, 1)
}

func TestAccumulator_DrainClears(t *testing.T) {
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	acc.AddEvent("src/foo.go", fsnotify.Write, false)
	time.Sleep(100 * time.Millisecond)

	changes := acc.DrainSettled()
	require.Len(t, changes, 1)

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

	time.Sleep(100 * time.Millisecond)

	changes := acc.DrainSettled()
	require.Len(t, changes, 3)

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
	time.Sleep(150 * time.Millisecond)

	changes = acc.DrainSettled()
	require.Len(t, changes, 2)
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

	time.Sleep(50 * time.Millisecond)

	paths := mockWatcher.AddedPaths()
	assert.Contains(t, paths, dir)
	assert.Contains(t, paths, sub1)
	assert.Contains(t, paths, sub2)

	cancel()
	<-done
}

func TestProjectWatcher_UntrackedNotWatched(t *testing.T) {
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

	time.Sleep(50 * time.Millisecond)

	paths := mockWatcher.AddedPaths()
	assert.Contains(t, paths, dir)
	assert.Contains(t, paths, srcDir)
	assert.NotContains(t, paths, nmDir)
	assert.NotContains(t, paths, buildDir)

	cancel()
	<-done
}

func TestProjectWatcher_EventsReachAccumulator(t *testing.T) {
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

	time.Sleep(50 * time.Millisecond)

	mockWatcher.SendEvent(fsnotify.Event{
		Name: filePath,
		Op:   fsnotify.Write,
	})

	// wait for settle
	time.Sleep(100 * time.Millisecond)

	changes := acc.DrainSettled()
	require.Len(t, changes, 1)
	assert.Equal(t, "main.go", changes[0].Path)
	assert.Equal(t, ChangeModified, changes[0].ChangeType)

	cancel()
	<-done
}

func TestProjectWatcher_UntrackedFileEventsFiltered(t *testing.T) {
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

	time.Sleep(50 * time.Millisecond)

	// modify both tracked and untracked files
	mockWatcher.SendEvent(fsnotify.Event{Name: trackedFile, Op: fsnotify.Write})
	mockWatcher.SendEvent(fsnotify.Event{Name: untrackedFile, Op: fsnotify.Write})

	time.Sleep(100 * time.Millisecond)

	changes := acc.DrainSettled()
	require.Len(t, changes, 1, "only tracked file should reach accumulator")
	assert.Equal(t, "main.go", changes[0].Path)

	cancel()
	<-done
}

func TestProjectWatcher_NewFileCreationPassesThrough(t *testing.T) {
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

	time.Sleep(50 * time.Millisecond)

	// create event passes through even for untracked files
	mockWatcher.SendEvent(fsnotify.Event{Name: newFile, Op: fsnotify.Create})

	time.Sleep(100 * time.Millisecond)

	changes := acc.DrainSettled()
	require.Len(t, changes, 1, "newly created files should pass through")
	assert.Equal(t, "new.go", changes[0].Path)
	assert.Equal(t, ChangeCreated, changes[0].ChangeType)

	cancel()
	<-done
}

// slogDiscard returns a logger that discards output.
func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}
