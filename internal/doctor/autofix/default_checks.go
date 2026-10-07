package autofix

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/hooks/claude"
	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/lfs"
)

// Default returns a registry seeded with the auto-fix-safe checks the
// daemon ships with today. New checks land here as they're migrated
// from cmd/ox/doctor_*.go (see ox-0xgx for the migration list).
//
// The seed set is deliberately small — proving the structural path
// works is the primary goal of the initial landing. Each check below
// is self-contained, idempotent, bounded in blast radius, and
// validated by its own regression test.
func Default() *Registry {
	r := NewRegistry()
	r.Register(&Check{
		Slug:        "init-reverted",
		Description: "Backfill .sageox/config.json when init artifacts were reverted from git but local state survives",
		MinInterval: 30 * time.Minute,
		BlastRadius: "single workspace; pure file write of recovered repo_id",
		Run:         checkInitReverted,
	})
	r.Register(&Check{
		Slug:        "claude-hooks-format",
		Description: "Rewrite legacy string-format hooks in .claude/settings.json into the array form Claude Code accepts",
		MinInterval: 30 * time.Minute,
		BlastRadius: "single file (.claude/settings.json); idempotent canonical re-marshal",
		Run:         checkClaudeHooksFormat,
	})
	r.Register(&Check{
		Slug:        "session-meta-titles",
		Description: "Recover empty meta.title from summary.json after summarization; skip draft and pending sessions; never counts summary attempts",
		Scope:       ScopeLedger,
		MinInterval: 30 * time.Minute,
		BlastRadius: "single ledger; per-session meta.json rewrite only when summary.json holds a title or summary holds leaked error prose",
		Run:         checkSessionMetaTitles,
	})
	r.Register(&Check{
		Slug:        "session-inline-summary-retry",
		Description: "Reset sessions that failed summarization due to the file-read prompt bug (pre-0.7.2); allows re-summarization with inline prompt",
		MinInterval: 24 * time.Hour,
		BlastRadius: "single ledger; resets summary_attempts on eligible sessions so daemon re-attempts with fixed inline prompt",
		Run:         checkSessionInlineSummaryRetry,
	})
	r.Register(&Check{
		Slug:        "ledger-rebase-wedge",
		Description: "Clear a STALE wedged rebase on the ledger — including a structurally-incomplete rebase-merge dir git rebase --abort cannot clear — so background sync resumes unattended",
		Scope:       ScopeLedger,
		MinInterval: 30 * time.Minute,
		BlastRadius: "single ledger; git rebase --abort/--quit on a STALE wedge only (fresh in-flight rebases untouched); no history rewrite, HEAD unchanged",
		Run:         checkLedgerRebaseWedge,
	})
	r.Register(&Check{
		Slug:        "skills-inventory-drift",
		Description: "Reconcile ox-managed skill files that drifted from the projected catalog (this binary's skills plus approved team skills)",
		MinInterval: 30 * time.Minute,
		BlastRadius: "single workspace; ox-managed skill files only — never installs into an unselected repo, never touches the git index, user-modified files preserved as conflicts",
		Run:         checkSkillsInventoryDrift,
	})
	r.Register(&Check{
		Slug:        "ledger-sacred-deletion",
		Description: "Detect and alert on any commit in recent ledger history that mass-deleted sacred plans/sessions (ADR-024 data-loss guard); detection only, never auto-restores",
		Scope:       ScopeLedger,
		MinInterval: 15 * time.Minute,
		BlastRadius: "read-only; scans recent ledger history and reports, no writes",
		Run:         checkLedgerSacredDeletion,
	})
	return r
}

// checkLedgerRebaseWedge is the daemon autofix twin of cmd/ox's
// ledger-stuck-operation check (bd ox-j3cl). It runs unattended on a slow
// ticker so a wedged ledger self-heals with no human and no `ox doctor --fix`:
// the reported production incident sat suspended for six weeks because the only
// recovery primitive (`git rebase --abort`) could not clear a zombie
// rebase-merge dir left by a process killed mid-`pull --rebase --autostash`.
//
// Complements the daemon's in-line recoverPreexistingRebase (which fires on the
// pull pipeline): this is an independent trigger with structured telemetry, so
// a self-heal is visible in the field rather than silent.
func checkLedgerRebaseWedge(ctx context.Context, ledgerPath string) CheckResult {
	if ledgerPath == "" {
		return CheckResult{Status: StatusClean}
	}
	return repairLedgerRebaseWedge(ctx, ledgerPath, ledgerPath)
}

