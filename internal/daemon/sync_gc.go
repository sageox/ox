package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/manifest"
	"github.com/sageox/ox/internal/paths"
)

// gcResult indicates the outcome of a blue-green GC attempt.
type gcResult int

const (
	gcSuccess       gcResult = iota // reclone completed successfully
	gcSkippedDirty                  // skipped: local changes could not be preserved
	gcSkippedLocked                 // skipped: another GC holds the workspace lock
	gcFailed                        // reclone attempted but failed (clone, validation, or swap error)
)

// checkAndRunGC iterates team context workspaces and triggers blue-green reclone
// for any that are past their gc_interval_days cadence.
func (s *SyncScheduler) checkAndRunGC(ctx context.Context) {
	// only one GC at a time
	if !atomic.CompareAndSwapInt32(&s.gcInProgress, 0, 1) {
		s.logger.Debug("gc: already in progress, skipping check")
		return
	}

	if err := s.workspaceRegistry.LoadFromConfig(); err != nil {
		s.logger.Warn("gc: failed to load workspace registry", "error", err)
		atomic.StoreInt32(&s.gcInProgress, 0)
		return
	}

	teamContexts := s.workspaceRegistry.GetTeamContexts()
	for _, ws := range teamContexts {
		interruptedSwap := gitutil.IsGitRepo(ws.Path + ".old")
		if (!ws.Exists && !interruptedSwap) || ws.CloneURL == "" {
			continue
		}

		// skip if clone is in flight
		if _, loaded := s.cloneInFlight.Load(ws.ID); loaded {
			continue
		}

		// trigger 1: GC interval exceeded
		intervalDays := ws.GCIntervalDays
		if intervalDays <= 0 {
			intervalDays = manifest.DefaultGCIntervalDays
		}
		interval := time.Duration(intervalDays) * 24 * time.Hour
		intervalExceeded := ws.LastGCTime.IsZero() || time.Since(ws.LastGCTime) >= interval

		// trigger 2: full clone detected (upgrade pre-v4 installs to partial clone)
		fullClone := !isPartialClone(ws.Path)

		if !intervalExceeded && !fullClone && !interruptedSwap {
			continue
		}

		reason := "interval exceeded"
		if fullClone {
			reason = "full clone upgrade"
		}
		if interruptedSwap {
			reason = "interrupted swap"
		}
		s.logger.Info("gc: workspace due for reclone", "team", ws.TeamName, "id", ws.ID,
			"reason", reason, "interval_days", intervalDays, "last_gc", ws.LastGCTime)

		// run GC synchronously (one at a time) then release the flag
		result := s.runBlueGreenGC(ctx, ws)

		repoName := ws.TeamName
		if repoName == "" {
			repoName = ws.ID
		}
		if s.issues != nil {
			switch result {
			case gcSkippedDirty:
				// surface dirty workspace so ox doctor / ox daemon status can notify the user
				s.issues.SetIssue(DaemonIssue{
					Type:     IssueTypeDirtyWorkspace,
					Severity: SeverityWarning,
					Repo:     repoName,
					Summary:  "local changes could not be preserved for GC reclone (push failed or changes could not be captured)",
				})
			case gcSuccess:
				s.issues.ClearIssue(IssueTypeDirtyWorkspace, repoName)
				s.issues.ClearIssue(IssueTypeSyncBackoff, ws.ID)
			}
		}
		// A reclone replaces the entire checkout, so any suspension recorded
		// against the OLD worktree's fingerprint no longer describes anything
		// that exists. Without this, a suspended team context stayed suspended
		// across reclones and only `ox sync --all-teams` could revive it.
		// Mirrors the ledger's post-GC ClearSyncFailures below.
		if result == gcSuccess {
			s.workspaceRegistry.ClearSyncFailures(ws.ID)
		}

		break // one GC per check cycle to avoid overloading
	}

	// ledger GC reclone — same blue-green pattern as team contexts
	if l := s.workspaceRegistry.GetLedger(); l != nil && (l.Exists || gitutil.IsGitRepo(l.Path+".old")) && l.CloneURL != "" {
		if _, loaded := s.cloneInFlight.Load(l.ID); !loaded {
			intervalDays := l.GCIntervalDays
			if intervalDays <= 0 {
				intervalDays = manifest.DefaultGCIntervalDays
			}
			interval := time.Duration(intervalDays) * 24 * time.Hour
			intervalExceeded := l.LastGCTime.IsZero() || time.Since(l.LastGCTime) >= interval
			fullClone := !isPartialClone(l.Path)
			interruptedSwap := gitutil.IsGitRepo(l.Path + ".old")

			// trigger 3: a genuinely wedged ledger (ahead AND behind for
			// longer than ledgerSyncWedgeAge) — checkAndRunGC's normal
			// triggers never catch this on their own because a wedge can
			// persist indefinitely without ever exceeding the GC interval.
			// Checked independently of the other two triggers whenever the
			// cooldown permits. An overdue interval or full-clone upgrade must
			// not hide divergence: those ordinary paths skip when the safety
			// push is rejected, which otherwise leaves the wedge untouched.
			//
			// Cooldown is tracked via s.lastWedgeCheck, NOT l.LastGCTime —
			// LastGCTime is updated by any successful GC regardless of
			// trigger reason, so an unrelated interval-triggered reclone
			// would otherwise silently delay the wedge CHECK itself (not
			// just repeated recovery attempts) by up to 2x
			// ledgerSyncWedgeAge if it happened to land shortly before an
			// unrelated wedge started forming.
			s.mu.Lock()
			wedgeCooldownElapsed := s.lastWedgeCheck.IsZero() || time.Since(s.lastWedgeCheck) >= ledgerGCWedgeCooldown
			s.mu.Unlock()
			wedged := false
			var wedgeAge time.Duration
			if wedgeCooldownElapsed {
				var lockBusy bool
				wedged, wedgeAge, _, lockBusy = s.ledgerSyncWedged(ctx, l.Path)
				// A lock-busy result means no check actually ran — spending
				// the cooldown on it would let frequent contention from the
				// daemon's own sync cycle silently disable wedge detection.
				// A canceled probe must remain retryable too.
				if !lockBusy && ctx.Err() == nil {
					s.mu.Lock()
					s.lastWedgeCheck = time.Now()
					s.mu.Unlock()
				}
			}

			if intervalExceeded || fullClone || wedged || interruptedSwap {
				reason := "interval exceeded"
				switch {
				case interruptedSwap:
					reason = "interrupted swap"
				case wedged:
					reason = "sync wedge detected"
				case fullClone:
					reason = "full clone upgrade"
				}
				s.logger.Info("gc: ledger due for reclone", "id", l.ID,
					"reason", reason, "interval_days", intervalDays, "last_gc", l.LastGCTime, "wedge_age", wedgeAge)

				// A confirmed wedge asks the reclone to capture and
				// carry forward unpushed commits that a plain push can't
				// land (diverged from remote), even when an interval or
				// full-clone trigger is due too. Fresh divergence keeps the
				// conservative gcSkippedDirty behavior.
				result, recovered := s.runBlueGreenGCOpts(ctx, *l, wedged)
				if s.issues != nil {
					switch {
					case result == gcSkippedDirty:
						s.issues.SetIssue(DaemonIssue{
							Type:     IssueTypeDirtyWorkspace,
							Severity: SeverityWarning,
							Repo:     "ledger",
							Summary:  "local changes could not be preserved for GC reclone (push failed or changes could not be captured)",
						})
					case result == gcSuccess && recovered:
						// recovered (not just wedged) means the diverge-capture
						// path actually ran and actually captured content — a
						// reclone that succeeded via the ordinary path (e.g.
						// the wedge resolved itself between detection and this
						// call) must not raise a "review and commit" alert for
						// content that was never actually recovered.
						s.issues.ClearIssue(IssueTypeDirtyWorkspace, "ledger")
						s.issues.ClearIssue(IssueTypeSessionConflictWedge, "ledger")
						// (sync-failure backoff is cleared inside
						// runBlueGreenGCOpts itself, gated on `recovered` —
						// see the comment there for why it lives at that
						// layer instead of here.)
						// the daemon never commits (.claude/rules/daemon-git.md)
						// — recovered content is uncommitted working-tree
						// changes after this reclone. Loud on purpose: this
						// needs a human/agent to review and commit it.
						s.issues.SetIssue(DaemonIssue{
							Type:            IssueTypeSessionConflictRecovered,
							Severity:        SeverityError,
							Repo:            "ledger",
							Summary:         "sessions recovered as uncommitted changes after a wedged-ledger reclone — review and commit",
							RequiresConfirm: true,
						})
					case result == gcSuccess:
						s.issues.ClearIssue(IssueTypeDirtyWorkspace, "ledger")
						if wedged {
							s.issues.ClearIssue(IssueTypeSessionConflictWedge, "ledger")
						}
					}
				}

				// reopen whisper store after ledger GC — the old sql.DB handle
				// points to a deleted inode after the rename-swap
				if result == gcSuccess {
					s.reopenWhisperStoreAfterGC()
				}
			}
		}
	}

	// knowledge-bubble GC — independent of ledger / team-context GC.
	s.TriggerKBGC(ctx)

	atomic.StoreInt32(&s.gcInProgress, 0)
}

