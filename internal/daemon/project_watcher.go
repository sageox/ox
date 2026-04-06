package daemon

import (
	"context"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/sageox/ox/internal/gitutil"
)

// ChangeType categorizes a filesystem change.
type ChangeType string

const (
	ChangeCreated  ChangeType = "created"
	ChangeModified ChangeType = "modified"
	ChangeDeleted  ChangeType = "deleted"
	ChangeRenamed  ChangeType = "renamed"
)

// Short returns a compact label for human-readable murmur content.
func (ct ChangeType) Short() string {
	switch ct {
	case ChangeCreated:
		return "new"
	case ChangeModified:
		return "mod"
	case ChangeDeleted:
		return "del"
	case ChangeRenamed:
		return "mv"
	default:
		return string(ct)
	}
}

// FileChange represents a single observed filesystem change.
type FileChange struct {
	Path       string // relative to project root
	ChangeType ChangeType
	IsDir      bool
	Timestamp  time.Time
}

// ChangeAccumulator batches raw fsnotify events into settled change sets.
// Inspired by Watchman's PendingCollection + settle mechanism.
type ChangeAccumulator struct {
	mu           sync.Mutex
	pending      map[string]*FileChange // path -> latest change
	settled      []FileChange           // changes ready for consumption
	settlePeriod time.Duration
	settleTimer  *time.Timer
	stopped      bool
	onSettled    func() // called (in a goroutine) when pending changes settle
}

// NewChangeAccumulator creates an accumulator with the given settle period.
func NewChangeAccumulator(settlePeriod time.Duration) *ChangeAccumulator {
	return &ChangeAccumulator{
		pending:      make(map[string]*FileChange),
		settlePeriod: settlePeriod,
	}
}

// AddEvent adds a filesystem event, collapsing with existing pending changes.
func (a *ChangeAccumulator) AddEvent(relPath string, op fsnotify.Op, isDir bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.stopped {
		return
	}

	now := time.Now()
	changeType := a.classifyChange(relPath, op)

	existing, exists := a.pending[relPath]
	if exists {
		// collapse: apply aggregation rules
		changeType = a.collapseChange(existing.ChangeType, changeType)
		if changeType == "" {
			// create+delete = suppressed (temp file)
			delete(a.pending, relPath)
			a.resetSettleTimer()
			return
		}
	}

	a.pending[relPath] = &FileChange{
		Path:       relPath,
		ChangeType: changeType,
		IsDir:      isDir,
		Timestamp:  now,
	}
	a.resetSettleTimer()
}

// classifyChange maps fsnotify ops to ChangeType.
func (a *ChangeAccumulator) classifyChange(_ string, op fsnotify.Op) ChangeType {
	switch {
	case op&fsnotify.Create != 0:
		return ChangeCreated
	case op&fsnotify.Remove != 0:
		return ChangeDeleted
	case op&fsnotify.Rename != 0:
		return ChangeRenamed
	case op&fsnotify.Write != 0:
		return ChangeModified
	case op&fsnotify.Chmod != 0:
		return ChangeModified
	default:
		return ChangeModified
	}
}

// collapseChange applies Watchman-inspired aggregation rules.
func (a *ChangeAccumulator) collapseChange(existing, incoming ChangeType) ChangeType {
	switch {
	case existing == ChangeCreated && incoming == ChangeDeleted:
		// create then delete = temp file, suppress
		return ""
	case existing == ChangeCreated && incoming == ChangeModified:
		// create then write = still created
		return ChangeCreated
	case existing == ChangeDeleted && incoming == ChangeCreated:
		// delete then create = atomic save pattern
		return ChangeModified
	case existing == ChangeRenamed && incoming == ChangeCreated:
		return ChangeModified
	default:
		// default: use the latest event type
		return incoming
	}
}

// resetSettleTimer resets the settle timer. Must be called with mu held.
func (a *ChangeAccumulator) resetSettleTimer() {
	if a.settleTimer != nil {
		a.settleTimer.Stop()
	}
	a.settleTimer = time.AfterFunc(a.settlePeriod, func() {
		a.settle()
	})
}

// SetOnSettled registers a callback invoked (in a goroutine) each time pending
// changes settle. Used by the dirty overlay debouncer to trigger index rebuilds.
func (a *ChangeAccumulator) SetOnSettled(fn func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.onSettled = fn
}

// settle moves pending changes to settled. Called when settle timer fires.
func (a *ChangeAccumulator) settle() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.pending) == 0 {
		return
	}

	for _, fc := range a.pending {
		a.settled = append(a.settled, *fc)
	}
	a.pending = make(map[string]*FileChange)

	if a.onSettled != nil {
		go a.onSettled()
	}
}

