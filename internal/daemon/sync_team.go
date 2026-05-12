package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/gitserver"
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
				if s.addClone() {
					go s.cloneInBackground(ws.CloneURL, ws.Path, "team-context", ws.ID) //nolint:gosec // G118 - intentionally uses background context; goroutine outlives request scope
					cloningCount++
				}
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

		if _, skipped := s.configSyncSkipped.Load(r.ws.ID); !skipped {
			if err := s.workspaceRegistry.UpdateConfigLastSync(r.ws.ID); err != nil {
				if strings.Contains(err.Error(), "project not initialized") {
					s.logger.Debug("skipping config last sync for uninitialized workspace", "team", r.ws.TeamName, "path", r.ws.Path)
					s.configSyncSkipped.Store(r.ws.ID, true)
				} else {
					s.logger.Warn("failed to update config last sync", "team", r.ws.TeamName, "path", r.ws.Path, "error", err)
				}
			}
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
	repoName := filepath.Base(path)

	// compute FETCH_HEAD min age from manifest if available
	minFetchAge := minTeamContextFetchAge
	if intervalMin := s.workspaceRegistry.GetSyncIntervalMin(path); intervalMin > 0 {
		minFetchAge = time.Duration(intervalMin) * time.Minute
	}

	// read manifest before pull to get auto-resolve prefixes. The manifest
	// is already on disk from the previous clone/pull. If missing, ParseFile
	// returns FallbackConfig which includes DefaultResolveRules.
	manifestPath := filepath.Join(path, ".sageox", "sync.manifest")
	mCfg := manifest.ParseFile(manifestPath)

	result := s.pullManagedRepo(ctx, ManagedRepoPullOpts{
		RepoPath:           path,
		RepoName:           repoName,
		ProjectRoot:        s.config.ProjectRoot,
		SyncInterval:       s.config.TeamContextSyncInterval,
		MinFetchAge:        minFetchAge,
		ValidateIntegrity:  true,
		DetectDivergence:   true,
		ResolveRules:       mCfg.ResolveRules,
		EnsureKBMergeAttrs: true, // shared kb resilience for team-context wedges
		Logger:             s.logger,
	})

	// corrupt repo: move aside so background clone picks it up next cycle
	if result.CorruptRepo {
		backupPath := fmt.Sprintf("%s.bak.%d", path, time.Now().Unix())
		s.logger.Warn("team context repo corrupt, moving aside for re-clone",
			"path", path, "backup", backupPath)
		if err := os.Rename(path, backupPath); err != nil {
			s.logger.Error("failed to move corrupt team context aside", "error", err)
			return fmt.Errorf("corrupt team context at %s but rename failed: %w", path, err)
		}
		return nil
	}

	// handle skip
	if result.Skipped {
		if result.Issue != nil && s.issues != nil {
			s.issues.SetIssue(*result.Issue)
		} else if s.issues != nil {
			s.issues.ClearIssue(IssueTypeGitLock, repoName)
		}
		return nil
	}

	// handle errors
	if result.Err != nil {
		if result.Issue != nil {
			if result.Issue.Type == IssueTypeMergeConflict {
				s.metrics.RecordConflict()
			}
			if s.issues != nil {
				s.issues.SetIssue(*result.Issue)
			}
		}
		return result.Err
	}

	// clear lock issue on success
	if s.issues != nil {
		s.issues.ClearIssue(IssueTypeGitLock, repoName)
	}

	// sync succeeded - clear merge conflict and diverged issues
	if s.issues != nil {
		s.issues.ClearIssue(IssueTypeMergeConflict, repoName)
		s.issues.ClearIssue(IssueTypeDiverged, repoName)
	}

	return nil
}

// applySparseCheckout reads the manifest from a team context repo and applies
// sparse-checkout rules. Returns the parsed ManifestConfig so callers can use
// SyncIntervalMin. Thin wrapper over applySparseFromManifest (sync_sparse.go)
// so kb sync and team-context sync share the same sparse application logic.
func (s *SyncScheduler) applySparseCheckout(ctx context.Context, tcPath string) *manifest.ManifestConfig {
	cfg := manifest.ParseFile(filepath.Join(tcPath, ".sageox", "sync.manifest"))
	_ = applySparseFromManifest(ctx, tcPath, cfg, s.logger)
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