// repairLedgerRebaseWedge is the side-effect-free-on-healthy core of
// checkLedgerRebaseWedge, split out so tests can drive it against a real repo
// without standing up a full ProjectContext. Only a STALE rebase (older than
// gitutil.StaleRebaseThreshold) is recovered — a fresh one is almost always the
// daemon's own in-flight pull and must be left alone.
func repairLedgerRebaseWedge(ctx context.Context, ledgerPath, repoPath string) CheckResult {
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	result := CheckResult{Status: StatusClean, Repo: repoPath}
	lockErr := gitutil.WithRepoLock(lockCtx, ledgerPath, func() error {
		// The age check belongs inside the clone lock. A rebase can cross the
		// stale threshold while this check waits, and a peer may finish or
		// replace it before the lock becomes available.
		age, inProgress := gitutil.RebaseAge(ledgerPath)
		if !inProgress || age < gitutil.StaleRebaseThreshold {
			return nil
		}

		rescueRef, err := gitutil.RescueIfNeededThenAbort(lockCtx, ledgerPath, "autofix stale ledger rebase wedge", slog.Default())
		if err != nil {
			result.Status = StatusError
			result.Summary = fmt.Sprintf("stale rebase wedge recovery failed (rescue_ref=%q): %v", rescueRef, err)
			return nil
		}
		result.Status = StatusFixed
		result.Summary = fmt.Sprintf("cleared stale wedged rebase on ledger (age %s, rescue_ref=%q)", age.Round(time.Second), rescueRef)
		return nil
	})
	if lockErr == nil {
		return result
	}
	if gitutil.IsRepoLockBusy(lockErr) {
		return CheckResult{Status: StatusClean, Repo: repoPath, Summary: "ledger busy; rebase recovery deferred"}
	}
	return CheckResult{Status: StatusError, Repo: repoPath, Summary: fmt.Sprintf("acquire ledger lock for rebase recovery: %v", lockErr)}
}

// checkInitReverted is the daemon-side counterpart of cmd/ox/doctor_init_reverted.go.
// It calls the same canonical recovery (config.BackfillProjectConfigFromLocalState),
// so the behavior is identical to `ox doctor --fix` invoked by hand —
// just runs unattended on a slow ticker.
func checkInitReverted(_ context.Context, repoPath string) CheckResult {
	if repoPath == "" {
		return CheckResult{Status: StatusClean}
	}
	wrote, err := config.BackfillProjectConfigFromLocalState(repoPath)
	switch {
	case err != nil:
		return CheckResult{
			Status:  StatusError,
			Repo:    repoPath,
			Summary: fmt.Sprintf("backfill failed: %v", err),
		}
	case wrote:
		return CheckResult{
			Status:  StatusFixed,
			Repo:    repoPath,
			Summary: "recovered .sageox/config.json from surviving local state (init was reverted from git)",
		}
	default:
		return CheckResult{Status: StatusClean, Repo: repoPath}
	}
}

