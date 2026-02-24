package daemon

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/paths"
)

// WorkspaceType identifies the type of workspace.
type WorkspaceType string

const (
	WorkspaceTypeLedger      WorkspaceType = "ledger"
	WorkspaceTypeTeamContext WorkspaceType = "team_context"
)

// WorkspaceState represents the runtime state of a workspace (repo).
// Combines config data with runtime sync status.
type WorkspaceState struct {
	// identity
	ID   string        `json:"id"`   // workspace ID (ledger=path hash, team=team_id)
	Type WorkspaceType `json:"type"` // ledger or team_context
	Path string        `json:"path"` // local path to the git repo

	// team-specific (only for team_context type)
	TeamID   string `json:"team_id,omitempty"`
	TeamName string `json:"team_name,omitempty"`
	CloneURL string `json:"clone_url,omitempty"` // git clone URL (from credentials)

	// config (from config.local.toml)
	Endpoint       string    `json:"endpoint,omitempty"`
	ConfigLastSync time.Time `json:"config_last_sync,omitempty"` // last sync recorded in config

	// runtime state
	Exists          bool      `json:"exists"`                      // whether the local path exists
	LastErr         string    `json:"last_error,omitempty"`        // last error during sync
	LastSyncAttempt time.Time `json:"last_sync_attempt,omitempty"` // last time we tried to sync
	SyncInProgress  bool      `json:"sync_in_progress,omitempty"`  // currently syncing

	// clone retry state (for background clones that fail)
	CloneAttempts    int       `json:"clone_attempts,omitempty"`     // number of failed clone attempts
	NextCloneAttempt time.Time `json:"next_clone_attempt,omitempty"` // when to retry clone (exponential backoff)

	// sync (fetch/pull) retry state — backoff on consecutive failures
	SyncFailures    int       `json:"sync_failures,omitempty"`     // consecutive failed sync attempts
	NextSyncAttempt time.Time `json:"next_sync_attempt,omitempty"` // when to retry sync (exponential backoff)
}

// WorkspaceRegistry tracks all workspaces (ledger + team contexts) for a daemon.
// Provides a unified view of workspace state for both daemon lifecycle and sync operations.
//
// Design rationale:
// - Single source of truth for workspace state (replaces duplicate tracking in sync.go)
// - Caches loaded config to avoid repeated disk reads
// - Adds runtime state (Exists, LastErr, SyncInProgress) on top of config
// - Thread-safe for concurrent access from daemon goroutines
//
// INVARIANT: WorkspaceRegistry is the sole writer to config.local.toml within the daemon.
// All config writes must go through registry methods (UpdateConfigLastSync, PersistLedgerPath, etc.)
// to prevent cache/disk divergence. Never call config.SaveLocalConfig directly from sync.go
// or other daemon code.
type WorkspaceRegistry struct {
	mu sync.RWMutex

	// project context
	projectRoot string
	repoName    string
	endpoint    string // SageOx API endpoint from project config
	repoID      string // repo ID from project config (for API calls)

	// workspaces indexed by ID
	workspaces map[string]*WorkspaceState

	// ledger is special-cased for quick access (most common use case)
	ledger *WorkspaceState

	// config cache - avoid reloading on every sync
	localConfigCache    *config.LocalConfig
	localConfigLoadedAt time.Time
	configCacheDuration time.Duration
}

// NewWorkspaceRegistry creates a new workspace registry for the given project.
func NewWorkspaceRegistry(projectRoot, repoName string) *WorkspaceRegistry {
	return &WorkspaceRegistry{
		projectRoot:         projectRoot,
		repoName:            repoName,
		workspaces:          make(map[string]*WorkspaceState),
		configCacheDuration: 30 * time.Second, // reload config every 30s max
	}
}

// LoadFromConfig loads workspace state from config.local.toml.
// Uses cached config if recently loaded, otherwise reloads from disk.
func (r *WorkspaceRegistry) LoadFromConfig() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.loadFromConfigLocked()
}

