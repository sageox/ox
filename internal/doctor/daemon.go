package doctor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/endpoint"
)

// DaemonBootstrapGrace is the grace period after daemon startup during which
// missing sync/heartbeat data is expected (initial clone not yet completed).
const DaemonBootstrapGrace = 3 * time.Minute

// DaemonRunningCheck verifies the daemon process is alive.
type DaemonRunningCheck struct{}

// NewDaemonRunningCheck creates a daemon running check.
func NewDaemonRunningCheck() *DaemonRunningCheck {
	return &DaemonRunningCheck{}
}

// Name returns the check name.
func (c *DaemonRunningCheck) Name() string {
	return "daemon running"
}

// Category returns the check category.
func (c *DaemonRunningCheck) Category() string {
	return "Daemon"
}

// Run executes the daemon running check.
func (c *DaemonRunningCheck) Run(_ context.Context, _ bool) CheckResult {
	switch daemon.GetState() {
	case daemon.DaemonStateRunning:
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusPass,
			Message: "yes",
		}
	case daemon.DaemonStateStarting:
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusSkip,
			Message: "starting",
			Fix:     "Daemon is starting up, wait a moment and try again",
		}
	case daemon.DaemonStateStuck:
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusFail,
			Message: "stuck",
			Fix:     "Daemon process is running but not accepting connections. Run `ox daemon restart`",
		}
	default:
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusSkip,
			Message: "not running",
			Fix:     "Run `ox daemon start` to enable background sync",
		}
	}
}

// DaemonResponsiveCheck verifies the daemon responds to IPC.
type DaemonResponsiveCheck struct{}

// NewDaemonResponsiveCheck creates a daemon responsive check.
func NewDaemonResponsiveCheck() *DaemonResponsiveCheck {
	return &DaemonResponsiveCheck{}
}

// Name returns the check name.
func (c *DaemonResponsiveCheck) Name() string {
	return "daemon responsive"
}

// Category returns the check category.
func (c *DaemonResponsiveCheck) Category() string {
	return "Daemon"
}

// Run executes the daemon responsive check.
func (c *DaemonResponsiveCheck) Run(_ context.Context, _ bool) CheckResult {
	state := daemon.GetState()
	switch state {
	case daemon.DaemonStateStopped:
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	case daemon.DaemonStateStarting:
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusSkip,
			Message: "starting",
		}
	case daemon.DaemonStateStuck:
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusFail,
			Message: "not responding (stuck)",
			Fix:     "Daemon process is alive but not accepting connections. Run `ox daemon restart`",
		}
	}

	// DaemonStateRunning — do a live IPC health check
	if err := daemon.IsHealthy(); err != nil {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusFail,
			Message: "not responding",
			Fix:     fmt.Sprintf("Daemon running but not responding: %v. Try `ox daemon restart`", err),
		}
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusPass,
		Message: "ok",
	}
}

// DaemonSyncStatusCheck verifies sync is working.
type DaemonSyncStatusCheck struct{}

// NewDaemonSyncStatusCheck creates a daemon sync status check.
func NewDaemonSyncStatusCheck() *DaemonSyncStatusCheck {
	return &DaemonSyncStatusCheck{}
}

// Name returns the check name.
func (c *DaemonSyncStatusCheck) Name() string {
	return "last sync"
}

// Category returns the check category.
func (c *DaemonSyncStatusCheck) Category() string {
	return "Daemon"
}

// Run executes the daemon sync status check.
func (c *DaemonSyncStatusCheck) Run(_ context.Context, _ bool) CheckResult {
	if !daemon.IsRunning() {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	client := daemon.NewClientForCurrentRepoWithTimeout(500 * time.Millisecond)
	status, err := client.Status()
	if err != nil {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusWarn,
			Message: "unknown",
			Fix:     fmt.Sprintf("Could not get daemon status: %v", err),
		}
	}

	if status.LastSync.IsZero() {
		// grace period: daemon just started, first sync not completed yet
		if status.Uptime < DaemonBootstrapGrace {
			return CheckResult{
				Name:    c.Name(),
				Status:  StatusSkip,
				Message: "initial sync pending",
				Fix:     "daemon just started, first sync cycle not completed yet",
			}
		}
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusWarn,
			Message: "never",
			Fix:     "Daemon started but no sync completed yet",
		}
	}

	// calculate time since last sync
	sinceSync := time.Since(status.LastSync)

	// warning if last sync was > 1 hour ago
	if sinceSync > time.Hour {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusWarn,
			Message: formatDuration(sinceSync) + " ago",
			Fix:     "No sync in over an hour. Check network or remote access",
		}
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusPass,
		Message: formatDuration(sinceSync) + " ago",
	}
}

