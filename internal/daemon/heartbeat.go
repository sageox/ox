package daemon

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

// HeartbeatPayload is sent by CLI commands to the daemon.
// All fields are optional - commands send what context they have.
//
// The heartbeat serves multiple purposes:
//  1. Activity tracking: lets daemon know CLI is active (prevents inactivity shutdown)
//  2. Context awareness: daemon learns which repos/teams/workspaces/agents are in use
//  3. Credential refresh: CLI pushes fresh tokens so daemon can make API calls
//  4. Version sync: ensures daemon and CLI are compatible
type HeartbeatPayload struct {
	// RepoPath identifies which git repository the CLI is operating in.
	// Used for activity tracking sparklines and to prioritize sync for active repos.
	RepoPath string `json:"repo_path,omitempty"`

	// WorkspaceID identifies the workspace context (derived from repo path hash).
	// Enables multi-workspace daemon support and request routing.
	WorkspaceID string `json:"workspace_id,omitempty"`

	// AgentID identifies the agent session (e.g., "Oxa7b3").
	// Used for tracking which agents are actively connected to the daemon.
	// Empty for non-agent CLI commands.
	AgentID string `json:"agent_id,omitempty"`

	// TeamIDs lists team contexts referenced in the current operation.
	// Triggers lazy loading of team context repos when daemon sees new team IDs.
	TeamIDs []string `json:"team_ids,omitempty"`

	// Credentials contains tokens for git operations and API calls.
	// CLI pushes credentials so daemon doesn't need filesystem access to token stores.
	// This is the primary mechanism for keeping daemon authenticated.
	Credentials *HeartbeatCreds `json:"credentials,omitempty"`

	// Timestamp is when the heartbeat was generated.
	// Used for staleness detection and activity timeline.
	Timestamp time.Time `json:"timestamp"`

	// CLIVersion is the version of the CLI sending the heartbeat.
	// Daemon compares this to its own version; mismatch triggers daemon restart
	// to ensure CLI and daemon stay in sync after upgrades.
	CLIVersion string `json:"cli_version,omitempty"`

	// ContextTokens is the estimated token count of context this command produced.
	// Accumulated per-agent by the daemon for visibility into context budget usage.
	// Zero means no context tracking for this heartbeat (e.g., non-agent commands).
	ContextTokens int64 `json:"context_tokens,omitempty"`

	// CommandName identifies which ox subcommand produced this context (e.g., "prime",
	// "team-ctx", "session list"). Used for per-command breakdown (ox-aw0).
	CommandName string `json:"command_name,omitempty"`
}

// HeartbeatCreds contains credentials for the daemon.
// Passed in heartbeats to keep daemon credentials fresh.
//
// Two credential types are passed:
//  1. Git credentials (Token/ServerURL): for clone/fetch/push operations
//  2. Auth credentials (AuthToken): for REST API calls (e.g., GET /api/v1/cli/repos)
//
// User identity (UserEmail/UserID) is included so daemon can:
//   - Log authentication events (login, logout, user switch)
//   - Include user context in telemetry
//   - Display authenticated user in `ox status`
type HeartbeatCreds struct {
	// Token is the git PAT (Personal Access Token) for clone/push/pull.
	// Issued by the SageOx git server, expires periodically (see ExpiresAt).
	Token string `json:"token"`

	// ServerURL is the git server base URL (e.g., "https://git.sageox.io").
	// Used to construct clone URLs for ledger and team context repos.
	ServerURL string `json:"server_url"`

	// ExpiresAt is when the git token expires.
	// Daemon uses this to trigger credential refresh before expiry.
	ExpiresAt time.Time `json:"expires_at"`

	// AuthToken is the OAuth access token for REST API calls.
	// Used to call endpoints like GET /api/v1/cli/repos to refresh git credentials.
	// This is separate from Token because git and API auth are different systems.
	AuthToken string `json:"auth_token"`

	// UserEmail is the authenticated user's email address.
	// Used for logging auth events and displaying in `ox status`.
	UserEmail string `json:"user_email"`

	// UserID is the authenticated user's unique identifier.
	// Used for telemetry and audit logging.
	UserID string `json:"user_id"`
}

// Copy returns a deep copy of HeartbeatCreds.
// Used to prevent races when storing credentials from external sources.
func (c *HeartbeatCreds) Copy() *HeartbeatCreds {
	if c == nil {
		return nil
	}
	newCreds := *c
	return &newCreds
}