// loadFromConfigLocked loads config (must hold lock).
func (r *WorkspaceRegistry) loadFromConfigLocked() error {
	// check cache
	if r.localConfigCache != nil && time.Since(r.localConfigLoadedAt) < r.configCacheDuration {
		return nil // use cached config
	}

	// load fresh config
	localCfg, err := config.LoadLocalConfig(r.projectRoot)
	if err != nil {
		return err
	}

	// load project config to get endpoint and repoID (needed for team context path computation and API calls)
	if r.projectRoot != "" {
		if projectCfg, err := config.LoadProjectConfig(r.projectRoot); err == nil && projectCfg != nil {
			if r.endpoint == "" {
				r.endpoint = projectCfg.GetEndpoint()
				slog.Debug("workspace registry: loaded endpoint from project config",
					"endpoint", r.endpoint,
					"repo_id", projectCfg.RepoID)
			}
			if r.repoID == "" {
				r.repoID = projectCfg.RepoID
			}
		} else if err != nil {
			slog.Debug("workspace registry: failed to load project config",
				"project_root", r.projectRoot,
				"error", err)
		}
	}

	r.localConfigCache = localCfg
	r.localConfigLoadedAt = time.Now()

	// rebuild workspace state from config
	r.rebuildFromConfigLocked(localCfg)

	return nil
}

