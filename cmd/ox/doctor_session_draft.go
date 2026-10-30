package main

import (
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/sageox/ox/internal/config"
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

// orphanedDraftAge is the abandonment threshold; the canonical value and
// rationale live on session.OrphanedDraftAge, shared with the daemon's periodic
// retraction so both agree on what "orphaned" means.
const orphanedDraftAge = session.OrphanedDraftAge

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

// findOrphanedDrafts resolves this project's local cache dirs and delegates to
// session.FindOrphanedDrafts (the shared source of truth). Thin wrapper: the
// detection logic lives in internal/session so the daemon shares it verbatim.
func findOrphanedDrafts(projectRoot, ledgerPath string) ([]string, error) {
	return session.FindOrphanedDrafts(ledgerPath, draftCacheDirs(projectRoot, ledgerPath))
}

// draftCacheDirs returns the local cache session directories a draft's recording
// could live in: the ledger cache and this process's XDG cache.
func draftCacheDirs(projectRoot, ledgerPath string) []string {
	dirs := []string{filepath.Join(ledgerPath, ".sageox", "cache", "sessions")}
	if contextPath := session.GetContextPath(getRepoIDOrDefault(projectRoot)); contextPath != "" {
		dirs = append(dirs, filepath.Join(contextPath, "sessions"))
	}
	return dirs
}

// hasLocalSessionData delegates to the shared detector.
func hasLocalSessionData(cacheDirs []string, sessionName string) bool {
	return session.DraftHasLocalSessionData(cacheDirs, sessionName)
}
