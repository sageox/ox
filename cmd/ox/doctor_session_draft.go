package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
)

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugSessionDraftOrphan,
		Name:        "orphaned session drafts",
		Category:    "Sessions",
		FixLevel:    FixLevelSuggested,
		Description: "Finds draft placeholders whose recording no longer exists",
		Run:         checkSessionDraftOrphan,
	})
}

// orphanedDraftAge is how long a draft must have gone without a refresh before
// it is considered abandoned.
//
// Comfortably longer than the refresh cadence (10 turns) takes in practice, so
// a slow but live session — a user thinking, or an agent on a long tool call —
// is never mistaken for a dead one. The cost of waiting is a stale "in
// progress" page; the cost of being wrong is deleting a live session's
// placeholder, so the asymmetry says wait.
const orphanedDraftAge = 24 * time.Hour

// checkSessionDraftOrphan finds draft placeholders in the ledger whose
// recording no longer exists anywhere.
//
// # Why this has to exist
//
// A draft is deliberately invisible to every other reclaimer, and that is what
// leaves it stranded. The daemon's anti-entropy skips drafts by design.
// `ox session prune` only touches local caches and no longer counts a draft as
// uploaded. `commitDraftRetraction` runs only from a clean session stop, and
// `deleteDraftFromLedger` only from an explicit abort — neither of which a
// crashed agent ever reaches.
//
// So every session that dies after turn 2 (reboot, closed laptop, killed
// container, a quality-discard finalize) leaves a placeholder that advertises
// an in-progress session forever: the /c/ page never resolves to real content,
// `ox session list` accumulates phantom draft rows, and nothing ever cleans it
// up. Without this check the ledger grows ghosts monotonically.
//
// Suggested rather than Auto because the fix produces a ledger commit and a
// push, and nobody expects a bare `ox doctor` to push on their behalf. Same
// reasoning as the session-ID backfill check.
func checkSessionDraftOrphan(fix bool) checkResult {
	const name = "orphaned session drafts"

	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck(name, "no git root", "")
	}
	if !config.IsInitialized(gitRoot) {
		return SkippedCheck(name, "not initialized", "")
	}
	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck(name, "no ledger", "")
	}

	orphans, err := findOrphanedDrafts(gitRoot, ledgerPath)
	if err != nil {
		return SkippedCheck(name, "scan error", err.Error())
	}
	if len(orphans) == 0 {
		return PassedCheck(name, "no orphaned drafts")
	}

	if !fix {
		return WarningCheck(name,
			fmt.Sprintf("%d draft placeholder(s) with no recording", len(orphans)),
			fmt.Sprintf("run 'ox doctor --fix' to retract them (oldest: %s)", orphans[0]))
	}

	var removed, failed int
	for _, sessionName := range orphans {
		res, err := deleteDraftFromLedger(ledgerPath, sessionName)
		if err != nil {
			slog.Warn("retract orphaned draft failed", "session", sessionName, "error", err)
			failed++
			continue
		}
		if res.PushWarning != "" {
			slog.Warn("retract orphaned draft: push pending", "session", sessionName, "warning", res.PushWarning)
		}
		if res.Deleted {
			removed++
		}
	}

	if failed > 0 {
		return WarningCheck(name,
			fmt.Sprintf("retracted %d, %d failed", removed, failed),
			"run 'ox doctor --fix' again to retry")
	}
	return PassedCheck(name, fmt.Sprintf("retracted %d orphaned draft(s)", removed))
}

// findOrphanedDrafts returns the names of ledger drafts that have no live or
// recoverable recording behind them, oldest first.
//
// Three conditions must ALL hold, and each one is deliberately conservative —
// a false positive here deletes a live session's placeholder:
//
//  1. The ledger directory is a draft (never a finalized session).
//  2. Its updated_at is older than orphanedDraftAge. This is the whole reason
//     drafts refresh their counters: without a heartbeat there is no way to
//     tell a live session from a dead one.
//  3. No recording state exists for it in any cache location, and no cached
//     transcript is waiting to be uploaded. If either exists, the session is
//     alive or recoverable and the upload-retry check owns it, not this one.
func findOrphanedDrafts(projectRoot, ledgerPath string) ([]string, error) {
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read ledger sessions: %w", err)
	}

	cacheDirs := []string{filepath.Join(ledgerPath, ".sageox", "cache", "sessions")}
	if contextPath := session.GetContextPath(getRepoIDOrDefault(projectRoot)); contextPath != "" {
		cacheDirs = append(cacheDirs, filepath.Join(contextPath, "sessions"))
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
		// deleting a placeholder for a session that might be live is worse
		// than leaving a stale page.
		if meta.UpdatedAt == nil || time.Since(*meta.UpdatedAt) < orphanedDraftAge {
			continue
		}
		if hasLocalSessionData(cacheDirs, name) {
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

// hasLocalSessionData reports whether any cache location still holds this
// session, either as an active recording or as a transcript awaiting upload.
func hasLocalSessionData(cacheDirs []string, sessionName string) bool {
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