// rebuildFromConfigLocked rebuilds workspace state from loaded config (must hold lock).
// Also discovers team contexts from git credentials when not in config.local.toml.
//
// Discovery sources by workspace type:
//
// TEAM CONTEXTS:
//  1. config.local.toml - explicit team context configuration
//  2. git credentials - team-context repos from GET /api/v1/cli/repos (auto-discovered)
//  3. repo detail API - public team contexts from GET /api/v1/cli/repos/{repo_id} (via RegisterTeamContextsFromAPI)
//
// LEDGER:
//   - config.local.toml provides the path only
//   - Clone URL comes from GET /api/v1/repos/{repo_id}/ledger-status (via InitializeLedger)
//   - NOTE: /api/v1/cli/repos does NOT return ledger URLs
//
// This allows the daemon to auto-clone team contexts even when config.local.toml
// doesn't exist, as long as the user has authenticated and credentials contain
// team context repos.
func (r *WorkspaceRegistry) rebuildFromConfigLocked(cfg *config.LocalConfig) {
	// track which workspaces we've seen for cleanup
	seen := make(map[string]bool)

	// load git credentials for clone URLs and discovery (use endpoint-specific credentials)
	var creds *gitserver.GitCredentials
	loadedCreds, err := gitserver.LoadCredentialsForEndpoint(r.endpoint)
	if err != nil {
		slog.Debug("workspace registry: failed to load credentials",
			"endpoint", r.endpoint,
			"error", err)
	} else if loadedCreds == nil {
		slog.Debug("workspace registry: no credentials found for endpoint",
			"endpoint", r.endpoint)
	} else {
		creds = loadedCreds
		slog.Debug("workspace registry: loaded credentials",
			"endpoint", r.endpoint,
			"repo_count", len(creds.Repos),
			"server_url", creds.ServerURL)
		for name, repo := range creds.Repos {
			slog.Debug("workspace registry: credential repo",
				"name", name,
				"type", repo.Type,
				"url", repo.URL)
		}
	}

	// process ledger from config OR preserve API-initialized ledger
	if cfg.Ledger != nil && cfg.Ledger.Path != "" {
		id := "ledger"
		seen[id] = true

		existing := r.workspaces[id]
		if existing == nil {
			existing = &WorkspaceState{
				ID:   id,
				Type: WorkspaceTypeLedger,
			}
			r.workspaces[id] = existing
		}

		existing.Path = cfg.Ledger.Path
		existing.Endpoint = r.endpoint
		existing.ConfigLastSync = cfg.Ledger.LastSync
		existing.Exists = pathIsGitRepo(cfg.Ledger.Path)

		// backfill: if repo exists but was never marked as synced, set it now
		// (fixes repos cloned before UpdateConfigLastSync was added to the pull path)
		if existing.Exists && existing.ConfigLastSync.IsZero() {
			existing.ConfigLastSync = time.Now()
			if cfg.Ledger != nil {
				cfg.Ledger.LastSync = existing.ConfigLastSync
				_ = config.SaveLocalConfig(r.projectRoot, cfg)
			}
		}

		// NOTE: Ledger clone URL comes from the ledger-status API, NOT from /api/v1/cli/repos.
		// The CloneURL is set via InitializeLedger() or SetLedgerCloneURL() when the daemon
		// fetches ledger status from GET /api/v1/repos/{repo_id}/ledger-status.
		// See: fetchLedgerURLFromAPI() in sync.go

		r.ledger = existing
	} else if r.ledger != nil && r.ledger.CloneURL != "" {
		// preserve API-initialized ledger that isn't in config.local.toml
		// this happens when the ledger URL was fetched via InitializeLedger()
		// but config.local.toml doesn't have the ledger entry yet
		seen[r.ledger.ID] = true
		r.ledger.Exists = pathIsGitRepo(r.ledger.Path)
		slog.Debug("workspace registry: preserving API-initialized ledger",
			"path", r.ledger.Path,
			"clone_url", r.ledger.CloneURL,
			"exists", r.ledger.Exists)
	}

	// process team contexts from config.local.toml
	configuredTeams := make(map[string]bool) // track teams from config
	for _, tc := range cfg.TeamContexts {
		id := tc.TeamID
		if id == "" {
			continue
		}
		seen[id] = true
		configuredTeams[tc.TeamName] = true
		configuredTeams[tc.TeamID] = true

		existing := r.workspaces[id]
		if existing == nil {
			existing = &WorkspaceState{
				ID:   id,
				Type: WorkspaceTypeTeamContext,
			}
			r.workspaces[id] = existing
		}

		existing.TeamID = tc.TeamID
		existing.TeamName = tc.TeamName
		existing.Path = tc.Path
		existing.Endpoint = r.endpoint
		existing.ConfigLastSync = tc.LastSync
		existing.Exists = tc.Path != "" && pathIsGitRepo(tc.Path)

		// populate clone URL from credentials (match by team ID or name)
		if creds != nil {
			for _, repo := range creds.Repos {
				if repo.Type == "team-context" && (repo.StableID() == tc.TeamID || repo.Name == tc.TeamName || repo.Name == tc.TeamID) {
					existing.CloneURL = repo.URL
					break
				}
			}
		}
	}

	// discover team contexts from credentials that aren't in config
	// this enables auto-clone even when config.local.toml doesn't exist
	if creds != nil && r.projectRoot != "" {
		slog.Debug("workspace registry: discovering team contexts from credentials",
			"project_root", r.projectRoot,
			"creds_repo_count", len(creds.Repos))

		for _, repo := range creds.Repos {
			if repo.Type != "team-context" || repo.URL == "" {
				continue
			}

			teamID := repo.StableID()

			// skip if already configured
			if configuredTeams[repo.Name] || configuredTeams[teamID] {
				slog.Debug("workspace registry: skipping already configured team",
					"team_id", teamID, "name", repo.Name)
				continue
			}

			slog.Debug("workspace registry: discovered team context from credentials",
				"team_id", teamID, "name", repo.Name, "url", repo.URL)

			seen[teamID] = true

			existing := r.workspaces[teamID]
			if existing == nil {
				existing = &WorkspaceState{
					ID:   teamID,
					Type: WorkspaceTypeTeamContext,
				}
				r.workspaces[teamID] = existing
			}

			existing.TeamID = teamID
			existing.TeamName = repo.Name
			existing.Endpoint = r.endpoint
			// use centralized path: ~/.sageox/data/<endpoint>/teams/<team_id>/
			existing.Path = paths.TeamContextDir(teamID, r.endpoint)
			existing.CloneURL = repo.URL
			existing.Exists = pathIsGitRepo(existing.Path)

			slog.Debug("workspace registry: team context added",
				"team_id", existing.TeamID,
				"name", existing.TeamName,
				"path", existing.Path,
				"exists", existing.Exists)
		}
	} else if creds == nil {
		slog.Debug("workspace registry: skipping team context discovery (no credentials)")
	} else if r.projectRoot == "" {
		slog.Debug("workspace registry: skipping team context discovery (no project root)")
	}

	// remove workspaces no longer in config or credentials
	for id := range r.workspaces {
		if !seen[id] {
			delete(r.workspaces, id)
		}
	}
}

