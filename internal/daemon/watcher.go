package daemon

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// FileSystemWatcher abstracts filesystem watching operations for testability.
// This interface allows tests to inject mock implementations that simulate
// file events without requiring actual filesystem operations.
type FileSystemWatcher interface {
	// Add adds a path to the watch list.
	Add(path string) error

	// Remove removes a path from the watch list, releasing its file descriptor.
	Remove(path string) error

	// Events returns the channel for receiving file system events.
	Events() <-chan fsnotify.Event

	// Errors returns the channel for receiving watcher errors.
	Errors() <-chan error

	// Close stops the watcher and releases resources.
	Close() error
}

// FileSystem abstracts filesystem operations for testability.
type FileSystem interface {
	// Stat returns file info for the given path.
	Stat(name string) (fs.FileInfo, error)

	// ReadDir reads a directory and returns its entries.
	ReadDir(name string) ([]fs.DirEntry, error)

	// ReadDirN reads up to n entries from a directory and returns them.
	// If n <= 0 returns all entries (equivalent to ReadDir). Used by the
	// kqueue mirror so a pathological million-entry directory doesn't
	// materialize the full listing just to truncate to maxFilesPerWatchedDir.
	ReadDirN(name string, n int) ([]fs.DirEntry, error)
}

// RealFileSystemWatcher implements FileSystemWatcher using fsnotify.
type RealFileSystemWatcher struct {
	watcher *fsnotify.Watcher
}

// NewRealFileSystemWatcher creates a new real filesystem watcher.
func NewRealFileSystemWatcher() (*RealFileSystemWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &RealFileSystemWatcher{watcher: w}, nil
}

// Add adds a path to the watch list.
func (r *RealFileSystemWatcher) Add(path string) error {
	return r.watcher.Add(path)
}

// Remove removes a path from the watch list, releasing its file descriptor.
func (r *RealFileSystemWatcher) Remove(path string) error {
	return r.watcher.Remove(path)
}

// Events returns the channel for receiving file system events.
func (r *RealFileSystemWatcher) Events() <-chan fsnotify.Event {
	return r.watcher.Events
}

// Errors returns the channel for receiving watcher errors.
func (r *RealFileSystemWatcher) Errors() <-chan error {
	return r.watcher.Errors
}

// Close stops the watcher and releases resources.
func (r *RealFileSystemWatcher) Close() error {
	return r.watcher.Close()
}

// RealFileSystem implements FileSystem using actual OS calls.
type RealFileSystem struct{}

// Stat returns file info for the given path.
func (r *RealFileSystem) Stat(name string) (fs.FileInfo, error) {
	return os.Stat(name)
}

// ReadDir reads a directory and returns its entries.
func (r *RealFileSystem) ReadDir(name string) ([]fs.DirEntry, error) {
	return os.ReadDir(name)
}

// ReadDirN reads up to n entries from a directory using *os.File.ReadDir(n),
// which streams entries from the underlying readdir(3) without materializing
// the full listing. This lets the kqueue mirror bound BOTH memory and read
// cost for pathological directories with millions of entries. Falls back to
// ReadDir when n <= 0.
func (r *RealFileSystem) ReadDirN(name string, n int) ([]fs.DirEntry, error) {
	if n <= 0 {
		return os.ReadDir(name)
	}
	d, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return d.ReadDir(n)
}

// WatcherFactory creates FileSystemWatcher instances.
// This abstraction allows tests to inject mock watchers.
type WatcherFactory func() (FileSystemWatcher, error)

// DefaultWatcherFactory creates real filesystem watchers.
func DefaultWatcherFactory() (FileSystemWatcher, error) {
	return NewRealFileSystemWatcher()
}

// Watcher monitors the ledger directory for changes.
type Watcher struct {
	path           string
	debounceWindow time.Duration
	logger         *slog.Logger
	watcherFactory WatcherFactory
	fs             FileSystem

	// debouncing
	mu       sync.Mutex
	timer    *time.Timer
	callback func()
	stopped  bool
}

