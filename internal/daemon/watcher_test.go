package daemon

import (
	"context"
	"errors"
	"io/fs"
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

func TestNewWatcher(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	w := NewWatcher("/some/path", 500*time.Millisecond, logger)

	assert.NotNil(t, w)
	assert.Equal(t, "/some/path", w.path)
	assert.Equal(t, 500*time.Millisecond, w.debounceWindow)
	assert.Equal(t, logger, w.logger)
}

func TestWatcher_HandleEvent_IgnoreGit(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWatcher("/tmp/test", 50*time.Millisecond, logger)

	called := atomic.Int32{}
	w.callback = func() { called.Add(1) }

	// .git directory should be ignored
	w.handleEvent(fsnotify.Event{
		Name: "/tmp/test/.git/objects/abc",
		Op:   fsnotify.Write,
	})

	// verify callback was NOT called (negative test — wait briefly then confirm)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(0), called.Load())
}

func TestWatcher_HandleEvent_IgnoreHidden(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWatcher("/tmp/test", 50*time.Millisecond, logger)

	called := atomic.Int32{}
	w.callback = func() { called.Add(1) }

	// hidden files should be ignored
	w.handleEvent(fsnotify.Event{
		Name: "/tmp/test/.DS_Store",
		Op:   fsnotify.Write,
	})

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(0), called.Load())
}

func TestWatcher_HandleEvent_AllowSageox(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWatcher("/tmp/test", 50*time.Millisecond, logger)

	called := atomic.Int32{}
	w.callback = func() { called.Add(1) }

	// .sageox should NOT be ignored
	w.handleEvent(fsnotify.Event{
		Name: "/tmp/test/.sageox/config.json",
		Op:   fsnotify.Write,
	})

	require.Eventually(t, func() bool {
		return called.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)
}

func TestWatcher_HandleEvent_IgnoreChmod(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWatcher("/tmp/test", 50*time.Millisecond, logger)

	called := atomic.Int32{}
	w.callback = func() { called.Add(1) }

	// chmod should be ignored
	w.handleEvent(fsnotify.Event{
		Name: "/tmp/test/file.txt",
		Op:   fsnotify.Chmod,
	})

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(0), called.Load())
}