// ledgerSyncWedgeAge is how long a ledger must be simultaneously ahead
// (unpushed local commits) and behind (remote has advanced) before it's
// considered wedged rather than transient lag an ordinary pull cycle will
// resolve on its own. Comfortably past the 30-minute backoff cap in
// workspace_registry.go's exponentialBackoff, so this never fires on a
// repo that's still within its normal retry window.
const ledgerSyncWedgeAge = 3 * time.Hour

// ledgerGCWedgeCooldown bounds how often a detected wedge can re-trigger a
// reclone. If the wedge condition persists after one reclone attempt (e.g.
// a concurrent writer keeps recreating divergence — see ox-q42i/ox-50d5),
// that's a signal to stop auto-acting and let severity escalation
// (escalateSessionConflictSeverity) raise the alarm instead of retrying
// every cycle.
const ledgerGCWedgeCooldown = 6 * time.Hour

// ledgerSyncWedged detects a ledger stuck simultaneously ahead and behind
// its remote for longer than ledgerSyncWedgeAge — the state a sessions/
// meta.json content conflict produces (see sessionConflictPaths in
// sync_managed.go), which checkAndRunGC's interval/full-clone triggers
// never catch on their own since a wedge can persist indefinitely without
// ever exceeding the GC interval.
//
// Deliberately git-plumbing only, not based on any in-memory daemon state
// (unlike workspace_registry.go's SyncFailures counter, which resets on
// daemon restart) — so detection survives across restarts during a
// multi-hour incident. Confirmed via a live fetch so a merely-offline
// machine, an explicitly supported normal state, is never mistaken for
// wedged.
func (s *SyncScheduler) ledgerSyncWedged(ctx context.Context, path string) (wedged bool, oldestUnpushedAge time.Duration, unpushedCount int, lockBusy bool) {
	ahead, err := revListCount(ctx, path, "@{upstream}..HEAD", "origin/main..HEAD")
	if err != nil || ahead <= 0 {
		return false, 0, 0, false
	}

	// bounded fetch to confirm we're actually online before concluding
	// wedged — a fetch failure means offline, not stuck. Locked (ADR-030
	// D1): this probe runs on its own schedule, concurrently with the
	// regular sync scheduler pull cycle in the SAME daemon process, so
	// without the per-clone lock its fetch could interleave with the pull
	// cycle's and corrupt FETCH_HEAD exactly like the 2026-09-02 incident.
	// A lock-busy result is treated the same as offline: not confirmed, not
	// wedged, retry next cycle.
	fetchErr := gitutil.WithRepoLock(ctx, path, func() error {
		_, err := gitutil.NewNetworkCmd(ctx, "-C", path, "fetch", "--quiet").CombinedOutput()
		return err
	})
	if fetchErr != nil {
		// A lock held by a concurrent ox process (the sync scheduler's own
		// pull cycle, most likely, since this probe now shares its lock)
		// is NOT the same signal as offline: the caller must not spend the
		// 6h cooldown on a cycle where no check actually happened, or
		// frequent lock contention could starve this detector indefinitely.
		return false, 0, ahead, gitutil.IsRepoLockBusy(fetchErr)
	}

	behind, err := revListCount(ctx, path, "HEAD..@{upstream}", "HEAD..origin/main")
	if err != nil || behind <= 0 {
		// ahead only, not behind — a plain push (gcPushUnpushedCommits)
		// resolves this; not wedged.
		return false, 0, ahead, false
	}

	oldestOutput, err := gitutil.RunGit(ctx, path, "log", "@{upstream}..HEAD", "--format=%ct")
	if err != nil {
		oldestOutput, err = gitutil.RunGit(ctx, path, "log", "origin/main..HEAD", "--format=%ct")
	}
	if err != nil {
		return false, 0, ahead, false
	}
	timestamps := strings.Fields(strings.TrimSpace(oldestOutput))
	if len(timestamps) == 0 {
		return false, 0, ahead, false
	}
	// git log lists newest-first; the last line is the oldest unpushed commit.
	oldestUnix, err := strconv.ParseInt(timestamps[len(timestamps)-1], 10, 64)
	if err != nil {
		return false, 0, ahead, false
	}

	age := time.Since(time.Unix(oldestUnix, 0))
	return age >= ledgerSyncWedgeAge, age, ahead, false
}

// revListCount runs `git rev-list --count <range>`, falling back to
// fallbackRange when the primary range fails (typically because
// @{upstream} has no tracking branch configured).
func revListCount(ctx context.Context, path, primaryRange, fallbackRange string) (int, error) {
	output, err := gitutil.RunGit(ctx, path, "rev-list", "--count", primaryRange)
	if err != nil {
		output, err = gitutil.RunGit(ctx, path, "rev-list", "--count", fallbackRange)
		if err != nil {
			return 0, err
		}
	}
	return strconv.Atoi(strings.TrimSpace(output))
}

// isNonFastForwardErr reports whether a git push failure looks like a
// non-fast-forward rejection (remote has commits we don't have) rather than
// an auth/network/LFS/protected-branch failure — mirrors the substring-match
// style already used for LFS errors in gcPushUnpushedCommits.
//
// Deliberately does NOT match on the bare word "rejected": both a genuine
// non-fast-forward push and a protected-branch/hook rejection use "rejected"
// somewhere in their git output ("[rejected]" is the generic push-refusal
// prefix; the actual reason is a separate suffix like "(non-fast-forward)"
// or "(protected branch hook declined)"). Matching on "rejected" alone would
// route protected-branch/hook failures into the diverge-capture path, which
// is only correct for genuine divergence.
func isNonFastForwardErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "non-fast-forward") ||
		strings.Contains(msg, "fetch first")
}

// TriggerGC forces a GC reclone of all eligible team contexts, bypassing the interval check.
// Returns immediately if GC is already in progress. Runs synchronously.
//
// Do not convert this to run in the background: CLI binaries that predate
// trigger_gc_async (before v0.12.0) send trigger_gc from `ox doctor --gc` and
// print the reclone counts from this response. Use TriggerGCAsync for callers
// that must not block on a multi-minute reclone.
//
// This does not run kb GC; TriggerKBGC does.
func (s *SyncScheduler) TriggerGC(ctx context.Context) *TriggerGCResponse {
	if !atomic.CompareAndSwapInt32(&s.gcInProgress, 0, 1) {
		return &TriggerGCResponse{Skipped: 1}
	}
	defer atomic.StoreInt32(&s.gcInProgress, 0)
	return s.runTriggerGC(ctx)
}

// TriggerGCAsync forces a GC reclone of all eligible team contexts in a
// background goroutine, mirroring daemonServiceImpl.Doctor()'s async
// pattern: the caller's IPC read deadline is milliseconds, but a
// blue-green reclone can take minutes, so the work must not run on the
// request path. Single-flight via the same gcInProgress guard TriggerGC
// uses — a concurrent call while GC is running returns AlreadyRunning
// instead of queuing a second sweep. The goroutine is tracked via the
// same cloneWg used for background clones so daemon shutdown can bound
// how long it waits for in-flight GC work (see waitClones).
func (s *SyncScheduler) TriggerGCAsync(ctx context.Context) *TriggerGCResponse {
	if !atomic.CompareAndSwapInt32(&s.gcInProgress, 0, 1) {
		return &TriggerGCResponse{AlreadyRunning: true}
	}
	if !s.addClone() {
		// scheduler is shutting down — don't spawn new background work
		atomic.StoreInt32(&s.gcInProgress, 0)
		return &TriggerGCResponse{Skipped: 1}
	}
	go func() {
		defer s.cloneWg.Done()
		defer atomic.StoreInt32(&s.gcInProgress, 0)
		if s.gcAsyncTestHook != nil {
			s.gcAsyncTestHook()
		}
		s.runTriggerGC(ctx)
	}()
	return &TriggerGCResponse{BackgroundStarted: true}
}

