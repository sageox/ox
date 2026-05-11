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

			if intervalExceeded || fullClone {
				reason := "interval exceeded"
				if fullClone {
					reason = "full clone upgrade"
				}
				s.logger.Info("gc: ledger due for reclone", "id", l.ID,
					"reason", reason, "interval_days", intervalDays, "last_gc", l.LastGCTime)

				result := s.runBlueGreenGC(ctx, *l)
				if s.issues != nil {
					switch result {
					case gcSkippedDirty:
						s.issues.SetIssue(DaemonIssue{
							Type:     IssueTypeDirtyWorkspace,
							Severity: SeverityWarning,
							Repo:     "ledger",
							Summary:  "local changes could not be preserved for GC reclone (push failed or changes could not be captured)",
						})
					case gcSuccess:
						s.issues.ClearIssue(IssueTypeDirtyWorkspace, "ledger")
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

// TriggerGC forces a GC reclone of all eligible team contexts, bypassing the interval check.
// Returns immediately if GC is already in progress. Runs synchronously.
func (s *SyncScheduler) TriggerGC(ctx context.Context) *TriggerGCResponse {
	if !atomic.CompareAndSwapInt32(&s.gcInProgress, 0, 1) {
		return &TriggerGCResponse{Skipped: 1}
	}
	defer atomic.StoreInt32(&s.gcInProgress, 0)

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
		return gcSkippedLocked
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

	// clean up leftover artifacts from a previous failed GC (safe under the lock)
	leftovers := []string{newPath, diffFile, untrackedDir, cacheBackupDir}
	for _, leftover := range leftovers {
		if _, err := os.Stat(leftover); err == nil {
			s.logger.Info("gc: cleaning up leftover artifact", "path", leftover)
			if err := os.RemoveAll(leftover); err != nil {
				s.logger.Error("gc: failed to remove leftover artifact", "path", leftover, "error", err)
				return gcFailed
			}
		}
	}

	// --- phase 0: preserve local state ---

	// step 0a: push unpushed commits so they survive reclone
	if err := s.gcPushUnpushedCommits(ctx, ws); err != nil {
		s.logger.Warn("gc: skipping reclone, cannot push unpushed commits",
			"path", ws.Path, "workspace", wsLabel, "error", err)
		return gcSkippedDirty
	}

	// step 0b: capture uncommitted tracked changes (staged + unstaged)
	hasDiff, err := s.gcCaptureDiff(ctx, ws.Path, diffFile)
	if err != nil {
		s.logger.Warn("gc: skipping reclone, cannot capture uncommitted changes",
			"path", ws.Path, "workspace", wsLabel, "error", err)
		return gcSkippedDirty
	}

	// step 0c: capture untracked files
	hasUntracked, err := s.gcCaptureUntracked(ctx, ws.Path, untrackedDir)
	if err != nil {
		s.logger.Warn("gc: skipping reclone, cannot capture untracked files",
			"path", ws.Path, "workspace", wsLabel, "error", err)
		return gcSkippedDirty
	}

	// step 0d (ledger only): preserve .sageox/cache/ (gitignored, contains codedb indexes)
	// cache must survive reclones — abort GC if preservation fails
	hasCache := false
	if isLedger {
		if err := gcPreserveCache(ws.Path, cacheBackupDir); err != nil {
			s.logger.Warn("gc: skipping reclone, cannot preserve cache",
				"path", ws.Path, "error", err)
			return gcFailed
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
			return gcFailed
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
			return gcFailed
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
		return gcFailed
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
	// ledgerMu to prevent concurrent pull/push conflicts during the swap.
	if isLedger {
		s.ledgerMu.Lock()
	}

	if _, err := os.Stat(oldPath); err == nil {
		_ = os.RemoveAll(oldPath)
	}

	if err := os.Rename(ws.Path, oldPath); err != nil {
		if isLedger {
			s.ledgerMu.Unlock()
		}
		s.logger.Error("gc: failed to move old repo aside", "path", ws.Path, "error", err)
		_ = os.RemoveAll(newPath)
		return gcFailed
	}

	if err := os.Rename(newPath, ws.Path); err != nil {
		s.logger.Error("gc: failed to move new repo into place, restoring old", "error", err)
		if restoreErr := os.Rename(oldPath, ws.Path); restoreErr != nil {
			s.logger.Error("gc: CRITICAL failed to restore old repo", "error", restoreErr)
		}
		if isLedger {
			s.ledgerMu.Unlock()
		}
		return gcFailed
	}

	if isLedger {
		s.ledgerMu.Unlock()
	}

	// step 4: cleanup old
	if err := os.RemoveAll(oldPath); err != nil {
		s.logger.Warn("gc: failed to remove old clone", "path", oldPath, "error", err)
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

	if hasUntracked {
		if err := s.gcRestoreUntracked(ws.Path, untrackedDir); err != nil {
			s.logger.Warn("gc: reclone succeeded but failed to restore some untracked files",
				"path", ws.Path, "workspace", wsLabel, "error", err)
		}
	}

	// clean up preservation artifacts (keep diff file if apply failed for manual recovery)
	if hasDiff && diffApplied {
		_ = os.Remove(diffFile)
	}
	if hasUntracked {
		_ = os.RemoveAll(untrackedDir)
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
	return gcSuccess
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
	if _, err := gitutil.RunGit(ctx, ws.Path, pushArgs...); err != nil {
		return fmt.Errorf("push failed: %w", err)
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

// gcCaptureDiff captures all uncommitted tracked changes (staged + unstaged)
// as a binary-safe patch file. Returns (hasDiff, error).
// Streams diff directly to disk (not into memory) to avoid OOM on large diffs.
// Diffs exceeding maxGCDiffSize are skipped with a warning.
//
// If the working tree is empty (only .git remains), treat this as corruption
// rather than an intentional mass-delete and skip diff capture so the reclone
// restores the committed content from remote.
func (s *SyncScheduler) gcCaptureDiff(ctx context.Context, repoPath, diffFile string) (bool, error) {
	if empty, err := workingTreeEmpty(repoPath); err == nil && empty {
		s.logger.Info("gc: working tree empty, skipping diff capture (will restore from remote)", "path", repoPath)
		return false, nil
	}

	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "diff", "--binary", "HEAD")
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

	// fall back to --reject (applies what it can, creates .rej for conflicts)
	if _, err := gitutil.RunGit(ctx, repoPath, "apply", "--reject", diffFile); err != nil {
		return fmt.Errorf("git apply failed (diff preserved at %s): %w", diffFile, err)
	}

	s.logger.Warn("gc: restored uncommitted changes with conflicts (.rej files created)", "path", repoPath)
	return nil
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
