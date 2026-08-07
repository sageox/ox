package daemon

import (
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
)

// credentialRefreshThreshold is how close to expiry credentials must be
// before we proactively refresh them (1 hour)
const credentialRefreshThreshold = 1 * time.Hour

// refreshCredentialsIfNeeded checks if the Git PAT is expired or near expiry,
// and refreshes from the cloud API if needed. This is LAZY — exits early when
// the PAT has >1h remaining. Only uses OAuth to obtain a fresh PAT; the PAT
// itself is what git/LFS operations use (HTTP Basic auth, not OAuth bearer).
// See docs/specs/session-auth-model.md for the credential model.
func (s *SyncScheduler) refreshCredentialsIfNeeded() {
	// dedup: stamp-then-release to prevent TOCTOU race where concurrent callers
	// both observe a stale timestamp and both proceed to hit the API.
	s.mu.Lock()
	if !s.lastCredentialRefresh.IsZero() && time.Since(s.lastCredentialRefresh) < 5*time.Minute {
		s.mu.Unlock()
		return
	}
	s.lastCredentialRefresh = time.Now() // stamp before releasing lock
	s.mu.Unlock()

	// get the endpoint for this project
	projectEndpoint := endpoint.GetForProject(s.config.ProjectRoot)

	// load credentials for this specific endpoint
	creds, err := gitserver.LoadCredentialsForEndpoint(projectEndpoint)
	if err != nil {
		s.logger.Debug("failed to load credentials for refresh check", "error", err)
	}

	// check if credentials exist and are fresh
	if creds != nil && !creds.ExpiresAt.IsZero() && time.Until(creds.ExpiresAt) > credentialRefreshThreshold {
		// credentials are still fresh, no refresh needed
		return
	}

	refreshReason := "no credentials for endpoint"
	if creds != nil {
		refreshReason = "credentials expired or near expiry"
	}

	s.logger.Info("refreshing git credentials from API", "reason", refreshReason, "endpoint", projectEndpoint)

	// get auth token for this endpoint
	token, err := auth.GetTokenForEndpoint(projectEndpoint)
	if err != nil {
		s.logger.Warn("failed to get auth token for credential refresh", "error", err)
		return
	}
	if token == nil || token.AccessToken == "" {
		s.logger.Debug("no auth token available for credential refresh")
		return
	}

	// fetch fresh credentials from API using project endpoint
	client := api.NewRepoClientWithEndpoint(projectEndpoint).WithAuthToken(token.AccessToken)
	reposResp, err := client.GetRepos()
	if err != nil {
		s.logger.Warn("failed to fetch repos for credential refresh", "error", err)
		return
	}
	if reposResp == nil {
		s.logger.Debug("no repos returned from API")
		return
	}

	// build and save new credentials
	newCreds := gitserver.GitCredentials{
		Token:     reposResp.Token,
		ServerURL: reposResp.ServerURL,
		Username:  reposResp.Username,
		ExpiresAt: reposResp.ExpiresAt,
		Repos:     make(map[string]gitserver.RepoEntry),
	}
	for _, repo := range reposResp.Repos {
		newCreds.AddRepo(gitserver.RepoEntry{
			Name:   repo.Name,
			Type:   repo.Type,
			URL:    repo.URL,
			TeamID: repo.StableID(),
			Slug:   repo.Slug,
		})
	}

	if err := gitserver.SaveCredentialsForEndpoint(projectEndpoint, newCreds); err != nil {
		s.logger.Warn("failed to save refreshed credentials", "error", err)
		return
	}

	s.logger.Info("git credentials refreshed successfully", "expires", newCreds.ExpiresAt)
}

