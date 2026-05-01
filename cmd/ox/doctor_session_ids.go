package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/sessionid"
)

// CheckSlugSessionIDsBackfilled is the slug for the legacy-session-id check.
const CheckSlugSessionIDsBackfilled = "session-ids"

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugSessionIDsBackfilled,
		Name:        "Legacy session IDs",
		Category:    "Ledger Git Health",
		FixLevel:    FixLevelSuggested,
		Description: "Detects session recordings whose meta.json predates the ses_<UUIDv7> field; opt-in backfill stamps a stable ID",
		Run: func(fix bool) checkResult {
			return checkLegacySessionIDs(fix)
		},
	})
}

// checkLegacySessionIDs walks <ledger>/sessions/* and reports recordings
// whose meta.json has no SessionID field (legacy, pre-rollout).
//
// The synthetic fallback in lfs.SessionMeta.EffectiveSessionID() already
// gives every consumer a stable ses_-prefixed handle for these recordings,
// so this check exists purely as quality-of-life: persisting the IDs lets
// downstream consumers read meta.session_id directly without implementing
// the v5 derivation themselves.
//
// Auto-fix is intentionally disabled (FixLevelSuggested). Each backfilled
// session produces a ledger commit; running unprompted on every doctor
// invocation would generate cross-coworker churn for a purely cosmetic
// improvement.
func checkLegacySessionIDs(fix bool) checkResult {
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return SkippedCheck("Legacy session IDs", "no ledger found", "")
	}

	sessionsDir := filepath.Join(ledgerPath, "sessions")
	if _, err := os.Stat(sessionsDir); errors.Is(err, fs.ErrNotExist) {
		return SkippedCheck("Legacy session IDs", "no sessions directory", "")
	}

	legacy, scanErr := findLegacySessions(sessionsDir)
	if scanErr != nil {
		return WarningCheck("Legacy session IDs",
			fmt.Sprintf("scan failed: %v", scanErr),
			"Re-run `ox doctor` after the ledger sync stabilizes")
	}

	if len(legacy) == 0 {
		return PassedCheck("Legacy session IDs", "all sessions have IDs")
	}

	if fix {
		return fixLegacySessionIDs(ledgerPath, legacy)
	}

	msg := fmt.Sprintf("%d session(s) without ses_<UUIDv7>", len(legacy))
	detail := []string{
		"Synthetic fallback (UUIDv5) covers correctness; backfill is optional cleanup.",
		"Run `ox doctor --fix-slug=session-ids` to stamp a stable ID into each meta.json.",
		fmt.Sprintf("Affected: %d session(s) under %s", len(legacy), sessionsDir),
	}
	return WarningCheck("Legacy session IDs", msg, strings.Join(detail, "\n"))
}

// findLegacySessions returns the names of session directories whose
// meta.json exists but has no SessionID field. Sessions whose meta.json
// is missing or unreadable are skipped (other doctor checks own that
// failure mode).
func findLegacySessions(sessionsDir string) ([]string, error) {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return nil, err
	}

	var legacy []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionDir := filepath.Join(sessionsDir, e.Name())
		meta, err := lfs.ReadSessionMeta(sessionDir)
		if err != nil {
			// missing or unreadable meta.json — out of scope here.
			continue
		}
		if meta.SessionID == "" {
			legacy = append(legacy, e.Name())
		}
	}
	sort.Strings(legacy)
	return legacy, nil
}

// fixLegacySessionIDs stamps a fresh ses_<UUIDv7> into every legacy
// meta.json under flock, then commits and pushes the batch.
//
// On a per-session basis the work is idempotent: MutateSessionMeta only
// rewrites the file when the mutator returns a non-nil meta, and we
// return nil if SessionID was already populated by a concurrent writer
// (e.g., another coworker's doctor finished first).
func fixLegacySessionIDs(ledgerPath string, sessionNames []string) checkResult {
	sessionsDir := filepath.Join(ledgerPath, "sessions")

	var stamped, raced, failed int
	var failures []string

	for _, name := range sessionNames {
		sessionDir := filepath.Join(sessionsDir, name)
		err := lfs.MutateSessionMeta(context.Background(), sessionDir, func(m *lfs.SessionMeta) (*lfs.SessionMeta, error) {
			if m == nil {
				return nil, nil // file vanished mid-fix; skip
			}
			if m.SessionID != "" {
				raced++
				return nil, nil // another writer beat us; preserve theirs
			}
			m.SessionID = sessionid.GenerateSessionID()
			return m, nil
		})
		if err != nil {
			failed++
			failures = append(failures, fmt.Sprintf("  %s: %v", name, err))
			continue
		}
		stamped++
	}

	if stamped == 0 {
		// nothing to commit — either all races or all failures.
		if failed > 0 {
			return WarningCheck("Legacy session IDs",
				fmt.Sprintf("%d failed", failed),
				strings.Join(failures, "\n"))
		}
		return PassedCheck("Legacy session IDs", "no changes (raced with concurrent writer)")
	}

	if err := commitAndPushSessionIDBackfill(ledgerPath, sessionNames); err != nil {
		// Local writes succeeded but commit/push failed. Next doctor run
		// will re-detect (in case a fresh meta.json clobber happened) or
		// commit them on its next attempt.
		return WarningCheck("Legacy session IDs",
			fmt.Sprintf("stamped %d, but commit/push failed: %v", stamped, err),
			"Re-run `ox doctor --fix-slug=session-ids` to retry the commit")
	}

	msg := fmt.Sprintf("backfilled %d session(s)", stamped)
	if raced > 0 || failed > 0 {
		msg = fmt.Sprintf("%s (skipped %d races, %d failures)", msg, raced, failed)
	}
	return PassedCheck("Legacy session IDs", msg)
}

// commitAndPushSessionIDBackfill stages every backfilled meta.json in one
// commit. Uses --sparse to satisfy git's sparse-checkout invariant per
// .claude/rules/daemon-git.md.
func commitAndPushSessionIDBackfill(ledgerPath string, sessionNames []string) error {
	if len(sessionNames) == 0 {
		return nil
	}

	sessionsDir := filepath.Join(ledgerPath, "sessions")
	addArgs := []string{"-C", ledgerPath, "add", "--sparse"}
	for _, name := range sessionNames {
		addArgs = append(addArgs, filepath.Join(sessionsDir, name, "meta.json"))
	}
	if output, err := exec.Command("git", addArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %s: %w", string(output), err)
	}

	commitMsg := fmt.Sprintf("session: backfill ses_<UUIDv7> for %d legacy session(s)", len(sessionNames))
	commitCmd := exec.Command("git", "-C", ledgerPath, "commit", "--no-verify", "-m", commitMsg)
	if output, err := commitCmd.CombinedOutput(); err != nil {
		if strings.Contains(string(output), "nothing to commit") {
			return nil
		}
		return fmt.Errorf("git commit: %s: %w", string(output), err)
	}

	return pushLedger(context.Background(), ledgerPath)
}