// DrainSettled returns and clears all settled changes.
func (a *ChangeAccumulator) DrainSettled() []FileChange {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.settled) == 0 {
		return nil
	}
	result := a.settled
	a.settled = nil
	return result
}

// PendingCount returns the number of pending (unsettled) changes.
func (a *ChangeAccumulator) PendingCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.pending)
}

// Stop prevents further timer callbacks.
func (a *ChangeAccumulator) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopped = true
	if a.settleTimer != nil {
		a.settleTimer.Stop()
		a.settleTimer = nil
	}
}

// GitTrackedMatcher determines whether a filesystem path should be watched
// by checking if it is tracked by git. Only git-versioned directories and
// files are watched — this is always correct and never drifts from reality.
type GitTrackedMatcher struct {
	projectRoot  string
	trackedDirs  map[string]struct{} // relative dir paths that contain tracked files
	trackedFiles map[string]struct{} // relative file paths tracked by git
	mu           sync.RWMutex
	logger       *slog.Logger
}

// NewGitTrackedMatcher creates a matcher that watches only git-tracked paths.
// Runs git ls-files at construction to build the initial set.
func NewGitTrackedMatcher(projectRoot string, logger *slog.Logger) *GitTrackedMatcher {
	m := &GitTrackedMatcher{
		projectRoot:  projectRoot,
		trackedDirs:  make(map[string]struct{}),
		trackedFiles: make(map[string]struct{}),
		logger:       logger,
	}
	m.Refresh()
	return m
}

// Refresh re-reads the tracked file set from git.
func (m *GitTrackedMatcher) Refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	output, err := gitutil.RunGit(ctx, m.projectRoot, "ls-files", "--cached")
	if err != nil {
		m.logger.Warn("git-tracked matcher: failed to list tracked files", "error", err)
		return
	}

	dirs := make(map[string]struct{})
	files := make(map[string]struct{})

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		files[line] = struct{}{}
		// record every parent directory
		dir := filepath.Dir(line)
		for dir != "." && dir != "" {
			dirs[dir] = struct{}{}
			dir = filepath.Dir(dir)
		}
	}

	m.mu.Lock()
	m.trackedDirs = dirs
	m.trackedFiles = files
	m.mu.Unlock()

	m.logger.Debug("git-tracked matcher refreshed", "tracked_dirs", len(dirs), "tracked_files", len(files))
}

// IsTrackedDir returns true if the relative directory contains tracked files.
// The project root (relPath == ".") is always tracked.
func (m *GitTrackedMatcher) IsTrackedDir(relPath string) bool {
	if relPath == "." || relPath == "" {
		return true
	}
	// always skip .git
	if relPath == ".git" || strings.HasPrefix(relPath, ".git"+string(filepath.Separator)) {
		return false
	}
	m.mu.RLock()
	_, ok := m.trackedDirs[relPath]
	m.mu.RUnlock()
	return ok
}

// IsTrackedFile returns true if the relative file path is tracked by git.
func (m *GitTrackedMatcher) IsTrackedFile(relPath string) bool {
	m.mu.RLock()
	_, ok := m.trackedFiles[relPath]
	m.mu.RUnlock()
	return ok
}

// TrackedDirs returns a sorted list of tracked directory paths.
func (m *GitTrackedMatcher) TrackedDirs() []string {
	m.mu.RLock()
	dirs := make([]string, 0, len(m.trackedDirs))
	for d := range m.trackedDirs {
		dirs = append(dirs, d)
	}
	m.mu.RUnlock()
	sort.Strings(dirs)
	return dirs
}

const (
	maxWatchedDirs = 10_000

	// gitRefreshInterval controls how often the git-tracked file set is refreshed.
	gitRefreshInterval = 30 * time.Second
)

// ProjectWatcher watches a project directory recursively for file changes
// and feeds events through a ChangeAccumulator for settled batch processing.
// Only watches directories that contain git-tracked files.
type ProjectWatcher struct {
	projectRoot    string
	logger         *slog.Logger
	watcherFactory WatcherFactory
	fs             FileSystem
	accumulator    *ChangeAccumulator
	tracker        *GitTrackedMatcher

	mu          sync.Mutex
	watchedDirs map[string]struct{}
}

// NewProjectWatcher creates a new project watcher.
func NewProjectWatcher(
	projectRoot string,
	logger *slog.Logger,
	watcherFactory WatcherFactory,
	fileSystem FileSystem,
	accumulator *ChangeAccumulator,
	tracker *GitTrackedMatcher,
) *ProjectWatcher {
	return &ProjectWatcher{
		projectRoot:    projectRoot,
		logger:         logger,
		watcherFactory: watcherFactory,
		fs:             fileSystem,
		accumulator:    accumulator,
		tracker:        tracker,
		watchedDirs:    make(map[string]struct{}),
	}
}