// HeartbeatHandler processes incoming heartbeats from CLI commands.
type HeartbeatHandler struct {
	logger *slog.Logger

	// activity trackers (cap ~100 entries each for sparkline display)
	// each tracker has its own mutex, so no additional locking needed
	repoActivity      *ActivityTracker
	teamActivity      *ActivityTracker
	workspaceActivity *ActivityTracker
	agentActivity     *ActivityTracker // tracks connected agent sessions

	// per-agent context consumption tracking
	ctxMu              sync.RWMutex
	agentContextTokens map[string]int64 // agent_id → cumulative estimated tokens
	agentCommandCount  map[string]int   // agent_id → command count

	// credentials (updated from heartbeats) - protected by credMu
	credMu          sync.RWMutex
	credentials     *HeartbeatCreds
	credentialsTime time.Time

	// callbacks - protected by cbMu
	cbMu              sync.RWMutex
	onTeamNeeded      func(teamID string)
	onActivity        func()
	onVersionMismatch func(cliVersion, daemonVersion string) // triggers daemon restart
}

// NewHeartbeatHandler creates a new heartbeat handler.
func NewHeartbeatHandler(logger *slog.Logger) *HeartbeatHandler {
	// 100 entries per key (~2.4KB per key) provides good sparkline resolution.
	const activityCap = 100

	// max keys limits prevent unbounded memory growth from malicious/buggy clients
	// sending unique IDs on every request
	const (
		maxRepos      = 100 // max unique repos to track
		maxTeams      = 50  // max unique teams to track
		maxWorkspaces = 50  // max unique workspaces to track
		maxAgents     = 50  // max unique agent sessions to track
	)

	return &HeartbeatHandler{
		logger:            logger,
		repoActivity:      NewActivityTrackerWithMaxKeys(activityCap, maxRepos),
		teamActivity:      NewActivityTrackerWithMaxKeys(activityCap, maxTeams),
		workspaceActivity: NewActivityTrackerWithMaxKeys(activityCap, maxWorkspaces),
		agentActivity:     NewActivityTrackerWithMaxKeys(activityCap, maxAgents),
		agentContextTokens: make(map[string]int64),
		agentCommandCount: make(map[string]int),
	}
}

// SetTeamNeededCallback sets the callback for when a team context is needed.
func (h *HeartbeatHandler) SetTeamNeededCallback(cb func(teamID string)) {
	h.cbMu.Lock()
	h.onTeamNeeded = cb
	h.cbMu.Unlock()
}

// SetActivityCallback sets the callback for any heartbeat activity.
func (h *HeartbeatHandler) SetActivityCallback(cb func()) {
	h.cbMu.Lock()
	h.onActivity = cb
	h.cbMu.Unlock()
}

// SetVersionMismatchCallback sets the callback for CLI/daemon version mismatch.
// Called when CLI version differs from daemon version, typically triggers restart.
func (h *HeartbeatHandler) SetVersionMismatchCallback(cb func(cliVersion, daemonVersion string)) {
	h.cbMu.Lock()
	h.onVersionMismatch = cb
	h.cbMu.Unlock()
}

// SetInitialCredentials pre-populates credentials (e.g., from credential store on startup).
// This allows daemon to have credentials immediately without waiting for first heartbeat.
func (h *HeartbeatHandler) SetInitialCredentials(creds *HeartbeatCreds) {
	if creds == nil {
		return
	}
	// reject expired credentials
	if !creds.ExpiresAt.IsZero() && creds.ExpiresAt.Before(time.Now()) {
		h.logger.Debug("rejecting expired initial credentials", "expires", creds.ExpiresAt)
		return
	}
	h.credMu.Lock()
	h.credentials = creds.Copy() // deep copy to prevent races with caller
	h.credentialsTime = time.Now()
	h.credMu.Unlock()
	h.logger.Info("initial credentials loaded", "server", creds.ServerURL, "expires", creds.ExpiresAt)
}

