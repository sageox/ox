package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/gitutil"
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
// Four conditions must ALL hold, and each is deliberately conservative — a
// false positive git-removes a live session's placeholder:
//
//  1. The ledger directory is a draft (never a finalized session).
//  2. Its updated_at heartbeat is older than OrphanedDraftAge.
//  3. No recording state or cached transcript exists for it locally. If either
//     exists the session is alive or recoverable and upload-retry owns it.
//  4. The directory contains only valid draft metadata. The draft flag can be
//     stale after an interrupted finalization; it never authorizes deleting
//     existing session artifacts.
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
		meta := ReadOrphanedDraftMeta(ledgerPath, entry.Name(), cacheDirs)
		if meta == nil {
			continue
		}
		found = append(found, candidate{name: entry.Name(), updatedAt: *meta.UpdatedAt})
	}

	sort.Slice(found, func(i, j int) bool { return found[i].updatedAt.Before(found[j].updatedAt) })
	names := make([]string, 0, len(found))
	for _, c := range found {
		names = append(names, c.name)
	}
	return names, nil
}

// ReadOrphanedDraftMeta returns metadata only when this session meets every
// FindOrphanedDrafts condition. nil means retain it, including unreadable state.
// Writers recheck this single candidate under the repo lock before removal.
func ReadOrphanedDraftMeta(ledgerPath, sessionName string, cacheDirs []string) *lfs.SessionMeta {
	if ValidateDraftSessionName(sessionName) != nil {
		return nil
	}
	dir := filepath.Join(ledgerPath, "sessions", sessionName)
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return nil
	}
	meta, err := lfs.ReadSessionMeta(dir)
	// Unreadable or malformed metadata cannot authorize deletion.
	if err != nil || !meta.IsDraft() || meta.Validate() != nil {
		return nil
	}
	// Without a heartbeat there is no evidence of abandonment.
	if meta.UpdatedAt == nil || time.Since(*meta.UpdatedAt) < OrphanedDraftAge {
		return nil
	}
	if DraftHasLocalSessionData(cacheDirs, sessionName) {
		return nil
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 || files[0].Name() != "meta.json" || !files[0].Type().IsRegular() {
		return nil
	}
	return meta
}

// DraftIsCleanPlaceholder verifies the Git state immediately before automatic
// retraction. The caller must hold the repo lock and recheck ReadOrphanedDraftMeta.
// A clean metadata-only HEAD makes restoring HEAD after failure safe: there are
// no pre-existing edits to overwrite, nor sparse/missing artifacts to delete.
func DraftIsCleanPlaceholder(ctx context.Context, ledgerPath, sessionName string) (bool, error) {
	if err := ValidateDraftSessionName(sessionName); err != nil {
		return false, err
	}
	rel := filepath.ToSlash(filepath.Join("sessions", sessionName))
	tracked, err := gitutil.RunGit(ctx, ledgerPath, "ls-tree", "-r", "--name-only", "HEAD", "--", rel)
	if err != nil {
		return false, err
	}
	if tracked != rel+"/meta.json" {
		return false, nil
	}
	status, err := gitutil.RunGit(ctx, ledgerPath, "status", "--porcelain", "--untracked-files=all", "--ignored=matching", "--", rel)
	return status == "" && err == nil, err
}

// RestoreDraftPlaceholder undoes a failed automatic retraction. The caller
// still holds the repo lock and proved DraftIsCleanPlaceholder before removal,
// so HEAD is the exact prior state of both the index and the worktree.
func RestoreDraftPlaceholder(ctx context.Context, ledgerPath, sessionName string) error {
	if err := ValidateDraftSessionName(sessionName); err != nil {
		return err
	}
	// The sync deadline may be why removal failed. One local metadata restore
	// gets its own 30-second budget, bounding recovery without reusing that
	// expired deadline. The repo lock remains held until recovery returns.
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	rel := filepath.ToSlash(filepath.Join("sessions", sessionName, "meta.json"))
	_, err := gitutil.RunGit(recoveryCtx, ledgerPath, "checkout", "-q", "HEAD", "--", rel)
	return err
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