// Accumulator returns the underlying change accumulator.
func (pw *ProjectWatcher) Accumulator() *ChangeAccumulator {
	return pw.accumulator
}

// Start begins watching the project directory. Blocks until ctx is canceled.
func (pw *ProjectWatcher) Start(ctx context.Context) {
	watcher, err := pw.watcherFactory()
	if err != nil {
		pw.logger.Error("project watcher: failed to create fsnotify watcher", "error", err)
		return
	}
	defer watcher.Close()

	// recursively add tracked directories
	pw.walkAndWatch(watcher)

	pw.mu.Lock()
	dirCount := len(pw.watchedDirs)
	dirs := make([]string, 0, dirCount)
	for d := range pw.watchedDirs {
		rel, err := filepath.Rel(pw.projectRoot, d)
		if err == nil {
			dirs = append(dirs, rel)
		}
	}
	pw.mu.Unlock()
	sort.Strings(dirs)

	pw.logger.Info("project watcher started", "root", pw.projectRoot, "dirs_watched", dirCount)
	for _, d := range dirs {
		pw.logger.Debug("project watcher: watching dir", "dir", d)
	}

	// periodic git refresh to pick up new tracked files
	refreshTicker := time.NewTicker(gitRefreshInterval)
	defer refreshTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			pw.accumulator.Stop()
			pw.logger.Info("project watcher stopped")
			return

		case event, ok := <-watcher.Events():
			if !ok {
				pw.accumulator.Stop()
				return
			}
			pw.handleEvent(watcher, event)

		case err, ok := <-watcher.Errors():
			if !ok {
				pw.accumulator.Stop()
				return
			}
			pw.logger.Error("project watcher error", "error", err)

		case <-refreshTicker.C:
			pw.tracker.Refresh()
		}
	}
}

// walkAndWatch adds git-tracked directories to the watcher.
// Only directories containing tracked files are watched.
func (pw *ProjectWatcher) walkAndWatch(watcher FileSystemWatcher) {
	// always watch root
	pw.addDir(watcher, pw.projectRoot)

	// watch each directory that contains tracked files
	for _, relDir := range pw.tracker.TrackedDirs() {
		pw.mu.Lock()
		atCap := len(pw.watchedDirs) >= maxWatchedDirs
		pw.mu.Unlock()
		if atCap {
			pw.logger.Warn("project watcher: reached directory watch limit",
				"max", maxWatchedDirs)
			break
		}

		absDir := filepath.Join(pw.projectRoot, relDir)
		// verify the directory actually exists on disk
		if info, err := pw.fs.Stat(absDir); err != nil || !info.IsDir() {
			continue
		}
		pw.addDir(watcher, absDir)
	}
}

// addDir adds a single directory to the watcher.
func (pw *ProjectWatcher) addDir(watcher FileSystemWatcher, path string) {
	if err := watcher.Add(path); err != nil {
		pw.logger.Debug("project watcher: failed to watch dir",
			"path", path, "error", err)
		return
	}
	pw.mu.Lock()
	pw.watchedDirs[path] = struct{}{}
	pw.mu.Unlock()
}

// handleEvent processes a single fsnotify event.
func (pw *ProjectWatcher) handleEvent(watcher FileSystemWatcher, event fsnotify.Event) {
	relPath, err := filepath.Rel(pw.projectRoot, event.Name)
	if err != nil || relPath == "." {
		return
	}

	// always skip .git internals
	if relPath == ".git" || strings.HasPrefix(relPath, ".git"+string(filepath.Separator)) {
		return
	}

	// only care about write/create/remove/rename
	if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
		return
	}

	// check if this is a directory event
	isDir := false
	info, statErr := pw.fs.Stat(event.Name)
	if statErr == nil {
		isDir = info.IsDir()
	}

	// new directory created — add to watch list (will be validated on next git refresh)
	if isDir && event.Op&fsnotify.Create != 0 {
		pw.mu.Lock()
		atCap := len(pw.watchedDirs) >= maxWatchedDirs
		pw.mu.Unlock()
		if !atCap {
			pw.addDir(watcher, event.Name)
		}
	}

	// directory removed — remove from tracked set
	if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		pw.mu.Lock()
		delete(pw.watchedDirs, event.Name)
		pw.mu.Unlock()
	}

	// only pass through tracked files (or newly created files that may become tracked)
	if !isDir && event.Op&fsnotify.Create == 0 && !pw.tracker.IsTrackedFile(relPath) {
		return
	}

	pw.accumulator.AddEvent(relPath, event.Op, isDir)
}

// WatchedDirCount returns the number of directories being watched.
func (pw *ProjectWatcher) WatchedDirCount() int {
	pw.mu.Lock()
	defer pw.mu.Unlock()
	return len(pw.watchedDirs)
}
