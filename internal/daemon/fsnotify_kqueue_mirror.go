package daemon

import (
	"log/slog"
	"path/filepath"
)

// Helpers for plugging fsnotify v1.9.0's kqueue per-file FD leak on macOS.
// Shared between ProjectWatcher (which watches many directories under a repo
// root) and Watcher (which watches a single ledger directory). On both, when
// a watched directory is renamed or removed mid-watch, fsnotify's kqueue
// backend at backend_kqueue.go:489 calls w.remove(name, false) — closing the
// dir's own FD but leaving the per-file FDs that watchDirectoryFiles
// auto-opened. We compensate by snapshotting the file set ourselves and
// calling watcher.Remove(child) per file, which routes through w.remove(name,
// true) and closes the FD via unix.Close at backend_kqueue.go:302.
//
// Returns nil unconditionally on non-Darwin: inotify uses one FD per watch,
// not per file, so there's nothing to mirror.
//
// See internal/daemon/project_watcher.go for the full diagnosis (host
// symptom: lsof on the daemon PID hung because the FD table was overflowing).

// snapshotKqueueChildren reads absolute paths of regular files inside path,
// capped at maxFilesPerWatchedDir. Errors are logged at Debug and treated as
// "no children" — drift is recovered by the periodic re-snapshot in
// pruneStaleWatches and by event-driven incremental updates.
func snapshotKqueueChildren(fs FileSystem, path string, logger *slog.Logger) []string {
	if !childMirrorEnabled {
		return nil
	}
	entries, err := fs.ReadDir(path)
	if err != nil {
		logger.Debug("kqueue mirror: ReadDir failed during snapshot",
			"path", path, "error", err)
		return nil
	}
	// Cap allocation up front so a pathological directory (millions of
	// generated files) can't dominate startup memory or snapshot cost.
	// Memory bound is independent of directory cardinality.
	capLen := len(entries)
	if capLen > maxFilesPerWatchedDir {
		capLen = maxFilesPerWatchedDir
	}
	out := make([]string, 0, capLen)
	truncated := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if len(out) >= maxFilesPerWatchedDir {
			truncated = true
			break
		}
		out = append(out, filepath.Join(path, e.Name()))
	}
	if truncated {
		logger.Warn("kqueue mirror: per-dir child snapshot truncated",
			"path", path, "cap", maxFilesPerWatchedDir,
			"total_entries", len(entries),
			"impact", "FD-leak guarantee weakened for files beyond cap")
	}
	return out
}

// unionKqueueChildren merges two child-path slices, preserving order from the
// first and de-duplicating. Used to combine pre-Add and post-Add snapshots
// and close the TOCTOU race against fsnotify's watchDirectoryFiles.
func unionKqueueChildren(a, b []string) []string {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, s := range b {
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
