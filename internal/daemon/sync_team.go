package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/manifest"
	"github.com/sageox/ox/internal/paths"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// pullTeamContexts syncs all team context repos from workspace registry (used by scheduler).
// For repos that exist locally: pulls latest changes.
// For repos that don't exist: spawns background clone (non-blocking).
//
// Auto-clone rationale: Team contexts are designed to be shared across repos.
// When the API returns a team context, the user has already consented (by installing
// ox and initializing a repo). Cloning happens in background goroutines to avoid
// blocking the sync scheduler event loop.
// Also performs anti-entropy: checks for missing workspaces and triggers clones.
func (s *SyncScheduler) pullTeamContexts(ctx context.Context) {
	// anti-entropy: ensure missing workspaces get cloned
	s.triggerMissingClones()

	// bound background sync to 60s so a DNS/network hang doesn't block
	// the scheduler for minutes (the caller ctx has no deadline)
	teamCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	s.doTeamSync(teamCtx, nil, false)
}

// TeamSync performs an on-demand sync of all team contexts with progress updates.
func (s *SyncScheduler) TeamSync(progress *ProgressWriter) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s.doTeamSync(ctx, progress, true)
	return nil
}

// doTeamSync syncs all team context repos with optional progress updates.
// Uses the WorkspaceRegistry to avoid repeated config file reads.
//
// Auto-clone behavior: If a team context doesn't exist locally but has a clone URL,
// spawns a background goroutine to clone it. This doesn't block the sync loop.
// Note: Ledger auto-clone is handled separately in doPull() on the ledger sync ticker.
func (s *SyncScheduler) doTeamSync(ctx context.Context, progress *ProgressWriter, forceSync bool) {
	// refresh credentials if expired or near expiry
	s.refreshCredentialsIfNeeded()

	// discover new teams independently of token refresh — ensures new teams
	// are found even when the credential token is still fresh
	s.discoverTeams()

	if s.config.ProjectRoot == "" {
		if progress != nil {
			_ = progress.WriteStage("skipped", "No project root configured")
		}
		return
	}

	// reload workspace state from config (uses cache if fresh)
	if err := s.workspaceRegistry.LoadFromConfig(); err != nil {
		s.logger.Warn("failed to load workspace registry for team context sync", "error", err)
		if progress != nil {
			_ = progress.WriteMessage(fmt.Sprintf("Failed to load config: %v", err))
		}
		return
	}

	// get team contexts from registry
	teamContexts := s.workspaceRegistry.GetTeamContexts()
	if len(teamContexts) == 0 {
		s.logger.Debug("no team contexts configured")
		if progress != nil {
			_ = progress.WriteStage("skipped", "No team contexts configured")
		}
		return
	}

	s.logger.Debug("syncing team contexts", "count", len(teamContexts))
	if progress != nil {
		_ = progress.WriteStage("starting", fmt.Sprintf("Syncing %d team context(s)...", len(teamContexts)))
	}

	var skippedCount, cloningCount int

	// partition: repos ready to sync vs skipped/cloning
	type syncTarget struct {
		ws WorkspaceState
	}
	var targets []syncTarget

	for _, ws := range teamContexts {
		if ws.Path == "" {
			s.workspaceRegistry.SetWorkspaceError(ws.ID, "no path configured")
			skippedCount++
			continue
		}

		if !ws.Exists {
			if ws.CloneURL != "" {
				if !s.workspaceRegistry.ShouldRetryClone(ws.ID) {
					attempts, nextRetry := s.workspaceRegistry.GetCloneRetryInfo(ws.ID)
					s.logger.Debug("team context clone in backoff, skipping",
						"team", ws.TeamName, "attempts", attempts, "next_retry", nextRetry)
					skippedCount++
					continue
				}

				s.logger.Info("team context not cloned, starting background clone",
					"team", ws.TeamName, "path", ws.Path)
				if progress != nil {
					_ = progress.WriteStage("cloning", fmt.Sprintf("Cloning team %s in background...", ws.TeamName))
				}
				s.cloneWg.Add(1)
				go s.cloneInBackground(ws.CloneURL, ws.Path, "team-context", ws.ID)
				cloningCount++
			} else {
				s.workspaceRegistry.SetWorkspaceError(ws.ID, "path does not exist and no clone URL available")
				s.logger.Debug("team context path not found and no clone URL", "team", ws.TeamName, "path", ws.Path)
				if progress != nil {
					_ = progress.WriteStage("skipped", fmt.Sprintf("Team %s: not cloned, no URL", ws.TeamName))
				}
				skippedCount++
			}
			continue
		}

		if !s.shouldSyncOrBypass(ws.ID, forceSync) {
			skippedCount++
			continue
		}

		targets = append(targets, syncTarget{ws: ws})
	}

	// sync eligible repos in parallel — each operates on its own repo path,
	// and the network I/O (ls-remote, fetch, pull) dominates wall time
	type syncResult struct {
		ws         WorkspaceState
		err        error
		duration   time.Duration
		prePullSHA string
	}
	results := make([]syncResult, len(targets))
	var wg sync.WaitGroup

	for i, t := range targets {
		s.workspaceRegistry.SetSyncInProgress(t.ws.ID, true)
		if progress != nil {
			_ = progress.WriteStage("syncing", fmt.Sprintf("Syncing team: %s", t.ws.TeamName))
		}

		wg.Add(1)
		go func(idx int, ws WorkspaceState) {
			defer wg.Done()
			preSHA := s.captureHEAD(ws.Path)
			start := time.Now()
			pullErr := s.pullTeamContext(ctx, ws.Path)
			results[idx] = syncResult{ws: ws, err: pullErr, duration: time.Since(start), prePullSHA: preSHA}
		}(i, t.ws)
	}
	wg.Wait()

	// process results sequentially (registry updates, progress messages)
	var syncedCount int
	for _, r := range results {
		s.workspaceRegistry.SetSyncInProgress(r.ws.ID, false)

		if r.err != nil {
			s.workspaceRegistry.SetWorkspaceError(r.ws.ID, r.err.Error())
			s.workspaceRegistry.RecordSyncFailure(r.ws.ID)
			s.recordSyncStateFailure(r.ws.Path)
			s.logger.Debug("team context pull failed", "team", r.ws.TeamName, "error", r.err)
			s.metrics.RecordTeamSyncError()
			if progress != nil {
				_ = progress.WriteStage("error", fmt.Sprintf("Team %s: %v", r.ws.TeamName, r.err))
			}
			continue
		}

		s.workspaceRegistry.ClearWorkspaceError(r.ws.ID)
		s.workspaceRegistry.ClearSyncFailures(r.ws.ID)
		if s.issues != nil {
			s.issues.ClearIssue(IssueTypeSyncBackoff, r.ws.ID)
		}

		mCfg := s.applySparseCheckout(ctx, r.ws.Path)
		if mCfg != nil {
			if mCfg.SyncIntervalMin > 0 {
				s.workspaceRegistry.SetSyncIntervalMin(r.ws.Path, mCfg.SyncIntervalMin)
			}
			if mCfg.GCIntervalDays > 0 {
				s.workspaceRegistry.SetGCInterval(r.ws.Path, mCfg.GCIntervalDays)
			}
		}

		if err := s.workspaceRegistry.UpdateConfigLastSync(r.ws.ID); err != nil {
			s.logger.Warn("failed to update config last sync", "team", r.ws.TeamName, "error", err)
		}
		s.recordSyncState(ctx, r.ws.Path)

		// open team whisper store (once per team) and relay murmurs after successful sync
		if s.whisperRegistry != nil && s.murmurRelay != nil {
			if !s.whisperRegistry.HasTeamStore(r.ws.TeamID) {
				ep := s.workspaceRegistry.GetEndpoint()
				teamWhisperDir := paths.TeamWhisperDBDir(r.ws.TeamID, ep)
				if teamWhisperDir != "" {
					dbPath := filepath.Join(teamWhisperDir, "whisper.db")
					if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err == nil {
						teamStore, err := whisperstore.Open(dbPath)
						if err != nil {
							s.logger.Warn("failed to open team whisper store", "team", r.ws.TeamName, "error", err)
						} else {
							s.whisperRegistry.AddTeamStore(r.ws.TeamID, teamStore)
						}
					}
				}
			}
			// always relay murmurs (even if store was already registered)
			s.murmurRelay.RelayFromPath(r.ws.Path, "team")
		}

		syncedCount++

		s.recordSync("team_context", r.ws.ID, r.duration, 0)
		s.metrics.RecordTeamSync()
		s.recordActivity()

		// emit trigger whispers for team context file changes
		// Primary team only for now — avoids noise from secondary team contexts.
		if s.whisperRegistry != nil && r.ws.TeamID == s.workspaceRegistry.ProjectTeamID() {
			if changedFiles := s.detectChangedFiles(r.ws.Path, r.prePullSHA); len(changedFiles) > 0 {
				s.logger.Debug("team context changes detected",
					"team", r.ws.TeamName, "count", len(changedFiles))
				for _, cf := range changedFiles {
					id, _ := uuid.NewV7()
					s.whisperRegistry.Add("ledger", whisperstore.WhisperEntry{
						ID:         id.String(),
						Scope:      "ledger",
						Type:       whisperstore.WhisperTrigger,
						Source:     "team-context",
						Topic:      "team-context",
						Content:    fmt.Sprintf("Team context updated: %s", cf),
						Importance: whisperstore.ImportanceNormal,
						CreatedAt:  time.Now(),
					})
				}
			}
		}
		s.logger.Debug("team context synced", "team", r.ws.TeamName, "duration", r.duration)
		if progress != nil {
			_ = progress.WriteStage("synced", fmt.Sprintf("Team %s synced", r.ws.TeamName))
		}
	}

	if progress != nil {
		msg := fmt.Sprintf("Synced %d, skipped %d team context(s)", syncedCount, skippedCount)
		if cloningCount > 0 {
			msg += fmt.Sprintf(", cloning %d in background", cloningCount)
		}
		_ = progress.WriteStage("complete", msg)
	}
}

