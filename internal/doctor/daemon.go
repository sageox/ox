package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
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

	// findProjectRoot is exposed for tests; zero-valued in production and
	// resolved to config.FindProjectRoot inside Run. See
	// alternateHeartbeatCandidate for why this exists.
	findProjectRoot func() string
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
		// The doctor CLI computes repoID/workspaceID from git's notion of
		// project root (the git top-level directory), while the daemon
		// derives its own workspace identity from config.FindProjectRoot()
		// (the directory that actually owns .sageox/) at startup. These are
		// USUALLY the same directory, but not guaranteed to be — e.g. a
		// monorepo where .sageox/ lives in a subdirectory rather than at
		// the git top-level. When they diverge, the primary heartbeatPath
		// above is a path the daemon never wrote to. Before concluding the
		// repo isn't syncing, retry against the path the daemon would
		// actually have written, derived the same way it derives it.
		findProjectRoot := c.findProjectRoot
		if findProjectRoot == nil {
			findProjectRoot = config.FindProjectRoot
		}
		if altPath := alternateHeartbeatPath(c.Type, ep, findProjectRoot); altPath != "" && altPath != heartbeatPath {
			if altEntry, altErr := daemon.ReadLastHeartbeatFromPath(altPath); altErr == nil && altEntry != nil {
				entry, err = altEntry, nil
			}
		}
	}

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

// alternateHeartbeatCandidate derives an independent (repoID, workspaceID)
// pair for a "workspace" or "ledger" heartbeat check using findProjectRoot —
// the SAME project-root-discovery function the daemon itself uses at
// startup (config.FindProjectRoot, which walks up looking for the
// .sageox-owning directory; see findProjectRootForDaemon in
// internal/daemon/fallback.go, which sets the daemon process's CWD to this
// value before it computes CurrentWorkspaceID()).
//
// The doctor CLI's primary computation instead starts from git's notion of
// project root (`git rev-parse --show-toplevel`). That is usually the same
// directory, but not guaranteed to be: whenever .sageox/ isn't at the git
// top-level (e.g. a monorepo where a project's .sageox/ lives in a
// subdirectory), the two diverge and the doctor reads a heartbeat path the
// daemon never wrote — producing a false "not syncing" warning even though
// the daemon is healthy and actively syncing this exact repo.
//
// Returns ok=false when findProjectRoot is nil, the check type isn't
// project-root-derived (team heartbeats key on team_id directly, which
// isn't subject to this divergence), or no repo_id can be found at the
// discovered root — callers should fall back to whatever the primary
// lookup found.
func alternateHeartbeatCandidate(checkType string, findProjectRoot func() string) (repoID, workspaceID string, ok bool) {
	if findProjectRoot == nil || (checkType != "workspace" && checkType != "ledger") {
		return "", "", false
	}
	root := findProjectRoot()
	if root == "" {
		return "", "", false
	}
	repoID = config.GetRepoID(root)
	if repoID == "" {
		return "", "", false
	}
	workspaceID = daemon.RepoBasedWorkspaceID(root)
	if workspaceID == "" {
		return "", "", false
	}
	return repoID, workspaceID, true
}