// discoverTeams re-fetches the team list from the API independently of token refresh.
// This ensures new teams are discovered promptly even when the credential token is still
// fresh (far from expiry). Only updates the Repos map in credentials; token/expiry are
// preserved from the existing credentials.
func (s *SyncScheduler) discoverTeams() {
	s.mu.Lock()
	if !s.lastTeamDiscovery.IsZero() && time.Since(s.lastTeamDiscovery) < teamDiscoveryInterval {
		s.mu.Unlock()
		return
	}
	s.lastTeamDiscovery = time.Now()
	s.mu.Unlock()

	projectEndpoint := endpoint.GetForProject(s.config.ProjectRoot)

	// load existing credentials — we need a valid token to call the API
	creds, err := gitserver.LoadCredentialsForEndpoint(projectEndpoint)
	if err != nil {
		s.logger.Debug("failed to load credentials for team discovery", "error", err)
		return
	}
	if creds == nil || creds.Token == "" {
		// no credentials available; refreshCredentialsIfNeeded will handle this
		return
	}

	// use the git PAT from credentials to call the repos API
	token, err := auth.GetTokenForEndpoint(projectEndpoint)
	if err != nil {
		s.logger.Debug("failed to get auth token for team discovery", "error", err)
		return
	}
	if token == nil || token.AccessToken == "" {
		return
	}

	client := api.NewRepoClientWithEndpoint(projectEndpoint).WithAuthToken(token.AccessToken)
	reposResp, err := client.GetRepos()
	if err != nil {
		s.logger.Warn("failed to fetch repos for team discovery", "error", err)
		return
	}
	if reposResp == nil {
		return
	}

	// build new repos map from API response
	newRepos := make(map[string]gitserver.RepoEntry)
	for _, repo := range reposResp.Repos {
		entry := gitserver.RepoEntry{
			Name:   repo.Name,
			Type:   repo.Type,
			URL:    repo.URL,
			TeamID: repo.StableID(),
			Slug:   repo.Slug,
		}
		newRepos[entry.StableID()] = entry
	}

	// check if repos changed before writing
	if reposEqual(creds.Repos, newRepos) {
		return
	}

	// update only the repos map; preserve existing token, expiry, server URL
	creds.Repos = newRepos
	if err := gitserver.SaveCredentialsForEndpoint(projectEndpoint, *creds); err != nil {
		s.logger.Warn("failed to save credentials after team discovery", "error", err)
		return
	}

	s.logger.Info("team discovery found updated team list", "repo_count", len(newRepos))
}

// reposEqual checks if two repo maps have identical entries.
func reposEqual(a, b map[string]gitserver.RepoEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok {
			return false
		}
		if va.Name != vb.Name || va.Type != vb.Type || va.URL != vb.URL || va.TeamID != vb.TeamID || va.Slug != vb.Slug {
			return false
		}
	}
	return true
}