// checkClaudeHooksFormat reads .claude/settings.json (if any) and
// rewrites it into the canonical array form when it has drifted.
//
// This is the same logic as cmd/ox/doctor_fix_hooks.go, ported to
// take an explicit repoPath rather than relying on findGitRoot() so
// it's safe to call from the daemon. The CLI version stays in place
// for `ox doctor --fix` invocations; this is the unattended twin.
func checkClaudeHooksFormat(_ context.Context, repoPath string) CheckResult {
	if repoPath == "" {
		return CheckResult{Status: StatusClean}
	}
	settingsPath := filepath.Join(repoPath, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if errors.Is(err, os.ErrNotExist) {
		return CheckResult{Status: StatusClean, Repo: repoPath}
	}
	if err != nil {
		return CheckResult{
			Status:  StatusError,
			Repo:    repoPath,
			Summary: fmt.Sprintf("read settings.json: %v", err),
		}
	}

	settings, rawMap, parseErr := claude.ParseSettingsRaw(data)
	if parseErr != nil {
		// can't even parse — surface as Found so a human can look,
		// don't try to rewrite garbage and lose the user's bytes.
		return CheckResult{
			Status:  StatusFound,
			Repo:    repoPath,
			Summary: fmt.Sprintf("settings.json unparseable: %v — manual repair needed", parseErr),
		}
	}
	canonical, err := claude.MarshalSettings(settings, rawMap)
	if err != nil {
		return CheckResult{
			Status:  StatusError,
			Repo:    repoPath,
			Summary: fmt.Sprintf("re-marshal: %v", err),
		}
	}
	// "Already canonical" requires both a strict on-disk shape check and a
	// semantic content compare. Byte equality alone was the original guard
	// and it failed forever on any file with HTML-escapable characters
	// (<, >, & in a permission rule), hand-written \uXXXX escapes, missing
	// trailing newline, or non-canonical indentation inside opaque
	// permissions blocks — the autofix scheduler then rewrote the file
	// every 30 minutes. Semantic equality alone is too permissive (legacy
	// string-form hooks parse into the same in-memory shape as the array
	// form Claude Code requires, so the check would refuse to migrate
	// broken files). Combining both gives idempotency on any file Claude
	// Code accepts and repair on any file it would reject.
	canonicalOnDisk, _ := claude.IsCanonicalHooksFormat(data)
	semEqual, _ := claude.SettingsSemanticallyEqual(data, canonical)
	if canonicalOnDisk && semEqual {
		return CheckResult{Status: StatusClean, Repo: repoPath}
	}

	// preserve existing perms (settings.json may hold tokens)
	perm := os.FileMode(0o600)
	if info, statErr := os.Stat(settingsPath); statErr == nil {
		perm = info.Mode().Perm()
	}
	if err := os.WriteFile(settingsPath, canonical, perm); err != nil {
		return CheckResult{
			Status:  StatusError,
			Repo:    repoPath,
			Summary: fmt.Sprintf("write settings.json: %v", err),
		}
	}
	return CheckResult{
		Status:  StatusFixed,
		Repo:    repoPath,
		Summary: "rewrote .claude/settings.json into canonical array form",
	}
}

// checkSessionInlineSummaryRetry resets sessions that were marked
// "unrecoverable" due to the pre-0.7.2 file-read prompt bug. The daemon
// previously asked the LLM subprocess to read raw.jsonl from disk, which
// consistently failed (96% failure rate). The fix embeds content inline.
//
// This check runs at most once per day (MinInterval: 24h). After all
// eligible sessions are reset, subsequent runs are no-ops (no sessions
// match the "unrecoverable" + "title too short" filter).
//
// The 24h interval prevents runaway token burn: if the inline prompt also
// fails for some reason, sessions exhaust MaxSummaryAttempts (3) and get
// re-marked unrecoverable. The daily check would reset them again, but
// only 3 LLM calls per session per day — bounded by design.
func checkSessionInlineSummaryRetry(ctx context.Context, repoPath string) CheckResult {
	if repoPath == "" {
		return CheckResult{Status: StatusClean}
	}
	projectCtx, err := config.LoadProjectContext(repoPath)
	if err != nil || projectCtx == nil {
		return CheckResult{Status: StatusClean, Repo: repoPath}
	}
	ledgerPath := projectCtx.DefaultLedgerPath()
	if ledgerPath == "" {
		return CheckResult{Status: StatusClean, Repo: repoPath}
	}

	// best-effort LFS client for hydrating pointer stubs
	ep := endpoint.GetForProject(repoPath)
	var lfsClient *lfs.Client
	if ep != "" {
		lfsClient, _ = lfs.NewClientFromLedger(ledgerPath, ep)
	}

	// same identity recording stamps into meta.json (privacy-safe display name)
	username := identity.AttributionDisplayName(ep, config.GetDisplayName())
	result := rearmInlineSummarySessions(ctx, ledgerPath, username, lfsClient)
	result.Repo = repoPath
	return result
}

// rearmInlineSummarySessions is the lock-, ownership- and commit-aware core of
// checkSessionInlineSummaryRetry.
//
// Resetting a session rewrites sessions/<s>/meta.json in the Ledger working
// tree. Left uncommitted, those edits make every later `git pull --rebase`
// fail with "cannot rebase: You have unstaged changes", and they accumulate
// autostash entries. So the pass:
//   - skips while the Ledger's push is suspended: finalize is paused too, so a
//     re-armed session could not be summarized and pushed anyway;
//   - holds the repo lock for the whole reset-and-commit transaction;
//   - only touches the current user's sessions (a teammate's meta.json is
//     theirs to repair, and editing it creates a conflict on their next push);
//   - commits what it rewrote in one snapshot commit, and restores the files if
//     that fails so the tree is never left dirty.
func rearmInlineSummarySessions(ctx context.Context, ledgerPath, username string, lfsClient *lfs.Client) CheckResult {
	if until, wedged := gitutil.PushWedgedUntil(ledgerPath); wedged {
		return CheckResult{
			Status:  StatusClean,
			Summary: fmt.Sprintf("ledger push suspended until %s; inline-summary re-arm deferred", until.Format(time.RFC3339)),
		}
	}
	if username == "" {
		// fail closed: without an identity no session can be proven ours
		return CheckResult{Status: StatusClean}
	}

	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := CheckResult{Status: StatusClean}
	lockErr := gitutil.WithRepoLock(lockCtx, ledgerPath, func() error {
		result = rearmInlineSummaryLocked(ctx, ledgerPath, username, lfsClient)
		return nil
	})
	switch {
	case lockErr == nil:
		return result
	case gitutil.IsRepoLockBusy(lockErr):
		return CheckResult{Status: StatusClean, Summary: "ledger busy; inline-summary re-arm deferred"}
	default:
		return CheckResult{Status: StatusError, Summary: fmt.Sprintf("acquire ledger lock for inline-summary re-arm: %v", lockErr)}
	}
}

// rearmInlineSummaryLocked must run inside gitutil.WithRepoLock.
func rearmInlineSummaryLocked(ctx context.Context, ledgerPath, username string, lfsClient *lfs.Client) CheckResult {
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return CheckResult{Status: StatusClean}
		}
		return CheckResult{Status: StatusError, Summary: fmt.Sprintf("read sessions dir: %v", err)}
	}

	var resetPaths []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionDir := filepath.Join(sessionsDir, e.Name())
		meta, readErr := lfs.ReadSessionMeta(sessionDir)
		if readErr != nil || meta == nil || !strings.EqualFold(meta.Username, username) {
			continue
		}
		if lfs.ResetInlineSummaryEligible(sessionDir, false, lfsClient, ledgerPath) {
			resetPaths = append(resetPaths, path.Join("sessions", e.Name(), "meta.json"))
		}
	}
	if len(resetPaths) == 0 {
		return CheckResult{Status: StatusClean}
	}

	msg := fmt.Sprintf("fix: re-arm %d sessions for re-summarization", len(resetPaths))
	if err := commitRearmedMetas(ctx, ledgerPath, msg, resetPaths); err != nil {
		restoreRearmedMetas(ctx, ledgerPath, resetPaths)
		return CheckResult{Status: StatusError, Summary: fmt.Sprintf("commit inline-summary re-arm: %v", err)}
	}
	return CheckResult{
		Status:  StatusFixed,
		Summary: fmt.Sprintf("reset %d sessions for inline-prompt re-summarization", len(resetPaths)),
	}
}

