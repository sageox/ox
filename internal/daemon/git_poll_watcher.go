package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// gitPollInterval is how often GitPollWatcher samples `git status`. Kept at or
// below the ChangeAccumulator settle period (3s) so a change lands in a settled
// batch about as fast as the old fsnotify path delivered it.
//
// `git status` is O(changed files) against a warm index — sub-100ms even on a
// large monorepo — and the daemon already runs it on every dirty-overlay
// rebuild, so steady-state cost is negligible. Crucially, unlike the recursive
// fsnotify ProjectWatcher this replaces, it holds ZERO file descriptors: the
// kqueue backend opened one FD per watched file+dir (~11k FDs on a monorepo),
// which is what this change exists to eliminate.
const gitPollInterval = 2 * time.Second

// gitStatusTimeout bounds a single status poll so a wedged git subprocess can't
// stall the poll loop. Kept ~10x the p99 of a warm `git status` on a large repo
// so a transiently slow poll still completes, while a genuinely hung subprocess
// (NFS stall, index.lock contention) is reaped promptly rather than dragging the
// poll cadence out toward 30s.
const gitStatusTimeout = 10 * time.Second

// GitPollWatcher detects working-tree changes by polling `git status` rather
// than holding an fsnotify watch on every file. It synthesizes ChangeAccumulator
// events from the delta between consecutive polls, so every downstream consumer
// (dirty-overlay debouncer, file-change murmurs) is unchanged.
//
// Polling git is the right tool here because both consumers are already
// git-status-shaped and rate-limited (5s debounce + 30s minimum rebuild
// interval), so real-time fsnotify events bought nothing — we were holding ~11k
// FDs only to answer "did anything change". git status answers that with no
// watches and honors .gitignore for free.
type GitPollWatcher struct {
	projectRoot string
	accumulator *ChangeAccumulator
	logger      *slog.Logger
	interval    time.Duration

	// prev maps a working-tree-relative path to its last-seen porcelain XY
	// status code. prevMtime tracks the mtime of each non-deleted dirty path so
	// repeated saves to an already-dirty file (unchanged XY code) still emit a
	// change. Both are owned solely by the Start goroutine — no locking needed.
	//
	// Both maps are bounded by the current dirty-set size: each poll rebuilds
	// them wholesale from `git status`, so paths that are reverted or committed
	// (and thus leave the dirty set) are implicitly pruned on the next poll.
	prev      map[string]string
	prevMtime map[string]time.Time
}

// NewGitPollWatcher creates a watcher that polls git status at projectRoot and
// feeds synthesized events into accumulator.
func NewGitPollWatcher(projectRoot string, accumulator *ChangeAccumulator, logger *slog.Logger) *GitPollWatcher {
	return &GitPollWatcher{
		projectRoot: projectRoot,
		accumulator: accumulator,
		logger:      logger,
		interval:    gitPollInterval,
		prev:        make(map[string]string),
		prevMtime:   make(map[string]time.Time),
	}
}

// Start polls until ctx is canceled. The first poll establishes a baseline
// WITHOUT emitting events — matching fsnotify semantics where only changes that
// occur after the watch starts are reported, so a daemon starting against an
// already-dirty tree doesn't murmur the entire pre-existing change set.
func (w *GitPollWatcher) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	w.poll(ctx, true) // baseline only — record state, emit nothing
	w.logger.Info("git poll watcher started", "root", w.projectRoot, "interval", w.interval)

	for {
		select {
		case <-ctx.Done():
			w.accumulator.Stop()
			w.logger.Info("git poll watcher stopped")
			return
		case <-ticker.C:
			w.poll(ctx, false)
		}
	}
}

