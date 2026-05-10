package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/sageox/ox/internal/gitutil"
)

// childMirrorEnabled reports whether we maintain a userspace mirror of
// fsnotify's per-file child set. Only useful on macOS where fsnotify's
// kqueue backend opens 1+N FDs per watched directory and skips closing the
// per-file FDs on dir Remove/Rename. On Linux/inotify each watch is a single
// FD (not per-file), so the mirror is dead weight.
var childMirrorEnabled = runtime.GOOS == "darwin"

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

	// maxFilesPerWatchedDir caps the per-dir child snapshot. fsnotify's kqueue
	// backend opens 1+N FDs per watched directory (1 for the dir, N per file via
	// watchDirectoryFiles). On Remove/Rename we replay this set to force fsnotify
	// to close the per-file FDs. A pathological directory with millions of files
	// would balloon both startup memory and the snapshot cost — cap it. Truncated
	// dirs lose the leak guarantee for any file beyond the cap, but those FDs
	// are bounded by file count regardless, and any single dir with >10k tracked
	// files almost certainly wants .gitignore tuning anyway.
	maxFilesPerWatchedDir = 10_000

	// gitRefreshInterval controls how often the git-tracked file set is refreshed.
	gitRefreshInterval = 30 * time.Second

	// fdPressureFloor is the minimum threshold the breaker can be armed at.
	// On a 256-FD soft ulimit (the macOS default) we want the breaker to fire
	// at the floor, not at half (128) — pruning under 128 already-tight FDs
	// has no headroom. 256 lets the breaker double as a "we're already at
	// limit" signal.
	fdPressureFloor = 256

	// fdPressureFallback is used when the soft RLIMIT_NOFILE can't be
	// determined (Windows, syscall failure). Sized for "small but not tiny"
	// — high enough to skip noise, low enough that even a misconfigured
	// host catches a leak before death.
	fdPressureFallback = 512
)

// computeFDPressureThreshold returns the FD count at which checkFDPressure
// triggers an emergency prune. Computed from the soft RLIMIT_NOFILE: half
// the soft limit, with fdPressureFloor as a lower bound and fdPressureFallback
// when the limit is unknown.
//
// Self-tuning so the breaker is meaningful on every platform:
//   - macOS default (256 soft limit) → 256 (kicks in at the floor)
//   - macOS dev raised (10240) → 5120 (ample recovery headroom)
//   - Linux server (65536+) → 32768
//
// The original hard-coded 4096 was effectively dead code on default ulimits
// because the OS would kill the process first.
func computeFDPressureThreshold(softLimit uint64) int {
	if softLimit == 0 {
		return fdPressureFallback
	}
	half := softLimit / 2
	if half < fdPressureFloor {
		return fdPressureFloor
	}
	// Cap conversion at math.MaxInt to be safe on 32-bit; in practice no
	// soft RLIMIT_NOFILE comes near that.
	if half > uint64(int(^uint(0)>>1)) {
		return int(^uint(0) >> 1)
	}
	return int(half)
}

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
	// dirChildren mirrors the per-directory child file set that fsnotify's
	// kqueue backend opens internally via watchDirectoryFiles. We need our
	// own copy because fsnotify's set is private and on Remove/Rename its
	// kqueue backend (backend_kqueue.go:489) calls w.remove(name, false) —
	// the false flag skips closing the per-file FDs. By replaying this
	// snapshot on Remove/Rename we issue watcher.Remove(child) per file,
	// forcing fsnotify down its file-Remove path which closes the FD via
	// unix.Close. Keyed by absolute directory path; values are absolute
	// child file paths.
	//
	// Empty on Linux (childMirrorEnabled=false) — inotify uses one FD per
	// watch, not per file, so there's nothing to mirror.
	dirChildren map[string][]string
	// dirMtimes tracks the directory mtime captured at snapshot time so the
	// periodic re-sync in pruneStaleWatches can detect drift cheaply (one
	// stat call per dir vs a full ReadDir). Only populated when
	// childMirrorEnabled.
	dirMtimes map[string]time.Time

	// fdPressureThreshold is the FD count at which checkFDPressure forces
	// an emergency prune. Computed from RLIMIT_NOFILE at construction.
	fdPressureThreshold int
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
		projectRoot:         projectRoot,
		logger:              logger,
		watcherFactory:      watcherFactory,
		fs:                  fileSystem,
		accumulator:         accumulator,
		tracker:             tracker,
		watchedDirs:         make(map[string]struct{}),
		dirChildren:         make(map[string][]string),
		dirMtimes:           make(map[string]time.Time),
		fdPressureThreshold: computeFDPressureThreshold(CurrentProcessFDLimit()),
	}
}