// DaemonUptimeCheck shows daemon uptime.
type DaemonUptimeCheck struct{}

// NewDaemonUptimeCheck creates a daemon uptime check.
func NewDaemonUptimeCheck() *DaemonUptimeCheck {
	return &DaemonUptimeCheck{}
}

// Name returns the check name.
func (c *DaemonUptimeCheck) Name() string {
	return "uptime"
}

// Category returns the check category.
func (c *DaemonUptimeCheck) Category() string {
	return "Daemon"
}

// Run executes the daemon uptime check.
func (c *DaemonUptimeCheck) Run(_ context.Context, _ bool) CheckResult {
	if !daemon.IsRunning() {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	client := daemon.NewClientForCurrentRepoWithTimeout(500 * time.Millisecond)
	status, err := client.Status()
	if err != nil {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusPass,
		Message: formatDuration(status.Uptime),
	}
}

// DaemonSyncErrorsCheck checks for recent sync errors.
type DaemonSyncErrorsCheck struct{}

// NewDaemonSyncErrorsCheck creates a sync errors check.
func NewDaemonSyncErrorsCheck() *DaemonSyncErrorsCheck {
	return &DaemonSyncErrorsCheck{}
}

// Name returns the check name.
func (c *DaemonSyncErrorsCheck) Name() string {
	return "sync errors"
}

// Category returns the check category.
func (c *DaemonSyncErrorsCheck) Category() string {
	return "Daemon"
}

// Run executes the sync errors check.
func (c *DaemonSyncErrorsCheck) Run(_ context.Context, _ bool) CheckResult {
	if !daemon.IsRunning() {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	client := daemon.NewClientForCurrentRepoWithTimeout(500 * time.Millisecond)
	status, err := client.Status()
	if err != nil {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	// check extended status if available
	if extStatus, ok := daemon.GetExtendedStatus(status); ok {
		if extStatus.RecentErrorCount > 0 {
			return CheckResult{
				Name:    c.Name(),
				Status:  StatusWarn,
				Message: fmt.Sprintf("%d recent errors", extStatus.RecentErrorCount),
				Fix:     fmt.Sprintf("Last error: %s. Run `ox daemon logs` for details", extStatus.LastError),
			}
		}
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusPass,
		Message: "none",
	}
}

// DaemonDirtyTeamContextCheck detects team contexts with uncommitted changes
// that are blocking GC. GC is a disk-space optimization (reclone) — it must
// not destroy user edits (docs/, conventions, etc.), so the daemon skips dirty
// workspaces and raises a DaemonIssue instead.
type DaemonDirtyTeamContextCheck struct{}

// NewDaemonDirtyTeamContextCheck creates a dirty team context check.
func NewDaemonDirtyTeamContextCheck() *DaemonDirtyTeamContextCheck {
	return &DaemonDirtyTeamContextCheck{}
}

// Name returns the check name.
func (c *DaemonDirtyTeamContextCheck) Name() string {
	return "team context clean"
}

// Category returns the check category.
func (c *DaemonDirtyTeamContextCheck) Category() string {
	return "Daemon"
}

// Run checks daemon issues for dirty_workspace entries.
func (c *DaemonDirtyTeamContextCheck) Run(_ context.Context, _ bool) CheckResult {
	if !daemon.IsRunning() {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	client := daemon.NewClientForCurrentRepoWithTimeout(500 * time.Millisecond)
	status, err := client.Status()
	if err != nil {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	// look for dirty_workspace issues
	var dirty []string
	for _, issue := range status.Issues {
		if issue.Type == daemon.IssueTypeDirtyWorkspace {
			name := issue.Repo
			if name == "" {
				name = "unknown"
			}
			dirty = append(dirty, name)
		}
	}

	if len(dirty) == 0 {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusPass,
			Message: "ok",
		}
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusWarn,
		Message: fmt.Sprintf("%d with uncommitted changes blocking GC", len(dirty)),
		Fix: fmt.Sprintf("Team contexts blocking GC reclone: %s\n"+
			"Run `ox doctor --fix gc-blocked-untracked` for details on which files are blocking.\n"+
			"Common cause: missing cache/ entry in .sageox/.gitignore — run `ox doctor --fix gitignore-missing` to fix.",
			strings.Join(dirty, ", ")),
	}
}

// DaemonGitHubAuthCheck detects GitHub authentication failures reported by the daemon.
type DaemonGitHubAuthCheck struct{}

// NewDaemonGitHubAuthCheck creates a GitHub auth check.
func NewDaemonGitHubAuthCheck() *DaemonGitHubAuthCheck {
	return &DaemonGitHubAuthCheck{}
}

// Name returns the check name.
func (c *DaemonGitHubAuthCheck) Name() string {
	return "GitHub auth"
}

// Category returns the check category.
func (c *DaemonGitHubAuthCheck) Category() string {
	return "Daemon"
}

// Run checks daemon issues for GitHub authentication failures.
func (c *DaemonGitHubAuthCheck) Run(_ context.Context, _ bool) CheckResult {
	if !daemon.IsRunning() {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	client := daemon.NewClientForCurrentRepoWithTimeout(500 * time.Millisecond)
	status, err := client.Status()
	if err != nil {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	for _, issue := range status.Issues {
		if issue.Type == daemon.IssueTypeGitHubAuth {
			return CheckResult{
				Name:    c.Name(),
				Status:  StatusWarn,
				Message: issue.Summary,
				Fix:     "Check GITHUB_TOKEN or run `gh auth login`. The token may lack the required scopes (e.g. repo or pull_request:read).",
			}
		}
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusPass,
		Message: "ok",
	}
}

// DaemonHeartbeatCheck verifies heartbeats are being written to repos.
type DaemonHeartbeatCheck struct {
	Type        string // "workspace", "ledger", "team"
	Identifier  string // repo_id or team_id
	DisplayName string // for output
	Endpoint    string // SageOx endpoint
}

// NewDaemonHeartbeatCheck creates a heartbeat check for a specific repo.
func NewDaemonHeartbeatCheck(checkType, identifier, displayName, ep string) *DaemonHeartbeatCheck {
	return &DaemonHeartbeatCheck{
		Type:        checkType,
		Identifier:  identifier,
		DisplayName: displayName,
		Endpoint:    ep,
	}
}

// Name returns the check name.
func (c *DaemonHeartbeatCheck) Name() string {
	return fmt.Sprintf("heartbeat (%s)", c.DisplayName)
}

// Category returns the check category.
func (c *DaemonHeartbeatCheck) Category() string {
	return "Daemon"
}

// Run executes the heartbeat check.
func (c *DaemonHeartbeatCheck) Run(_ context.Context, _ bool) CheckResult {
	if c.Identifier == "" || c.Endpoint == "" {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	// determine heartbeat path based on check type
	// CRITICAL: workspace/ledger use composite identifier (repo_id_workspace_id)
	// while team uses team_id directly. See UserHeartbeatPath docs.
	var heartbeatPath string
	ep := endpoint.NormalizeEndpoint(c.Endpoint)

	switch c.Type {
	case "workspace":
		// Identifier format: repo_id_workspace_id (e.g., repo_abc123_a1b2c3d4)
		// Split to pass to UserHeartbeatPath
		repoID, workspaceID := splitCompositeID(c.Identifier)
		if repoID == "" || workspaceID == "" {
			return CheckResult{
				Name:    c.Name(),
				Status:  StatusSkip,
				Message: "invalid identifier format",
			}
		}
		heartbeatPath = daemon.UserHeartbeatPath(ep, repoID, workspaceID)
	case "ledger":
		// Identifier format: repo_id_workspace_id (same as workspace)
		repoID, workspaceID := splitCompositeID(c.Identifier)
		if repoID == "" || workspaceID == "" {
			return CheckResult{
				Name:    c.Name(),
				Status:  StatusSkip,
				Message: "invalid identifier format",
			}
		}
		heartbeatPath = daemon.UserLedgerHeartbeatPath(ep, repoID, workspaceID)
	case "team":
		// Identifier is just team_id (shared across workspaces)
		heartbeatPath = daemon.UserTeamHeartbeatPath(ep, c.Identifier)
	default:
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusSkip,
			Message: "unknown type",
		}
	}

	entry, err := daemon.ReadLastHeartbeatFromPath(heartbeatPath)
	if err != nil || entry == nil {
		if daemon.IsRunning() {
			// grace period: daemon just started
			client := daemon.NewClientForCurrentRepoWithTimeout(500 * time.Millisecond)
			if dStatus, dErr := client.Status(); dErr == nil && dStatus.Uptime < DaemonBootstrapGrace {
				return CheckResult{
					Name:    c.Name(),
					Status:  StatusSkip,
					Message: "daemon starting up",
				}
			}
			// daemon is running but no heartbeat — it's not syncing this repo
			return CheckResult{
				Name:    c.Name(),
				Status:  StatusWarn,
				Message: "not syncing",
				Fix:     "Daemon running but not monitoring this repo. Try `ox daemon restart`",
			}
		}
		// daemon not running — skip, the "daemon running" check handles this
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusSkip,
			Message: "daemon not running",
		}
	}

	sinceHeartbeat := time.Since(entry.Timestamp)

	// warning if last heartbeat was > 10 minutes ago (heartbeats every 5 min)
	if sinceHeartbeat > 10*time.Minute {
		// if daemon is not running, skip — the "daemon running" check already
		// tells the user. Stale heartbeats are only actionable when daemon IS running.
		if !daemon.IsRunning() {
			return CheckResult{
				Name:    c.Name(),
				Status:  StatusSkip,
				Message: fmt.Sprintf("last seen %s ago", formatDuration(sinceHeartbeat)),
			}
		}
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusWarn,
			Message: formatDuration(sinceHeartbeat) + " ago",
			Fix:     "Daemon running but not syncing this repo. Try `ox daemon restart`",
		}
	}

	msg := formatDuration(sinceHeartbeat) + " ago"
	if entry.Status == "error" && entry.ErrorCount > 0 {
		msg = fmt.Sprintf("%s (%d errors)", msg, entry.ErrorCount)
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusPass,
		Message: msg,
	}
}

// splitCompositeID splits a composite identifier (repo_id_workspace_id) into parts.
// Returns empty strings if the identifier doesn't contain exactly one underscore.
//
// Example: "repo_abc123_a1b2c3d4" → ("repo_abc123", "a1b2c3d4")
func splitCompositeID(id string) (repoID, workspaceID string) {
	// Find the last underscore to split repo_id from workspace_id
	// This handles repo_ids that may contain underscores
	lastIdx := strings.LastIndex(id, "_")
	if lastIdx == -1 || lastIdx == 0 || lastIdx == len(id)-1 {
		return "", ""
	}
	return id[:lastIdx], id[lastIdx+1:]
}

// DaemonFDPressureCheck warns when the daemon is consuming a worrying
// fraction of its open-file-descriptor limit. Surfaces FD leaks within hours
// instead of months — see internal/daemon/fd_count.go for the rationale.
type DaemonFDPressureCheck struct{}

// NewDaemonFDPressureCheck creates an FD-pressure check for the daemon.
func NewDaemonFDPressureCheck() *DaemonFDPressureCheck {
	return &DaemonFDPressureCheck{}
}

// Name returns the check name.
func (c *DaemonFDPressureCheck) Name() string {
	return "fd pressure"
}

// Category returns the check category.
func (c *DaemonFDPressureCheck) Category() string {
	return "Daemon"
}

// FD-pressure thresholds, expressed as a fraction of the soft RLIMIT_NOFILE.
// Crossing 50% warns; crossing 80% fails. Computed against the current limit
// rather than hard-coded numbers so the check stays meaningful across very
// different host configs (macOS default 256 vs Linux container 1M+).
const (
	fdPressureWarn = 0.50
	fdPressureFail = 0.80
)

// Run executes the FD-pressure check.
func (c *DaemonFDPressureCheck) Run(_ context.Context, _ bool) CheckResult {
	if !daemon.IsRunning() {
		return CheckResult{Name: c.Name(), Status: StatusSkip}
	}
	client := daemon.NewClientForCurrentRepoWithTimeout(500 * time.Millisecond)
	status, err := client.Status()
	if err != nil {
		return CheckResult{Name: c.Name(), Status: StatusSkip}
	}
	if status.OpenFDs <= 0 {
		// platform doesn't surface FD count (e.g. windows) — skip silently
		return CheckResult{Name: c.Name(), Status: StatusSkip}
	}
	if status.OpenFDLimit == 0 {
		// no limit info — report raw count without a verdict
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusPass,
			Message: fmt.Sprintf("%d FDs open (limit unknown)", status.OpenFDs),
		}
	}
	pct := float64(status.OpenFDs) / float64(status.OpenFDLimit)
	msg := fmt.Sprintf("%d / %d open (%.0f%%)", status.OpenFDs, status.OpenFDLimit, pct*100)
	switch {
	case pct >= fdPressureFail:
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusFail,
			Message: msg,
			Fix:  "Daemon is approaching its file-descriptor limit. Restart with `ox daemon restart` while we investigate the leak source.",
		}
	case pct >= fdPressureWarn:
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusWarn,
			Message: msg,
			Fix:  "FD count is climbing toward the soft limit. Worth monitoring; check `ox status --verbose` over the next hour.",
		}
	default:
		return CheckResult{Name: c.Name(), Status: StatusPass, Message: msg}
	}
}

// formatDuration formats a duration for display.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