func TestWatcher_HandleEvent_ProcessWrite(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWatcher("/tmp/test", 50*time.Millisecond, logger)

	called := atomic.Int32{}
	w.callback = func() { called.Add(1) }

	// write should be processed
	w.handleEvent(fsnotify.Event{
		Name: "/tmp/test/file.txt",
		Op:   fsnotify.Write,
	})

	require.Eventually(t, func() bool {
		return called.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)
}

func TestWatcher_HandleEvent_ProcessCreate(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWatcher("/tmp/test", 50*time.Millisecond, logger)

	called := atomic.Int32{}
	w.callback = func() { called.Add(1) }

	// create should be processed
	w.handleEvent(fsnotify.Event{
		Name: "/tmp/test/new_file.txt",
		Op:   fsnotify.Create,
	})

	require.Eventually(t, func() bool {
		return called.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)
}

func TestWatcher_HandleEvent_ProcessRemove(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWatcher("/tmp/test", 50*time.Millisecond, logger)

	called := atomic.Int32{}
	w.callback = func() { called.Add(1) }

	// remove should be processed
	w.handleEvent(fsnotify.Event{
		Name: "/tmp/test/deleted.txt",
		Op:   fsnotify.Remove,
	})

	require.Eventually(t, func() bool {
		return called.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)
}

func TestWatcher_HandleEvent_ProcessRename(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWatcher("/tmp/test", 50*time.Millisecond, logger)

	called := atomic.Int32{}
	w.callback = func() { called.Add(1) }

	// rename should be processed
	w.handleEvent(fsnotify.Event{
		Name: "/tmp/test/renamed.txt",
		Op:   fsnotify.Rename,
	})

	require.Eventually(t, func() bool {
		return called.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)
}

func TestWatcher_Debounce(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWatcher("/tmp/test", 100*time.Millisecond, logger)

	called := atomic.Int32{}
	w.callback = func() { called.Add(1) }

	// rapid events should be debounced
	for i := 0; i < 10; i++ {
		w.handleEvent(fsnotify.Event{
			Name: "/tmp/test/file.txt",
			Op:   fsnotify.Write,
		})
		time.Sleep(10 * time.Millisecond)
	}

	// wait for debounce window plus some buffer
	time.Sleep(200 * time.Millisecond)

	// should only be called once despite 10 events
	assert.Equal(t, int32(1), called.Load())
}

func TestWatcher_Debounce_MultipleWindows(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWatcher("/tmp/test", 50*time.Millisecond, logger)

	called := atomic.Int32{}
	w.callback = func() { called.Add(1) }

	// first batch
	w.handleEvent(fsnotify.Event{
		Name: "/tmp/test/file.txt",
		Op:   fsnotify.Write,
	})

	// wait for first debounce to complete
	time.Sleep(100 * time.Millisecond)

	// second batch
	w.handleEvent(fsnotify.Event{
		Name: "/tmp/test/file.txt",
		Op:   fsnotify.Write,
	})

	// wait for second debounce
	time.Sleep(100 * time.Millisecond)

	// should be called twice (once per window)
	assert.Equal(t, int32(2), called.Load())
}

func TestWatcher_Start_Context(t *testing.T) {
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWatcher(tmpDir, 50*time.Millisecond, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan bool)
	go func() {
		w.Start(ctx, func() {})
		done <- true
	}()

	select {
	case <-done:
		// expected - watcher stopped when context canceled
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watcher should stop when context canceled")
	}
}

func TestWatcher_Start_InvalidPath(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWatcher("/nonexistent/path/that/does/not/exist", 50*time.Millisecond, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan bool)
	go func() {
		w.Start(ctx, func() {})
		done <- true
	}()

	select {
	case <-done:
		// expected - watcher should exit early due to invalid path
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watcher should exit quickly for invalid path")
	}
}

// Integration test with real file system events
func TestWatcher_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	tmpDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWatcher(tmpDir, 50*time.Millisecond, logger)

	called := atomic.Int32{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go w.Start(ctx, func() { called.Add(1) })

	// give watcher time to start
	time.Sleep(50 * time.Millisecond)

	// create a file
	testFile := filepath.Join(tmpDir, "test.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("hello"), 0644))

	require.Eventually(t, func() bool {
		return called.Load() >= 1
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
}

// Tests using mock filesystem watcher

func TestWatcher_WithMockWatcher_EventProcessing(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mockWatcher := NewMockFileSystemWatcher()
	mockFS := NewMockFileSystem()

	watcherFactory := func() (FileSystemWatcher, error) {
		return mockWatcher, nil
	}

	w := NewWatcherWithFS("/test/path", 50*time.Millisecond, logger, watcherFactory, mockFS)

	called := atomic.Int32{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go w.Start(ctx, func() { called.Add(1) })

	// wait for watcher to register paths
	require.Eventually(t, func() bool {
		paths := mockWatcher.AddedPaths()
		for _, p := range paths {
			if p == "/test/path" {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond)

	// send a write event
	mockWatcher.SendEvent(fsnotify.Event{
		Name: "/test/path/file.txt",
		Op:   fsnotify.Write,
	})

	require.Eventually(t, func() bool {
		return called.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
}

func TestWatcher_WithMockWatcher_FactoryError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	expectedErr := errors.New("failed to create watcher")
	watcherFactory := func() (FileSystemWatcher, error) {
		return nil, expectedErr
	}

	w := NewWatcherWithFS("/test/path", 50*time.Millisecond, logger, watcherFactory, &RealFileSystem{})

	called := atomic.Int32{}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan bool)
	go func() {
		w.Start(ctx, func() { called.Add(1) })
		done <- true
	}()

	// should exit quickly due to factory error
	select {
	case <-done:
		// expected
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watcher should exit quickly when factory fails")
	}

	assert.Equal(t, int32(0), called.Load())
}

func TestWatcher_WithMockWatcher_AddPathError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mockWatcher := NewMockFileSystemWatcher()
	mockWatcher.SetAddError(errors.New("permission denied"))

	watcherFactory := func() (FileSystemWatcher, error) {
		return mockWatcher, nil
	}

	w := NewWatcherWithFS("/test/path", 50*time.Millisecond, logger, watcherFactory, &RealFileSystem{})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan bool)
	go func() {
		w.Start(ctx, func() {})
		done <- true
	}()

	// should exit quickly due to add error
	select {
	case <-done:
		// expected
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watcher should exit quickly when add fails")
	}
}

func TestWatcher_WithMockWatcher_ErrorChannel(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mockWatcher := NewMockFileSystemWatcher()

	watcherFactory := func() (FileSystemWatcher, error) {
		return mockWatcher, nil
	}

	w := NewWatcherWithFS("/test/path", 50*time.Millisecond, logger, watcherFactory, &RealFileSystem{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// track callback invocations from the start to avoid data race
	called := atomic.Int32{}
	go w.Start(ctx, func() { called.Add(1) })

	// wait for watcher to register paths
	require.Eventually(t, func() bool {
		return len(mockWatcher.AddedPaths()) > 0
	}, 2*time.Second, 10*time.Millisecond)

	// send an error - should be logged but not crash
	mockWatcher.SendError(errors.New("watch error"))

	// send an event to confirm watcher still works after error
	mockWatcher.SendEvent(fsnotify.Event{
		Name: "/test/path/file.txt",
		Op:   fsnotify.Write,
	})

	require.Eventually(t, func() bool {
		return called.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
}

func TestWatcher_WithMockWatcher_ChannelClose(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mockWatcher := NewMockFileSystemWatcher()

	watcherFactory := func() (FileSystemWatcher, error) {
		return mockWatcher, nil
	}

	w := NewWatcherWithFS("/test/path", 50*time.Millisecond, logger, watcherFactory, &RealFileSystem{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan bool)
	go func() {
		w.Start(ctx, func() {})
		done <- true
	}()

	// wait for watcher to register paths
	require.Eventually(t, func() bool {
		return len(mockWatcher.AddedPaths()) > 0
	}, 2*time.Second, 10*time.Millisecond)

	// close events channel - should cause watcher to exit
	mockWatcher.CloseEvents()

	select {
	case <-done:
		// expected
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watcher should exit when events channel closes")
	}
}

func TestWatcher_WithMockWatcher_MultipleEvents(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mockWatcher := NewMockFileSystemWatcher()

	watcherFactory := func() (FileSystemWatcher, error) {
		return mockWatcher, nil
	}

	w := NewWatcherWithFS("/test/path", 50*time.Millisecond, logger, watcherFactory, &RealFileSystem{})

	called := atomic.Int32{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go w.Start(ctx, func() { called.Add(1) })

	// wait for watcher to register paths
	require.Eventually(t, func() bool {
		return len(mockWatcher.AddedPaths()) > 0
	}, 2*time.Second, 10*time.Millisecond)

	// send multiple rapid events - should be debounced
	for i := 0; i < 5; i++ {
		mockWatcher.SendEvent(fsnotify.Event{
			Name: "/test/path/file.txt",
			Op:   fsnotify.Write,
		})
		time.Sleep(10 * time.Millisecond)
	}

	// wait for debounce
	time.Sleep(100 * time.Millisecond)

	// should only be called once due to debouncing
	assert.Equal(t, int32(1), called.Load())

	cancel()
}

func TestWatcher_WithMockWatcher_FilteredEvents(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mockWatcher := NewMockFileSystemWatcher()

	watcherFactory := func() (FileSystemWatcher, error) {
		return mockWatcher, nil
	}

	w := NewWatcherWithFS("/test/path", 50*time.Millisecond, logger, watcherFactory, &RealFileSystem{})

	called := atomic.Int32{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go w.Start(ctx, func() { called.Add(1) })

	// wait for watcher to register paths
	require.Eventually(t, func() bool {
		return len(mockWatcher.AddedPaths()) > 0
	}, 2*time.Second, 10*time.Millisecond)

	// send events that should be filtered
	filteredEvents := []fsnotify.Event{
		{Name: "/test/path/.git/objects/abc", Op: fsnotify.Write},
		{Name: "/test/path/.DS_Store", Op: fsnotify.Write},
		{Name: "/test/path/.hidden", Op: fsnotify.Create},
		{Name: "/test/path/file.txt", Op: fsnotify.Chmod},
	}

	for _, event := range filteredEvents {
		mockWatcher.SendEvent(event)
	}

	// verify none triggered callback (negative test — wait briefly then confirm)
	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, int32(0), called.Load())

	// now send an event that should pass through
	mockWatcher.SendEvent(fsnotify.Event{
		Name: "/test/path/real-file.txt",
		Op:   fsnotify.Write,
	})

	require.Eventually(t, func() bool {
		return called.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
}

// Tests for MockFileSystem

func TestMockFileSystem_Stat(t *testing.T) {
	mockFS := NewMockFileSystem()

	// file not found
	_, err := mockFS.Stat("/nonexistent")
	assert.ErrorIs(t, err, os.ErrNotExist)

	// add a file
	mockFS.AddFile("/test/file.txt", 100, 0644)
	info, err := mockFS.Stat("/test/file.txt")
	require.NoError(t, err)
	assert.Equal(t, "file.txt", info.Name())
	assert.Equal(t, int64(100), info.Size())
	assert.False(t, info.IsDir())

	// add a directory
	mockFS.AddDir("/test/dir", []string{"a.txt", "b.txt"})
	info, err = mockFS.Stat("/test/dir")
	require.NoError(t, err)
	assert.Equal(t, "dir", info.Name())
	assert.True(t, info.IsDir())

	// configured error
	mockFS.SetStatError("/test/error", errors.New("permission denied"))
	_, err = mockFS.Stat("/test/error")
	assert.Error(t, err)
	assert.Equal(t, "permission denied", err.Error())
}

func TestMockFileSystem_ReadDir(t *testing.T) {
	mockFS := NewMockFileSystem()

	// directory not found
	_, err := mockFS.ReadDir("/nonexistent")
	assert.ErrorIs(t, err, os.ErrNotExist)

	// add a directory with entries
	mockFS.AddDir("/test/dir", []string{"file1.txt", "file2.txt", "file3.txt"})
	entries, err := mockFS.ReadDir("/test/dir")
	require.NoError(t, err)
	assert.Len(t, entries, 3)

	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	assert.Contains(t, names, "file1.txt")
	assert.Contains(t, names, "file2.txt")
	assert.Contains(t, names, "file3.txt")

	// configured error
	mockFS.SetReadDirError("/test/error", errors.New("access denied"))
	_, err = mockFS.ReadDir("/test/error")
	assert.Error(t, err)
	assert.Equal(t, "access denied", err.Error())
}

func TestMockFileSystemWatcher_Operations(t *testing.T) {
	mock := NewMockFileSystemWatcher()

	// test Add
	require.NoError(t, mock.Add("/path/a"))
	require.NoError(t, mock.Add("/path/b"))
	paths := mock.AddedPaths()
	assert.Equal(t, []string{"/path/a", "/path/b"}, paths)

	// test Add with error
	mock.SetAddError(errors.New("watch limit exceeded"))
	err := mock.Add("/path/c")
	assert.Error(t, err)
	assert.Equal(t, "watch limit exceeded", err.Error())

	// test Close
	mock.SetCloseError(errors.New("close failed"))
	err = mock.Close()
	assert.Error(t, err)
	assert.Equal(t, "close failed", err.Error())
}

func TestMockFileInfo(t *testing.T) {
	info := mockFileInfo{
		name:    "test.txt",
		size:    1024,
		mode:    fs.FileMode(0644),
		modTime: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		isDir:   false,
	}

	assert.Equal(t, "test.txt", info.Name())
	assert.Equal(t, int64(1024), info.Size())
	assert.Equal(t, fs.FileMode(0644), info.Mode())
	assert.Equal(t, time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC), info.ModTime())
	assert.False(t, info.IsDir())
	assert.Nil(t, info.Sys())
}

func TestMockDirEntry(t *testing.T) {
	entry := mockDirEntry{
		name:  "subdir",
		isDir: true,
		mode:  fs.ModeDir | 0755,
		info: mockFileInfo{
			name:  "subdir",
			isDir: true,
			mode:  fs.ModeDir | 0755,
		},
	}

	assert.Equal(t, "subdir", entry.Name())
	assert.True(t, entry.IsDir())
	assert.Equal(t, fs.ModeDir, entry.Type())

	info, err := entry.Info()
	require.NoError(t, err)
	assert.Equal(t, "subdir", info.Name())
	assert.True(t, info.IsDir())
}

func TestRealFileSystem(t *testing.T) {
	tmpDir := t.TempDir()
	realFS := &RealFileSystem{}

	// test Stat on directory
	info, err := realFS.Stat(tmpDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	// create a file and test Stat
	testFile := filepath.Join(tmpDir, "test.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("hello"), 0644))

	info, err = realFS.Stat(testFile)
	require.NoError(t, err)
	assert.Equal(t, "test.txt", info.Name())
	assert.Equal(t, int64(5), info.Size())
	assert.False(t, info.IsDir())

	// test ReadDir
	entries, err := realFS.ReadDir(tmpDir)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	assert.Equal(t, "test.txt", entries[0].Name())

	// test Stat on nonexistent
	_, err = realFS.Stat("/nonexistent/path")
	assert.Error(t, err)
}

func TestDefaultWatcherFactory(t *testing.T) {
	// verify DefaultWatcherFactory creates a real watcher
	watcher, err := DefaultWatcherFactory()
	require.NoError(t, err)
	defer watcher.Close()

	// verify it's a RealFileSystemWatcher
	_, ok := watcher.(*RealFileSystemWatcher)
	assert.True(t, ok, "DefaultWatcherFactory should return *RealFileSystemWatcher")
}

func TestNewWatcherWithFS(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mockFS := NewMockFileSystem()
	mockWatcher := NewMockFileSystemWatcher()

	factory := func() (FileSystemWatcher, error) {
		return mockWatcher, nil
	}

	w := NewWatcherWithFS("/test/path", 100*time.Millisecond, logger, factory, mockFS)

	assert.NotNil(t, w)
	assert.Equal(t, "/test/path", w.path)
	assert.Equal(t, 100*time.Millisecond, w.debounceWindow)
	assert.Equal(t, logger, w.logger)
	assert.Equal(t, mockFS, w.fs)
}

func TestWatcher_Stop(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWatcher("/tmp/test", 100*time.Millisecond, logger)

	called := atomic.Int32{}
	w.callback = func() { called.Add(1) }

	// trigger a debounce
	w.handleEvent(fsnotify.Event{
		Name: "/tmp/test/file.txt",
		Op:   fsnotify.Write,
	})

	// immediately stop before timer fires
	w.Stop()

	// wait longer than debounce window
	time.Sleep(200 * time.Millisecond)

	// callback should NOT have been called due to stop
	assert.Equal(t, int32(0), called.Load())
}

func TestWatcher_Stop_Idempotent(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWatcher("/tmp/test", 100*time.Millisecond, logger)

	// stop can be called multiple times without panic
	w.Stop()
	w.Stop()
	w.Stop()

	// verify stopped state
	w.mu.Lock()
	assert.True(t, w.stopped)
	w.mu.Unlock()
}

func TestWatcher_Stop_PreventsNewTimers(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWatcher("/tmp/test", 50*time.Millisecond, logger)

	called := atomic.Int32{}
	w.callback = func() { called.Add(1) }

	// stop first
	w.Stop()

	// try to trigger events after stop
	for i := 0; i < 5; i++ {
		w.handleEvent(fsnotify.Event{
			Name: "/tmp/test/file.txt",
			Op:   fsnotify.Write,
		})
	}

	// wait for potential callbacks
	time.Sleep(150 * time.Millisecond)

	// no callbacks should have been scheduled
	assert.Equal(t, int32(0), called.Load())
}

func TestWatcher_Stop_RaceCondition(t *testing.T) {
	// test that stopping during active debouncing doesn't cause race conditions
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWatcher("/tmp/test", 10*time.Millisecond, logger)

	called := atomic.Int32{}
	w.callback = func() { called.Add(1) }

	// rapidly trigger events and stop concurrently
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			w.handleEvent(fsnotify.Event{
				Name: "/tmp/test/file.txt",
				Op:   fsnotify.Write,
			})
			time.Sleep(time.Millisecond)
		}
		close(done)
	}()

	// stop after some events
	time.Sleep(20 * time.Millisecond)
	w.Stop()

	<-done

	// wait for any pending timers
	time.Sleep(50 * time.Millisecond)

	// verify no crashes occurred (test passes if we get here)
	// callback count is indeterminate but should be small
	t.Logf("callback count: %d", called.Load())
}

func TestWatcher_ContextCancelCallsStop(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mockWatcher := NewMockFileSystemWatcher()
	watcherFactory := func() (FileSystemWatcher, error) {
		return mockWatcher, nil
	}

	w := NewWatcherWithFS("/test/path", 100*time.Millisecond, logger, watcherFactory, &RealFileSystem{})

	called := atomic.Int32{}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		w.Start(ctx, func() { called.Add(1) })
		close(done)
	}()

	// wait for watcher to register paths
	require.Eventually(t, func() bool {
		return len(mockWatcher.AddedPaths()) > 0
	}, 2*time.Second, 10*time.Millisecond)

	// trigger a debounce
	mockWatcher.SendEvent(fsnotify.Event{
		Name: "/test/path/file.txt",
		Op:   fsnotify.Write,
	})

	// immediately cancel context (before debounce completes)
	time.Sleep(10 * time.Millisecond)
	cancel()

	<-done

	// wait longer than debounce window
	time.Sleep(200 * time.Millisecond)

	// callback should NOT have been called because context was canceled
	assert.Equal(t, int32(0), called.Load())

	// verify stopped flag is set
	w.mu.Lock()
	assert.True(t, w.stopped)
	w.mu.Unlock()
}

// --- MockFileSystemWatcher.Remove tests ---

func TestMockFileSystemWatcher_Remove(t *testing.T) {
	mock := NewMockFileSystemWatcher()

	require.NoError(t, mock.Remove("/path/a"))
	require.NoError(t, mock.Remove("/path/b"))
	assert.Equal(t, []string{"/path/a", "/path/b"}, mock.RemovedPaths())

	mock.SetRemoveError(errors.New("not watched"))
	err := mock.Remove("/path/c")
	assert.Error(t, err)
	assert.Equal(t, 3, len(mock.RemovedPaths()), "path still recorded even on error")
}

// TestWatcher_LedgerDirRemove_FlushesPerFileFDs verifies that when the
// watched ledger directory is removed mid-watch (the blue-green GC reclone
// case), the singleton ledger Watcher explicitly Removes each child file
// from fsnotify so its kqueue per-file FDs get closed via unix.Close.
//
// Without this, fsnotify v1.9.0's kqueue backend on macOS leaves per-file
// FDs open until the daemon's defer Close() fires — for a long-lived
// daemon that's effectively forever, accumulating FDs across reclones.
//
// Mock-level test: MockFileSystemWatcher tracks Remove calls regardless of
// platform, so we drive a Remove event for the watched path and assert the
// per-file Removes were issued. Skipped on non-Darwin since the production
// code is gated by childMirrorEnabled (no children snapshotted on Linux,
// no per-file Removes expected).
//
// Failure prevented: regression of the per-file FD leak pattern that, in
// production, made `lsof` itself hang on the daemon PID.
func TestWatcher_LedgerDirRemove_FlushesPerFileFDs(t *testing.T) {
	// Force the mirror on so this mock-based regression runs (and fails
	// against broken code) on Linux CI as well as Darwin. Production
	// behavior is unchanged — the mirror still no-ops on non-Darwin in
	// real binaries.
	forceChildMirrorForTest(t)

	tmp := t.TempDir()
	ledgerDir := filepath.Join(tmp, "ledger")
	require.NoError(t, os.MkdirAll(ledgerDir, 0o755))

	// Create children fsnotify would auto-watch via watchDirectoryFiles.
	// MockFileSystem's ReadDir is what snapshotKqueueChildren calls — it
	// must return these as non-dir entries.
	mockFS := NewMockFileSystem()
	mockFS.AddDir(ledgerDir, []string{"a.json", "b.json", "c.json"})

	mockWatcher := NewMockFileSystemWatcher()
	w := NewWatcherWithFS(
		ledgerDir,
		50*time.Millisecond,
		slog.New(slog.NewTextHandler(os.Stderr, nil)),
		func() (FileSystemWatcher, error) { return mockWatcher, nil },
		mockFS,
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Start(ctx, func() {})
		close(done)
	}()

	// Wait until Add has been recorded (Start has snapshotted children).
	require.Eventually(t, func() bool {
		for _, p := range mockWatcher.AddedPaths() {
			if p == ledgerDir {
				return true
			}
		}
		return false
	}, 2*time.Second, 5*time.Millisecond, "Watcher should have called Add on ledgerDir")

	// Simulate the kernel firing NOTE_DELETE on the watched dir (blue-green
	// GC just rm'd it to make way for the new clone). Pre-fix this would
	// be silently absorbed and the per-file FDs would orphan.
	mockWatcher.SendEvent(fsnotify.Event{Name: ledgerDir, Op: fsnotify.Remove})

	require.Eventually(t, func() bool {
		removed := mockWatcher.RemovedPaths()
		need := []string{
			filepath.Join(ledgerDir, "a.json"),
			filepath.Join(ledgerDir, "b.json"),
			filepath.Join(ledgerDir, "c.json"),
		}
		for _, want := range need {
			found := false
			for _, got := range removed {
				if got == want {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}, 2*time.Second, 5*time.Millisecond, "Watcher must Remove each child file to close fsnotify's per-file FD on dir-removed event")

	cancel()
	<-done
}