// pullTeamContext performs a git pull on a single team context repo.
// Returns nil if skipped due to recent fetch (by another daemon).
//
// Multi-daemon deduplication: Users often work on multiple repos that share
// the same team context (e.g., 5-6 project repos all pointing to one team
// context directory). Each repo has its own daemon, so without coordination,
// they'd all try to git pull the same team context simultaneously.
//
// We solve this by checking .git/FETCH_HEAD mtime before fetching. Git updates
// this file on every fetch, so if it was recently modified (by any process),
// we skip the pull. This naturally deduplicates without locks - whichever
// daemon fetches first "wins" and others skip for that interval.
//
// Change Detection: After a successful pull, this function compares file states
// before and after to detect changes in key team context files (distilled discussions,
// agent definitions, etc.). When changes are detected, a notification marker is written
// so that CLI commands can "whisper" updates to agents.
func (s *SyncScheduler) pullTeamContext(ctx context.Context, path string) error {
	// skip if repo stuck in broken rebase state
	if gitutil.IsRebaseInProgress(path) {
		s.logger.Debug("repo in rebase state, skipping pull", "path", path)
		return nil
	}

	// check for stale lock files from crashed git processes
	gitDir := filepath.Join(path, ".git")
	if locks := gitutil.HasLockFiles(gitDir); len(locks) > 0 {
		repoName := filepath.Base(path)
		s.logger.Warn("git lock files detected, skipping pull",
			"path", path,
			"locks", strings.Join(locks, ", "))
		if s.issues != nil {
			s.issues.SetIssue(DaemonIssue{
				Type:     IssueTypeGitLock,
				Severity: SeverityWarning,
				Repo:     repoName,
				Summary: fmt.Sprintf("Stale lock files blocking sync: %s. If no git commands are running, remove with: rm %s/{%s}",
					strings.Join(locks, ", "),
					gitDir,
					strings.Join(locks, ",")),
			})
		}
		return nil
	}
	// clear lock issue if previously set but now resolved
	if s.issues != nil {
		repoName := filepath.Base(path)
		s.issues.ClearIssue(IssueTypeGitLock, repoName)
	}

	// ls-remote SHA check — skip if remote HEAD matches local (nothing new to pull).
	// Cheaper than git fetch: only hits /info/refs, no upload-pack negotiation.
	if s.remoteRefCheck(ctx, path) {
		return nil
	}

	// FETCH_HEAD mtime dedup (secondary: multi-daemon dedup on shared team context paths).
	// Kept as fallback for when ls-remote can't run (credential issues, etc).
	if age, ok := gitutil.FetchHeadAge(path); ok {
		// use manifest-derived interval if available, otherwise fall back to default
		minFetchAge := minTeamContextFetchAge
		if intervalMin := s.workspaceRegistry.GetSyncIntervalMin(path); intervalMin > 0 {
			minFetchAge = time.Duration(intervalMin) * time.Minute
		}
		threshold := max(s.config.TeamContextSyncInterval/2, minFetchAge)
		if age < threshold {
			s.logger.Debug("team context recently fetched, skipping", "path", path, "age", age)
			return nil
		}
	}

	// refresh remote URL if credentials changed (e.g., user switch via ox login)
	teamEndpoint := endpoint.GetForProject(s.config.ProjectRoot)
	if err := gitserver.RefreshRemoteCredentials(path, teamEndpoint); err != nil {
		s.logger.Warn("team context remote credential refresh failed", "path", path, "error", err)
	}

	// git fetch (capture stderr for diagnosable error messages)
	tcFetchArgs := append([]string{"-C", path}, gitHTTPTimeoutFlags()...)
	tcFetchArgs = append(tcFetchArgs, "fetch", "--quiet")
	fetchCmd := exec.CommandContext(ctx, "git", tcFetchArgs...)
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		detail := gitutil.SanitizeOutput(strings.TrimSpace(string(output)))
		if detail != "" {
			return fmt.Errorf("fetch failed: %s (%w)", detail, err)
		}
		return fmt.Errorf("fetch failed: %w", err)
	}

	// track FETCH_HEAD mtime for team context repos
	if info, err := os.Stat(filepath.Join(path, ".git", "FETCH_HEAD")); err == nil {
		s.recordRemoteChange(path, info.ModTime())
	}

	// git pull --rebase --autostash (capture stderr for diagnosable error messages)
	// --autostash: team context repos are collaborative workspaces — users may have
	// uncommitted local edits (docs/, data/) that must not block background sync
	tcPullArgs := append([]string{"-C", path}, gitHTTPTimeoutFlags()...)
	tcPullArgs = append(tcPullArgs, "pull", "--rebase", "--autostash", "--quiet")
	pullCmd := exec.CommandContext(ctx, "git", tcPullArgs...)
	if output, err := pullCmd.CombinedOutput(); err != nil {
		detail := gitutil.SanitizeOutput(strings.TrimSpace(string(output)))

		// check if it's a merge conflict
		statusCmd := exec.CommandContext(ctx, "git", "-C", path, "status", "--porcelain")
		if statusOutput, _ := statusCmd.Output(); strings.Contains(string(statusOutput), "UU") {
			s.metrics.RecordConflict()

			// report merge conflict issue — daemon does not write; next pull will skip via rebase-state check
			if s.issues != nil {
				repoName := filepath.Base(path)
				s.issues.SetIssue(DaemonIssue{
					Type:            IssueTypeMergeConflict,
					Severity:        SeverityError,
					Repo:            repoName,
					Summary:         fmt.Sprintf("Team context %s has merge conflicts. Run 'ox doctor --fix' to re-clone.", repoName),
					RequiresConfirm: true, // merge resolution needs human approval
				})
			}
		}
		if detail != "" {
			return fmt.Errorf("pull failed: %s (%w)", detail, err)
		}
		return fmt.Errorf("pull failed: %w", err)
	}

	// sync succeeded - clear any previous merge conflict issue for this repo
	if s.issues != nil {
		repoName := filepath.Base(path)
		s.issues.ClearIssue(IssueTypeMergeConflict, repoName)
	}

	return nil
}

