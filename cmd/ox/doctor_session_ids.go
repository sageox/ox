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
)

// CheckSlugSessionIDsBackfilled is the slug for the legacy-session-id check.
const CheckSlugSessionIDsBackfilled = "session-ids"

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugSessionIDsBackfilled,
		Name:        "Legacy session IDs",
		Category:    "Ledger Git Health",
		FixLevel:    FixLevelSuggested,
		Description: "Detects session recordings whose meta.json has no session_id field; opt-in backfill persists the deterministic EffectiveSessionID() (ses_-prefixed UUIDv5 fallback for legacy)",
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

	msg := fmt.Sprintf("%d legacy session(s) missing session_id", len(legacy))
	detail := []string{
		"Synthetic fallback (EffectiveSessionID, UUIDv5 over RepoID + SessionName) covers correctness on every read.",
		"Run `ox doctor --fix-slug=session-ids` to persist that same value into each meta.json.",
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

// fixLegacySessionIDs persists EffectiveSessionID() (the deterministic
// ses_<UUIDv5> over (RepoID, SessionName)) into every legacy meta.json
// under flock, then commits and pushes the batch.
//
// # Why we persist EffectiveSessionID() and NOT a fresh UUIDv7
//
// EffectiveSessionID() is the canonical accessor every consumer uses. For
// legacy recordings it returns a stable v5 derivation that the SageOx
// server independently computes the same way. Backfilling that exact
// value is a pure no-op for any consumer — it just promotes on-the-fly
// synthesis to on-disk persistence. Stamping a fresh v7 here would change
// the visible identity of every legacy recording at backfill time,
// invalidating any reference (cache, server-side index, cross-machine
// link) that captured the v5 value before this doctor ran. That violates
// the documented contract on EffectiveSessionID and undoes the entire
// rationale for opt-in backfill.
//
// On a per-session basis the work is idempotent: MutateSessionMeta only
// rewrites the file when the mutator returns a non-nil meta, and we
// return nil if SessionID was already populated by a concurrent writer
// (e.g., another coworker's doctor finished first).
func fixLegacySessionIDs(ledgerPath string, sessionNames []string) checkResult {
	sessionsDir := filepath.Join(ledgerPath, "sessions")

	var raced, failed int
	var failures []string
	var rewritten []string // sessions whose meta.json we actually mutated

	for _, name := range sessionNames {
		sessionDir := filepath.Join(sessionsDir, name)
		var didWrite bool
		err := lfs.MutateSessionMeta(context.Background(), sessionDir, func(m *lfs.SessionMeta) (*lfs.SessionMeta, error) {
			if m == nil {
				return nil, nil // file vanished mid-fix; skip
			}
			if m.SessionID != "" {
				raced++
				return nil, nil // another writer beat us; preserve theirs
			}
			// Persist the v5 derivation, NOT a fresh v7. EffectiveSessionID()
			// returns the v5 fallback because m.SessionID is currently "".
			// See the doc comment on this function for why this matters.
			m.SessionID = m.EffectiveSessionID()
			didWrite = true
			return m, nil
		})
		if err != nil {
			failed++
			failures = append(failures, fmt.Sprintf("  %s: %v", name, err))
			continue
		}
		if !didWrite {
			continue // raced or vanished; already counted
		}
		rewritten = append(rewritten, name)
	}

	stamped := len(rewritten)
	if stamped == 0 {
		// nothing to commit — either all races or all failures.
		if failed > 0 {
			return WarningCheck("Legacy session IDs",
				fmt.Sprintf("%d failed", failed),
				strings.Join(failures, "\n"))
		}
		return PassedCheck("Legacy session IDs", "no changes (raced with concurrent writer)")
	}

	// Commit only the meta.json files we actually rewrote — staging the
	// full sessionNames list would dirty the index for races/failures and
	// could include unrelated meta.json edits made between scan and fix.
	if err := commitAndPushSessionIDBackfill(ledgerPath, rewritten); err != nil {
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
	// Any per-session failure leaves legacy sessions behind — that's a
	// degraded outcome, not a clean pass. Surface as a warning with the
	// per-session details so the operator can investigate.
	if failed > 0 {
		return WarningCheck("Legacy session IDs", msg, strings.Join(failures, "\n"))
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

	commitMsg := fmt.Sprintf("session: backfill stable session_id for %d legacy session(s)", len(sessionNames))
	commitCmd := exec.Command("git", "-C", ledgerPath, "commit", "--no-verify", "-m", commitMsg)
	if output, err := commitCmd.CombinedOutput(); err != nil {
		if strings.Contains(string(output), "nothing to commit") {
			return nil
		}
		return fmt.Errorf("git commit: %s: %w", string(output), err)
	}

	return pushLedger(context.Background(), ledgerPath)
}