// runTriggerGC performs the forced-reclone sweep across team contexts and
// the ledger. Callers must already hold the gcInProgress single-flight
// guard (both TriggerGC and TriggerGCAsync do).
func (s *SyncScheduler) runTriggerGC(ctx context.Context) *TriggerGCResponse {
	if err := s.workspaceRegistry.LoadFromConfig(); err != nil {
		return &TriggerGCResponse{Errors: []string{fmt.Sprintf("load registry: %v", err)}}
	}

	resp := &TriggerGCResponse{}
	teamContexts := s.workspaceRegistry.GetTeamContexts()
	for _, ws := range teamContexts {
		if (!ws.Exists && !gitutil.IsGitRepo(ws.Path+".old")) || ws.CloneURL == "" {
			continue
		}
		if _, loaded := s.cloneInFlight.Load(ws.ID); loaded {
			resp.Skipped++
			continue
		}

		s.logger.Info("trigger_gc: forced reclone", "team", ws.TeamName, "id", ws.ID)
		name := ws.TeamName
		if name == "" {
			name = ws.ID
		}

		result := s.runBlueGreenGC(ctx, ws)
		switch result {
		case gcSuccess:
			resp.Triggered++
			if s.issues != nil {
				s.issues.ClearIssue(IssueTypeDirtyWorkspace, name)
				s.issues.ClearIssue(IssueTypeGCFailed, name)
				// Keyed on ws.ID, not name: shouldSyncOrBypass raises
				// SyncBackoff with the workspace ID, while the two issues
				// above are keyed by display name. The two read alike.
				s.issues.ClearIssue(IssueTypeSyncBackoff, ws.ID)
			}
			// A forced reclone replaces the checkout just as the interval-driven
			// one does, so it must also retire a suspension recorded against the
			// worktree that no longer exists. Without this, `ox doctor --gc`
			// looked like a recovery path but left background sync skipped.
			s.workspaceRegistry.ClearSyncFailures(ws.ID)
		case gcSkippedDirty:
			resp.Errors = append(resp.Errors, fmt.Sprintf("%s: local changes could not be preserved for GC", name))
			if s.issues != nil {
				s.issues.SetIssue(DaemonIssue{
					Type:     IssueTypeDirtyWorkspace,
					Severity: SeverityWarning,
					Repo:     name,
					Summary:  "local changes could not be preserved for GC reclone (push failed or changes could not be captured)",
				})
			}
		case gcSkippedLocked:
			// another GC holds the workspace lock — not a dirty workspace,
			// just a concurrency skip
			resp.Skipped++
		case gcFailed:
			resp.Errors = append(resp.Errors, fmt.Sprintf("%s: reclone failed (check daemon logs)", name))
			// TriggerGCAsync discards this response's return value, so the
			// IssueTracker is the only way a background GC failure becomes
			// visible (via `ox daemon status`).
			if s.issues != nil {
				s.issues.SetIssue(DaemonIssue{
					Type:     IssueTypeGCFailed,
					Severity: SeverityError,
					Repo:     name,
					Summary:  "GC reclone failed (check daemon logs)",
				})
			}
		}
	}

	// ledger GC reclone
	if l := s.workspaceRegistry.GetLedger(); l != nil && (l.Exists || gitutil.IsGitRepo(l.Path+".old")) && l.CloneURL != "" {
		if _, loaded := s.cloneInFlight.Load(l.ID); !loaded {
			s.logger.Info("trigger_gc: forced ledger reclone", "id", l.ID)
			// A forced repair is an explicit recovery request, so preserve
			// diverged local commits as working-tree changes rather than
			// skipping the one broken state the repair is meant to fix.
			result, recovered := s.runBlueGreenGCOpts(ctx, *l, true)
			switch result {
			case gcSuccess:
				resp.LedgerTriggered = true
				if s.issues != nil {
					s.issues.ClearIssue(IssueTypeDirtyWorkspace, "ledger")
					s.issues.ClearIssue(IssueTypeGCFailed, "ledger")
					if recovered {
						s.issues.ClearIssue(IssueTypeSessionConflictWedge, "ledger")
						s.issues.SetIssue(DaemonIssue{
							Type:            IssueTypeSessionConflictRecovered,
							Severity:        SeverityError,
							Repo:            "ledger",
							Summary:         "sessions recovered as uncommitted changes after a forced Ledger reclone — review and commit",
							RequiresConfirm: true,
						})
					}
				}
				// runBlueGreenGC closes the whisper store before the rename to release
				// SQLite's mmap; reopen it now so writes don't silently fail until the
				// next daemon restart.
				s.reopenWhisperStoreAfterGC()
			case gcSkippedDirty:
				resp.Errors = append(resp.Errors, "ledger: local changes could not be preserved for GC")
				if s.issues != nil {
					s.issues.SetIssue(DaemonIssue{
						Type:     IssueTypeDirtyWorkspace,
						Severity: SeverityWarning,
						Repo:     "ledger",
						Summary:  "local changes could not be preserved for GC reclone (push failed or changes could not be captured)",
					})
				}
			case gcSkippedLocked:
				// another GC holds the workspace lock — counted as skip
				resp.Skipped++
			case gcFailed:
				resp.Errors = append(resp.Errors, "ledger: reclone failed (check daemon logs)")
				if s.issues != nil {
					s.issues.SetIssue(DaemonIssue{
						Type:     IssueTypeGCFailed,
						Severity: SeverityError,
						Repo:     "ledger",
						Summary:  "GC reclone failed (check daemon logs)",
					})
				}
			}
		}
	}

	return resp
}

// acquireGCLock creates an exclusive filesystem lock for the GC swap window.
// Returns the lock file handle on success, or an error if the lock is already held.
// The lock is short-lived (only held during the two-rename atomic swap).
func acquireGCLock(lockPath string) (*os.File, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("gc lock create failed: %w", err)
		}
		// lock file exists — check if stale (>5 min old = likely crashed process)
		if info, statErr := os.Stat(lockPath); statErr == nil {
			if time.Since(info.ModTime().UTC()) > 5*time.Minute {
				// stale lock from a crashed process — remove and retry once
				_ = os.Remove(lockPath)
				return os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
			}
		}
		return nil, fmt.Errorf("gc lock held: %w", err)
	}
	return f, nil
}

// releaseGCLock closes and removes the GC lock file.
func releaseGCLock(f *os.File, lockPath string) {
	_ = f.Close()
	_ = os.Remove(lockPath)
}

// runBlueGreenGC performs a blue-green reclone for a single workspace (team context or ledger).
// Steps: preserve local state → clone .new → restore and validate → swap → remove old.
//
// GC is a disk-space optimization — it should never impair the user experience.
// Local changes (unpushed commits, uncommitted edits, untracked files) are preserved
// across the reclone. If preservation fails, GC is skipped rather than risk data loss.
func (s *SyncScheduler) runBlueGreenGC(ctx context.Context, ws WorkspaceState) gcResult {
	result, _ := s.runBlueGreenGCOpts(ctx, ws, false)
	return result
}

