package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sageox/ox/internal/config"
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
		if !ws.Exists || ws.CloneURL == "" {
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

		if !intervalExceeded && !fullClone {
			continue
		}

		reason := "interval exceeded"
		if fullClone {
			reason = "full clone upgrade"
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
			}
		}

		break // one GC per check cycle to avoid overloading
	}

	// ledger GC reclone — same blue-green pattern as team contexts
	if l := s.workspaceRegistry.GetLedger(); l != nil && l.Exists && l.CloneURL != "" {
		if _, loaded := s.cloneInFlight.Load(l.ID); !loaded {
			intervalDays := l.GCIntervalDays
			if intervalDays <= 0 {
				intervalDays = manifest.DefaultGCIntervalDays
			}
			interval := time.Duration(intervalDays) * 24 * time.Hour
			intervalExceeded := l.LastGCTime.IsZero() || time.Since(l.LastGCTime) >= interval
			fullClone := !isPartialClone(l.Path)

			// trigger 3: a genuinely wedged ledger (ahead AND behind for
			// longer than ledgerSyncWedgeAge) — checkAndRunGC's normal
			// triggers never catch this on their own because a wedge can
			// persist indefinitely without ever exceeding the GC interval.
			// Only checked when the other two triggers didn't already fire
			// and the cooldown has elapsed, since it costs a live fetch.
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
			if !intervalExceeded && !fullClone && wedgeCooldownElapsed {
				wedged, wedgeAge, _ = s.ledgerSyncWedged(ctx, l.Path)
				s.mu.Lock()
				s.lastWedgeCheck = time.Now()
				s.mu.Unlock()
			}

			if intervalExceeded || fullClone || wedged {
				reason := "interval exceeded"
				switch {
				case fullClone:
					reason = "full clone upgrade"
				case wedged:
					reason = "sync wedge detected"
				}
				s.logger.Info("gc: ledger due for reclone", "id", l.ID,
					"reason", reason, "interval_days", intervalDays, "last_gc", l.LastGCTime, "wedge_age", wedgeAge)

				// only the wedge trigger asks the reclone to capture and
				// carry forward unpushed commits that a plain push can't
				// land (diverged from remote) — the interval/full-clone
				// triggers keep today's conservative gcSkippedDirty
				// behavior on any local changes that can't be preserved.
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
	// Wrapped in a defer/recover so a bug in the kb GC path can never
	// prevent the ledger / team-context passes above from running, nor
	// stall future GC ticks.
	func() {
		defer func() {
			if r := recover(); r != nil {
				s.logger.Warn("kb_gc panic recovered", "panic", r)
			}
		}()
		s.runKBGC(ctx, s.buildKBGCListFn())
	}()

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
func (s *SyncScheduler) ledgerSyncWedged(ctx context.Context, path string) (wedged bool, oldestUnpushedAge time.Duration, unpushedCount int) {
	ahead, err := revListCount(ctx, path, "@{upstream}..HEAD", "origin/main..HEAD")
	if err != nil || ahead <= 0 {
		return false, 0, 0
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
		return false, 0, ahead
	}

	behind, err := revListCount(ctx, path, "HEAD..@{upstream}", "HEAD..origin/main")
	if err != nil || behind <= 0 {
		// ahead only, not behind — a plain push (gcPushUnpushedCommits)
		// resolves this; not wedged.
		return false, 0, ahead
	}

	oldestOutput, err := gitutil.RunGit(ctx, path, "log", "@{upstream}..HEAD", "--format=%ct")
	if err != nil {
		oldestOutput, err = gitutil.RunGit(ctx, path, "log", "origin/main..HEAD", "--format=%ct")
	}
	if err != nil {
		return false, 0, ahead
	}
	timestamps := strings.Fields(strings.TrimSpace(oldestOutput))
	if len(timestamps) == 0 {
		return false, 0, ahead
	}
	// git log lists newest-first; the last line is the oldest unpushed commit.
	oldestUnix, err := strconv.ParseInt(timestamps[len(timestamps)-1], 10, 64)
	if err != nil {
		return false, 0, ahead
	}

	age := time.Since(time.Unix(oldestUnix, 0))
	return age >= ledgerSyncWedgeAge, age, ahead
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
// Do not convert this to run in the background: defaultKBDoctorGC
// (cmd/ox/doctor_kb.go) calls TriggerGC and immediately rechecks disk
// state for orphaned kb dirs, which depends on GC having actually
// finished by the time this call returns. Use TriggerGCAsync for callers
// (like `ox doctor --gc`) that must not block on a multi-minute reclone.
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
		if !ws.Exists || ws.CloneURL == "" {
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
			}
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
	if l := s.workspaceRegistry.GetLedger(); l != nil && l.Exists && l.CloneURL != "" {
		if _, loaded := s.cloneInFlight.Load(l.ID); !loaded {
			s.logger.Info("trigger_gc: forced ledger reclone", "id", l.ID)
			result := s.runBlueGreenGC(ctx, *l)
			switch result {
			case gcSuccess:
				resp.LedgerTriggered = true
				if s.issues != nil {
					s.issues.ClearIssue(IssueTypeDirtyWorkspace, "ledger")
					s.issues.ClearIssue(IssueTypeGCFailed, "ledger")
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
// Steps: preserve local state → clone .new → validate → atomic swap → remove old → restore state.
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
	newPath := ws.Path + ".new"
	oldPath := ws.Path + ".old"
	diffFile := ws.Path + ".gc-diff"
	untrackedDir := ws.Path + ".gc-untracked"
	lockPath := ws.Path + ".gc-lock"
	cacheBackupDir := ws.Path + ".gc-cache"
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

	// If a prior GC attempt on this workspace crashed AFTER completing the
	// rename-swap but BEFORE finishing phase 2's restore, ws.Path already
	// holds the fresh clone and diffFile/untrackedDir/cacheBackupDir (if
	// present) are the ONLY surviving copy of whatever that prior run
	// captured — the pre-GC tree is already gone. The swap marker (written
	// right after the swap succeeds, removed only once phase 2 fully
	// completes — see below) is how this is told apart from the ordinary
	// "a prior attempt died before ever cloning" case: an earlier version
	// of this code treated diffFile/untrackedDir as generic leftover
	// artifacts and deleted them unconditionally here regardless of which
	// case it was, silently discarding live wedge-recovered content on
	// exactly this crash timing.
	swapMarker := ws.Path + ".gc-swap-done"
	if _, err := os.Stat(swapMarker); err == nil {
		s.logger.Warn("gc: found an interrupted prior GC that completed its swap but not its restore, recovering before continuing",
			"path", ws.Path, "workspace", wsLabel)
		if isLedger {
			if _, statErr := os.Stat(cacheBackupDir); statErr == nil {
				if applyErr := gcRestoreCache(cacheBackupDir, ws.Path); applyErr != nil {
					s.logger.Error("gc: failed to restore orphaned cache backup, preserving for manual recovery",
						"path", ws.Path, "cache_backup", cacheBackupDir, "error", applyErr)
					return gcFailed, false
				}
				_ = os.RemoveAll(cacheBackupDir)
			}
		}
		if _, statErr := os.Stat(diffFile); statErr == nil {
			if applyErr := s.gcRestoreDiff(ctx, ws.Path, diffFile); applyErr != nil {
				s.logger.Error("gc: failed to apply orphaned diff, preserving for manual recovery",
					"path", ws.Path, "workspace", wsLabel, "diff_file", diffFile, "error", applyErr)
				return gcFailed, false
			}
			_ = os.Remove(diffFile)
		}
		if _, statErr := os.Stat(untrackedDir); statErr == nil {
			if applyErr := s.gcRestoreUntracked(ws.Path, untrackedDir); applyErr != nil {
				s.logger.Error("gc: failed to restore orphaned untracked backup, preserving for manual recovery",
					"path", ws.Path, "workspace", wsLabel, "untracked_dir", untrackedDir, "error", applyErr)
				return gcFailed, false
			}
			_ = os.RemoveAll(untrackedDir)
		}
		_ = os.Remove(swapMarker)
		s.logger.Info("gc: recovered orphaned artifacts from an interrupted prior GC", "path", ws.Path, "workspace", wsLabel)
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

	// step 0d (ledger only): preserve .sageox/cache/ (gitignored, contains codedb indexes)
	// cache must survive reclones — abort GC if preservation fails
	hasCache := false
	if isLedger {
		if err := gcPreserveCache(ws.Path, cacheBackupDir); err != nil {
			s.logger.Warn("gc: skipping reclone, cannot preserve cache",
				"path", ws.Path, "error", err)
			return gcFailed, false
		} else if _, err := os.Stat(cacheBackupDir); err == nil {
			hasCache = true
		}
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
		if err := ledger.CloneWithSparseCheckout(newPath, cloneURL); err != nil {
			s.logger.Error("gc: reclone failed, keeping old", "workspace", wsLabel, "error", err)
			_ = os.RemoveAll(newPath)
			return gcFailed, false
		}
		// Install the credential helper into the freshly cloned ledger so
		// subsequent pulls/pushes resolve auth via the helper.
		if _, err := gitserver.MigrateLedgerCredentials(newPath, gitserver.DefaultHelperCommand()); err != nil {
			s.logger.Warn("gc: failed to install credential helper after reclone",
				"path", newPath, "error", err)
		}
	} else {
		mCfg, err = s.twoPhaseClone(ctx, cloneURL, newPath, nil)
		if err != nil {
			s.logger.Error("gc: reclone failed, keeping old", "workspace", wsLabel, "error", err)
			_ = os.RemoveAll(newPath)
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
		_ = os.RemoveAll(newPath)
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

	// Close any whisper SQLite store rooted under ws.Path BEFORE the rename.
	// SQLite keeps -shm/-wal files mmap'd; on POSIX, os.Rename + os.RemoveAll
	// unlinks the underlying inodes but the kernel pins them as long as the
	// mmap region is alive, leaking FDs across every reclone. Ledger reopens
	// via reopenWhisperStoreAfterGC; team stores reopen lazily on next sync.
	if s.whisperRegistry != nil {
		if isLedger {
			s.whisperRegistry.CloseLedgerStore()
		} else if ws.TeamID != "" {
			s.whisperRegistry.CloseTeamStore(ws.TeamID)
		}
	}

	// step 3: atomic swap — the GC lock (held since entry) serializes against
	// concurrent GCs on this same workspace. For ledger repos, also acquire
	// ledgerMu to prevent concurrent pull/push conflicts during the swap —
	// but ledgerMu is an in-process sync.Mutex, and the CLI's direct
	// ledger writes (cmd/ox/session_upload.go's commitAndPushLedger) run
	// as a SEPARATE OS process per .claude/rules/daemon-git.md's own
	// architecture, so it cannot serialize against them. A CLI git
	// operation whose open file descriptors are still resolving through
	// ws.Path at the exact moment of the rename below can end up writing
	// into what becomes oldPath, which is then removed a few lines down —
	// silently losing that write. swapLockPath is a filesystem-based,
	// genuinely cross-process signal scoped tightly to just this
	// rename+cleanup window (not the whole, potentially multi-minute GC)
	// so the CLI can check it without risking a long wait; see
	// commitAndPushLedger's corresponding check.
	swapLockPath := ws.Path + ".gc-swap-lock"
	if isLedger {
		if err := os.WriteFile(swapLockPath, nil, 0o600); err != nil {
			s.logger.Warn("gc: failed to write swap lock, CLI writes racing this swap would be undetected",
				"path", ws.Path, "error", err)
		}
		s.ledgerMu.Lock()
	}
	if s.gcSwapWindowTestHook != nil {
		s.gcSwapWindowTestHook()
	}

	if _, err := os.Stat(oldPath); err == nil {
		_ = os.RemoveAll(oldPath)
	}

	if err := os.Rename(ws.Path, oldPath); err != nil {
		if isLedger {
			s.ledgerMu.Unlock()
			_ = os.Remove(swapLockPath)
		}
		s.logger.Error("gc: failed to move old repo aside", "path", ws.Path, "error", err)
		_ = os.RemoveAll(newPath)
		return gcFailed, false
	}

	if err := os.Rename(newPath, ws.Path); err != nil {
		s.logger.Error("gc: failed to move new repo into place, restoring old", "error", err)
		if restoreErr := os.Rename(oldPath, ws.Path); restoreErr != nil {
			s.logger.Error("gc: CRITICAL failed to restore old repo", "error", restoreErr)
		}
		if isLedger {
			s.ledgerMu.Unlock()
			_ = os.Remove(swapLockPath)
		}
		return gcFailed, false
	}

	if isLedger {
		s.ledgerMu.Unlock()
	}

	// The swap just succeeded: ws.Path now holds the fresh clone, and
	// diffFile/untrackedDir/cacheBackupDir (if any) are the ONLY surviving
	// copy of whatever phase 0 captured — the original pre-GC tree at
	// oldPath is about to be removed below. If the process dies anywhere
	// between here and the end of phase 2, the next invocation must NOT
	// treat those backup files as ordinary leftovers to discard; this
	// marker is how it tells the difference (see the orphan-recovery
	// check at the top of this function). Best-effort: if the marker
	// itself can't be written, proceed anyway — restore still runs
	// immediately below in the common (non-crash) case either way.
	if err := os.WriteFile(swapMarker, nil, 0o600); err != nil {
		s.logger.Warn("gc: failed to write swap marker, crash-recovery for this cycle would be degraded",
			"path", ws.Path, "error", err)
	}

	// step 4: cleanup old
	if err := os.RemoveAll(oldPath); err != nil {
		s.logger.Warn("gc: failed to remove old clone", "path", oldPath, "error", err)
	}

	// The risky window for a concurrent CLI write (see the comment where
	// swapLockPath was written, above) ends once oldPath is gone — release
	// the cross-process signal now rather than holding it through the
	// (potentially much longer) phase 2 restore below.
	if isLedger {
		_ = os.Remove(swapLockPath)
	}

	// --- phase 1.5 (ledger only): restore cache ---
	if isLedger && hasCache {
		if err := gcRestoreCache(cacheBackupDir, ws.Path); err != nil {
			// keep backup so manual recovery is possible
			s.logger.Error("gc: failed to restore cache, backup retained",
				"path", ws.Path, "backup", cacheBackupDir, "error", err)
		} else {
			_ = os.RemoveAll(cacheBackupDir)
		}
	}

	// --- phase 2: restore local state ---

	diffApplied := true
	if hasDiff {
		if err := s.gcRestoreDiff(ctx, ws.Path, diffFile); err != nil {
			diffApplied = false
			s.logger.Warn("gc: reclone succeeded but failed to restore uncommitted changes",
				"path", ws.Path, "workspace", wsLabel, "error", err,
				"recovery_file", diffFile)
		}
	}

	untrackedRestored := true
	if hasUntracked {
		if err := s.gcRestoreUntracked(ws.Path, untrackedDir); err != nil {
			untrackedRestored = false
			s.logger.Warn("gc: reclone succeeded but failed to restore some untracked files, backup retained",
				"path", ws.Path, "workspace", wsLabel, "error", err,
				"recovery_dir", untrackedDir)
		}
	}

	// clean up preservation artifacts — but only the ones that actually
	// landed. gcRestoreUntracked is a best-effort walk that can fail
	// partway through (one locked/colliding file) and still return an
	// error; deleting untrackedDir unconditionally here would silently
	// discard the only surviving copy of whatever didn't make it, which is
	// exactly the data-loss GC's own doc comment says this mechanism must
	// never risk. Mirrors the diffFile gate immediately above.
	if hasDiff && diffApplied {
		_ = os.Remove(diffFile)
	}
	if hasUntracked && untrackedRestored {
		_ = os.RemoveAll(untrackedDir)
	}

	// phase 2 has now been fully attempted (successfully or with a
	// preserved-for-manual-recovery fallback above) — this invocation is
	// no longer in the "crashed mid-restore" state the marker exists to
	// flag, so clear it regardless of diffApplied/untrackedRestored.
	// Leaving it set here would make the next GC re-attempt an
	// auto-recovery apply on top of whatever a human already did with the
	// preserved files, which is the wrong kind of "recovery".
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

// gcPushUnpushedCommits pushes any local commits not yet on the remote.
// Returns nil if there are no unpushed commits or if push succeeds.
// Returns an error if unpushed commits exist and push fails.
//
// NOTE: This intentionally does NOT use gitutil.PushWithRetry. The GC push
// injects credentials directly into the remote URL and uses "push origin HEAD",
// which is fundamentally different from PushWithRetry's "push --quiet" approach.
// The credential injection + URL restore pattern is GC-specific (reclone context).
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

	pushArgs := []string{
		"-c", "credential.helper=",
		"-c", "credential.helper=" + helperCmd,
		"push", "origin", "HEAD", "--quiet",
	}
	if _, pushErr := gitutil.RunGit(ctx, ws.Path, pushArgs...); pushErr != nil {
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
			if _, retryErr := gitutil.RunGit(ctx, ws.Path, retryArgs...); retryErr != nil {
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

	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "diff", "--binary", baseRef)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, fmt.Errorf("git diff pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("git diff start: %w", err)
	}

	outFile, err := os.OpenFile(diffFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		_ = cmd.Wait()
		return false, fmt.Errorf("create diff file: %w", err)
	}

	written, copyErr := io.Copy(outFile, io.LimitReader(stdout, maxGCDiffSize+1))
	outFile.Close()

	if waitErr := cmd.Wait(); waitErr != nil {
		_ = os.Remove(diffFile)
		return false, fmt.Errorf("git diff HEAD: %w", waitErr)
	}

	if copyErr != nil {
		_ = os.Remove(diffFile)
		return false, fmt.Errorf("write diff file: %w", copyErr)
	}

	if written == 0 {
		_ = os.Remove(diffFile)
		return false, nil
	}

	if written > maxGCDiffSize {
		_ = os.Remove(diffFile)
		s.logger.Warn("gc: diff too large, cannot preserve changes", "path", repoPath, "size", written, "max", maxGCDiffSize)
		return false, fmt.Errorf("diff too large (%d bytes, max %d) — cannot safely preserve uncommitted changes", written, maxGCDiffSize)
	}

	s.logger.Info("gc: captured uncommitted changes", "path", repoPath, "diff_size", written)
	return true, nil
}

// gcCaptureUntracked copies untracked files (excluding gitignored) to a temp directory.
// Returns (hasFiles, error).
func (s *SyncScheduler) gcCaptureUntracked(ctx context.Context, repoPath, destDir string) (bool, error) {
	output, err := gitutil.RunGit(ctx, repoPath, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return false, fmt.Errorf("git ls-files: %w", err)
	}

	output = strings.TrimSpace(output)
	if output == "" {
		return false, nil
	}

	files := strings.Split(output, "\n")
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return false, fmt.Errorf("create untracked dir: %w", err)
	}

	copied := 0
	for _, relPath := range files {
		relPath = strings.TrimSpace(relPath)
		if relPath == "" {
			continue
		}

		// guard against path traversal and symlink escape
		absResolved, err := filepath.Abs(filepath.Join(repoPath, relPath))
		if err != nil {
			s.logger.Warn("gc: skipping untracked file, cannot resolve path", "path", relPath, "error", err)
			continue
		}
		absRepo, _ := filepath.Abs(repoPath)
		// use filepath.Rel for separator-aware containment (prevents /tmp/repo-evil matching /tmp/repo)
		rel, err := filepath.Rel(absRepo, absResolved)
		if err != nil || strings.HasPrefix(rel, "..") {
			s.logger.Warn("gc: skipping untracked file outside repo boundary", "path", relPath)
			continue
		}

		srcPath := filepath.Join(repoPath, relPath)
		dstPath := filepath.Join(destDir, relPath)

		if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
			s.logger.Warn("gc: failed to create dir for untracked file", "path", relPath, "error", err)
			continue
		}

		if err := copyFile(srcPath, dstPath); err != nil {
			s.logger.Warn("gc: failed to copy untracked file", "path", relPath, "error", err)
			continue
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

// gcPreserveCache copies the .sageox/cache/ directory from the old clone to a temp location.
// Cache is gitignored and contains codedb indexes that are expensive to rebuild.
// Returns nil if no cache exists (nothing to preserve).
func gcPreserveCache(srcRepo, cacheBackupDir string) error {
	cacheDir := filepath.Join(srcRepo, ".sageox", "cache")
	if _, err := os.Stat(cacheDir); err != nil {
		return nil // no cache to preserve
	}
	return copyDir(cacheDir, cacheBackupDir)
}

// gcRestoreCache copies preserved cache back into the new clone's .sageox/cache/ directory.
func gcRestoreCache(cacheBackupDir, dstRepo string) error {
	if _, err := os.Stat(cacheBackupDir); err != nil {
		return nil // no backup to restore
	}
	dstCache := filepath.Join(dstRepo, ".sageox", "cache")
	if err := os.MkdirAll(filepath.Dir(dstCache), 0755); err != nil {
		return fmt.Errorf("create .sageox dir: %w", err)
	}
	return copyDir(cacheBackupDir, dstCache)
}

// reopenWhisperStoreAfterGC reopens the ledger whisper store after a
// successful GC reclone. The rename-swap invalidates the old sql.DB handle
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

// copyFile copies src to dst, preserving file mode.
// Uses Lstat to avoid following symlinks — a symlink is recreated as a symlink,
// never dereferenced. This prevents a rogue symlink (e.g., pointing to /etc/shadow)
// from exfiltrating host files into the repo during GC backup.
func copyFile(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}

	// recreate symlinks as symlinks, never dereference
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return fmt.Errorf("readlink %s: %w", src, err)
		}
		return os.Symlink(target, dst)
	}

	// skip non-regular files (devices, sockets, etc.)
	if !info.Mode().IsRegular() {
		return nil
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// copyDir recursively copies a directory tree from src to dst.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, relPath)

		if d.IsDir() {
			return os.MkdirAll(dstPath, 0755)
		}

		return copyFile(path, dstPath)
	})
}