// NewWatcher creates a new file watcher.
func NewWatcher(path string, debounceWindow time.Duration, logger *slog.Logger) *Watcher {
	return NewWatcherWithFS(path, debounceWindow, logger, DefaultWatcherFactory, &RealFileSystem{})
}

// NewWatcherWithFS creates a new file watcher with injectable dependencies.
// This constructor is primarily for testing, allowing injection of mock filesystem
// and watcher implementations.
func NewWatcherWithFS(
	path string,
	debounceWindow time.Duration,
	logger *slog.Logger,
	watcherFactory WatcherFactory,
	fileSystem FileSystem,
) *Watcher {
	return &Watcher{
		path:           path,
		debounceWindow: debounceWindow,
		logger:         logger,
		watcherFactory: watcherFactory,
		fs:             fileSystem,
	}
}

// Start starts watching for file changes.
// Calls the callback when changes are detected (debounced).
func (w *Watcher) Start(ctx context.Context, onChange func()) {
	watcher, err := w.watcherFactory()
	if err != nil {
		w.logger.Error("failed to create watcher", "error", err)
		return
	}
	defer watcher.Close()

	w.mu.Lock()
	w.callback = onChange
	w.stopped = false
	w.mu.Unlock()

	// Snapshot the per-file children that fsnotify's kqueue backend will
	// auto-open via watchDirectoryFiles when we Add a directory on macOS.
	// We need our own copy because fsnotify's set is private and on
	// Remove/Rename of the watched dir its kqueue backend
	// (backend_kqueue.go:489) calls w.remove(name, false) — which leaves
	// the per-file FDs open. They'd remain open until our defer Close()
	// fires, which for the ledger watcher means the entire daemon
	// lifetime after the first reclone. Snapshot pre AND post Add to
	// close the TOCTOU race against watchDirectoryFiles. No-op on
	// non-Darwin (snapshotKqueueChildren returns nil there).
	preChildren := snapshotKqueueChildren(w.fs, w.path, w.logger)
	if err := watcher.Add(w.path); err != nil {
		w.logger.Error("failed to watch path", "path", w.path, "error", err)
		return
	}
	postChildren := snapshotKqueueChildren(w.fs, w.path, w.logger)
	children := unionKqueueChildren(preChildren, postChildren)

	w.logger.Info("watching ledger directory", "path", w.path)

	for {
		select {
		case <-ctx.Done():
			w.Stop()
			w.logger.Info("watcher stopped")
			return

		case event, ok := <-watcher.Events():
			if !ok {
				w.Stop()
				return
			}
			// Keep our child mirror in sync with fsnotify's: kqueue's
			// dirChange watches new files via sendCreateIfNew on
			// NOTE_WRITE for the dir, and closes per-file FDs on
			// individual NOTE_DELETE. The post-Add snapshot would
			// otherwise drift from fsnotify's actual watch set, missing
			// FDs for files created during daemon operation. Darwin-only.
			if childMirrorEnabled && filepath.Dir(event.Name) == w.path && event.Name != w.path {
				switch {
				case event.Op&fsnotify.Create != 0 && len(children) < maxFilesPerWatchedDir:
					present := false
					for _, c := range children {
						if c == event.Name {
							present = true
							break
						}
					}
					if !present {
						children = append(children, event.Name)
					}
				case event.Op&(fsnotify.Remove|fsnotify.Rename) != 0:
					out := children[:0]
					for _, c := range children {
						if c != event.Name {
							out = append(out, c)
						}
					}
					children = out
				}
			}
			// Plug fsnotify v1.9.0 kqueue per-file FD leak: when our
			// watched dir is removed/renamed (e.g., blue-green GC reclone
			// of the ledger), force fsnotify to close each per-file kqueue
			// FD by replaying our (now-up-to-date) snapshot.
			if childMirrorEnabled && event.Name == w.path &&
				event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				for _, f := range children {
					_ = watcher.Remove(f)
				}
				children = nil
			}
			w.handleEvent(event)

		case err, ok := <-watcher.Errors():
			if !ok {
				w.Stop()
				return
			}
			w.logger.Error("watcher error", "error", err)
		}
	}
}

// Stop stops any pending debounce timer and prevents future callbacks.
// Safe to call multiple times.
func (w *Watcher) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.stopped = true
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
}