// runBlueGreenGCOpts is runBlueGreenGC with captureUnpushedOnDiverge
// exposed. Kept as a separate entry point (rather than adding the param to
// runBlueGreenGC directly) so every existing caller — 50+ call sites across
// the test suite — keeps its current, unchanged behavior with zero edits;
// only the new ledger-wedge trigger in checkAndRunGC calls this directly.
//
// The second return value is true only when the diverge-capture path
// actually ran AND actually captured content (gcCaptureDiff found a
// non-empty diff). It is NOT a synonym for "captureUnpushedOnDiverge was
// requested" or "the reclone succeeded" — a reclone can succeed via the
// ordinary path even when the caller passed captureUnpushedOnDiverge=true
// (e.g. the wedge condition resolved itself between detection and this
// call, so the push just succeeded normally). Callers must gate anything
// implying "there is recovered content for a human to review" on this
// value specifically, not on gcSuccess or on their own pre-call wedge
// sample.
func (s *SyncScheduler) runBlueGreenGCOpts(ctx context.Context, ws WorkspaceState, captureUnpushedOnDiverge bool) (result gcResult, recovered bool) {
	// Match publication's lock order. The shared repo lock must cover capture
	// as well as swap, or a writer can publish after the snapshot is taken.
	if ws.Type == WorkspaceTypeLedger {
		// GC is optional: do not wait behind a sync or publication before its
		// execution budget starts. A later cycle can retry the optimization.
		if !s.ledgerMu.TryLock() {
			s.logger.Info("gc: ledger is busy, skipping", "path", ws.Path)
			return gcSkippedLocked, false
		}
		defer s.ledgerMu.Unlock()
	}
	result = gcSkippedLocked
	err := gitutil.WithRepoLock(ctx, ws.Path, func() error {
		// The repo lock's timeout only bounds acquisition. Reuse the clone
		// budgets to bound preparation too; a partial clone can fetch on apply.
		budget := teamContextCloneTimeout
		if ws.Type == WorkspaceTypeLedger {
			budget = ledgerCloneTimeout
		}
		ctx, cancel := context.WithTimeout(ctx, budget)
		defer cancel()
		result, recovered = s.runBlueGreenGCLocked(ctx, ws, captureUnpushedOnDiverge)
		return nil
	})
	if err != nil {
		s.logger.Warn("gc: cannot acquire repository lock", "path", ws.Path, "error", err)
		if !gitutil.IsRepoLockBusy(err) {
			result = gcFailed
		}
	}
	return result, recovered
}