// pathIsGitRepo checks if a path exists on disk AND is a valid git repository.
// For workspaces, "exists" means "is a valid git repo" not just "directory exists".
// This is critical for self-healing: a directory without .git should be recloned.
func pathIsGitRepo(path string) bool {
	if path == "" {
		return false
	}
	gitDir := filepath.Join(path, ".git")
	_, err := os.Stat(gitDir)
	return err == nil
}

// InvalidateConfigCache forces the next LoadFromConfig to reload from disk.
func (r *WorkspaceRegistry) InvalidateConfigCache() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.localConfigLoadedAt = time.Time{}
}

// GetLedger returns a copy of the ledger workspace state.
// Returns nil if no ledger workspace exists.
func (r *WorkspaceRegistry) GetLedger() *WorkspaceState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.ledger == nil {
		return nil
	}
	copy := *r.ledger
	return &copy
}

// GetWorkspace returns a copy of a workspace by ID.
// Returns nil if the workspace doesn't exist.
func (r *WorkspaceRegistry) GetWorkspace(id string) *WorkspaceState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ws := r.workspaces[id]
	if ws == nil {
		return nil
	}
	copy := *ws
	return &copy
}

// GetTeamContexts returns copies of all team context workspaces.
func (r *WorkspaceRegistry) GetTeamContexts() []WorkspaceState {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []WorkspaceState
	for _, ws := range r.workspaces {
		if ws.Type == WorkspaceTypeTeamContext {
			result = append(result, *ws)
		}
	}
	return result
}

// GetAllWorkspaces returns copies of all workspaces (ledger + team contexts).
func (r *WorkspaceRegistry) GetAllWorkspaces() []WorkspaceState {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]WorkspaceState, 0, len(r.workspaces))
	for _, ws := range r.workspaces {
		result = append(result, *ws)
	}
	return result
}

// SetWorkspaceError records an error for a workspace.
func (r *WorkspaceRegistry) SetWorkspaceError(id, errMsg string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if ws := r.workspaces[id]; ws != nil {
		ws.LastErr = errMsg
	}
}

// ClearWorkspaceError clears the error for a workspace.
func (r *WorkspaceRegistry) ClearWorkspaceError(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if ws := r.workspaces[id]; ws != nil {
		ws.LastErr = ""
	}
}

// SetSyncInProgress marks a workspace as syncing.
func (r *WorkspaceRegistry) SetSyncInProgress(id string, inProgress bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if ws := r.workspaces[id]; ws != nil {
		ws.SyncInProgress = inProgress
		if inProgress {
			ws.LastSyncAttempt = time.Now()
		}
	}
}

// RecordSyncAttempt records that a sync was attempted for a workspace.
func (r *WorkspaceRegistry) RecordSyncAttempt(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if ws := r.workspaces[id]; ws != nil {
		ws.LastSyncAttempt = time.Now()
	}
}

// RefreshExists updates the Exists field for all workspaces.
func (r *WorkspaceRegistry) RefreshExists() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, ws := range r.workspaces {
		ws.Exists = pathIsGitRepo(ws.Path)
	}
}

// UpdateConfigLastSync updates the last sync time in both registry and config file.
// This should be called after a successful sync.
func (r *WorkspaceRegistry) UpdateConfigLastSync(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	ws := r.workspaces[id]
	if ws == nil {
		return nil
	}

	now := time.Now()
	ws.ConfigLastSync = now

	// also update the config file
	if r.localConfigCache == nil {
		return nil // no config to update
	}

	switch ws.Type {
	case WorkspaceTypeLedger:
		r.localConfigCache.UpdateLedgerLastSync()
	case WorkspaceTypeTeamContext:
		r.localConfigCache.UpdateTeamContextLastSync(ws.TeamID)
	}

	return config.SaveLocalConfig(r.projectRoot, r.localConfigCache)
}

