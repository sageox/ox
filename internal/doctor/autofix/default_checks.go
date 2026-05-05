package autofix

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/hooks/claude"
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
		Description: "Recover empty meta.title from summary.json on finalized sessions; cap retries at MaxSummaryAttempts",
		MinInterval: 30 * time.Minute,
		BlastRadius: "single ledger; per-session meta.json rewrite, bounded by MaxSummaryAttempts",
		Run:         checkSessionMetaTitles,
	})
	r.Register(&Check{
		Slug:        "session-inline-summary-retry",
		Description: "Reset sessions that failed summarization due to the file-read prompt bug (pre-0.7.2); allows re-summarization with inline prompt",
		MinInterval: 24 * time.Hour,
		BlastRadius: "single ledger; resets summary_attempts on eligible sessions so daemon re-attempts with fixed inline prompt",
		Run:         checkSessionInlineSummaryRetry,
	})
	return r
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
	if string(canonical) == string(data) {
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
func checkSessionInlineSummaryRetry(_ context.Context, repoPath string) CheckResult {
	if repoPath == "" {
		return CheckResult{Status: StatusClean}
	}
	ctx, err := config.LoadProjectContext(repoPath)
	if err != nil || ctx == nil {
		return CheckResult{Status: StatusClean, Repo: repoPath}
	}
	ledgerPath := ctx.DefaultLedgerPath()
	if ledgerPath == "" {
		return CheckResult{Status: StatusClean, Repo: repoPath}
	}

	sessionsDir := filepath.Join(ledgerPath, "sessions")
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

	// best-effort LFS client for hydrating pointer stubs
	ep := endpoint.GetForProject(repoPath)
	var lfsClient *lfs.Client
	if ep != "" {
		lfsClient, _ = lfs.NewClientFromLedger(ledgerPath, ep)
	}

	var resetCount int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if lfs.ResetInlineSummaryEligible(filepath.Join(sessionsDir, e.Name()), false, lfsClient, ledgerPath) {
			resetCount++
		}
	}

	if resetCount == 0 {
		return CheckResult{Status: StatusClean, Repo: repoPath}
	}
	return CheckResult{
		Status:  StatusFixed,
		Repo:    repoPath,
		Summary: fmt.Sprintf("reset %d sessions for inline-prompt re-summarization", resetCount),
	}
}

// checkSessionMetaTitles is the daemon-side empty-title repair. It
// resolves the ledger for repoPath, walks sessions/, and runs
// lfs.RecoverEmptyTitleMeta on each session whose meta.title is
// empty. Recovers from summary.json when possible; otherwise
// increments the bounded attempt counter and at lfs.MaxSummaryAttempts
// flips status to "unrecoverable" so the next pass short-circuits.
//
// Why per-ledger and not per-session: the autofix scheduler iterates
// repoPaths (workspaces). The session repair lives on the LEDGER
// (separate path), so each tick we resolve the workspace's ledger
// once and walk it. Skipping a workspace whose ledger isn't on disk
// yet is normal during clone.
//
// Blast radius: per-session meta.json rewrites, bounded by the cap.
// No git operations, no LFS calls, no network. Worst-case if the
// recovery is wrong: the affected session row title shows the wrong
// string, fixable by a future regenerate.
func checkSessionMetaTitles(_ context.Context, repoPath string) CheckResult {
	if repoPath == "" {
		return CheckResult{Status: StatusClean}
	}
	ctx, err := config.LoadProjectContext(repoPath)
	if err != nil || ctx == nil {
		// uninitialized workspace or no ledger configured — nothing to do
		return CheckResult{Status: StatusClean, Repo: repoPath}
	}
	ledgerPath := ctx.DefaultLedgerPath()
	if ledgerPath == "" {
		return CheckResult{Status: StatusClean, Repo: repoPath}
	}
	return repairLedgerSessionTitles(filepath.Join(ledgerPath, "sessions"), repoPath)
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

	var recovered, bumped, flipped, errored int
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
		case out.FlippedTerminal:
			flipped++
		case out.BumpedAttempts:
			bumped++
		}
	}
	if recovered == 0 && bumped == 0 && flipped == 0 && errored == 0 {
		return CheckResult{Status: StatusClean, Repo: repoPath}
	}
	if recovered > 0 || flipped > 0 {
		return CheckResult{
			Status:  StatusFixed,
			Repo:    repoPath,
			Summary: fmt.Sprintf("session meta titles: recovered=%d flipped_terminal=%d bumped=%d errored=%d", recovered, flipped, bumped, errored),
		}
	}
	// only bumps and/or errors — surface as Found so it's visible in
	// the issue tracker but doesn't claim we actively fixed anything
	// (we just made progress toward the terminal cap).
	return CheckResult{
		Status:  StatusFound,
		Repo:    repoPath,
		Summary: fmt.Sprintf("session meta titles: bumped=%d errored=%d (no recoveries this pass)", bumped, errored),
	}
}