// The caller holds the repo lock and, for a Ledger, ledgerMu until cleanup.
func (s *SyncScheduler) runBlueGreenGCLocked(ctx context.Context, ws WorkspaceState, captureUnpushedOnDiverge bool) (result gcResult, recovered bool) {
	newPath := ws.Path + ".new"
	oldPath := ws.Path + ".old"
	diffFile := ws.Path + ".gc-diff"
	untrackedDir := ws.Path + ".gc-untracked"
	lockPath := ws.Path + ".gc-lock"
	cacheBackupDir := ws.Path + ".gc-cache"
	swapLockPath := ws.Path + ".gc-swap-lock"
	isLedger := ws.Type == WorkspaceTypeLedger

	wsLabel := ws.TeamName
	if wsLabel == "" {
		wsLabel = ws.ID
	}

	// acquire the GC lock up front (before cloning, not just around the swap)
	// so concurrent GC attempts on the same workspace don't race on .new/.old
	// or erase each other's in-flight artifacts. Stale locks (>5min) are
	// reclaimed by acquireGCLock itself — we refresh the lock file mtime below
	// so a long clone/restore cycle doesn't get its lock stolen.
	gcLock, lockErr := acquireGCLock(lockPath)
	if lockErr != nil {
		s.logger.Info("gc: another GC holds the lock for this workspace, skipping",
			"path", ws.Path, "workspace", wsLabel, "lock", lockPath)
		return gcSkippedLocked, false
	}
	defer releaseGCLock(gcLock, lockPath)
	if isLedger {
		defer os.Remove(swapLockPath)
	}

	// Heartbeat the lock mtime every 30s so acquireGCLock's 5-minute
	// stale-lock reclaim doesn't steal an actively-held lock when the
	// clone/restore phase legitimately runs long.
	stopLockHeartbeat := make(chan struct{})
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stopLockHeartbeat:
				return
			case <-t.C:
				now := time.Now()
				_ = os.Chtimes(lockPath, now, now)
			}
		}
	}()
	defer close(stopLockHeartbeat)

	swapMarker := ws.Path + ".gc-swap-done"
	var carryErr error
	if _, err := os.Stat(oldPath); err == nil {
		// Recover .old before cleanup: it may hold the only copy of a retry.
		if isLedger {
			if err := os.WriteFile(swapLockPath, nil, 0o600); err != nil {
				s.logger.Error("gc: cannot protect interrupted swap recovery", "path", ws.Path, "error", err)
				return gcFailed, false
			}
		}
		err := func() error {
			if _, err := os.Stat(ws.Path); errors.Is(err, fs.ErrNotExist) {
				// The process stopped between the two renames.
				if err := os.Rename(oldPath, ws.Path); err != nil {
					return err
				}
				s.workspaceRegistry.RefreshExists()
				return nil
			} else if err != nil {
				return err
			}
			if err := os.WriteFile(swapMarker, nil, 0o600); err != nil {
				return err
			}
			carryErr = gcCarryPlanRetries(oldPath, ws.Path)
			return nil
		}()
		if err != nil {
			s.logger.Error("gc: cannot recover interrupted swap, retaining old clone", "path", oldPath, "error", err)
			return gcFailed, false
		}
		if carryErr != nil {
			s.logger.Error("gc: cannot recover plan retries, retaining old clone", "path", oldPath, "error", carryErr)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		s.logger.Error("gc: cannot inspect old clone", "path", oldPath, "error", err)
		return gcFailed, false
	}
	if isLedger {
		_ = os.Remove(swapLockPath)
	}

	// Older GC versions restored after swapping. Their marker protects
	// captured state that may be the only surviving copy after a crash.
	// Recover it before cleaning leftovers or preparing another replacement.
	if _, err := os.Stat(swapMarker); err == nil {
		s.logger.Warn("gc: found an interrupted prior GC that completed its swap but not its restore, recovering before continuing",
			"path", ws.Path, "workspace", wsLabel)
		// Not ledger-only: team contexts now have a preserved cache too, and
		// the leftover sweep below deletes cacheBackupDir unconditionally. A
		// crash between the swap and phase 1.5 would otherwise discard the
		// backup — taking sync-state.json with it, which is the file a
		// non-owner daemon reads to learn when a team context last synced.
		if _, statErr := os.Stat(cacheBackupDir); statErr == nil {
			if applyErr := gcRestoreCache(cacheBackupDir, ws.Path); applyErr != nil {
				s.logger.Error("gc: failed to restore orphaned cache backup, preserving for manual recovery",
					"path", ws.Path, "cache_backup", cacheBackupDir, "error", applyErr)
				return gcFailed, false
			}
			_ = os.RemoveAll(cacheBackupDir)
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			s.logger.Error("gc: cannot inspect orphaned cache backup", "path", cacheBackupDir, "error", statErr)
			return gcFailed, false
		}
		if _, statErr := os.Stat(diffFile); statErr == nil {
			if applyErr := s.gcRestoreDiff(ctx, ws.Path, diffFile); applyErr != nil {
				s.logger.Error("gc: failed to apply orphaned diff, preserving for manual recovery",
					"path", ws.Path, "workspace", wsLabel, "diff_file", diffFile, "error", applyErr)
				return gcFailed, false
			}
			_ = os.Remove(diffFile)
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			s.logger.Error("gc: cannot inspect orphaned diff", "path", diffFile, "error", statErr)
			return gcFailed, false
		}
		if _, statErr := os.Stat(untrackedDir); statErr == nil {
			if applyErr := s.gcRestoreUntracked(ws.Path, untrackedDir); applyErr != nil {
				s.logger.Error("gc: failed to restore orphaned untracked backup, preserving for manual recovery",
					"path", ws.Path, "workspace", wsLabel, "untracked_dir", untrackedDir, "error", applyErr)
				return gcFailed, false
			}
			_ = os.RemoveAll(untrackedDir)
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			s.logger.Error("gc: cannot inspect orphaned untracked backup", "path", untrackedDir, "error", statErr)
			return gcFailed, false
		}
		_ = os.Remove(swapMarker)
		s.logger.Info("gc: recovered orphaned artifacts from an interrupted prior GC", "path", ws.Path, "workspace", wsLabel)
	} else if !errors.Is(err, fs.ErrNotExist) {
		s.logger.Error("gc: cannot inspect interrupted swap marker", "path", swapMarker, "error", err)
		return gcFailed, false
	}
	if carryErr != nil {
		return gcFailed, false
	}
	// Retire the recovered clone before capturing new backups; otherwise a
	// later run can recreate the swap marker and replay a failed preparation.
	if err := os.RemoveAll(oldPath); err != nil {
		s.logger.Error("gc: cannot remove recovered old clone", "path", oldPath, "error", err)
		return gcFailed, false
	}

	// clean up leftover artifacts from a previous failed GC (safe under the
	// lock) — reaching here means there was no pending swap-marker recovery
	// above, so newPath/diffFile/untrackedDir/cacheBackupDir (if any still
	// exist) are genuinely from an attempt that died before ever cloning;
	// the original content at ws.Path was never touched, and it's safe to
	// discard these and let this call redo capture/clone from scratch.
	leftovers := []string{newPath, diffFile, untrackedDir, cacheBackupDir}
	for _, leftover := range leftovers {
		if _, err := os.Stat(leftover); err == nil {
			s.logger.Info("gc: cleaning up leftover artifact", "path", leftover)
			if err := os.RemoveAll(leftover); err != nil {
				s.logger.Error("gc: failed to remove leftover artifact", "path", leftover, "error", err)
				return gcFailed, false
			}
		}
	}
	// Before the swap the original remains authoritative. After the swap
	// .new no longer exists, so this also cleans failed/canceled preparations.
	defer os.RemoveAll(newPath)

	// --- phase 0: preserve local state ---

	// step 0a: push unpushed commits so they survive reclone. When the push
	// is rejected because the remote diverged (not auth/network/LFS) and
	// the caller opted in, capture everything not on the remote — unpushed
	// commits AND any uncommitted changes, combined into one diff against
	// the merge-base — instead of bailing gcSkippedDirty. This is the one
	// scenario a wedged ledger sync actually needs GC to rescue: a plain
	// push can never land here (that's the definition of wedged), so
	// today's push-then-bail sequencing is exactly what makes GC unable to
	// help the one case that most needs it.
	divergeCaptured := false
	if err := s.gcPushUnpushedCommits(ctx, ws); err != nil {
		if !captureUnpushedOnDiverge || !isNonFastForwardErr(err) {
			s.logger.Warn("gc: skipping reclone, cannot push unpushed commits",
				"path", ws.Path, "workspace", wsLabel, "error", err)
			return gcSkippedDirty, false
		}

		mergeBase, mbErr := gitutil.RunGit(ctx, ws.Path, "merge-base", "@{upstream}", "HEAD")
		if mbErr != nil {
			mergeBase, mbErr = gitutil.RunGit(ctx, ws.Path, "merge-base", "origin/main", "HEAD")
		}
		if mbErr != nil {
			s.logger.Warn("gc: skipping reclone, cannot determine merge-base for diverge capture",
				"path", ws.Path, "workspace", wsLabel, "error", mbErr)
			return gcSkippedDirty, false
		}

		s.logger.Warn("gc: push rejected (diverged), capturing unpushed commits as a diff instead of skipping",
			"path", ws.Path, "workspace", wsLabel, "push_error", err)
		captured, capErr := s.gcCaptureDiff(ctx, ws.Path, diffFile, strings.TrimSpace(mergeBase))
		if capErr != nil {
			s.logger.Warn("gc: skipping reclone, cannot capture diverged commits as diff",
				"path", ws.Path, "workspace", wsLabel, "error", capErr)
			return gcSkippedDirty, false
		}
		divergeCaptured = captured
	}

	// step 0b: capture uncommitted tracked changes (staged + unstaged).
	// Skipped when step 0a's diverge-capture already ran — that diff was
	// taken against the merge-base, so it already includes any uncommitted
	// changes; capturing again here against HEAD would double them up.
	hasDiff := divergeCaptured
	var err error
	if !divergeCaptured {
		hasDiff, err = s.gcCaptureDiff(ctx, ws.Path, diffFile, "HEAD")
		if err != nil {
			s.logger.Warn("gc: skipping reclone, cannot capture uncommitted changes",
				"path", ws.Path, "workspace", wsLabel, "error", err)
			return gcSkippedDirty, false
		}
	}

	// step 0c: capture untracked files
	hasUntracked, err := s.gcCaptureUntracked(ctx, ws.Path, untrackedDir)
	if err != nil {
		s.logger.Warn("gc: skipping reclone, cannot capture untracked files",
			"path", ws.Path, "workspace", wsLabel, "error", err)
		return gcSkippedDirty, false
	}

	// step 0d: preserve .sageox/cache/ (gitignored: codedb indexes on the
	// ledger, sync-state.json on every workspace).
	// cache must survive reclones — abort GC if preservation fails
	//
	// Not ledger-only: recordSyncState writes each team context's
	// sync-state.json here, and that file is how a non-owner daemon learns
	// when the global-sync owner last pulled (see the LoadSyncState fallback
	// in daemon.go Status). Dropping it on reclone left every follower daemon
	// reporting a healthy team context as "not synced".
	hasCache := false
	if err := gcPreserveCache(ws.Path, cacheBackupDir); err != nil {
		s.logger.Warn("gc: skipping reclone, cannot preserve cache",
			"path", ws.Path, "error", err)
		return gcFailed, false
	} else if _, err := os.Stat(cacheBackupDir); err == nil {
		hasCache = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		s.logger.Error("gc: cannot inspect cache backup, keeping original", "path", ws.Path, "error", err)
		return gcFailed, false
	}

	// --- phase 1: clone, validate, swap ---

	// GC reclones use the bare clone URL with the ox credential helper
	// instead of embedded credentials. Per ox-eeqi — embedded credentials
	// in the cloned .git/config leak via backups, `git remote -v`, etc.
	cloneURL := ws.CloneURL
	ep := s.workspaceRegistry.GetEndpoint()
	// Sanity check that credentials exist for this endpoint; the helper
	// resolves them at clone time but a missing-creds situation should
	// surface as a clear log line, not a silent git prompt.
	if creds, err := gitserver.LoadCredentialsForEndpoint(ep); err != nil || creds == nil || creds.Token == "" {
		s.logger.Warn("gc: clone may fail — no credentials for endpoint", "endpoint", ep)
	}

	s.logger.Info("gc: starting reclone", "workspace", wsLabel, "path", newPath)

	// step 1: clone into .new (method depends on workspace type)
	var mCfg *manifest.ManifestConfig
	if isLedger {
		if err := ledger.CloneWithSparseCheckoutContext(ctx, newPath, cloneURL); err != nil {
			s.logger.Error("gc: reclone failed, keeping old", "workspace", wsLabel, "error", err)
			return gcFailed, false
		}
	} else {
		mCfg, err = s.twoPhaseClone(ctx, cloneURL, newPath, nil)
		if err != nil {
			s.logger.Error("gc: reclone failed, keeping old", "workspace", wsLabel, "error", err)
			return gcFailed, false
		}
	}

	// step 2: validate new clone
	valid := false
	if isLedger {
		valid = s.validateLedgerGCClone(newPath)
	} else {
		valid = s.validateGCClone(newPath, mCfg)
	}
	if !valid {
		s.logger.Error("gc: validation failed, keeping old", "workspace", wsLabel)
		return gcFailed, false
	}

	// configure pull.rebase=true on the new clone (ledger's ConfigureSparseCheckout already sets this)
	if !isLedger {
		if _, err := gitutil.RunGit(ctx, newPath, "config", "pull.rebase", "true"); err != nil {
			s.logger.Warn("gc: failed to set pull.rebase on new clone", "error", err)
		}
	}

	// ensure .sageox/.gitignore excludes daemon-written files (cache/, checkout.json, etc.)
	if err := gitserver.EnsureCheckoutGitignoreCtx(ctx, newPath); err != nil {
		s.logger.Warn("gc: failed to ensure checkout .gitignore on new clone", "error", err)
	}

	// Close Whisper stores before restoration to avoid extending the write window.
	// SQLite keeps -shm/-wal files mmap'd; on POSIX, os.Rename + os.RemoveAll
	// unlinks the underlying inodes but the kernel pins them as long as the
	// mmap region is alive, leaking FDs across every reclone. Ledger reopens
	// via reopenWhisperStoreAfterGC; team stores reopen lazily on next sync.
	if s.whisperRegistry != nil {
		if isLedger {
			s.whisperRegistry.CloseLedgerStore()
			defer func() {
				// Failure can leave either the replacement or a rolled-back
				// original live. Callers only reopen stores on GC success.
				if result != gcSuccess && gitutil.IsGitRepo(ws.Path) {
					s.reopenWhisperStoreAfterGC()
				}
			}()
		} else if ws.TeamID != "" {
			s.whisperRegistry.CloseTeamStore(ws.TeamID)
		}
	}

	// Restore into the disposable replacement. A failure or cancellation here
	// must never turn the original's captured state into the only backup.
	if hasCache {
		if err := gcRestoreCache(cacheBackupDir, newPath); err != nil {
			s.logger.Error("gc: cannot restore cache into replacement, keeping original", "path", ws.Path, "error", err)
			return gcFailed, false
		}
	}
	if hasDiff {
		if err := s.gcRestoreDiff(ctx, newPath, diffFile); err != nil {
			s.logger.Error("gc: cannot restore changes into replacement, keeping original", "path", ws.Path, "error", err)
			return gcFailed, false
		}
	}
	if hasUntracked {
		if err := s.gcRestoreUntracked(newPath, untrackedDir); err != nil {
			s.logger.Error("gc: cannot restore untracked files into replacement, keeping original", "path", ws.Path, "error", err)
			return gcFailed, false
		}
	}
	if err := ctx.Err(); err != nil {
		s.logger.Info("gc: preparation canceled, keeping original", "path", ws.Path, "error", err)
		return gcFailed, false
	}

	// Keep the legacy signal for older CLIs; current writers share the repo
	// lock held since capture. A failed signal must stop before any rename.
	if isLedger {
		if err := os.WriteFile(swapLockPath, nil, 0o600); err != nil {
			s.logger.Error("gc: cannot protect swap, keeping original", "path", ws.Path, "error", err)
			return gcFailed, false
		}
	}

	if s.gcSwapWindowTestHook != nil {
		s.gcSwapWindowTestHook()
	}

	if err := ctx.Err(); err != nil {
		s.logger.Info("gc: swap canceled, keeping original", "path", ws.Path, "error", err)
		return gcFailed, false
	}
	// Recovery treats these files as not-yet-restored state. Remove them
	// before swapping an already-restored replacement, while the original
	// still contains every byte; otherwise a crash would apply them twice.
	for _, artifact := range []string{diffFile, untrackedDir, cacheBackupDir} {
		if err := os.RemoveAll(artifact); err != nil {
			s.logger.Error("gc: cannot retire restored backup, keeping original", "path", artifact, "error", err)
			return gcFailed, false
		}
	}

	if err := ctx.Err(); err != nil {
		s.logger.Info("gc: swap canceled, keeping original", "path", ws.Path, "error", err)
		return gcFailed, false
	}

	// From the first rename onward, finish the swap or roll it back even if
	// the caller cancels. Keep .old until the marker and retry transfer succeed;
	// it also identifies an interrupted swap if the marker cannot be written.
	if err := os.Rename(ws.Path, oldPath); err != nil {
		s.logger.Error("gc: failed to move old repo aside", "path", ws.Path, "error", err)
		return gcFailed, false
	}

	if err := os.Rename(newPath, ws.Path); err != nil {
		s.logger.Error("gc: failed to move new repo into place, restoring old", "error", err)
		if restoreErr := os.Rename(oldPath, ws.Path); restoreErr != nil {
			s.logger.Error("gc: CRITICAL failed to restore old repo", "error", restoreErr)
		}
		return gcFailed, false
	}

	if err := os.WriteFile(swapMarker, nil, 0o600); err != nil {
		s.logger.Error("gc: failed to write swap marker, retaining old clone",
			"path", ws.Path, "error", err)
		return gcFailed, false
	}

	// Carry .git/ retries, including those written during cloning. Keep .old
	// until every entry is copied; an entry may hold the only record (#1356).
	carryErr = gcCarryPlanRetries(oldPath, ws.Path)
	if carryErr != nil {
		s.logger.Error("gc: failed to carry plan retries, retaining old clone", "path", oldPath, "error", carryErr)
		return gcFailed, false
	}
	if err := os.RemoveAll(oldPath); err != nil {
		s.logger.Warn("gc: failed to remove old clone, retaining recovery marker", "path", oldPath, "error", err)
		return gcFailed, false
	}
	_ = os.Remove(swapMarker)

	if isLedger && divergeCaptured {
		// The conflict that caused the wedge also drove RecordSyncFailure
		// on every failed pull cycle leading up to this recovery (doPull,
		// sync.go), climbing workspace_registry's exponential backoff
		// toward its 30-min cap. Without clearing it here, the very next
		// scheduled pull can still hit that stale backoff and re-log "sync
		// in backoff, skipping" — the literal diagnostic line from the
		// original incident report — immediately after the incident was
		// supposedly resolved. Lives here (gated on divergeCaptured, i.e.
		// what the caller sees as `recovered`) rather than in
		// checkAndRunGC's caller-side switch so it fires for any future
		// caller of the diverge-capture path, not just this one call site.
		s.workspaceRegistry.ClearSyncFailures("ledger")
	}

	s.workspaceRegistry.UpdateLastGC(ws.ID)
	if mCfg != nil {
		if mCfg.SyncIntervalMin > 0 {
			s.workspaceRegistry.SetSyncIntervalMin(ws.Path, mCfg.SyncIntervalMin)
		}
		if mCfg.GCIntervalDays > 0 {
			s.workspaceRegistry.SetGCInterval(ws.Path, mCfg.GCIntervalDays)
		}
	}

	s.logger.Info("gc: reclone complete", "workspace", wsLabel, "path", ws.Path)
	return gcSuccess, divergeCaptured
}