func commitRearmedMetas(ctx context.Context, ledgerPath, msg string, paths []string) error {
	addArgs := append([]string{"add", "--sparse", "--"}, paths...)
	if _, err := gitutil.RunGit(ctx, ledgerPath, addArgs...); err != nil {
		return fmt.Errorf("stage meta.json: %w", err)
	}
	if _, err := gitutil.CommitLedgerSnapshot(ctx, ledgerPath, msg, paths...); err != nil {
		return fmt.Errorf("snapshot commit: %w", err)
	}
	return nil
}

// restoreRearmedMetas puts the rewritten files back to HEAD after a failed
// commit. Best effort by design: the caller already reports StatusError.
func restoreRearmedMetas(ctx context.Context, ledgerPath string, paths []string) {
	resetArgs := append([]string{"reset", "-q", "--"}, paths...)
	if _, err := gitutil.RunGit(ctx, ledgerPath, resetArgs...); err != nil {
		slog.WarnContext(ctx, "unstage inline-summary re-arm failed", "error", err)
	}
	checkoutArgs := append([]string{"checkout", "--"}, paths...)
	if _, err := gitutil.RunGit(ctx, ledgerPath, checkoutArgs...); err != nil {
		slog.WarnContext(ctx, "restore inline-summary re-arm failed", "error", err)
	}
}

