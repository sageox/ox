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
)

// defaultLedgerPollInterval is how often the ledger Watcher samples its
// directory. The ledger root has only a handful of direct children, so a
// ReadDir per tick is cheap; like GitPollWatcher this holds zero file
// descriptors (the old fsnotify path opened one FD per file in the dir on
// macOS via the kqueue backend).
const defaultLedgerPollInterval = 2 * time.Second

// FileSystem abstracts filesystem operations for testability.
type FileSystem interface {
	// Stat returns file info for the given path.
	Stat(name string) (fs.FileInfo, error)

	// ReadDir reads a directory and returns its entries.
	ReadDir(name string) ([]fs.DirEntry, error)

	// ReadDirN reads up to n entries from a directory and returns them.
	// If n <= 0 returns all entries (equivalent to ReadDir).
	ReadDirN(name string, n int) ([]fs.DirEntry, error)
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
// the full listing. Falls back to ReadDir when n <= 0.
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

// dirEntrySig fingerprints a direct child of the watched directory so the
// poller can detect adds, removes, and content changes between ticks.
type dirEntrySig struct {
	mtime time.Time
	size  int64
	isDir bool
}

// Watcher monitors the ledger directory for changes by polling. It watches the
// ledger root's direct children (non-recursive, matching the prior single-dir
// fsnotify behavior) and fires a debounced callback when they change.
type Watcher struct {
	path           string
	debounceWindow time.Duration
	pollInterval   time.Duration
	logger         *slog.Logger
	fs             FileSystem

	// debouncing
	mu       sync.Mutex
	timer    *time.Timer
	callback func()
	stopped  bool

	// prev is the last-seen fingerprint of the watched directory's direct
	// children; owned solely by the Start goroutine.
	prev map[string]dirEntrySig
}

// NewWatcher creates a new ledger watcher.
func NewWatcher(path string, debounceWindow time.Duration, logger *slog.Logger) *Watcher {
	return NewWatcherWithFS(path, debounceWindow, defaultLedgerPollInterval, logger, &RealFileSystem{})
}

// NewWatcherWithFS creates a watcher with injectable filesystem and poll
// interval. Primarily for testing.
func NewWatcherWithFS(
	path string,
	debounceWindow time.Duration,
	pollInterval time.Duration,
	logger *slog.Logger,
	fileSystem FileSystem,
) *Watcher {
	if pollInterval <= 0 {
		pollInterval = defaultLedgerPollInterval
	}
	return &Watcher{
		path:           path,
		debounceWindow: debounceWindow,
		pollInterval:   pollInterval,
		logger:         logger,
		fs:             fileSystem,
	}
}

// Start polls for ledger changes until ctx is canceled, invoking onChange
// (debounced) when the watched directory changes. The first snapshot is a
// baseline, so pre-existing content does not trigger a callback.
func (w *Watcher) Start(ctx context.Context, onChange func()) {
	w.mu.Lock()
	w.callback = onChange
	w.stopped = false
	w.mu.Unlock()

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	w.prev = w.snapshot()
	w.logger.Info("watching ledger directory", "path", w.path, "poll_interval", w.pollInterval)

	for {
		select {
		case <-ctx.Done():
			w.Stop()
			w.logger.Info("watcher stopped")
			return
		case <-ticker.C:
			w.pollOnce()
		}
	}
}

// pollOnce snapshots the directory and debounces a callback if it changed.
func (w *Watcher) pollOnce() {
	cur := w.snapshot()
	changed := !sameFingerprint(w.prev, cur)
	w.prev = cur
	if changed {
		w.logger.Debug("ledger change detected", "path", w.path)
		w.debounce()
	}
}

// snapshot fingerprints the watched directory's relevant direct children.
func (w *Watcher) snapshot() map[string]dirEntrySig {
	entries, err := w.fs.ReadDir(w.path)
	if err != nil {
		w.logger.Debug("ledger watcher: ReadDir failed", "path", w.path, "error", err)
		return nil
	}
	sig := make(map[string]dirEntrySig, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !ledgerWatchIncludes(name) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		sig[name] = dirEntrySig{mtime: info.ModTime(), size: info.Size(), isDir: e.IsDir()}
	}
	return sig
}

// ledgerWatchIncludes reports whether a direct child name is relevant. Mirrors
// the prior fsnotify handler: skip .git internals and hidden files, except the
// .sageox directory which carries ledger content.
func ledgerWatchIncludes(name string) bool {
	if strings.Contains(name, ".git") {
		return false
	}
	if strings.HasPrefix(name, ".") && name != ".sageox" {
		return false
	}
	return true
}

// sameFingerprint reports whether two directory fingerprints are equal.
func sameFingerprint(a, b map[string]dirEntrySig) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || av.size != bv.size || av.isDir != bv.isDir || !av.mtime.Equal(bv.mtime) {
			return false
		}
	}
	return true
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

// debounce triggers the callback after the debounce window.
func (w *Watcher) debounce() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.stopped {
		return
	}

	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}

	w.timer = time.AfterFunc(w.debounceWindow, func() {
		w.mu.Lock()
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