// handleEvent processes a file system event.
func (w *Watcher) handleEvent(event fsnotify.Event) {
	// ignore .git directory changes
	if strings.Contains(event.Name, ".git") {
		return
	}

	// ignore hidden files
	base := filepath.Base(event.Name)
	if strings.HasPrefix(base, ".") && base != ".sageox" {
		return
	}

	// only care about write/create/remove operations
	if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
		return
	}

	w.logger.Debug("file change detected", "path", event.Name, "op", event.Op)
	w.debounce()
}

// debounce triggers the callback after the debounce window.
func (w *Watcher) debounce() {
	w.mu.Lock()
	defer w.mu.Unlock()

	// don't schedule new timers if stopped
	if w.stopped {
		return
	}

	if w.timer != nil {
		// Stop returns false if the timer already fired or was stopped.
		// For time.AfterFunc timers, the callback runs in its own goroutine,
		// so we don't need to drain a channel. The callback checks w.stopped
		// to avoid executing after shutdown.
		w.timer.Stop()
		w.timer = nil
	}

	w.timer = time.AfterFunc(w.debounceWindow, func() {
		w.mu.Lock()
		// check if watcher was stopped while timer was pending
		if w.stopped {
			w.mu.Unlock()
			return
		}
		cb := w.callback
		w.timer = nil
		w.mu.Unlock()

		if cb != nil {
			cb()
		}
	})
}

// MockFileSystemWatcher implements FileSystemWatcher for testing.
// It allows tests to inject events and errors without real filesystem operations.
type MockFileSystemWatcher struct {
	events       chan fsnotify.Event
	errors       chan error
	addedPaths   []string
	removedPaths []string
	addErr       error
	removeErr    error
	closeErr     error
	mu           sync.Mutex
}

// NewMockFileSystemWatcher creates a new mock watcher for testing.
func NewMockFileSystemWatcher() *MockFileSystemWatcher {
	return &MockFileSystemWatcher{
		events:       make(chan fsnotify.Event, 100),
		errors:       make(chan error, 10),
		addedPaths:   make([]string, 0),
		removedPaths: make([]string, 0),
	}
}

// Add records the path and returns the configured error (if any).
func (m *MockFileSystemWatcher) Add(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addedPaths = append(m.addedPaths, path)
	return m.addErr
}

// Remove records the path removal and returns the configured error (if any).
func (m *MockFileSystemWatcher) Remove(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removedPaths = append(m.removedPaths, path)
	return m.removeErr
}

// Events returns the events channel.
func (m *MockFileSystemWatcher) Events() <-chan fsnotify.Event {
	return m.events
}

// Errors returns the errors channel.
func (m *MockFileSystemWatcher) Errors() <-chan error {
	return m.errors
}

// Close closes the channels and returns the configured error (if any).
func (m *MockFileSystemWatcher) Close() error {
	return m.closeErr
}

// SendEvent sends an event to the watcher.
func (m *MockFileSystemWatcher) SendEvent(event fsnotify.Event) {
	m.events <- event
}

// SendError sends an error to the watcher.
func (m *MockFileSystemWatcher) SendError(err error) {
	m.errors <- err
}

// CloseEvents closes the events channel to signal completion.
func (m *MockFileSystemWatcher) CloseEvents() {
	close(m.events)
}

// CloseErrors closes the errors channel.
func (m *MockFileSystemWatcher) CloseErrors() {
	close(m.errors)
}

// AddedPaths returns the paths that were added to the watcher.
func (m *MockFileSystemWatcher) AddedPaths() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	paths := make([]string, len(m.addedPaths))
	copy(paths, m.addedPaths)
	return paths
}

// SetAddError configures the error to return from Add().
func (m *MockFileSystemWatcher) SetAddError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addErr = err
}

// RemovedPaths returns the paths that were removed from the watcher.
func (m *MockFileSystemWatcher) RemovedPaths() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	paths := make([]string, len(m.removedPaths))
	copy(paths, m.removedPaths)
	return paths
}