// Accumulator returns the underlying change accumulator.
func (pw *ProjectWatcher) Accumulator() *ChangeAccumulator {
	return pw.accumulator
}

// isAncestorTracked returns true if any parent directory of relPath is
// git-tracked. Used to allow new subdirectories under tracked parents
// (e.g. src/newpkg/) while blocking directories whose entire ancestry
// is untracked (e.g. node_modules/.pnpm/foo/).
func (pw *ProjectWatcher) isAncestorTracked(relPath string) bool {
	dir := filepath.Dir(relPath)
	for dir != "." && dir != "" {
		if pw.tracker.IsTrackedDir(dir) {
			return true
		}
		dir = filepath.Dir(dir)
	}
	return false
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

	pw.logger.Info("project watcher started",
		"root", pw.projectRoot,
		"dirs_watched", dirCount,
		"fd_pressure_threshold", pw.fdPressureThreshold,
		"fd_soft_limit", CurrentProcessFDLimit())
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
			pw.pruneStaleWatches(watcher)
			pw.checkFDPressure(watcher)
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

// addDir adds a single directory to the watcher and (on macOS) snapshots the
// per-file child set that fsnotify will auto-watch. The snapshot is consulted
// on Remove/Rename to force fsnotify to close each child FD.
//
// To close the TOCTOU window between snapshot and Add, we snapshot twice
// (pre-Add and post-Add) and union the results. A file racing into the dir
// will be present in at least one snapshot; a file racing out leaves a
// harmless ErrNonExistentWatch on its eventual Remove.
func (pw *ProjectWatcher) addDir(watcher FileSystemWatcher, path string) {
	pre := pw.snapshotDirFiles(path)
	if err := watcher.Add(path); err != nil {
		pw.logger.Debug("project watcher: failed to watch dir",
			"path", path, "error", err)
		return
	}
	post := pw.snapshotDirFiles(path)
	children := unionChildren(pre, post)
	mtime := pw.dirMtime(path)
	pw.mu.Lock()
	pw.watchedDirs[path] = struct{}{}
	if childMirrorEnabled {
		pw.dirChildren[path] = children
		pw.dirMtimes[path] = mtime
	}
	pw.mu.Unlock()
}

// snapshotDirFiles delegates to the package-level kqueue mirror helper. Kept
// as a method so the existing call sites and tests don't need to change.
func (pw *ProjectWatcher) snapshotDirFiles(path string) []string {
	return snapshotKqueueChildren(pw.fs, path, pw.logger)
}

// dirMtime returns the directory mtime, used by pruneStaleWatches as a cheap
// drift detector. Returns zero time on error or non-Darwin (where we don't
// use it).
func (pw *ProjectWatcher) dirMtime(path string) time.Time {
	if !childMirrorEnabled {
		return time.Time{}
	}
	info, err := pw.fs.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// unionChildren is a thin wrapper around the package-level kqueue mirror
// helper, kept so the existing call sites in addDir/acquireDir don't need
// renaming.
func unionChildren(a, b []string) []string {
	return unionKqueueChildren(a, b)
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

	// new directory created — add to watch list only if it's under a git-tracked
	// parent. Without this guard, transient directories (node_modules, build
	// outputs) get watched unconditionally, opening 1+N kqueue FDs per directory
	// on macOS. Those FDs only get pruned on the next gitRefreshInterval (30s),
	// but by then fsnotify has already opened per-file FDs that the child
	// snapshot may miss — producing revoked-but-never-closed FDs that accumulate
	// for the daemon's lifetime.
	if isDir && event.Op&fsnotify.Create != 0 {
		if pw.tracker.IsTrackedDir(relPath) || pw.isAncestorTracked(relPath) {
			pw.mu.Lock()
			_, alreadyWatched := pw.watchedDirs[event.Name]
			atCap := len(pw.watchedDirs) >= maxWatchedDirs
			pw.mu.Unlock()
			if !alreadyWatched && !atCap {
				pw.addDir(watcher, event.Name)
			}
		}
	}

	// individual file Create inside a watched dir — add to the parent's
	// child snapshot. fsnotify v1.9.0's kqueue backend reacts to NOTE_WRITE
	// on the directory by calling dirChange → sendCreateIfNew, which opens
	// a per-file kqueue FD for any new file. So files created post-Add do
	// have FDs we'll need to release if the parent later disappears. Skip
	// if the parent isn't tracked or we're already at the snapshot cap.
	// Darwin-only.
	if childMirrorEnabled && !isDir && event.Op&fsnotify.Create != 0 {
		parent := filepath.Dir(event.Name)
		pw.mu.Lock()
		if _, watched := pw.watchedDirs[parent]; watched {
			children := pw.dirChildren[parent]
			if len(children) < maxFilesPerWatchedDir {
				present := false
				for _, c := range children {
					if c == event.Name {
						present = true
						break
					}
				}
				if !present {
					pw.dirChildren[parent] = append(children, event.Name)
				}
			}
		}
		pw.mu.Unlock()
	}

	// individual file Remove/Rename inside a watched dir — drop the file
	// from the parent's child snapshot. fsnotify's kqueue backend already
	// closed the per-file FD via the file's own NOTE_DELETE; keeping it in
	// the snapshot would cause a noisy ErrNonExistentWatch on the next dir
	// Remove and waste effort. Darwin-only since dirChildren is empty on
	// other platforms.
	if childMirrorEnabled && !isDir && event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		parent := filepath.Dir(event.Name)
		pw.mu.Lock()
		if children, ok := pw.dirChildren[parent]; ok {
			out := children[:0]
			for _, c := range children {
				if c != event.Name {
					out = append(out, c)
				}
			}
			pw.dirChildren[parent] = out
		}
		pw.mu.Unlock()
	}

	// directory removed — remove from tracked set and release kqueue/inotify FDs.
	// fsnotify v1.9.0's kqueue backend (backend_kqueue.go:489) calls
	// w.remove(name, false) on NOTE_DELETE, which does NOT recursively close
	// the per-file FDs that watchDirectoryFiles auto-opened. We compensate by
	// replaying our snapshot of those file paths and calling watcher.Remove on
	// each — which routes through w.remove(name, true) in fsnotify, closing
	// the FD via unix.Close. We also walk descendant watched dirs (in case
	// the kernel only fires a single Remove for the top of an rm -rf), and
	// flush their snapshots too. Without this, a long-lived daemon against
	// churning build-output dirs accumulates FDs without bound — the host
	// symptom we saw was lsof itself hanging on the daemon PID.
	if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		pw.mu.Lock()
		_, wasWatched := pw.watchedDirs[event.Name]
		delete(pw.watchedDirs, event.Name)
		childFiles := pw.dirChildren[event.Name]
		delete(pw.dirChildren, event.Name)
		delete(pw.dirMtimes, event.Name)

		prefix := event.Name + string(filepath.Separator)
		var descendantDirs []string
		for d := range pw.watchedDirs {
			if strings.HasPrefix(d, prefix) {
				descendantDirs = append(descendantDirs, d)
			}
		}
		for _, d := range descendantDirs {
			childFiles = append(childFiles, pw.dirChildren[d]...)
			delete(pw.watchedDirs, d)
			delete(pw.dirChildren, d)
			delete(pw.dirMtimes, d)
		}
		pw.mu.Unlock()

		if wasWatched {
			_ = watcher.Remove(event.Name)
		}
		for _, d := range descendantDirs {
			_ = watcher.Remove(d)
		}
		// The crucial bit: explicitly Remove each tracked child file so
		// fsnotify closes its per-file kqueue FD. Tolerates ErrNonExistentWatch
		// for files that the kernel already auto-cleaned via individual
		// NOTE_DELETE — that's a no-op on our side. childFiles is empty on
		// non-Darwin so this loop costs nothing there.
		for _, f := range childFiles {
			_ = watcher.Remove(f)
		}
	}

	// only pass through tracked files (or newly created files that may become tracked)
	if !isDir && event.Op&fsnotify.Create == 0 && !pw.tracker.IsTrackedFile(relPath) {
		return
	}

	pw.accumulator.AddEvent(relPath, event.Op, isDir)
}