// applySparseCheckout reads the manifest from a team context repo and applies
// sparse-checkout rules. Returns the parsed ManifestConfig so callers can use
// SyncIntervalMin. Errors are logged as warnings but never fatal.
func (s *SyncScheduler) applySparseCheckout(ctx context.Context, tcPath string) *manifest.ManifestConfig {
	manifestPath := filepath.Join(tcPath, ".sageox", "sync.manifest")
	cfg := manifest.ParseFile(manifestPath)

	sparsePaths := manifest.ComputeSparseSet(cfg)
	if len(sparsePaths) == 0 {
		s.logger.Debug("manifest: no sparse paths computed, skipping sparse-checkout", "path", tcPath)
		return cfg
	}

	// use --no-cone mode to support both file and directory patterns
	// (cone mode only supports directories, but fallback includes files like AGENTS.md)
	args := append([]string{"sparse-checkout", "set", "--no-cone"}, sparsePaths...)
	if _, err := gitutil.RunGit(ctx, tcPath, args...); err != nil {
		s.logger.Warn("sparse-checkout set failed, continuing without sparse checkout",
			"path", tcPath, "error", err)
		return cfg
	}

	s.logger.Debug("sparse-checkout applied",
		"path", tcPath, "paths", sparsePaths, "sync_interval_min", cfg.SyncIntervalMin)
	return cfg
}

// twoPhaseClone delegates to the shared gitserver.TwoPhaseClone implementation,
// adding progress reporting and validation on top.
func (s *SyncScheduler) twoPhaseClone(ctx context.Context, cloneURL, repoPath string, progress *ProgressWriter) (*manifest.ManifestConfig, error) {
	if progress != nil {
		_ = progress.WriteStage("materializing", "Reading manifest and materializing files...")
	}

	result, err := gitserver.TwoPhaseClone(ctx, cloneURL, repoPath)
	if err != nil {
		return nil, err
	}

	gitserver.ValidateTeamContextClone(repoPath, result.ManifestConfig)

	s.logger.Info("two-phase clone complete", "path", repoPath, "sparse_paths", result.SparsePaths)
	return result.ManifestConfig, nil
}

// TeamContextStatus returns the current team context sync status.
// Uses the WorkspaceRegistry for a unified view of workspace state.
func (s *SyncScheduler) TeamContextStatus() []TeamContextSyncStatus {
	return s.workspaceRegistry.GetTeamContextStatus()
}