// PersistLedgerPath saves the ledger path to the config cache and disk.
// This keeps the cache in sync so UpdateConfigLastSync doesn't overwrite it.
func (r *WorkspaceRegistry) PersistLedgerPath(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.localConfigCache == nil {
		return nil
	}

	if r.localConfigCache.Ledger == nil {
		r.localConfigCache.Ledger = &config.LedgerConfig{Path: path}
	} else if r.localConfigCache.Ledger.Path == "" {
		r.localConfigCache.Ledger.Path = path
	} else {
		return nil // already has a path
	}

	if err := config.SaveLocalConfig(r.projectRoot, r.localConfigCache); err != nil {
		return err
	}
	slog.Info("persisted ledger path to config.local.toml", "path", path)
	return nil
}

// GetLedgerPath returns the ledger path for quick access.
func (r *WorkspaceRegistry) GetLedgerPath() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.ledger != nil {
		return r.ledger.Path
	}
	return ""
}

// GetTeamContextStatus returns team context status in the legacy format.
// This provides backward compatibility with existing status display code.
func (r *WorkspaceRegistry) GetTeamContextStatus() []TeamContextSyncStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []TeamContextSyncStatus
	for _, ws := range r.workspaces {
		if ws.Type != WorkspaceTypeTeamContext {
			continue
		}
		result = append(result, TeamContextSyncStatus{
			TeamID:   ws.TeamID,
			TeamName: ws.TeamName,
			Path:     ws.Path,
			CloneURL: ws.CloneURL,
			LastSync: ws.ConfigLastSync,
			LastErr:  ws.LastErr,
			Exists:   ws.Exists,
		})
	}
	return result
}

// HasFetchHead checks if the workspace has a .git/FETCH_HEAD file
// and returns its modification time.
func (r *WorkspaceRegistry) HasFetchHead(id string) (exists bool, mtime time.Time) {
	r.mu.RLock()
	ws := r.workspaces[id]
	path := ""
	if ws != nil {
		path = ws.Path
	}
	r.mu.RUnlock()

	if path == "" {
		return false, time.Time{}
	}

	fetchHead := filepath.Join(path, ".git", "FETCH_HEAD")
	info, err := os.Stat(fetchHead)
	if err != nil {
		return false, time.Time{}
	}
	return true, info.ModTime()
}

// GetRepoID returns the repo ID from project config.
// Used for API calls like GetLedgerStatus.
func (r *WorkspaceRegistry) GetRepoID() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.repoID
}

// GetEndpoint returns the API endpoint from project config.
func (r *WorkspaceRegistry) GetEndpoint() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.endpoint
}

// SetLedgerCloneURL sets the clone URL for the ledger workspace.
// Called when ledger URL is fetched from API.
// Returns false if no ledger workspace exists (caller should ensure ledger is initialized first).
func (r *WorkspaceRegistry) SetLedgerCloneURL(cloneURL string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.ledger == nil {
		return false
	}
	r.ledger.CloneURL = cloneURL
	return true
}

// InitializeLedger creates a ledger workspace from API-fetched URL.
// Called when ledger URL is fetched from API but no ledger workspace exists yet.
//
// Path is computed via config.DefaultLedgerPath (user directory) for consistency.
func (r *WorkspaceRegistry) InitializeLedger(cloneURL, projectRoot string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// use canonical path helper for consistent path derivation
	ledgerPath := config.DefaultLedgerPath(r.repoID, r.endpoint)

	if r.ledger != nil {
		// ledger already initialized, update URL and ensure path is set
		r.ledger.CloneURL = cloneURL
		// fix empty path if it was somehow unset (ensures config.local.toml gets correct path)
		if r.ledger.Path == "" {
			r.ledger.Path = ledgerPath
			r.ledger.Exists = pathIsGitRepo(ledgerPath)
		}
		return
	}

	id := "ledger"
	r.ledger = &WorkspaceState{
		ID:       id,
		Type:     WorkspaceTypeLedger,
		Path:     ledgerPath,
		CloneURL: cloneURL,
		Endpoint: r.endpoint,
		Exists:   pathIsGitRepo(ledgerPath),
	}
	r.workspaces[id] = r.ledger
}