// alternateHeartbeatPath returns the heartbeat file path the daemon would
// have written for checkType, derived via findProjectRoot. Returns "" if no
// alternate candidate is available (see alternateHeartbeatCandidate).
func alternateHeartbeatPath(checkType, ep string, findProjectRoot func() string) string {
	repoID, workspaceID, ok := alternateHeartbeatCandidate(checkType, findProjectRoot)
	if !ok {
		return ""
	}
	if checkType == "ledger" {
		return daemon.UserLedgerHeartbeatPath(ep, repoID, workspaceID)
	}
	return daemon.UserHeartbeatPath(ep, repoID, workspaceID)
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

// FD-pressure thresholds. Two independent signals, most-severe-wins:
//
//   - Relative, as a fraction of the soft RLIMIT_NOFILE (warn 50%, fail 80%) —
//     stays meaningful across host configs (macOS default 256 vs Linux 1M+).
//   - Absolute FD counts (warn 1024, fail 4096) — a hard ceiling independent of
//     the limit. The daemon holds NO per-file watch FDs (it polls git status
//     rather than watching the tree), so a healthy steady state is dozens to
//     low-hundreds (sqlite + WAL, bleve segments, team whisper stores, the IPC
//     socket, log files). Thousands is anomalous regardless of the limit. This
//     is the signal the recursive-fsnotify-watcher regression (~11k FDs) would
//     have tripped even though the daemon inherits the shell's soft limit, which
//     on dev machines is often raised high enough to keep the percentage — and
//     thus the relative check — deceptively low.
const (
	fdPressureWarn = 0.50
	fdPressureFail = 0.80
	fdAbsoluteWarn = 1024
	fdAbsoluteFail = 4096
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
	return fdPressureVerdict(c.Name(), status.OpenFDs, status.OpenFDLimit)
}

// fdPressureVerdict computes the FD-pressure CheckResult from a sampled FD count
// and the process soft limit. Pure (no daemon/IPC) so the verdict logic is
// unit-testable without a running daemon. Returns the more severe of the
// relative (% of limit) and absolute (raw count) signals; see the threshold
// constants for why both exist.
func fdPressureVerdict(name string, openFDs int, openFDLimit uint64) CheckResult {
	if openFDs <= 0 {
		// platform doesn't surface FD count (e.g. windows) — skip silently
		return CheckResult{Name: name, Status: StatusSkip}
	}

	// Absolute signal — independent of the (possibly high, possibly unknown) limit.
	absStatus := StatusPass
	switch {
	case openFDs >= fdAbsoluteFail:
		absStatus = StatusFail
	case openFDs >= fdAbsoluteWarn:
		absStatus = StatusWarn
	}

	// Relative signal — only when the limit is known.
	relStatus := StatusPass
	msg := fmt.Sprintf("%d FDs open (limit unknown)", openFDs)
	if openFDLimit > 0 {
		pct := float64(openFDs) / float64(openFDLimit)
		msg = fmt.Sprintf("%d / %d open (%.0f%%)", openFDs, openFDLimit, pct*100)
		switch {
		case pct >= fdPressureFail:
			relStatus = StatusFail
		case pct >= fdPressureWarn:
			relStatus = StatusWarn
		}
	}

	switch moreSevereStatus(absStatus, relStatus) {
	case StatusFail:
		return CheckResult{
			Name:    name,
			Status:  StatusFail,
			Message: msg,
			Fix:     "Daemon is holding an abnormal number of file descriptors. Restart with `ox daemon restart` and investigate the leak source (recent watcher / index / store changes).",
		}
	case StatusWarn:
		return CheckResult{
			Name:    name,
			Status:  StatusWarn,
			Message: msg,
			Fix:     "FD count is elevated. Worth monitoring; check `ox status --verbose` over the next hour.",
		}
	default:
		return CheckResult{Name: name, Status: StatusPass, Message: msg}
	}
}

// moreSevereStatus returns whichever status is more severe (Fail > Warn > Pass).
func moreSevereStatus(a, b Status) Status {
	rank := func(s Status) int {
		switch s {
		case StatusFail:
			return 3
		case StatusWarn:
			return 2
		case StatusPass:
			return 1
		default:
			return 0
		}
	}
	if rank(a) >= rank(b) {
		return a
	}
	return b
}

// DaemonFDGrowthCheck warns when the daemon's open-FD count is climbing
// over time. DaemonFDPressureCheck catches a runaway leak only once it
// nears RLIMIT_NOFILE; this check catches the slow-drip case days before
// that, by comparing the current sample against a rolling history.
//
// History lives in ~/.local/state/sageox/daemon/fd-history-<workspace>.json.
// Each run appends a sample and trims to the most recent fdHistoryMaxSamples;
// the warn verdict fires only when the spread between the oldest and newest
// sample exceeds fdGrowthWarnDelta AND the oldest sample is at least
// fdGrowthMinSpan old (so a single noisy spike or a brand-new history
// doesn't trip it).
//
// Informational: there is no `--fix` for FD growth — the right response is
// to investigate, not to restart blindly.
type DaemonFDGrowthCheck struct {
	// now / historyPath are exposed for tests; both are zero-valued in
	// production and resolved to time.Now / canonical path inside Run.
	now         func() time.Time
	historyPath func(workspaceID string) string
}

// NewDaemonFDGrowthCheck creates the FD-growth check.
func NewDaemonFDGrowthCheck() *DaemonFDGrowthCheck { return &DaemonFDGrowthCheck{} }

// Name returns the check name.
func (c *DaemonFDGrowthCheck) Name() string { return "fd growth" }

// Category returns the check category.
func (c *DaemonFDGrowthCheck) Category() string { return "Daemon" }

// fdHistorySample is one sample point in the rolling history.
type fdHistorySample struct {
	At    time.Time `json:"at"`
	Count int       `json:"count"`
	PID   int       `json:"pid,omitempty"`
}

// fdHistoryFile is the on-disk schema for the rolling history.
type fdHistoryFile struct {
	WorkspaceID string            `json:"workspace_id,omitempty"`
	Samples     []fdHistorySample `json:"samples"`
}

const (
	// fdHistoryMaxSamples bounds the on-disk history. With one sample per
	// `ox doctor` run, this covers many days of history for normal use.
	fdHistoryMaxSamples = 60

	// fdGrowthMinSpan is the minimum elapsed time between the oldest
	// retained sample and the newest before a growth warning can fire.
	// Stops a fresh history (1-2 samples) from producing a false warn.
	fdGrowthMinSpan = 6 * time.Hour

	// fdGrowthWarnDelta is the absolute FD count growth (newest - oldest)
	// over the retained window that trips a warn verdict. Calibrated so
	// real-world subsystem additions don't trip it (~5 extra FDs for a
	// new persistent SQLite store is normal); a per-team or per-KB leak
	// would compound far above this bound.
	fdGrowthWarnDelta = 20
)

// Run executes the FD-growth check.
func (c *DaemonFDGrowthCheck) Run(_ context.Context, _ bool) CheckResult {
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

	now := time.Now
	if c.now != nil {
		now = c.now
	}
	historyPath := DefaultFDGrowthHistoryPath
	if c.historyPath != nil {
		historyPath = c.historyPath
	}
	workspaceID := daemon.CurrentWorkspaceID()
	path := historyPath(workspaceID)

	history, _ := loadFDHistory(path) // missing/corrupt → start fresh
	history.WorkspaceID = workspaceID
	history.Samples = append(history.Samples, fdHistorySample{
		At:    now(),
		Count: status.OpenFDs,
		PID:   status.Pid,
	})
	if len(history.Samples) > fdHistoryMaxSamples {
		history.Samples = history.Samples[len(history.Samples)-fdHistoryMaxSamples:]
	}

	// Persist BEFORE computing the verdict so the next run sees this sample
	// even on a fast doctor invocation. A failed write is non-fatal — we
	// still report the in-memory verdict.
	_ = saveFDHistory(path, history)

	return fdGrowthVerdict(c.Name(), history)
}

// samplesForCurrentLifetime returns the trailing run of samples that share
// the newest sample's PID — i.e. only the samples captured during the
// currently-running daemon process's lifetime.
//
// The daemon restarts many times over the life of a history file (each
// restart gets a fresh PID from the OS, and a fresh FD count that has
// nothing to do with the previous process's count). Comparing a sample from
// a previous process's lifetime against the current one is not a valid
// leak signal — over enough restarts it produces exactly the kind of
// "+44 FDs over 81 days" reading that looks like a slow leak but is really
// just two unrelated processes' steady-state counts happening to differ.
//
// A PID of 0 means "unknown" (a sample written before this field existed,
// or a platform that doesn't report it) and is treated as its own
// unmatched lifetime rather than silently bridging across restarts.
func samplesForCurrentLifetime(samples []fdHistorySample) []fdHistorySample {
	if len(samples) == 0 {
		return nil
	}
	currentPID := samples[len(samples)-1].PID
	start := len(samples) - 1
	for start > 0 && samples[start-1].PID == currentPID {
		start--
	}
	return samples[start:]
}

// fdGrowthVerdict computes the FD-growth CheckResult from the (already
// updated + persisted) sample history. Pure (no daemon/IPC) so the
// PID-segmentation and threshold logic is unit-testable without a running
// daemon — mirrors fdPressureVerdict.
//
// Only samples sharing the newest sample's PID are compared; see
// samplesForCurrentLifetime for why cross-restart comparisons are invalid.
func fdGrowthVerdict(name string, history fdHistoryFile) CheckResult {
	current := history.Samples[len(history.Samples)-1]
	lifetime := samplesForCurrentLifetime(history.Samples)

	if current.PID == 0 || len(lifetime) < 2 {
		return CheckResult{
			Name:    name,
			Status:  StatusPass,
			Message: fmt.Sprintf("%d FDs (history seeding)", current.Count),
		}
	}

	oldest := lifetime[0]
	newest := lifetime[len(lifetime)-1]
	span := newest.At.Sub(oldest.At)
	delta := newest.Count - oldest.Count

	msg := fmt.Sprintf("%d FDs (delta %+d over %s, %d samples this run)",
		current.Count, delta, formatDuration(span), len(lifetime))

	if span < fdGrowthMinSpan {
		// Not enough history within this daemon lifetime to call growth
		// meaningful yet.
		return CheckResult{Name: name, Status: StatusPass, Message: msg}
	}
	if delta >= fdGrowthWarnDelta {
		return CheckResult{
			Name:    name,
			Status:  StatusWarn,
			Message: msg,
			Fix: "Daemon FD count has been climbing across recent doctor runs. " +
				"Run `ox status --verbose` to inspect per-subsystem counts, and check the " +
				"daemon log for any subsystem opening per-team/per-KB handles without releasing them.",
		}
	}
	return CheckResult{Name: name, Status: StatusPass, Message: msg}
}

// DefaultFDGrowthHistoryPath returns the canonical on-disk path for the
// rolling FD-growth history file. Lives under DataDir (persistent across
// reboots, unlike DaemonStateDir which is XDG_RUNTIME_DIR-rooted) so the
// growth signal accumulates over weeks. Exposed so the doctor wiring and
// tests can agree on the location without duplicating the layout.
func DefaultFDGrowthHistoryPath(workspaceID string) string {
	dir := filepath.Join(paths.DataDir(), "doctor")
	name := "fd-history.json"
	if workspaceID != "" {
		name = "fd-history-" + workspaceID + ".json"
	}
	return filepath.Join(dir, name)
}

// loadFDHistory reads a saved history file. Missing / unreadable / corrupt
// files return an empty history with no error — this is a best-effort cache,
// not authoritative state.
func loadFDHistory(path string) (fdHistoryFile, error) {
	var h fdHistoryFile
	data, err := os.ReadFile(path)
	if err != nil {
		return h, err
	}
	if err := json.Unmarshal(data, &h); err != nil {
		return fdHistoryFile{}, err
	}
	return h, nil
}

// saveFDHistory writes the history file atomically (write-temp + rename) so
// a concurrent reader never sees a half-written JSON document.
func saveFDHistory(path string, h fdHistoryFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
