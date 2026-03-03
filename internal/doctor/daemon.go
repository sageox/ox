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

// Run executes the daemon running check.
func (c *DaemonRunningCheck) Run(ctx context.Context) CheckResult {
	if daemon.IsRunning() {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusPass,
			Message: "yes",
		}
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusSkip,
		Message: "not running",
		Fix:     "Run `ox daemon start` to enable background sync",
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

// Run executes the daemon responsive check.
func (c *DaemonResponsiveCheck) Run(ctx context.Context) CheckResult {
	if err := daemon.IsHealthy(); err != nil {
		// Distinguish between "not running" and "not responding"
		if !daemon.IsRunning() {
			return CheckResult{
				Name:   c.Name(),
				Status: StatusSkip,
			}
		}
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

// Run executes the daemon sync status check.
func (c *DaemonSyncStatusCheck) Run(ctx context.Context) CheckResult {
	if !daemon.IsRunning() {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	client := daemon.NewClient()
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

// Run executes the daemon uptime check.
func (c *DaemonUptimeCheck) Run(ctx context.Context) CheckResult {
	if !daemon.IsRunning() {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	client := daemon.NewClient()
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

// Run executes the sync errors check.
func (c *DaemonSyncErrorsCheck) Run(ctx context.Context) CheckResult {
	if !daemon.IsRunning() {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	client := daemon.NewClient()
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

// Run checks daemon issues for dirty_workspace entries.
func (c *DaemonDirtyTeamContextCheck) Run(ctx context.Context) CheckResult {
	if !daemon.IsRunning() {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	client := daemon.NewClient()
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
		Message: fmt.Sprintf("%d with uncommitted changes", len(dirty)),
		Fix:     fmt.Sprintf("Team contexts with local edits blocking GC: %s. Commit or discard changes to allow reclone.", strings.Join(dirty, ", ")),
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

// Run executes the heartbeat check.
func (c *DaemonHeartbeatCheck) Run(ctx context.Context) CheckResult {
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
			client := daemon.NewClient()
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