// poll runs one `git status` and emits accumulator events for the delta versus
// the previous poll. When baseline is true it only records state.
//
// A path emits an event when it is newly dirty, when its porcelain status code
// transitions (e.g. " M" -> "MM"), or when its mtime advances while still dirty
// (a repeated save). Paths that leave the dirty set (reverted/committed) emit
// nothing — matching the old fsnotify watcher, which saw no file-content event
// in those cases either; the hourly index maintenance and the next genuine edit
// reconcile the overlay.
func (w *GitPollWatcher) poll(ctx context.Context, baseline bool) {
	cur, err := w.gitStatus(ctx)
	if err != nil {
		// Transient (e.g. git index.lock during a checkout). Keep the previous
		// snapshot so the next successful poll emits a true delta, not a burst.
		w.logger.Debug("git poll watcher: git status failed", "error", err)
		return
	}

	nextMtime := make(map[string]time.Time, len(cur))
	for path, code := range cur {
		ct := porcelainChangeType(code)
		emit := false
		if prevCode, existed := w.prev[path]; !existed {
			emit = true // newly dirty
		} else if prevCode != code {
			emit = true // status transitioned
		}

		// Track mtime for non-deleted paths so a repeated save to an
		// already-dirty file (unchanged XY code) still emits a Modified.
		if ct != ChangeDeleted {
			if info, statErr := os.Stat(filepath.Join(w.projectRoot, path)); statErr == nil {
				mt := info.ModTime()
				if !emit {
					if last, ok := w.prevMtime[path]; ok && mt.After(last) {
						emit = true
					}
				}
				nextMtime[path] = mt
			}
		}

		if emit && !baseline {
			w.accumulator.AddChange(path, ct, false)
		}
	}

	w.prev = cur
	w.prevMtime = nextMtime
}

// gitStatus runs `git status --porcelain -z -uall` and returns a map of
// working-tree-relative path to its two-char XY status code.
//
//   - -uall lists every individual untracked file (not a collapsed directory),
//     so brand-new files that were never `git add`ed surface with per-file
//     fidelity for murmurs.
//   - -z NUL-delimits records, sidestepping all path-quoting/escaping issues.
//   - git omits .gitignore'd paths unless --ignored is passed, so node_modules
//     and build output never appear — the filtering the old GitTrackedMatcher
//     did is now built in.
//
// Run via exec (not gitutil.RunGit) because RunGit trims whitespace, mixes
// stderr, and sanitizes output — all of which corrupt NUL-delimited -z data.
func (w *GitPollWatcher) gitStatus(ctx context.Context) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitStatusTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain", "-z", "-uall")
	cmd.Dir = w.projectRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parsePorcelainZ(out), nil
}

// parsePorcelainZ parses NUL-delimited `git status --porcelain -z` output into a
// map of path -> XY status code. For renames/copies the "from" path occupies
// the following NUL field and is skipped — the new path is what exists on disk.
func parsePorcelainZ(out []byte) map[string]string {
	result := make(map[string]string)
	entries := bytes.Split(out, []byte{0})
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		// Each record is "XY PATH": 2 status chars, a space, then the path.
		if len(entry) < 4 {
			continue
		}
		code := string(entry[:2])
		path := string(entry[3:])
		// renames/copies: the "from" path is the next NUL field — skip it.
		// R/C can appear in either the index (X) or work-tree (Y) column
		// (the latter when status.renames detects a work-tree rename), and a
		// rename/copy always emits a from-path, so check both columns. No
		// non-rename porcelain code uses R/C, so this never over-skips.
		if entry[0] == 'R' || entry[0] == 'C' || entry[1] == 'R' || entry[1] == 'C' {
			i++
		}
		result[path] = code
	}
	return result
}

// porcelainChangeType maps a two-char porcelain XY status code to a ChangeType.
// X is the staged (index) state, Y the working-tree state; we report the single
// most meaningful classification for murmurs.
func porcelainChangeType(code string) ChangeType {
	if len(code) < 2 {
		return ChangeModified
	}
	x, y := code[0], code[1]
	switch {
	case x == 'R' || y == 'R':
		return ChangeRenamed
	case x == 'D' || y == 'D':
		return ChangeDeleted
	case code == "??" || x == 'A' || x == 'C':
		return ChangeCreated
	default:
		return ChangeModified
	}
}