// Handle processes an incoming heartbeat message.
func (h *HeartbeatHandler) Handle(payload json.RawMessage) {
	var hb HeartbeatPayload
	if err := json.Unmarshal(payload, &hb); err != nil {
		h.logger.Debug("failed to unmarshal heartbeat", "error", err)
		return
	}

	h.logger.Debug("heartbeat received",
		"repo", hb.RepoPath,
		"workspace", hb.WorkspaceID,
		"agent_id", hb.AgentID,
		"teams", hb.TeamIDs,
		"has_creds", hb.Credentials != nil,
		"cli_version", hb.CLIVersion,
	)

	// copy callbacks under lock to call outside lock (avoids potential deadlock)
	h.cbMu.RLock()
	activityCb := h.onActivity
	teamNeededCb := h.onTeamNeeded
	versionMismatchCb := h.onVersionMismatch
	h.cbMu.RUnlock()

	// check for version mismatch (CLI upgraded but daemon still running old version)
	// compare semver only — build timestamps differ on every rebuild and would
	// cause restart loops during development (same 0.2.0 but different +builddate)
	if hb.CLIVersion != "" && semverOnly(hb.CLIVersion) != semverOnly(Version()) {
		h.logger.Warn("CLI/daemon version mismatch detected",
			"cli_version", hb.CLIVersion,
			"daemon_version", Version(),
		)
		if versionMismatchCb != nil {
			versionMismatchCb(hb.CLIVersion, Version())
		}
	}

	// record general activity
	if activityCb != nil {
		activityCb()
	}

	// update credentials if provided (reject already-expired creds)
	if hb.Credentials != nil {
		if !hb.Credentials.ExpiresAt.IsZero() && hb.Credentials.ExpiresAt.Before(time.Now()) {
			h.logger.Debug("rejecting expired credentials from heartbeat",
				"expires", hb.Credentials.ExpiresAt,
			)
		} else {
			h.credMu.Lock()
			// detect auth token and user changes
			oldAuthToken := ""
			oldUserEmail := ""
			if h.credentials != nil {
				oldAuthToken = h.credentials.AuthToken
				oldUserEmail = h.credentials.UserEmail
			}
			newAuthToken := hb.Credentials.AuthToken
			newUserEmail := hb.Credentials.UserEmail
			authTokenChanged := oldAuthToken != newAuthToken && newAuthToken != ""
			userChanged := oldUserEmail != newUserEmail && newUserEmail != ""

			h.credentials = hb.Credentials.Copy() // deep copy to prevent races with caller
			h.credentialsTime = time.Now()
			h.credMu.Unlock()

			// log authentication events
			if userChanged {
				if oldUserEmail == "" {
					h.logger.Info("user authenticated via heartbeat", "user", newUserEmail)
				} else {
					h.logger.Info("user changed via heartbeat",
						"old_user", oldUserEmail,
						"new_user", newUserEmail,
					)
				}
			} else if authTokenChanged {
				h.logger.Info("auth token refreshed via heartbeat", "user", newUserEmail)
			}

			h.logger.Debug("credentials updated from heartbeat",
				"server", hb.Credentials.ServerURL,
				"expires", hb.Credentials.ExpiresAt,
				"has_auth_token", hb.Credentials.AuthToken != "",
				"user", hb.Credentials.UserEmail,
			)
		}
	}

	// record activity by repo
	if hb.RepoPath != "" {
		h.repoActivity.Record(hb.RepoPath)
	}

	// record activity by workspace
	if hb.WorkspaceID != "" {
		h.workspaceActivity.Record(hb.WorkspaceID)
	}

	// record activity by agent (capped at maxAgents unique agents)
	if hb.AgentID != "" {
		h.agentActivity.Record(hb.AgentID)

		// accumulate context tokens if reported.
		// only track agents already admitted by the bounded activity tracker
		// to prevent unbounded map growth from spoofed agent IDs.
		if hb.ContextTokens > 0 && h.agentActivity.Has(hb.AgentID) {
			h.ctxMu.Lock()
			h.agentContextTokens[hb.AgentID] += hb.ContextTokens
			h.agentCommandCount[hb.AgentID]++
			h.ctxMu.Unlock()
		}
	}

	// record activity by team and trigger lazy loading if needed
	for _, teamID := range hb.TeamIDs {
		h.teamActivity.Record(teamID)

		// trigger lazy team load if callback is set
		if teamNeededCb != nil {
			teamNeededCb(teamID)
		}
	}
}

// GetCredentials returns the current credentials and their freshness.
// Returns nil if no credentials have been received.
func (h *HeartbeatHandler) GetCredentials() (*HeartbeatCreds, time.Time) {
	h.credMu.RLock()
	defer h.credMu.RUnlock()
	return h.credentials, h.credentialsTime
}