// fetchLedgerURLFromAPI fetches the ledger URL from the cloud API and caches it.
// Called when the ledger needs to be cloned but no clone URL is available from credentials.
// Prefers GetRepoDetail (returns ledger + team contexts in one call), falling back to
// GetLedgerStatus if the server hasn't implemented the new endpoint yet (404 -> nil).
//
// This function handles many failure modes (no config, no auth, network errors, ledger not ready)
// by logging and returning early. This is intentional - the daemon should continue operating
// even if ledger URL fetch fails (e.g., when offline).
func (s *SyncScheduler) fetchLedgerURLFromAPI() {
	// check if we already have a ledger URL
	if ledger := s.workspaceRegistry.GetLedger(); ledger != nil && ledger.CloneURL != "" {
		return
	}

	// backoff on repeated API failures (separate key from git sync —
	// a cloud API outage should not block git fetch/pull against a healthy repo)
	if !s.workspaceRegistry.ShouldSync("ledger-api") {
		return
	}

	// get repo ID from workspace registry (loaded from project config)
	repoID := s.workspaceRegistry.GetRepoID()
	if repoID == "" {
		s.logger.Debug("no repo_id in project config, cannot fetch ledger URL")
		return
	}

	// get the endpoint for this project
	projectEndpoint := s.workspaceRegistry.GetEndpoint()
	if projectEndpoint == "" {
		projectEndpoint = endpoint.GetForProject(s.config.ProjectRoot)
	}

	// get auth token for this endpoint
	token, err := auth.GetTokenForEndpoint(projectEndpoint)
	if err != nil {
		s.logger.Debug("failed to get auth token for ledger status", "error", err)
		return
	}
	if token == nil || token.AccessToken == "" {
		s.logger.Debug("no auth token available for ledger status")
		return
	}

	// prefer GetRepoDetail (returns ledger + team contexts in one call)
	// fall back to GetLedgerStatus if server hasn't implemented new endpoint (404 -> nil)
	client := api.NewRepoClientWithEndpoint(projectEndpoint).WithAuthToken(token.AccessToken)

	detail, detailErr := client.GetRepoDetail(repoID)
	if detailErr != nil {
		s.logger.Warn("failed to fetch repo detail", "repo_id", repoID, "error", detailErr)
		s.workspaceRegistry.RecordSyncFailure("ledger-api")
	}

	// if GetRepoDetail succeeded, use its data
	if detail != nil {
		// register team contexts from API response (includes public TCs for non-members)
		s.registerTeamContextsFromDetail(detail)

		// use ledger data from detail
		if detail.Ledger != nil && detail.Ledger.Status == "ready" && detail.Ledger.RepoURL != "" {
			s.logger.Info("fetched ledger URL from repo detail", "repo_id", repoID)
			if !s.workspaceRegistry.SetLedgerCloneURL(detail.Ledger.RepoURL) {
				s.workspaceRegistry.InitializeLedger(detail.Ledger.RepoURL, s.config.ProjectRoot)
				s.logger.Info("initialized ledger workspace from repo detail", "clone_url", detail.Ledger.RepoURL)
			}
			s.workspaceRegistry.ClearSyncFailures("ledger-api")
			s.persistLedgerPath()
			return
		} else if detail.Ledger != nil {
			// "not ready" is a transient provisioning state, not a failure —
			// don't apply backoff, just skip this tick and retry next cycle
			s.logger.Debug("ledger not ready from repo detail", "status", detail.Ledger.Status, "message", detail.Ledger.Message)
			return
		}
		return
	}

	// fallback: GetRepoDetail returned nil (404 -- server not updated yet)
	status, err := client.GetLedgerStatus(repoID)
	if err != nil {
		// network errors are expected when offline - use Warn not Error
		s.logger.Warn("failed to fetch ledger status", "repo_id", repoID, "error", err)
		s.workspaceRegistry.RecordSyncFailure("ledger-api")
		return
	}

	// defensive: GetLedgerStatus should never return (nil, nil)
	if status == nil {
		s.logger.Debug("unexpected nil ledger status from API")
		return
	}

	// check if ledger is ready
	if status.Status != "ready" {
		// "not ready" is a transient provisioning state, not a failure —
		// don't apply backoff, just skip this tick and retry next cycle
		s.logger.Debug("ledger not ready", "status", status.Status, "message", status.Message)
		return
	}

	// update workspace registry with the ledger URL
	if status.RepoURL != "" {
		s.logger.Info("fetched ledger URL from API", "repo_id", repoID)
		if !s.workspaceRegistry.SetLedgerCloneURL(status.RepoURL) {
			s.workspaceRegistry.InitializeLedger(status.RepoURL, s.config.ProjectRoot)
			s.logger.Info("initialized ledger workspace from API", "clone_url", status.RepoURL)
		}
		s.workspaceRegistry.ClearSyncFailures("ledger-api")
		s.persistLedgerPath()
	}
}

// persistLedgerPath saves the ledger path to config.local.toml for persistence across daemon restarts.
// Uses the workspace registry's config cache to avoid stale-cache overwrites from UpdateConfigLastSync.
func (s *SyncScheduler) persistLedgerPath() {
	ledger := s.workspaceRegistry.GetLedger()
	if ledger == nil || ledger.Path == "" {
		return
	}
	if err := s.workspaceRegistry.PersistLedgerPath(ledger.Path); err != nil {
		s.logger.Warn("failed to persist ledger to config.local.toml", "error", err)
	}
	// trigger clone if ledger doesn't exist on disk (self-healing)
	if !ledger.Exists && ledger.CloneURL != "" {
		if s.workspaceRegistry.ShouldRetryClone(ledger.ID) {
			s.logger.Info("triggering ledger clone after API fetch", "path", ledger.Path)
			if s.addClone() {
				go s.cloneInBackground(ledger.CloneURL, ledger.Path, "ledger", ledger.ID)
			}
		}
	}
}

// registerTeamContextsFromDetail registers team contexts from a GetRepoDetail response.
// This enables the daemon to discover and sync public team contexts that non-members have
// viewer access to, even if those team contexts aren't in the user's credentials.
func (s *SyncScheduler) registerTeamContextsFromDetail(detail *api.RepoDetailResponse) {
	if detail == nil {
		return
	}

	// register new team contexts
	if len(detail.TeamContexts) > 0 {
		s.workspaceRegistry.RegisterTeamContextsFromAPI(detail.TeamContexts)
	}

	// cleanup team contexts no longer in the response
	currentTeamIDs := make(map[string]bool)
	for _, tc := range detail.TeamContexts {
		currentTeamIDs[tc.TeamID] = true
		currentTeamIDs[tc.Name] = true
	}
	s.workspaceRegistry.CleanupRevokedTeamContexts(currentTeamIDs)
}