// SetCloneRetry records a failed clone attempt with exponential backoff.
// attempts is the total number of failed attempts (1-based).
// nextAttempt is when the next clone should be attempted.
func (r *WorkspaceRegistry) SetCloneRetry(id string, attempts int, nextAttempt time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if ws := r.workspaces[id]; ws != nil {
		ws.CloneAttempts = attempts
		ws.NextCloneAttempt = nextAttempt
	}
}

// ClearCloneRetry clears the clone retry state after a successful clone.
func (r *WorkspaceRegistry) ClearCloneRetry(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if ws := r.workspaces[id]; ws != nil {
		ws.CloneAttempts = 0
		ws.NextCloneAttempt = time.Time{}
		ws.LastErr = ""
	}
}

// ShouldRetryClone checks if enough time has passed to retry a failed clone.
// Returns true if:
// - No previous clone attempts (first try)
// - Current time is after NextCloneAttempt (backoff expired)
func (r *WorkspaceRegistry) ShouldRetryClone(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ws := r.workspaces[id]
	if ws == nil {
		return true // unknown workspace, allow clone attempt
	}

	// first attempt or no backoff set
	if ws.CloneAttempts == 0 || ws.NextCloneAttempt.IsZero() {
		return true
	}

	// check if backoff has expired
	return time.Now().After(ws.NextCloneAttempt)
}

// GetCloneRetryInfo returns the current clone retry state for a workspace.
// Returns attempts=0 if no retry state exists.
func (r *WorkspaceRegistry) GetCloneRetryInfo(id string) (attempts int, nextAttempt time.Time) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ws := r.workspaces[id]
	if ws == nil {
		return 0, time.Time{}
	}
	return ws.CloneAttempts, ws.NextCloneAttempt
}

// syncBackoffMax caps sync (fetch/pull) backoff at 30 minutes.
// Shorter than clone backoff (1hr) since syncs are more frequent and recovery is faster.
const syncBackoffMax = 30 * time.Minute

// exponentialBackoff calculates a capped exponential backoff duration.
// Returns base * 2^(failures-1), capped at maxBackoff.
// Used by both sync and clone backoff paths.
func exponentialBackoff(failures int, base, maxBackoff time.Duration) time.Duration {
	if failures <= 0 {
		return base
	}
	d := base * (1 << min(failures-1, 10)) // cap shift to avoid overflow
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}

// RecordSyncFailure increments the consecutive failure count and sets backoff.
// Backoff: 1min, 2min, 4min, 8min, 16min, 32min→capped to 30min.
// Creates a minimal workspace entry if one doesn't exist yet.
func (r *WorkspaceRegistry) RecordSyncFailure(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ws := r.workspaces[id]
	if ws == nil {
		ws = &WorkspaceState{ID: id}
		r.workspaces[id] = ws
	}
	ws.SyncFailures++
	ws.NextSyncAttempt = time.Now().Add(exponentialBackoff(ws.SyncFailures, time.Minute, syncBackoffMax))
}

// ClearSyncFailures resets sync retry state after a successful sync.
func (r *WorkspaceRegistry) ClearSyncFailures(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ws := r.workspaces[id]
	if ws == nil {
		return
	}
	ws.SyncFailures = 0
	ws.NextSyncAttempt = time.Time{}
}

// ShouldSync checks if enough time has passed since the last sync failure to retry.
// Returns true if no previous failures or backoff has expired.
func (r *WorkspaceRegistry) ShouldSync(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ws := r.workspaces[id]
	if ws == nil {
		return true
	}
	if ws.SyncFailures == 0 || ws.NextSyncAttempt.IsZero() {
		return true
	}
	return time.Now().After(ws.NextSyncAttempt)
}