// Link retries without overwriting destination entries. Retire each source
// after its link is durable so cleared retries cannot return from .old.
func gcCarryPlanRetries(oldPath, newPath string) error {
	for _, dir := range []string{ledger.PlanPushPendingDir, ledger.PlanReviewPendingDir} {
		srcDir, dstDir := filepath.Join(oldPath, dir), filepath.Join(newPath, dir)
		entries, err := os.ReadDir(srcDir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dstDir, 0o755); err != nil {
			return err
		}
		for _, entry := range entries {
			src, dst := filepath.Join(srcDir, entry.Name()), filepath.Join(dstDir, entry.Name())
			if !entry.Type().IsRegular() {
				return fmt.Errorf("retry entry is not a regular file: %s", src)
			}
			if err := os.Link(src, dst); err != nil {
				if !errors.Is(err, fs.ErrExist) {
					return err
				}
				info, err := os.Lstat(dst)
				if err != nil {
					return err
				}
				if !info.Mode().IsRegular() {
					return fmt.Errorf("retry destination is not a regular file: %s", dst)
				}
				source, err := os.ReadFile(src)
				if err != nil {
					return err
				}
				destination, err := os.ReadFile(dst)
				if err != nil {
					return err
				}
				if !bytes.Equal(source, destination) {
					return fmt.Errorf("conflicting retry entries: %s and %s", src, dst)
				}
			}
			// Persist the link and clone swap before removing its source.
			// Windows cannot fsync directories.
			if runtime.GOOS != "windows" {
				for _, path := range []string{dstDir, filepath.Dir(dstDir), newPath, filepath.Dir(newPath)} {
					d, err := os.Open(path)
					if err != nil {
						return err
					}
					if err := errors.Join(d.Sync(), d.Close()); err != nil {
						return fmt.Errorf("sync retry destination %s: %w", path, err)
					}
				}
			}
			if err := os.Remove(src); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("remove copied retry %s: %w", src, err)
			}
			if runtime.GOOS != "windows" {
				d, err := os.Open(srcDir)
				if err != nil {
					return err
				}
				if err := errors.Join(d.Sync(), d.Close()); err != nil {
					return fmt.Errorf("sync retired retries %s: %w", srcDir, err)
				}
			}
		}
	}
	return nil
}