// pruneStaleWatches removes watches for directories that are no longer
// git-tracked. Called after each tracker.Refresh() to release kqueue/inotify
// FDs for directories that were transiently watched (build outputs, temp dirs).
func (pw *ProjectWatcher) pruneStaleWatches(watcher FileSystemWatcher) {
	pw.mu.Lock()
	snapshot := make(map[string]struct{}, len(pw.watchedDirs))
	for d := range pw.watchedDirs {
		snapshot[d] = struct{}{}
	}
	pw.mu.Unlock()

	var pruned int
	for absDir := range snapshot {
		if absDir == pw.projectRoot {
			continue
		}
		relDir, err := filepath.Rel(pw.projectRoot, absDir)
		if err != nil {
			continue
		}
		if !pw.tracker.IsTrackedDir(relDir) {
			pw.mu.Lock()
			children := pw.dirChildren[absDir]
			delete(pw.watchedDirs, absDir)
			delete(pw.dirChildren, absDir)
			delete(pw.dirMtimes, absDir)
			pw.mu.Unlock()
			_ = watcher.Remove(absDir)
			// Force fsnotify to close per-file kqueue FDs for any child files
			// it auto-watched on Add. Without this, pruning a stale dir leaks
			// the same per-file FDs that the Remove/Rename handler plugs.
			// children is empty on non-Darwin — no-op there.
			for _, f := range children {
				_ = watcher.Remove(f)
			}
			pruned++
			continue
		}

		// Still-tracked dir: re-snapshot if mtime advanced since last check.
		// This is the safety net for any drift the event-driven incremental
		// updates miss (e.g., events the kernel coalesced under churn).
		// Cheap on Darwin (one stat per watched dir per refresh), no-op on
		// other platforms because the mirror is unused there.
		if childMirrorEnabled {
			pw.resnapshotIfChanged(absDir)
		}
	}
	if pruned > 0 {
		pw.logger.Info("project watcher: pruned stale watches", "count", pruned)
	}
}

