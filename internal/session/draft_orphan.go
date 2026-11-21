package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/sageox/ox/internal/lfs"
)

// draftTimestampLen is len("2006-01-02T15-04"), the timestamp prefix that
// GenerateSessionName writes ahead of the username slug.
const draftTimestampLen = len("2006-01-02T15-04")

// draftOwnedByLocalUser requires the stable user ID recorded when the draft
// was published. Username is a display name and the session-name slug is not
// globally unique, so neither proves ownership. AgentID must also match the
// generated name shape. Unknown owners and legacy metadata without UserID or
// AgentID fail closed.
func draftOwnedByUserID(sessionName string, meta *lfs.SessionMeta, ownerID string) bool {
	if ownerID == "" || meta == nil || meta.UserID == "" || meta.UserID != ownerID || meta.AgentID == "" {
		return false
	}
	agentID := sanitizeFilename(meta.AgentID)
	if agentID == "" || len(sessionName) <= draftTimestampLen+1 || sessionName[draftTimestampLen] != '-' {
		return false
	}
	if _, err := time.Parse("2006-01-02T15-04", sessionName[:draftTimestampLen]); err != nil {
		return false
	}
	remaining := sessionName[draftTimestampLen+1:]
	suffix := "-" + agentID
	if !strings.HasSuffix(remaining, suffix) || len(remaining) <= len(suffix) {
		return false
	}
	return true
}

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

// ValidateDraftSessionName requires a single path component free of control
// characters and of the characters sanitizeFilename strips from generated
// names (including the Git wildcards '*', '?', '[' and ']' and pathspec magic
// ':').
// Every name GenerateSessionName produces passes, including usernames with
// spaces or non-ASCII letters. Ledger entries are untrusted, so Git callers
// must still use literal pathspecs rather than rely on this check.
func ValidateDraftSessionName(sessionName string) error {
	if sessionName == "" || sessionName == "." || sessionName == ".." {
		return fmt.Errorf("invalid session name %q", sessionName)
	}
	if strings.ContainsAny(sessionName, `/\`) {
		return fmt.Errorf("session name %q must be a single path component", sessionName)
	}
	for _, c := range sessionName {
		if unicode.IsControl(c) || strings.ContainsRune(`:*?[]"<>|`, c) {
			return fmt.Errorf("session name %q contains disallowed character %q", sessionName, c)
		}
	}
	return nil
}

// validDraftSessionEntries filters the untrusted ledger directory entries
// before any metadata is read. Keeping this gate separate makes it testable on
// platforms (such as Windows) that cannot create filenames containing Git
// wildcards like '*'.
func validDraftSessionEntries(entries []os.DirEntry) []os.DirEntry {
	valid := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || ValidateDraftSessionName(entry.Name()) != nil {
			continue
		}
		valid = append(valid, entry)
	}
	return valid
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
func FindOrphanedDrafts(ledgerPath string, cacheDirs []string, localUserID string) ([]string, error) {
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

	for _, entry := range validDraftSessionEntries(entries) {
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
		// Owner-only reaping: only the owning user's machine can see that
		// owner's cache, so only it can evaluate condition 3. meta.UserID records
		// the stable account/coworker ID used when recording began.
		// meta.Username is a privacy-safe display name and is not an ownership key.
		if !draftOwnedByUserID(name, meta, localUserID) {
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