// gcPushUnpushedCommits pushes any local commits not yet on the remote.
// Returns nil if there are no unpushed commits or if push succeeds.
// Returns an error if unpushed commits exist and push fails.
//
// The caller holds WithRepoLock through capture and swap. Publication still
// shares mandatory exact-history validation with every other publisher.
func (s *SyncScheduler) gcPushUnpushedCommits(ctx context.Context, ws WorkspaceState) error {
	// count unpushed commits against the upstream tracking branch
	countOutput, err := gitutil.RunGit(ctx, ws.Path, "rev-list", "--count", "@{upstream}..HEAD")
	if err != nil {
		// no tracking branch — fall back to origin/main
		countOutput, err = gitutil.RunGit(ctx, ws.Path, "rev-list", "--count", "origin/main..HEAD")
		if err != nil {
			return fmt.Errorf("cannot determine unpushed commit count: %w", err)
		}
	}

	count := strings.TrimSpace(countOutput)
	if count == "" || count == "0" {
		return nil
	}

	s.logger.Info("gc: pushing unpushed commits before reclone", "path", ws.Path, "count", count)

	// Per ox-eeqi: push via the ox credential helper rather than embedding
	// the PAT in the remote URL. The helper installed in the repo's
	// .git/config resolves credentials; we still pass an explicit
	// `-c credential.helper=...` to make this push self-contained — in
	// case migration hasn't run on this repo yet (e.g., a fresh daemon
	// that just adopted an old ledger without sweeping it).
	ep := s.workspaceRegistry.GetEndpoint()
	if creds, err := gitserver.LoadCredentialsForEndpoint(ep); err != nil || creds == nil || creds.Token == "" {
		s.logger.Warn("gc: push may fail — no credentials for endpoint", "endpoint", ep)
	}
	helperCmd := gitserver.DefaultHelperCommand()

	// Never interpret a validation refusal as an LFS failure eligible for a
	// fallback push. The surrounding GC owns the non-reentrant repo lock.
	push := func(args ...string) error {
		if err := gitutil.IsSafeForGitOps(ws.Path); err != nil {
			return err
		}
		if err := gitutil.ValidateSessionPublication(ctx, ws.Path); err != nil {
			return err
		}
		_, err := gitutil.RunGit(ctx, ws.Path, args...)
		return err
	}
	pushArgs := []string{
		"-c", "credential.helper=",
		"-c", "credential.helper=" + helperCmd,
		"push", "origin", "HEAD", "--quiet",
	}
	if pushErr := push(pushArgs...); pushErr != nil {
		errMsg := pushErr.Error()
		if strings.Contains(errMsg, "LFS") || strings.Contains(errMsg, "lfs") || strings.Contains(errMsg, "allowincompletepush") {
			s.logger.Warn("gc: LFS objects missing, retrying push with allowincompletepush",
				"path", ws.Path, "error", pushErr)
			retryArgs := []string{
				"-c", "credential.helper=",
				"-c", "credential.helper=" + helperCmd,
				"-c", "lfs.allowincompletepush=true",
				"push", "origin", "HEAD", "--quiet",
			}
			if retryErr := push(retryArgs...); retryErr != nil {
				return fmt.Errorf("push failed (even with allowincompletepush): %w", retryErr)
			}
			s.logger.Info("gc: push succeeded with allowincompletepush", "path", ws.Path)
			return nil
		}
		return fmt.Errorf("push failed: %w", pushErr)
	}

	return nil
}

// maxGCDiffSize is the maximum diff size (50 MB) we'll capture during GC.
// Diffs larger than this are skipped to prevent OOM from a rogue agent
// committing and then modifying a huge binary.
const maxGCDiffSize = 50 * 1024 * 1024

// workingTreeEmpty reports whether the repo's working tree contains only .git
// (nothing else — no tracked files, no untracked files, no directories). This
// is the "rogue agent nuked the working tree" signal: treat it as corruption,
// not as a diff to preserve.
func workingTreeEmpty(repoPath string) (bool, error) {
	entries, err := os.ReadDir(repoPath)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		return false, nil
	}
	return true, nil
}

// gcCaptureDiff captures everything not yet reflected at baseRef — working
// tree plus index, diffed against baseRef — as a binary-safe patch file.
// Returns (hasDiff, error). Streams diff directly to disk (not into memory)
// to avoid OOM on large diffs. Diffs exceeding maxGCDiffSize are skipped
// with a warning.
//
// baseRef is "HEAD" for the normal uncommitted-changes-only capture (step
// 0b); callers that need to also carry forward committed-but-unpushed
// commits (the ledger-wedge diverge-capture path in
// runBlueGreenGCOpts) pass the merge-base with the remote instead, which
// folds both into one diff.
//
// If the working tree is empty (only .git remains), treat this as corruption
// rather than an intentional mass-delete and skip diff capture so the reclone
// restores the committed content from remote.
func (s *SyncScheduler) gcCaptureDiff(ctx context.Context, repoPath, diffFile, baseRef string) (bool, error) {
	if empty, err := workingTreeEmpty(repoPath); err == nil && empty {
		s.logger.Info("gc: working tree empty, skipping diff capture (will restore from remote)", "path", repoPath)
		return false, nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := gitutil.NewNetworkCmd(ctx, "-C", repoPath, "diff", "--binary", baseRef)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, fmt.Errorf("git diff pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("git diff start: %w", err)
	}

	outFile, err := os.OpenFile(diffFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		cancel()
		_ = cmd.Wait()
		return false, fmt.Errorf("create diff file: %w", err)
	}

	written, copyErr := io.Copy(outFile, io.LimitReader(stdout, maxGCDiffSize+1))
	copyErr = errors.Join(copyErr, outFile.Close())
	if copyErr != nil || written > maxGCDiffSize {
		// Once we stop reading, git can block on the full pipe. Cancel its
		// process group before Wait so GC does not retain the repo lock.
		cancel()
	}
	waitErr := cmd.Wait()

	if copyErr != nil {
		_ = os.Remove(diffFile)
		return false, fmt.Errorf("write diff file: %w", copyErr)
	}

	if written > maxGCDiffSize {
		_ = os.Remove(diffFile)
		s.logger.Warn("gc: diff too large, cannot preserve changes", "path", repoPath, "size", written, "max", maxGCDiffSize)
		return false, fmt.Errorf("diff too large (%d bytes, max %d) — cannot safely preserve uncommitted changes", written, maxGCDiffSize)
	}
	if waitErr != nil {
		_ = os.Remove(diffFile)
		return false, fmt.Errorf("git diff HEAD: %w", waitErr)
	}
	if written == 0 {
		_ = os.Remove(diffFile)
		return false, nil
	}

	s.logger.Info("gc: captured uncommitted changes", "path", repoPath, "diff_size", written)
	return true, nil
}

// gcCaptureUntracked copies untracked files (excluding gitignored) to a temp directory.
// Returns (hasFiles, error).
func (s *SyncScheduler) gcCaptureUntracked(ctx context.Context, repoPath, destDir string) (bool, error) {
	// RunGit trims output for humans; here leading whitespace is filename data.
	output, err := gitutil.NewNetworkCmd(ctx, "-C", repoPath, "ls-files", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return false, fmt.Errorf("git ls-files: %w", err)
	}

	if len(output) == 0 {
		return false, nil
	}

	files := strings.Split(string(output), "\x00")
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return false, fmt.Errorf("create untracked dir: %w", err)
	}

	copied := 0
	for _, relPath := range files {
		if relPath == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}

		// guard against path traversal and symlink escape
		absResolved, err := filepath.Abs(filepath.Join(repoPath, relPath))
		if err != nil {
			return false, fmt.Errorf("resolve untracked path %s: %w", relPath, err)
		}
		absRepo, _ := filepath.Abs(repoPath)
		// use filepath.Rel for separator-aware containment (prevents /tmp/repo-evil matching /tmp/repo)
		rel, err := filepath.Rel(absRepo, absResolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return false, fmt.Errorf("untracked path outside repo boundary: %s", relPath)
		}

		srcPath := filepath.Join(repoPath, relPath)
		dstPath := filepath.Join(destDir, relPath)

		if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
			return false, fmt.Errorf("create directory for untracked file %s: %w", relPath, err)
		}

		if err := copyFile(srcPath, dstPath); err != nil {
			return false, fmt.Errorf("capture untracked file %s: %w", relPath, err)
		}
		copied++
	}

	s.logger.Info("gc: captured untracked files", "path", repoPath, "count", copied)
	return copied > 0, nil
}