// SetRemoveError configures the error to return from Remove().
func (m *MockFileSystemWatcher) SetRemoveError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeErr = err
}

// SetCloseError configures the error to return from Close().
func (m *MockFileSystemWatcher) SetCloseError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeErr = err
}

// MockFileSystem implements FileSystem for testing.
type MockFileSystem struct {
	files   map[string]mockFileInfo
	dirs    map[string][]mockDirEntry
	statErr map[string]error
	readErr map[string]error
	mu      sync.RWMutex
}

// mockFileInfo implements fs.FileInfo for testing.
type mockFileInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
	isDir   bool
}

func (m mockFileInfo) Name() string       { return m.name }
func (m mockFileInfo) Size() int64        { return m.size }
func (m mockFileInfo) Mode() fs.FileMode  { return m.mode }
func (m mockFileInfo) ModTime() time.Time { return m.modTime }
func (m mockFileInfo) IsDir() bool        { return m.isDir }
func (m mockFileInfo) Sys() any           { return nil }

// mockDirEntry implements fs.DirEntry for testing.
type mockDirEntry struct {
	name  string
	isDir bool
	mode  fs.FileMode
	info  mockFileInfo
}

func (m mockDirEntry) Name() string               { return m.name }
func (m mockDirEntry) IsDir() bool                { return m.isDir }
func (m mockDirEntry) Type() fs.FileMode          { return m.mode.Type() }
func (m mockDirEntry) Info() (fs.FileInfo, error) { return m.info, nil }

// NewMockFileSystem creates a new mock filesystem for testing.
func NewMockFileSystem() *MockFileSystem {
	return &MockFileSystem{
		files:   make(map[string]mockFileInfo),
		dirs:    make(map[string][]mockDirEntry),
		statErr: make(map[string]error),
		readErr: make(map[string]error),
	}
}

// Stat returns file info for the given path.
func (m *MockFileSystem) Stat(name string) (fs.FileInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if err, ok := m.statErr[name]; ok {
		return nil, err
	}
	if info, ok := m.files[name]; ok {
		return info, nil
	}
	return nil, os.ErrNotExist
}

// ReadDir reads a directory and returns its entries.
func (m *MockFileSystem) ReadDir(name string) ([]fs.DirEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if err, ok := m.readErr[name]; ok {
		return nil, err
	}
	if entries, ok := m.dirs[name]; ok {
		result := make([]fs.DirEntry, len(entries))
		for i, e := range entries {
			result[i] = e
		}
		return result, nil
	}
	return nil, os.ErrNotExist
}

// ReadDirN reads up to n entries from a mock directory. n <= 0 returns all.
func (m *MockFileSystem) ReadDirN(name string, n int) ([]fs.DirEntry, error) {
	all, err := m.ReadDir(name)
	if err != nil {
		return nil, err
	}
	if n <= 0 || n >= len(all) {
		return all, nil
	}
	return all[:n], nil
}

// AddFile adds a mock file to the filesystem.
func (m *MockFileSystem) AddFile(path string, size int64, mode fs.FileMode) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[path] = mockFileInfo{
		name:    filepath.Base(path),
		size:    size,
		mode:    mode,
		modTime: time.Now(),
		isDir:   false,
	}
}

// AddDir adds a mock directory to the filesystem.
func (m *MockFileSystem) AddDir(path string, entries []string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.files[path] = mockFileInfo{
		name:    filepath.Base(path),
		mode:    fs.ModeDir | 0755,
		modTime: time.Now(),
		isDir:   true,
	}

	var dirEntries []mockDirEntry
	for _, name := range entries {
		dirEntries = append(dirEntries, mockDirEntry{
			name:  name,
			isDir: false,
			mode:  0644,
			info: mockFileInfo{
				name:    name,
				mode:    0644,
				modTime: time.Now(),
			},
		})
	}
	m.dirs[path] = dirEntries
}

// SetStatError sets the error to return for Stat on a specific path.
func (m *MockFileSystem) SetStatError(path string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statErr[path] = err
}

// SetReadDirError sets the error to return for ReadDir on a specific path.
func (m *MockFileSystem) SetReadDirError(path string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readErr[path] = err
}