// dirOffender names a watched directory and the size of its kqueue per-file
// FD footprint (1 + len(children) on macOS, ~1 on Linux).
type dirOffender struct {
	Dir   string
	Files int
}

// topWatchedDirsByChildCount snapshots watchedDirs and returns the top n
// by per-dir child file count, descending. Used by the FD-pressure breaker
// to surface the actual culprit so the user can act without lsof.
//
// On Linux the dirChildren mirror is empty (one inotify FD per watch, not
// per file), so this returns nothing meaningful — the breaker rarely fires
// there anyway.
func (pw *ProjectWatcher) topWatchedDirsByChildCount(n int) []dirOffender {
	pw.mu.Lock()
	out := make([]dirOffender, 0, len(pw.watchedDirs))
	for d := range pw.watchedDirs {
		files := len(pw.dirChildren[d])
		if files == 0 {
			continue
		}
		rel, err := filepath.Rel(pw.projectRoot, d)
		if err != nil {
			rel = d
		}
		out = append(out, dirOffender{Dir: rel, Files: files})
	}
	pw.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].Files > out[j].Files
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// formatOffenders renders dirOffender slices as a single grep-friendly token
// like "node_modules/.pnpm/@aws-sdk(4200),dist(1204)". Empty string when the
// slice is empty.
func formatOffenders(offenders []dirOffender) string {
	if len(offenders) == 0 {
		return ""
	}
	parts := make([]string, len(offenders))
	for i, o := range offenders {
		parts[i] = fmt.Sprintf("%s(%d)", o.Dir, o.Files)
	}
	return strings.Join(parts, ",")
}

// checkFDPressure forces an aggressive prune when the process FD count
// exceeds the per-watcher threshold. Safety net for any transient watches
// that slip through the git-tracking guard during rapid churn.
//
// Logs the top offenders before pruning and the before/after watched-dir
// counts after, so users see *which* tree is leaking and can add it to
// .gitignore — turning a vague "daemon FD count is high" into actionable
// "node_modules/.pnpm has 4,200 watched files."
func (pw *ProjectWatcher) checkFDPressure(watcher FileSystemWatcher) {
	count := CurrentProcessFDCount()
	if count < 0 || count < pw.fdPressureThreshold {
		return
	}
	offenders := pw.topWatchedDirsByChildCount(5)
	before := pw.WatchedDirCount()
	pw.logger.Warn("project watcher: FD pressure detected",
		"fd_count", count,
		"threshold", pw.fdPressureThreshold,
		"watched_dirs", before,
		"top_offenders", formatOffenders(offenders))
	pw.pruneStaleWatches(watcher)
	after := pw.WatchedDirCount()
	pw.logger.Warn("project watcher: emergency prune complete",
		"watched_before", before,
		"watched_after", after,
		"freed", before-after,
		"fd_count_after", CurrentProcessFDCount())
}

