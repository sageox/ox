package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/lfs"
)

// OrphanedDraftAge is how long a committed draft placeholder must go without a
// refresh before it is considered abandoned and eligible for git-removal from
// the ledger.
//
// A draft refreshes its updated_at heartbeat every few turns while a session is
// live, and that heartbeat is committed to the SHARED ledger — so a session
// running on ANOTHER machine keeps this fresh here too. That is what makes
// removal cross-machine safe: only a draft nobody has touched in this whole
// window, on any machine, is reaped.
//
// Set to a few days rather than a single day so a session merely paused across a
// weekend (closed laptop, idle container) is never mistaken for a dead one. The
// cost of waiting is a stale "in progress" page; the cost of being wrong is
// git-removing a live session's placeholder, and that asymmetry says wait.
const OrphanedDraftAge = 72 * time.Hour

// ValidateDraftSessionName rejects empty and traversal-shaped names before any
// path built from a session name is handed to `git rm` / os.RemoveAll. An
// unvalidated name widens a pathspec to the whole sessions tree.
func ValidateDraftSessionName(sessionName string) error {
	if sessionName == "" || sessionName == "." || sessionName == ".." {
		return fmt.Errorf("invalid session name %q", sessionName)
	}
	if strings.ContainsAny(sessionName, `/\`) || strings.Contains(sessionName, "..") {
		return fmt.Errorf("session name %q must be a single path component", sessionName)
	}
	return nil
}

// FindOrphanedDrafts returns the names of ledger draft placeholders that have no
// live or recoverable recording behind them, oldest first. cacheDirs are the
// local cache session directories to consult for an in-flight recording.
//
// Three conditions must ALL hold, and each is deliberately conservative — a
// false positive git-removes a live session's placeholder:
//
//  1. The ledger directory is a draft (never a finalized session).
//  2. Its updated_at heartbeat is older than OrphanedDraftAge.
//  3. No recording state or cached transcript exists for it locally. If either
//     exists the session is alive or recoverable and upload-retry owns it.
//
// This is the single source of truth for "orphaned draft"; both `ox doctor` and
// the daemon's periodic retraction call it.
func FindOrphanedDrafts(ledgerPath string, cacheDirs []string) ([]string, error) {
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read ledger sessions: %w", err)
	}

	type candidate struct {
		name      string
		updatedAt time.Time
	}
	var found []candidate

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		meta, metaErr := lfs.ReadSessionMeta(filepath.Join(sessionsDir, name))
		// Fail safe: an unreadable meta.json is not a draft we are willing to
		// delete. Some other check owns diagnosing it.
		if metaErr != nil || !meta.IsDraft() {
			continue
		}
		// No updated_at means we cannot age it. Refuse rather than guess —
		// deleting a placeholder for a session that might be live is worse than
		// leaving a stale page.
		if meta.UpdatedAt == nil || time.Since(*meta.UpdatedAt) < OrphanedDraftAge {
			continue
		}
		if DraftHasLocalSessionData(cacheDirs, name) {
			continue
		}
		found = append(found, candidate{name: name, updatedAt: *meta.UpdatedAt})
	}

	sort.Slice(found, func(i, j int) bool { return found[i].updatedAt.Before(found[j].updatedAt) })
	names := make([]string, 0, len(found))
	for _, c := range found {
		names = append(names, c.name)
	}
	return names, nil
}

// DraftHasLocalSessionData reports whether any cache location still holds this
// session, either as an active recording or as a transcript awaiting upload.
func DraftHasLocalSessionData(cacheDirs []string, sessionName string) bool {
	for _, dir := range cacheDirs {
		sessionDir := filepath.Join(dir, sessionName)
		for _, marker := range []string{".recording.json", "raw.jsonl"} {
			if _, err := os.Stat(filepath.Join(sessionDir, marker)); err == nil {
				return true
			}
		}
	}
	return false
}