// HasValidCredentials returns true if we have non-expired credentials.
func (h *HeartbeatHandler) HasValidCredentials() bool {
	h.credMu.RLock()
	defer h.credMu.RUnlock()

	if h.credentials == nil {
		return false
	}
	if h.credentials.ExpiresAt.IsZero() {
		return true // no expiration set
	}
	return time.Now().Before(h.credentials.ExpiresAt)
}

// GetAuthToken returns the cached auth token for API calls.
// Returns empty string if no auth token is available.
func (h *HeartbeatHandler) GetAuthToken() string {
	h.credMu.RLock()
	defer h.credMu.RUnlock()

	if h.credentials == nil {
		return ""
	}
	return h.credentials.AuthToken
}

// AuthenticatedUser holds info about the authenticated user.
type AuthenticatedUser struct {
	Email string `json:"email,omitempty"`
	ID    string `json:"id,omitempty"`
}

// GetAuthenticatedUser returns info about the currently authenticated user.
// Returns nil if no user is authenticated.
func (h *HeartbeatHandler) GetAuthenticatedUser() *AuthenticatedUser {
	h.credMu.RLock()
	defer h.credMu.RUnlock()

	if h.credentials == nil || h.credentials.UserEmail == "" {
		return nil
	}
	return &AuthenticatedUser{
		Email: h.credentials.UserEmail,
		ID:    h.credentials.UserID,
	}
}

// GetRepoActivity returns the activity tracker for repos.
func (h *HeartbeatHandler) GetRepoActivity() *ActivityTracker {
	return h.repoActivity
}

// GetTeamActivity returns the activity tracker for teams.
func (h *HeartbeatHandler) GetTeamActivity() *ActivityTracker {
	return h.teamActivity
}

// GetWorkspaceActivity returns the activity tracker for workspaces.
func (h *HeartbeatHandler) GetWorkspaceActivity() *ActivityTracker {
	return h.workspaceActivity
}

// GetAgentActivity returns the activity tracker for connected agents.
func (h *HeartbeatHandler) GetAgentActivity() *ActivityTracker {
	return h.agentActivity
}

// AgentContextStats holds cumulative context consumption for an agent.
type AgentContextStats struct {
	ContextTokens int64 `json:"context_tokens"`
	CommandCount  int   `json:"command_count"`
}

// GetAgentContextStats returns the cumulative context consumption for a given agent.
func (h *HeartbeatHandler) GetAgentContextStats(agentID string) AgentContextStats {
	h.ctxMu.RLock()
	defer h.ctxMu.RUnlock()
	return AgentContextStats{
		ContextTokens: h.agentContextTokens[agentID],
		CommandCount:  h.agentCommandCount[agentID],
	}
}

// ActivitySummary returns a summary of all activity for status display.
type ActivitySummary struct {
	Repos      []ActivityEntry `json:"repos,omitempty"`
	Teams      []ActivityEntry `json:"teams,omitempty"`
	Workspaces []ActivityEntry `json:"workspaces,omitempty"`
	Agents     []ActivityEntry `json:"agents,omitempty"` // connected agent sessions
}

// ActivityEntry represents activity for a single key.
type ActivityEntry struct {
	Key        string      `json:"key"`
	Count      int         `json:"count"`
	Last       time.Time   `json:"last"`
	Timestamps []time.Time `json:"timestamps,omitempty"` // for sparkline
}

// GetActivitySummary returns a summary of all tracked activity.
func (h *HeartbeatHandler) GetActivitySummary() ActivitySummary {
	return ActivitySummary{
		Repos:      h.getActivityEntries(h.repoActivity),
		Teams:      h.getActivityEntries(h.teamActivity),
		Workspaces: h.getActivityEntries(h.workspaceActivity),
		Agents:     h.getActivityEntries(h.agentActivity),
	}
}

func (h *HeartbeatHandler) getActivityEntries(tracker *ActivityTracker) []ActivityEntry {
	keys := tracker.Keys()
	entries := make([]ActivityEntry, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, ActivityEntry{
			Key:        key,
			Count:      tracker.Count(key),
			Last:       tracker.Last(key),
			Timestamps: tracker.Get(key),
		})
	}
	return entries
}