// resnapshotIfChanged re-reads the per-file child snapshot for absDir if its
// directory mtime has advanced since the last snapshot. Catches drift from
// kernel-event coalescing without requiring a full ReadDir on every cycle.
// Caller must NOT hold pw.mu.
func (pw *ProjectWatcher) resnapshotIfChanged(absDir string) {
	currentMtime := pw.dirMtime(absDir)
	if currentMtime.IsZero() {
		return // dir gone or unstattable; Remove/Rename handler will catch up
	}
	pw.mu.Lock()
	last, known := pw.dirMtimes[absDir]
	pw.mu.Unlock()
	if known && !currentMtime.After(last) {
		return // no drift since last snapshot
	}
	fresh := pw.snapshotDirFiles(absDir)
	pw.mu.Lock()
	if _, stillWatched := pw.watchedDirs[absDir]; stillWatched {
		pw.dirChildren[absDir] = fresh
		pw.dirMtimes[absDir] = currentMtime
	}
	pw.mu.Unlock()
}

// WatchedDirCount returns the number of directories being watched.
func (pw *ProjectWatcher) WatchedDirCount() int {
	pw.mu.Lock()
	defer pw.mu.Unlock()
	return len(pw.watchedDirs)
}

// watchedDirHandle pairs a watcher.Add with the per-file child snapshot and a
// matching Release that closes everything fsnotify opened, including the
// per-file FDs its kqueue backend auto-opens via watchDirectoryFiles.
//
// This is NOT RAII — Go has no scope-bound deterministic cleanup that survives
// the function in which the resource is held — and the daemon's watcher must
// outlive any single function. The handle is a discipline helper: a future
// caller that grabs a watch through acquireDir cannot forget to register the
// children for cleanup, because the only path to a watch goes through this
// type. Callers must call Release explicitly (or via defer) before the handle
// is dropped.
type watchedDirHandle struct {
	watcher FileSystemWatcher
	path    string
	pw      *ProjectWatcher
}

// acquireDir wraps addDir, returning a handle whose Release is paired with
// the underlying Add. Equivalent in effect to addDir (including the pre/post
// snapshot to close the TOCTOU window with watchDirectoryFiles) but enforces
// the register-and-release pairing through the type system rather than
// convention.
func (pw *ProjectWatcher) acquireDir(watcher FileSystemWatcher, path string) (*watchedDirHandle, error) {
	pre := pw.snapshotDirFiles(path)
	if err := watcher.Add(path); err != nil {
		pw.logger.Debug("project watcher: acquireDir failed",
			"path", path, "error", err)
		return nil, err
	}
	post := pw.snapshotDirFiles(path)
	children := unionChildren(pre, post)
	mtime := pw.dirMtime(path)
	pw.mu.Lock()
	pw.watchedDirs[path] = struct{}{}
	if childMirrorEnabled {
		pw.dirChildren[path] = children
		pw.dirMtimes[path] = mtime
	}
	pw.mu.Unlock()
	return &watchedDirHandle{watcher: watcher, path: path, pw: pw}, nil
}

// Release closes the watch on the dir AND every per-file FD fsnotify opened
// for it. Safe to call multiple times.
func (h *watchedDirHandle) Release() {
	if h == nil || h.pw == nil {
		return
	}
	h.pw.mu.Lock()
	children := h.pw.dirChildren[h.path]
	delete(h.pw.dirChildren, h.path)
	delete(h.pw.dirMtimes, h.path)
	delete(h.pw.watchedDirs, h.path)
	h.pw.mu.Unlock()
	for _, f := range children {
		_ = h.watcher.Remove(f)
	}
	_ = h.watcher.Remove(h.path)
}
