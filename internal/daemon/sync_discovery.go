package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
)

// credentialRefreshThreshold is how close to expiry credentials must be
// before we proactively refresh them (1 hour)
const credentialRefreshThreshold = 1 * time.Hour

// refreshCredentialsIfNeeded refreshes a Git PAT after bearer rotation or near
// expiry. This is LAZY — exits early when the matching PAT has >1h remaining.
// Only uses the bearer to obtain a fresh PAT; the PAT
// itself is what git/LFS operations use (HTTP Basic auth, not OAuth bearer).
// See docs/specs/session-auth-model.md for the credential model.
func (s *SyncScheduler) refreshCredentialsIfNeeded() {
	s.refreshCredentials(false)
}

func (s *SyncScheduler) refreshCredentials(force bool) {
	projectEndpoint := endpoint.GetForProject(s.config.ProjectRoot)
	token, err := auth.GetTokenForEndpoint(projectEndpoint)
	if err != nil {
		s.logger.Warn("failed to get auth token for credential refresh", "error", err)
		return
	}
	var bearerHash string
	if token != nil && token.AccessToken != "" {
		bearerHash = gitserver.BearerTokenFingerprint(token.AccessToken)
	}

	// Rotation and a server rejection bypass the timer, but concurrent fetches
	// still share one in-flight operation.
	s.mu.Lock()
	if s.credentialRefreshInProgress || (!force && bearerHash == s.lastCredentialBearerHash &&
		!s.lastCredentialRefresh.IsZero() && time.Since(s.lastCredentialRefresh) < 5*time.Minute) {
		s.mu.Unlock()
		return
	}
	s.credentialRefreshInProgress = true
	s.lastCredentialRefresh = time.Now()
	s.lastCredentialBearerHash = bearerHash
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.credentialRefreshInProgress = false
		s.mu.Unlock()
	}()

	if bearerHash == "" {
		return
	}
	creds, err := gitserver.LoadCredentialsForEndpoint(projectEndpoint)
	if err != nil {
		s.logger.Debug("failed to load credentials for refresh check", "error", err)
	}
	if !force && creds != nil && creds.BearerTokenHash == bearerHash &&
		!creds.ExpiresAt.IsZero() && time.Until(creds.ExpiresAt) > credentialRefreshThreshold {
		return
	}

	// The scheduler refreshes within an hour of expiry; the shared cache
	// helper also handles a changed bearer and explicit server rejections.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = auth.RefreshGitCredentialsForEndpoint(ctx, projectEndpoint, true)
	if err != nil {
		s.logger.Warn("failed to refresh git credentials", "error", err)
		if errors.Is(err, api.ErrUnauthorized) && s.issues != nil {
			s.issues.SetIssue(DaemonIssue{
				Type: IssueTypeAuthExpiring, Severity: SeverityError,
				Summary: "Authentication rejected. " + auth.ReauthenticationRemedy(projectEndpoint),
			})
		}
		return
	}
	if s.issues != nil {
		s.issues.ClearIssue(IssueTypeAuthExpiring, "")
	}
	s.logger.Info("git credentials refreshed successfully", "endpoint", projectEndpoint)
}

// refreshAfterAuthFailure repairs a rejected PAT before the next sync attempt.
func (s *SyncScheduler) refreshAfterAuthFailure(err error) {
	if err == nil || !gitserver.IsAuthFailure(err.Error()) {
		return
	}
	ep := endpoint.GetForProject(s.config.ProjectRoot)
	creds, _ := gitserver.LoadCredentialsForEndpoint(ep)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if probe := gitserver.ValidatePATLiveness(ctx, creds); probe.Valid {
		return // the credential helper already refreshed the rejected PAT
	}
	s.refreshCredentials(true)
}

// discoverTeams re-fetches the team list from the API independently of token refresh.
// This ensures new teams are discovered promptly even when the credential token is
// still fresh. The PAT and its bearer fingerprint are saved with the discovered repos.
func (s *SyncScheduler) discoverTeams(ctx context.Context) {
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

	// refresh personal bearers before the API call; a fresh PAT can outlive them
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	token, err := auth.EnsureValidTokenForEndpointContext(ctx, projectEndpoint, 300)
	if err != nil {
		s.logger.Debug("failed to get auth token for team discovery", "error", err)
		return
	}
	if token == nil || token.AccessToken == "" {
		return
	}

	client := api.NewRepoClientWithEndpoint(projectEndpoint).WithAuthToken(token.AccessToken)
	newCreds, err := client.GetGitCredentials(ctx)
	if err != nil {
		s.logger.Warn("failed to fetch repos for team discovery", "error", err)
		if errors.Is(err, api.ErrUnauthorized) && s.issues != nil {
			s.issues.SetIssue(DaemonIssue{
				Type: IssueTypeAuthExpiring, Severity: SeverityError,
				Summary: "Authentication rejected. " + auth.ReauthenticationRemedy(projectEndpoint),
			})
		}
		return
	}
	if s.issues != nil {
		s.issues.ClearIssue(IssueTypeAuthExpiring, "")
	}
	if creds.Token == newCreds.Token && creds.BearerTokenHash == newCreds.BearerTokenHash &&
		creds.ExpiresAt.Equal(newCreds.ExpiresAt) && creds.ServerURL == newCreds.ServerURL &&
		creds.Username == newCreds.Username && reposEqual(creds.Repos, newCreds.Repos) {
		return
	}
	if err := gitserver.SaveCredentialsForEndpoint(projectEndpoint, *newCreds); err != nil {
		s.logger.Warn("failed to save credentials after team discovery", "error", err)
		return
	}
	s.logger.Info("team discovery found updated team list", "repo_count", len(newCreds.Repos))
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