// gcRestoreDiff applies a previously captured diff to the recloned repo.
// Tries --3way first for merge support, falls back to --reject for partial apply.
func (s *SyncScheduler) gcRestoreDiff(ctx context.Context, repoPath, diffFile string) error {
	// try clean apply with 3-way merge
	if _, err := gitutil.RunGit(ctx, repoPath, "apply", "--3way", diffFile); err == nil {
		s.logger.Info("gc: restored uncommitted changes", "path", repoPath)
		return nil
	}

	// Fall back to --reject: applies whatever hunks it can and writes .rej
	// files for the rest. `git apply --reject` exits non-zero whenever AT
	// LEAST ONE hunk is rejected — even when every other hunk in the same
	// invocation applied cleanly — so the exit code alone cannot tell
	// "nothing was restored" from "most of it was, a few hunks conflicted".
	// Judge the outcome by what's actually on disk (.rej files) instead of
	// by rejectErr, and always sweep .rej markers regardless of the exit
	// code — previously that sweep only ran on the (in practice
	// unreachable, since any real reject makes the exit code non-zero)
	// success path, so real partial-reject runs left .rej files sitting in
	// the working tree permanently, unswept, and undocumented as such.
	_, rejectErr := gitutil.RunGit(ctx, repoPath, "apply", "--reject", diffFile)

	// Delete the .rej artifacts immediately. They are useless conflict markers
	// that, left in the tree, get swept into ledger history by the broad
	// `git add -A` commit paths AND keep the checkout permanently "dirty"
	// (blocking future GC reclone). The complete change is still recoverable
	// from diffFile, which we surface in the log.
	removed := removeRejFiles(repoPath)

	switch {
	case removed > 0:
		// partial apply: some hunks landed, some were rejected and
		// discarded. Report this as an error so the caller keeps diffFile
		// around (diffApplied=false) — the rejected content isn't purely
		// gone, but it's not been cleanly restored either, and a human
		// should know that rather than assume everything came back.
		s.logger.Warn("gc: restored uncommitted changes with conflicts; some hunks could not be applied",
			"path", repoPath, "rej_discarded", removed, "diff_preserved_at", diffFile)
		return fmt.Errorf("partial apply: %d hunk(s) rejected (diff preserved at %s)", removed, diffFile)
	case rejectErr != nil:
		// --reject itself failed and produced no .rej markers at all — a
		// harder failure than "some hunks conflicted" (e.g. the patch
		// doesn't apply to this tree in any form). Nothing was restored.
		return fmt.Errorf("git apply failed (diff preserved at %s): %w", diffFile, rejectErr)
	default:
		// no error and nothing rejected — every hunk applied via --reject
		// with zero conflicts (the --3way attempt above failed for some
		// other reason, e.g. a 3-way-specific limitation, not an actual
		// content conflict).
		s.logger.Info("gc: restored uncommitted changes (reject fallback, no conflicts)", "path", repoPath)
		return nil
	}
}

// removeRejFiles deletes every *.rej file under repoPath (excluding .git) and
// returns the count removed. Best-effort: individual delete failures are
// skipped so one unreadable path can't abort the sweep.
func removeRejFiles(repoPath string) int {
	removed := 0
	_ = filepath.WalkDir(repoPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		// only regular files (skip symlinks) to avoid following a link out of
		// the repo; the tree is ox-managed, not adversarial input.
		if strings.HasSuffix(d.Name(), ".rej") && d.Type().IsRegular() {
			if os.Remove(path) == nil { //nolint:gosec // G122: ox-managed repo tree, symlinks skipped above
				removed++
			}
		}
		return nil
	})
	return removed
}

// gcRestoreUntracked copies previously captured untracked files back into the repo.
func (s *SyncScheduler) gcRestoreUntracked(repoPath, untrackedDir string) error {
	if err := gcCheckRestorePaths(untrackedDir, repoPath, "."); err != nil {
		return err
	}
	var firstErr error
	err := filepath.WalkDir(untrackedDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		relPath, err := filepath.Rel(untrackedDir, path)
		if err != nil {
			return nil
		}

		dstPath := filepath.Join(repoPath, relPath)
		if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
			s.logger.Warn("gc: failed to create dir for untracked restore", "path", relPath, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			return nil
		}

		if err := copyFile(path, dstPath); err != nil {
			s.logger.Warn("gc: failed to restore untracked file", "path", relPath, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return firstErr
}

// GC holds the repo lock, but a cloned or previously restored symlink can still
// redirect a copy into the original checkout. Check each restore immediately
// before copying, including parents above the cache's destination directory.
func gcCheckRestorePaths(backupDir, repoPath, subdir string) error {
	repoPath = filepath.Clean(repoPath)
	return filepath.WalkDir(backupDir, func(path string, _ fs.DirEntry, err error) error {
		if path == backupDir && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(backupDir, path)
		if err != nil {
			return err
		}
		for dst := filepath.Join(repoPath, subdir, rel); dst != repoPath; dst = filepath.Dir(dst) {
			info, err := os.Lstat(dst)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if err == nil && info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("restore destination is a symlink: %s", dst)
			}
		}
		return nil
	})
}

// validateGCClone checks that a freshly cloned repo has the minimum expected
// content for a team context. Returns false if validation fails.
func (s *SyncScheduler) validateGCClone(repoPath string, cfg *manifest.ManifestConfig) bool {
	// .git must exist
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
		s.logger.Error("gc validate: .git missing", "path", repoPath)
		return false
	}

	// .sageox must exist
	if _, err := os.Stat(filepath.Join(repoPath, ".sageox")); err != nil {
		s.logger.Error("gc validate: .sageox missing", "path", repoPath)
		return false
	}

	// at least one core file
	coreFiles := []string{"SOUL.md", "TEAM.md", "MEMORY.md"}
	found := false
	for _, f := range coreFiles {
		if _, err := os.Stat(filepath.Join(repoPath, f)); err == nil {
			found = true
			break
		}
	}
	if !found {
		s.logger.Error("gc validate: no core files (SOUL.md, TEAM.md, MEMORY.md)", "path", repoPath)
		return false
	}

	// verify no denied paths materialized
	if cfg != nil {
		for _, denied := range cfg.Denies {
			if _, err := os.Stat(filepath.Join(repoPath, denied)); err == nil {
				s.logger.Error("gc validate: denied path materialized", "path", repoPath, "denied", denied)
				return false
			}
		}
	}

	return true
}

// validateLedgerGCClone checks that a freshly cloned repo has the minimum expected
// content for a ledger. Returns false if validation fails.
func (s *SyncScheduler) validateLedgerGCClone(repoPath string) bool {
	// .git must exist
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
		s.logger.Error("gc validate: .git missing", "path", repoPath)
		return false
	}

	// sessions/ directory must exist (core ledger directory)
	if _, err := os.Stat(filepath.Join(repoPath, "sessions")); err != nil {
		s.logger.Error("gc validate: sessions/ missing", "path", repoPath)
		return false
	}

	return true
}

var (
	gcPreserveCache = ledger.PreserveCache
	gcRestoreCache  = func(backupDir, repoPath string) error {
		if err := gcCheckRestorePaths(backupDir, repoPath, filepath.Join(".sageox", "cache")); err != nil {
			return err
		}
		return ledger.RestoreCache(backupDir, repoPath)
	}
	copyFile = fileutil.CopyFile
)

// reopenWhisperStoreAfterGC reopens the ledger whisper store after a
// GC reclone or rollback. The rename-swap invalidates the old sql.DB handle
// because the underlying inode is deleted — even though cache files are
// preserved and copied back, the daemon's open file descriptor is stale.
func (s *SyncScheduler) reopenWhisperStoreAfterGC() {
	s.mu.Lock()
	registry := s.whisperRegistry
	s.mu.Unlock()

	if registry == nil {
		return
	}

	ep := s.workspaceRegistry.GetEndpoint()
	repoID := config.GetRepoID(s.config.ProjectRoot)
	if ep == "" || repoID == "" {
		s.logger.Warn("gc: cannot reopen whisper store, endpoint or repoID not yet loaded",
			"endpoint", ep, "repoID", repoID)
		return
	}

	dbPath := filepath.Join(paths.WhisperDBDir(repoID, ep), "whisper.db")
	if err := registry.ReopenLedgerStore(dbPath); err != nil {
		s.logger.Error("gc: failed to reopen whisper store after ledger reclone", "error", err)
	}
}