// GetSyncRetryInfo returns the current sync retry state for a workspace.
func (r *WorkspaceRegistry) GetSyncRetryInfo(id string) (failures int, nextAttempt time.Time) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ws := r.workspaces[id]
	if ws == nil {
		return 0, time.Time{}
	}
	return ws.SyncFailures, ws.NextSyncAttempt
}

// RegisterTeamContextsFromAPI registers team contexts discovered from the repo detail API.
// This is the third discovery source: public team contexts visible to non-members.
// Only adds new team contexts that aren't already tracked (from config or credentials).
func (r *WorkspaceRegistry) RegisterTeamContextsFromAPI(teamContexts []api.RepoDetailTeamContext) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, tc := range teamContexts {
		if tc.TeamID == "" || tc.RepoURL == "" {
			continue
		}

		// skip if already tracked
		if _, exists := r.workspaces[tc.TeamID]; exists {
			continue
		}
		// also check by name (workspace IDs can be team name or team ID)
		if _, exists := r.workspaces[tc.Name]; exists {
			continue
		}

		slog.Debug("workspace registry: discovered team context from repo detail API",
			"team_id", tc.TeamID,
			"name", tc.Name,
			"access_level", tc.AccessLevel)

		id := tc.TeamID
		ws := &WorkspaceState{
			ID:       id,
			Type:     WorkspaceTypeTeamContext,
			TeamID:   tc.TeamID,
			TeamName: tc.Name,
			Endpoint: r.endpoint,
			Path:     paths.TeamContextDir(tc.TeamID, r.endpoint),
			CloneURL: tc.RepoURL,
		}
		ws.Exists = pathIsGitRepo(ws.Path)
		r.workspaces[id] = ws
	}
}

// CleanupRevokedTeamContexts removes team contexts that are no longer in the repo detail API response.
// Only removes team contexts that were discovered via the detail API (public TCs for non-members).
// Team contexts discovered from user credentials (member teams) are never removed by this cleanup,
// since the detail API is project-scoped and doesn't include all user teams.
func (r *WorkspaceRegistry) CleanupRevokedTeamContexts(currentTeamIDs map[string]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// load credentials to know which teams the user is a member of
	credTeamIDs := make(map[string]bool)
	if creds, err := gitserver.LoadCredentialsForEndpoint(r.endpoint); err == nil && creds != nil {
		for _, repo := range creds.Repos {
			if repo.Type == "team-context" {
				credTeamIDs[repo.StableID()] = true
			}
		}
	}

	for id, ws := range r.workspaces {
		if ws.Type != WorkspaceTypeTeamContext {
			continue
		}
		// skip if still in detail API response
		if currentTeamIDs[ws.TeamID] || currentTeamIDs[ws.TeamName] {
			continue
		}
		// skip if user is a member (discovered from credentials, not detail API)
		if credTeamIDs[ws.TeamID] {
			continue
		}
		// only clean up TCs that exist on disk; if not on disk, just remove from registry
		if ws.Exists && ws.Path != "" {
			clean := isCheckoutClean(ws.Path)
			if clean {
				slog.Info("workspace registry: team context no longer accessible, removing clean checkout",
					"team_id", ws.TeamID, "path", ws.Path)
				os.RemoveAll(ws.Path)
				delete(r.workspaces, id)
			} else {
				slog.Warn("workspace registry: team context no longer accessible but has uncommitted changes",
					"team_id", ws.TeamID, "path", ws.Path)
				ws.LastErr = "access revoked — local copy retained (has uncommitted changes)"
			}
		} else {
			delete(r.workspaces, id)
		}
	}
}

// isCheckoutClean returns true if a git repo has no uncommitted changes.
func isCheckoutClean(path string) bool {
	cmd := exec.Command("git", "-C", path, "status", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return false // assume dirty if we can't check
	}
	return strings.TrimSpace(string(output)) == ""
}