// checkSessionMetaTitles is the daemon-side empty-title repair. It
// receives a canonical Ledger path, walks sessions/, and runs
// lfs.RecoverEmptyTitleMeta on each session whose meta.title is
// empty. Draft and pending sessions are left to their recording and
// summarization paths. For other sessions, it recovers from summary.json
// when possible and otherwise leaves the session alone: only the finalize
// worker counts summary attempts (GH #1107).
//
// Why per-ledger and not per-session: the global-sync owner enumerates every
// canonical Ledger checkout once, including repos that are not currently open.
//
// Blast radius: per-session meta.json rewrites, only where there is a title
// to recover or leaked error prose to move. No git operations, no LFS calls,
// no network. Worst-case if the
// recovery is wrong: the affected session row title shows the wrong
// string, fixable by a future regenerate.
func checkSessionMetaTitles(ctx context.Context, ledgerPath string) CheckResult {
	if ledgerPath == "" {
		return CheckResult{Status: StatusClean}
	}
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := CheckResult{Status: StatusClean, Repo: ledgerPath}
	lockErr := gitutil.WithRepoLock(lockCtx, ledgerPath, func() error {
		result = repairLedgerSessionTitles(filepath.Join(ledgerPath, "sessions"), ledgerPath)
		return nil
	})
	if lockErr == nil {
		return result
	}
	if gitutil.IsRepoLockBusy(lockErr) {
		return CheckResult{Status: StatusClean, Repo: ledgerPath, Summary: "ledger busy; session title repair deferred"}
	}
	return CheckResult{Status: StatusError, Repo: ledgerPath, Summary: fmt.Sprintf("acquire ledger lock for session title repair: %v", lockErr)}
}

// repairLedgerSessionTitles is the side-effect-free-on-healthy core of
// checkSessionMetaTitles, exported (within the package) for tests that
// want to drive it without standing up a full ProjectContext +
// LoadProjectContext stack. Iterates session subdirs and aggregates
// per-session recovery outcomes into a CheckResult.
func repairLedgerSessionTitles(sessionsDir, repoPath string) CheckResult {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return CheckResult{Status: StatusClean, Repo: repoPath}
		}
		return CheckResult{
			Status:  StatusError,
			Repo:    repoPath,
			Summary: fmt.Sprintf("read sessions dir: %v", err),
		}
	}

	var recovered, movedDiagnostic, errored int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		out := lfs.RecoverEmptyTitleMeta(filepath.Join(sessionsDir, e.Name()), false)
		switch {
		case out.Error != "":
			errored++
		case out.RecoveredFromJSON:
			recovered++
		case out.MovedDiagnostic:
			movedDiagnostic++
		}
	}
	if recovered == 0 && movedDiagnostic == 0 && errored == 0 {
		return CheckResult{Status: StatusClean, Repo: repoPath}
	}
	status := StatusFixed
	if errored > 0 {
		status = StatusFound // partial progress must not clear unresolved errors
	}
	return CheckResult{
		Status:  status,
		Repo:    repoPath,
		Summary: fmt.Sprintf("session meta titles: recovered=%d moved_diagnostic=%d errored=%d", recovered, movedDiagnostic, errored),
	}
}
