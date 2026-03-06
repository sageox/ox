// Package notification provides lightweight team context change detection.
//
// Design: CLI checks file mtimes directly. Git updates mtime when it writes
// files during pull, so no daemon-side touching is needed.
//
// Cost: ~1μs per file (stat only). Safe to call on every command.
package notification

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// ClockSkewTolerance is the tolerance for clock skew between daemon and CLI.
// Files with mtime within this window of lastNotified are considered changed.
const ClockSkewTolerance = 5 * time.Second

// TeamContextFiles lists files agents should re-read when updated.
// Paths are relative to team context root.
//
// TODO: revisit whether manually listing files is the right long-term approach.
// This works for now with a small set of known files, but may not scale
// as team context structure evolves. Consider glob patterns or directory watches.
var TeamContextFiles = []string{
	"AGENTS.md",  // team instructions (root)
	"CLAUDE.md",  // team instructions (root, Claude-specific)
	"agent-context/distilled-discussions.md",
	"coworkers/AGENTS.md",
	"coworkers/CLAUDE.md",
}

// CheckForUpdates checks if any team context files have been modified.
//
// Returns:
// - latestMtime: the most recent mtime across all files (for updating session marker)
// - updatedFiles: full paths to files newer than lastNotified (for agent to re-read)
//
// Cost: ~1μs per file (stat only, no disk reads). Safe to call on every command.
//
// On first run (lastNotified.IsZero()), returns no updated files to avoid
// notification spam. The latestMtime is still returned for initialization.
func CheckForUpdates(teamContextPath string, lastNotified time.Time) (time.Time, []string) {
	if teamContextPath == "" {
		return time.Time{}, nil
	}

	var latestMtime time.Time
	var updatedFiles []string

	// threshold accounts for clock skew
	threshold := lastNotified.Add(-ClockSkewTolerance)

	for _, relPath := range TeamContextFiles {
		fullPath := filepath.Join(teamContextPath, relPath)
		info, err := os.Stat(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue // expected - file doesn't exist yet
			}
			// log other errors but continue checking other files
			slog.Warn("cannot stat team context file", "path", fullPath, "error", err)
			continue
		}

		mtime := info.ModTime()
		if mtime.After(latestMtime) {
			latestMtime = mtime
		}

		// skip notification on first run to avoid spam
		if lastNotified.IsZero() {
			continue
		}

		if mtime.After(threshold) {
			updatedFiles = append(updatedFiles, fullPath)
		}
	}

	return latestMtime, updatedFiles
}
