package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/tui"
)

// --- JSON output types (mirror the human-readable status) ---

// StatusJSON is the top-level JSON output for `ox daemon status --json`.
// It mirrors the information shown in the human-readable output.
type StatusJSON struct {
	Running bool   `json:"running"`
	Health  string `json:"health"`
	Pid     int    `json:"pid"`
	Version string `json:"version"`
	Uptime  string `json:"uptime"`

	// backward-compatible top-level aliases (deprecated; use project.ledger)
	LedgerPath string     `json:"ledger_path,omitempty"`
	LastSync   *time.Time `json:"last_sync,omitempty"`

	Sync statusSyncJSON `json:"sync"`

	Project    statusProjectGroupJSON  `json:"project"`
	OtherTeams []statusTeamContextJSON `json:"other_teams,omitempty"`
	Issues     []statusIssueJSON       `json:"issues,omitempty"`
	AutoExitIn string                  `json:"auto_exit_in,omitempty"`
}

type statusSyncJSON struct {
	Repos      int    `json:"repos"`
	Interval   string `json:"interval"`
	TotalSyncs int    `json:"total_syncs"`
	AvgTime    string `json:"avg_time,omitempty"`
	Errors     int    `json:"errors"`
	LastError  string `json:"last_error,omitempty"`
}

type statusProjectGroupJSON struct {
	Ledger      *statusWorkspaceJSON  `json:"ledger,omitempty"`
	TeamContext *statusTeamContextJSON `json:"team_context,omitempty"`
	CodeIndex   *statusCodeIndexJSON  `json:"code_index,omitempty"`
}

type statusWorkspaceJSON struct {
	Status   string     `json:"status"`
	Path     string     `json:"path"`
	LastSync *time.Time `json:"last_sync,omitempty"`
}

type statusTeamContextJSON struct {
	TeamID   string     `json:"team_id"`
	TeamName string     `json:"team_name"`
	Status   string     `json:"status"`
	Path     string     `json:"path"`
	LastSync *time.Time `json:"last_sync,omitempty"`
	GCStatus string     `json:"gc_status,omitempty"`
}

type statusCodeIndexJSON struct {
	Status      string     `json:"status"`
	LastIndexed *time.Time `json:"last_indexed,omitempty"`
	Commits     int        `json:"commits,omitempty"`
	Blobs       int        `json:"blobs,omitempty"`
	PRs         int        `json:"prs,omitempty"`
	Issues      int        `json:"issues,omitempty"`
	Error       string     `json:"error,omitempty"`
}

type statusIssueJSON struct {
	Type     string `json:"type"`
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
	Repo     string `json:"repo,omitempty"`
}

// BuildStatusJSON constructs the curated JSON output that mirrors the human-readable status.
func BuildStatusJSON(status *StatusData, cliVersion string) *StatusJSON {
	health := determineHealth(status)
	var healthStr string
	switch health {
	case HealthHealthy:
		healthStr = "healthy"
	case HealthWarning:
		healthStr = "warning"
	case HealthCritical:
		healthStr = "critical"
	}

	out := &StatusJSON{
		Running: status.Running,
		Health:  healthStr,
		Pid:     status.Pid,
		Version: semverOnly(status.Version),
		Uptime:  formatDurationCompact(status.Uptime),
	}

	// sync summary
	out.Sync = statusSyncJSON{
		Repos:      countWorkspaces(status),
		TotalSyncs: status.TotalSyncs,
		Errors:     status.RecentErrorCount,
		LastError:  status.LastError,
	}
	if status.SyncIntervalRead > 0 {
		out.Sync.Interval = formatDurationCompact(status.SyncIntervalRead)
	}
	if status.AvgSyncTime > 0 {
		out.Sync.AvgTime = formatDurationCompact(status.AvgSyncTime)
	}

	// project group
	ledgers := status.Workspaces["ledger"]
	teamContexts := status.Workspaces["team-context"]

	if len(ledgers) > 0 {
		l := ledgers[0]
		ws := &statusWorkspaceJSON{
			Status: wsStatusString(l),
			Path:   l.Path,
		}
		if !l.LastSync.IsZero() {
			ws.LastSync = &l.LastSync
		}
		out.Project.Ledger = ws
		out.LedgerPath = ws.Path
		out.LastSync = ws.LastSync
	} else if status.LedgerPath != "" {
		// fallback for CLI/daemon version skew
		out.LedgerPath = status.LedgerPath
		if !status.LastSync.IsZero() {
			t := status.LastSync
			out.LastSync = &t
		}
	}

	for _, tc := range teamContexts {
		if status.ProjectTeamID != "" && tc.TeamID == status.ProjectTeamID {
			out.Project.TeamContext = buildTeamContextJSON(tc)
			break
		}
	}

	if status.CodeDB != nil {
		out.Project.CodeIndex = buildCodeIndexJSON(status.CodeDB)
	}

	// other teams
	for _, tc := range teamContexts {
		if status.ProjectTeamID != "" && tc.TeamID == status.ProjectTeamID {
			continue
		}
		out.OtherTeams = append(out.OtherTeams, *buildTeamContextJSON(tc))
	}

	// issues
	for _, issue := range status.Issues {
		out.Issues = append(out.Issues, statusIssueJSON{
			Type:     issue.Type,
			Severity: issue.Severity,
			Summary:  issue.Summary,
			Repo:     issue.Repo,
		})
	}

	// auto-exit
	if status.InactivityTimeout > 0 {
		remaining := status.InactivityTimeout - status.TimeSinceActivity
		if remaining < 0 {
			remaining = 0
		}
		out.AutoExitIn = formatDurationCompact(remaining)
	}

	return out
}

func wsStatusString(ws WorkspaceSyncStatus) string {
	switch {
	case !ws.Exists:
		return "not_cloned"
	case ws.LastErr != "":
		return "error"
	case ws.Syncing:
		return "syncing"
	case ws.LastSync.IsZero():
		return "not_synced"
	default:
		return "ok"
	}
}

func buildTeamContextJSON(tc WorkspaceSyncStatus) *statusTeamContextJSON {
	name := tc.TeamName
	if name == "" {
		name = tc.TeamID
	}
	out := &statusTeamContextJSON{
		TeamID:   tc.TeamID,
		TeamName: name,
		Status:   wsStatusString(tc),
		Path:     tc.Path,
		GCStatus: gcStatusString(tc),
	}
	if !tc.LastSync.IsZero() {
		out.LastSync = &tc.LastSync
	}
	return out
}

func gcStatusString(ws WorkspaceSyncStatus) string {
	if ws.LastGCTime.IsZero() {
		return "due"
	}
	intervalDays := ws.GCIntervalDays
	if intervalDays <= 0 {
		intervalDays = 7
	}
	age := time.Since(ws.LastGCTime)
	interval := time.Duration(intervalDays) * 24 * time.Hour
	remaining := interval - age
	if remaining <= 0 {
		return "due"
	}
	return "in " + formatDurationCompact(remaining)
}

func buildCodeIndexJSON(cs *CodeDBStats) *statusCodeIndexJSON {
	out := &statusCodeIndexJSON{}
	switch {
	case cs.IndexingNow:
		out.Status = "indexing"
	case !cs.IndexExists:
		out.Status = "pending"
	case cs.LastError != "" && cs.Commits == 0:
		out.Status = "pending"
	case cs.LastError != "":
		out.Status = "error"
		out.Error = cs.LastError
	case !cs.LastIndexed.IsZero():
		out.Status = "ok"
		t := cs.LastIndexed
		out.LastIndexed = &t
		out.Commits = cs.Commits
		out.Blobs = cs.Blobs
		out.PRs = cs.PRs
		out.Issues = cs.Issues
	default:
		out.Status = "ready"
	}
	return out
}

var (
	// semantic colors for status
	colorHealthy  = lipgloss.Color("2") // green
	colorWarning  = lipgloss.Color("3") // yellow
	colorCritical = lipgloss.Color("1") // red
	colorMuted    = lipgloss.Color("8") // gray

	// styles
	styleHealthy  = lipgloss.NewStyle().Foreground(colorHealthy).Bold(true)
	styleWarning  = lipgloss.NewStyle().Foreground(colorWarning).Bold(true)
	styleCritical = lipgloss.NewStyle().Foreground(colorCritical).Bold(true)
	styleMuted    = lipgloss.NewStyle().Foreground(colorMuted)
	styleBold     = lipgloss.NewStyle().Bold(true)
	styleLabel    = lipgloss.NewStyle().Foreground(colorMuted)
)

// HealthStatus represents overall daemon health.
type HealthStatus int

const (
	HealthHealthy HealthStatus = iota
	HealthWarning
	HealthCritical
)

// FormatNotRunning renders the "daemon not running" state with context.
// inProject indicates whether the user is inside an initialized SageOx project.
func FormatNotRunning(inProject bool) string {
	var out strings.Builder

	out.WriteString(styleCritical.Render("○ Daemon: Not running"))
	out.WriteString("\n\n")

	if inProject {
		out.WriteString(styleMuted.Render("  No active AI coworker session detected."))
		out.WriteString("\n")
		out.WriteString(styleMuted.Render("  The daemon auto-starts when a coding agent begins working."))
		out.WriteString("\n\n")
		out.WriteString(styleMuted.Render("  To start manually: "))
		out.WriteString(styleHealthy.Render("ox daemon start"))
		out.WriteString("\n")
	} else {
		out.WriteString(styleMuted.Render("  Not inside an initialized SageOx project."))
		out.WriteString("\n")
		out.WriteString(styleMuted.Render("  Run 'ox init' in a git repo to get started."))
		out.WriteString("\n")
	}

	return out.String()
}

// FormatStarting renders the daemon status when the process exists but IPC isn't ready yet.
func FormatStarting() string {
	var out strings.Builder
	out.WriteString(styleWarning.Render("◐ Daemon: Starting"))
	out.WriteString("\n\n")
	out.WriteString(styleMuted.Render("  Process is running but not yet accepting connections."))
	out.WriteString("\n")
	out.WriteString(styleMuted.Render("  This may take a few seconds (longer if recently restarted)."))
	out.WriteString("\n")
	return out.String()
}

// FormatStuck renders the daemon status when the process is alive but has exceeded the
// startup window without accepting IPC connections — indicating it is hung.
func FormatStuck() string {
	var out strings.Builder
	out.WriteString(styleCritical.Render("⚠ Daemon: Stuck"))
	out.WriteString("\n\n")
	out.WriteString(styleMuted.Render("  Process is running but not accepting connections."))
	out.WriteString("\n")
	out.WriteString(styleMuted.Render("  The process has been starting for longer than expected."))
	out.WriteString("\n\n")
	out.WriteString(styleMuted.Render("  To fix: "))
	out.WriteString(styleHealthy.Render("ox daemon restart"))
	out.WriteString("\n")
	return out.String()
}

// FormatStatus renders compact daemon status (Tufte-inspired: maximize data-ink ratio).
// cliVersion is the current CLI version for match comparison.
func FormatStatus(status *StatusData, cliVersion string) string {
	return formatStatusCore(status, cliVersion, false)
}

func formatStatusCore(status *StatusData, cliVersion string, verbose bool) string {
	var out strings.Builder

	health := determineHealth(status)

	// line 1: ● Healthy — 3m uptime, v0.2.0 ✓ (matching)
	out.WriteString(formatHeader(health, status, cliVersion))
	out.WriteString("\n\n")

	// line 2: operational summary
	out.WriteString(formatSummaryLine(status))
	out.WriteString("\n")

	// errors (only when present)
	if status.RecentErrorCount > 0 || status.LastError != "" {
		out.WriteString(formatErrors(status))
	}

	// workspace sync groups
	out.WriteString(formatWorkspaceGroups(status, verbose))

	// code index is now part of the "This Project" tree — see formatWorkspaceGroups

	// issues (if any)
	if issues := formatIssues(status.NeedsHelp, status.Issues); issues != "" {
		out.WriteString("\n")
		out.WriteString(issues)
	}

	// auto-exit (compact)
	if status.InactivityTimeout > 0 {
		remaining := status.InactivityTimeout - status.TimeSinceActivity
		if remaining < 0 {
			remaining = 0
		}
		out.WriteString("\n")
		out.WriteString(styleMuted.Render("  Auto-exit in " + formatDurationCompact(remaining)))
		out.WriteString("\n")
	}

	return out.String()
}

// FormatStatusWithSparkline adds a 4h activity sparkline to the status output.
func FormatStatusWithSparkline(status *StatusData, history []SyncEvent, cliVersion string) string {
	out := FormatStatus(status, cliVersion)
	out += formatSparklineSection(history)
	return out
}

// FormatStatusVerbose includes sparkline, internals, and sync history table.
func FormatStatusVerbose(status *StatusData, history []SyncEvent, cliVersion string) string {
	out := formatStatusCore(status, cliVersion, true)
	out += formatSparklineSection(history)

	// verbose internals
	out += "\n"
	out += styleBold.Render("Internals") + "\n"
	out += formatKV("PID", fmt.Sprintf("%d", status.Pid)) + "\n"
	if status.WorkspacePath != "" {
		out += formatKV("Workspace", shortenPath(status.WorkspacePath)) + "\n"
	}
	if len(status.Callers) > 0 {
		for _, c := range status.Callers {
			label := shortenPath(c.Path)
			if c.AgentID != "" {
				label += fmt.Sprintf(" (%s)", c.AgentID)
			}
			out += formatKV("  Clone", label) + "\n"
		}
	}
	out += formatKV("Ledger", shortenPath(status.LedgerPath)) + "\n"
	if status.AuthenticatedUser != nil && status.AuthenticatedUser.Email != "" {
		out += formatKV("User", status.AuthenticatedUser.Email) + "\n"
	}
	if status.StartupDurationMs > 0 {
		startupStr := fmt.Sprintf("%dms", status.StartupDurationMs)
		if status.ThrottleDurationMs > 0 {
			startupStr += fmt.Sprintf(" (throttled %dms)", status.ThrottleDurationMs)
		}
		out += formatKV("Startup", startupStr) + "\n"
	}

	// activity details
	if activity := formatActivity(status.Activity); activity != "" {
		out += "\n" + activity
	}

	if len(history) > 0 {
		out += "\n" + formatSyncHistory(history)
	}

	return out
}

func formatSparklineSection(history []SyncEvent) string {
	if len(history) == 0 {
		return ""
	}
	timestamps := make([]time.Time, len(history))
	for i, e := range history {
		timestamps[i] = e.Time
	}
	var out strings.Builder
	out.WriteString("\n")
	sparkline := styleMuted.Render(tui.RenderSparkline(timestamps, tui.SparklineBuckets, tui.SparklineWindow))
	out.WriteString("  " + styleBold.Render("Activity (4h)") + "  " + sparkline)
	out.WriteString("\n")
	out.WriteString("                 " + styleMuted.Render(tui.RenderSparklineTimeMarkers()))
	out.WriteString("\n")
	return out.String()
}

func formatHeader(health HealthStatus, status *StatusData, cliVersion string) string {
	var indicator, statusText string
	var style lipgloss.Style

	switch health {
	case HealthHealthy:
		indicator = "●"
		statusText = "Healthy"
		style = styleHealthy
	case HealthWarning:
		indicator = "◐"
		statusText = "Warning"
		style = styleWarning
	case HealthCritical:
		indicator = "○"
		statusText = "Critical"
		style = styleCritical
	}

	uptime := formatDurationCompact(status.Uptime)
	versionMatch := formatVersionMatch(status.Version, cliVersion)

	return style.Render(fmt.Sprintf("%s %s", indicator, statusText)) +
		styleMuted.Render(fmt.Sprintf(" — %s uptime, v%s ", uptime, semverOnly(status.Version))) +
		versionMatch
}

// formatVersionMatch compares daemon and CLI semver, returns styled match indicator.
func formatVersionMatch(daemonVersion, cliVersion string) string {
	dv := semverOnly(daemonVersion)
	cv := semverOnly(cliVersion)
	if dv == cv {
		return styleHealthy.Render("✓") + " " + styleMuted.Render("(matching)")
	}
	return styleWarning.Render("⚠") + " " + styleWarning.Render(fmt.Sprintf("(mismatch: daemon v%s)", dv))
}

// semverOnly strips build metadata (everything after +) from a version string.
func semverOnly(v string) string {
	if idx := strings.Index(v, "+"); idx >= 0 {
		return v[:idx]
	}
	return v
}

// formatSummaryLine renders a single operational summary line.
func formatSummaryLine(status *StatusData) string {
	wsCount := countWorkspaces(status)
	var parts []string

	// sync interval
	if status.SyncIntervalRead > 0 {
		parts = append(parts, fmt.Sprintf("every %s", formatDurationCompact(status.SyncIntervalRead)))
	}

	// total syncs
	if status.TotalSyncs > 0 {
		parts = append(parts, fmt.Sprintf("%d syncs", status.TotalSyncs))
	}

	// avg duration
	if status.AvgSyncTime > 0 {
		parts = append(parts, formatDurationCompact(status.AvgSyncTime)+" avg")
	}

	// errors
	if status.RecentErrorCount > 0 {
		parts = append(parts, styleCritical.Render(fmt.Sprintf("%d errors", status.RecentErrorCount)))
	} else {
		parts = append(parts, "0 errors")
	}

	prefix := fmt.Sprintf("  Syncing %d repos", wsCount)
	if len(parts) > 0 {
		return styleMuted.Render(prefix) + " " + styleMuted.Render(strings.Join(parts, ", "))
	}
	return styleMuted.Render(prefix)
}

// countWorkspaces counts total workspaces being synced.
func countWorkspaces(status *StatusData) int {
	count := 0
	for _, wsList := range status.Workspaces {
		count += len(wsList)
	}
	return count
}

// formatErrors renders error details (only called when errors exist).
func formatErrors(status *StatusData) string {
	var out strings.Builder

	if status.LastError != "" {
		out.WriteString("\n")
		lastErrLine := "  " + styleCritical.Render("Last error: "+humanizeError(status.LastError))
		if status.LastErrorTime != "" {
			if t, err := time.Parse(time.RFC3339, status.LastErrorTime); err == nil {
				lastErrLine += styleMuted.Render(" (" + formatRelativeTime(time.Since(t)) + ")")
			}
		}
		out.WriteString(lastErrLine)
		out.WriteString("\n")

		hint := getErrorHint(status.LastError)
		if hint != "" {
			out.WriteString(styleMuted.Render("  Hint: " + hint))
			out.WriteString("\n")
		}
	}

	return out.String()
}

// formatWorkspaceGroups renders workspaces grouped: project (ledger + primary team) then other teams.
// Uses tree visualization to make the relationship between ledger and team context clear.
func formatWorkspaceGroups(status *StatusData, verbose bool) string {
	var out strings.Builder

	ledgers := status.Workspaces["ledger"]
	teamContexts := status.Workspaces["team-context"]

	// partition team contexts into project (primary) vs other
	var projectTCs, otherTCs []WorkspaceSyncStatus
	for _, tc := range teamContexts {
		if status.ProjectTeamID != "" && tc.TeamID == status.ProjectTeamID {
			projectTCs = append(projectTCs, tc)
		} else {
			otherTCs = append(otherTCs, tc)
		}
	}

	// project section: ledger + primary team context + code index shown as a tree
	projectCount := len(ledgers) + len(projectTCs)
	hasCodeIndex := status.CodeDB != nil
	if hasCodeIndex {
		projectCount++
	}
	if projectCount > 0 {
		out.WriteString("\n")
		out.WriteString(styleBold.Render("This Project"))
		out.WriteString("\n")

		// collect project items for tree rendering
		type treeItem struct {
			label  string
			status string
		}
		var items []treeItem

		for _, l := range ledgers {
			items = append(items, treeItem{
				label:  "Ledger",
				status: formatWSStatus(l),
			})
		}
		for _, tc := range projectTCs {
			name := tc.TeamName
			if name == "" {
				name = tc.TeamID
			}
			items = append(items, treeItem{
				label:  name + styleMuted.Render(" (team context)"),
				status: formatWSStatus(tc) + formatGCStatus(tc, verbose),
			})
		}

		// code index as tree item
		if hasCodeIndex {
			items = append(items, treeItem{
				label:  "Code index",
				status: formatCodeIndexInline(status, verbose),
			})
		}

		// compute alignment width
		alignWidth := 0
		for _, item := range items {
			// use raw label length for alignment (strip ANSI for measurement)
			rawLen := len(stripANSI(item.label))
			if rawLen > alignWidth {
				alignWidth = rawLen
			}
		}
		alignWidth += 2 // padding

		for i, item := range items {
			branch := "├── "
			if i == len(items)-1 {
				branch = "└── "
			}
			rawLen := len(stripANSI(item.label))
			padding := strings.Repeat(" ", alignWidth-rawLen)
			out.WriteString(styleMuted.Render("    "+branch) + item.label + padding + item.status + "\n")
		}
	}

	// other team contexts
	if len(otherTCs) > 0 {
		out.WriteString("\n")
		out.WriteString(styleBold.Render("Other Team Contexts"))
		out.WriteString(styleMuted.Render(fmt.Sprintf(" (%d)", len(otherTCs))))
		out.WriteString("\n")

		// compute alignment width
		alignWidth := 0
		for _, tc := range otherTCs {
			name := tc.TeamName
			if name == "" {
				name = tc.TeamID
			}
			if len(name) > alignWidth {
				alignWidth = len(name)
			}
		}
		alignWidth += 2 // padding

		for i, tc := range otherTCs {
			name := tc.TeamName
			if name == "" {
				name = tc.TeamID
			}
			branch := "├── "
			if i == len(otherTCs)-1 {
				branch = "└── "
			}
			padding := strings.Repeat(" ", alignWidth-len(name))
			out.WriteString(styleMuted.Render("    "+branch) + name + padding + formatWSStatus(tc) + formatGCStatus(tc, verbose) + "\n")
		}
	}

	return out.String()
}

// formatCodeIndexInline renders a compact single-line code index status for tree display.
func formatCodeIndexInline(status *StatusData, verbose bool) string {
	cs := status.CodeDB
	if cs == nil {
		return styleMuted.Render("○ unknown")
	}

	var line string
	switch {
	case cs.IndexingNow:
		line = styleWarning.Render("◐ indexing…")
	case !cs.IndexExists:
		line = styleWarning.Render("○ pending")
	case cs.LastError != "" && cs.Commits == 0:
		line = styleWarning.Render("○ pending")
	case cs.LastError != "":
		line = styleCritical.Render("○ " + humanizeError(cs.LastError))
	case !cs.LastIndexed.IsZero():
		age := formatRelativeTime(time.Since(cs.LastIndexed))
		metrics := fmt.Sprintf("%d commits, %d blobs", cs.Commits, cs.Blobs)
		if cs.PRs > 0 || cs.Issues > 0 {
			metrics += fmt.Sprintf(", %d PRs, %d issues", cs.PRs, cs.Issues)
		}
		line = styleHealthy.Render("● "+age) + "  " + styleMuted.Render(metrics)
	default:
		line = styleHealthy.Render("● ready")
	}

	if verbose && cs.IndexExists && cs.Commits > 0 {
		if cs.DataDir != "" {
			line += "\n" + styleMuted.Render("              "+shortenPath(cs.DataDir))
		}
		for _, r := range cs.Repos {
			line += "\n" + styleMuted.Render(fmt.Sprintf("              %s  %d commits, %d blobs", r.Name, r.Commits, r.Blobs))
		}
	}

	return line
}

// stripANSI removes ANSI escape sequences for measuring display width.
func stripANSI(s string) string {
	var result strings.Builder
	inEscape := false
	for _, r := range s {
		if r == '\033' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		result.WriteRune(r)
	}
	return result.String()
}

// formatWSStatus renders sync status for a single workspace (compact: icon + time).
func formatWSStatus(ws WorkspaceSyncStatus) string {
	if !ws.Exists {
		return styleMuted.Render("○ not cloned")
	}
	if ws.LastErr != "" {
		return styleCritical.Render("○ " + ws.LastErr)
	}
	if ws.Syncing {
		return styleWarning.Render("◐ syncing...")
	}
	if ws.LastSync.IsZero() {
		return styleWarning.Render("◐ not synced")
	}
	return styleHealthy.Render("● " + formatRelativeTime(time.Since(ws.LastSync)))
}

// formatGCStatus renders GC timing suffix for team context items.
// Leads with the actionable info: when the next GC will happen.
//
//	Default:  "· gc in 4d"              (countdown to next reclone)
//	Verbose:  "· gc in 4d (last 3d ago)" (countdown + history)
//	Overdue:  "· gc due"                (interval exceeded, runs on next hourly check)
//	Never:    "· gc due"                (first GC, runs on next hourly check)
func formatGCStatus(ws WorkspaceSyncStatus, verbose bool) string {
	if ws.LastGCTime.IsZero() {
		// GC has never run — will trigger on the next hourly check cycle
		return styleMuted.Render(" · gc due")
	}

	intervalDays := ws.GCIntervalDays
	if intervalDays <= 0 {
		intervalDays = 7 // DefaultGCIntervalDays
	}

	age := time.Since(ws.LastGCTime)
	interval := time.Duration(intervalDays) * 24 * time.Hour
	remaining := interval - age

	var s string
	if remaining <= 0 {
		s = styleMuted.Render(" · gc due")
	} else {
		s = styleMuted.Render(" · gc in " + formatRelativeTime(remaining))
	}

	if verbose {
		s += styleMuted.Render(" (last " + formatRelativeTime(age) + " ago)")
	}

	return s
}

// formatActivity renders heartbeat activity tracking (verbose only).
func formatActivity(activity *ActivitySummary) string {
	if activity == nil {
		return ""
	}

	type category struct {
		label   string
		entries []ActivityEntry
	}

	categories := []category{
		{"Repos", activity.Repos},
		{"Teams", activity.Teams},
		{"Agents", activity.Agents},
	}

	// check if all categories are empty
	hasAny := false
	for _, c := range categories {
		if len(c.entries) > 0 {
			hasAny = true
			break
		}
	}
	if !hasAny {
		return ""
	}

	var out strings.Builder
	out.WriteString(styleBold.Render("Activity"))
	out.WriteString("\n")

	for _, c := range categories {
		if len(c.entries) == 0 {
			continue
		}
		// find most recent across all entries in this category
		var mostRecent time.Time
		for _, e := range c.entries {
			if e.Last.After(mostRecent) {
				mostRecent = e.Last
			}
		}
		val := fmt.Sprintf("%d", len(c.entries))
		if !mostRecent.IsZero() {
			val += " (last " + formatRelativeTime(time.Since(mostRecent)) + ")"
		}
		out.WriteString(formatKV(c.label, val))
		out.WriteString("\n")
	}

	return out.String()
}

// formatIssues renders daemon issues that need attention.
func formatIssues(needsHelp bool, issues []DaemonIssue) string {
	if !needsHelp && len(issues) == 0 {
		return ""
	}

	var out strings.Builder
	out.WriteString(styleBold.Render("Issues"))
	out.WriteString(styleMuted.Render(fmt.Sprintf(" (%d)", len(issues))))
	out.WriteString("\n")

	for _, issue := range issues {
		var icon string
		var style lipgloss.Style
		switch issue.Severity {
		case "warning":
			icon = "⚠"
			style = styleWarning
		default: // error, critical
			icon = "○"
			style = styleCritical
		}

		line := fmt.Sprintf("  %s %-10s ", icon, issue.Severity)
		desc := issue.Summary
		if issue.Repo != "" {
			desc = issue.Repo + ": " + desc
		}
		if !issue.Since.IsZero() {
			desc += " (since " + formatRelativeTime(time.Since(issue.Since)) + ")"
		}
		line += desc

		out.WriteString(style.Render(line))
		out.WriteString("\n")
	}

	return out.String()
}

// formatSyncHistory renders recent sync history as a table (for verbose mode).
func formatSyncHistory(history []SyncEvent) string {
	var out strings.Builder

	out.WriteString(styleBold.Render("Recent Syncs"))
	out.WriteString(styleMuted.Render(" (last 10)"))
	out.WriteString("\n\n")

	// header
	fmt.Fprintf(&out, "%s\n", styleLabel.Render(fmt.Sprintf("  %-12s %-8s %-10s %s",
		"Time", "Type", "Duration", "Files")))

	// show last 10
	start := len(history) - 10
	if start < 0 {
		start = 0
	}

	for i := start; i < len(history); i++ {
		event := history[i]
		timeStr := event.Time.Format("15:04:05")
		typeStr := event.Type
		durationStr := formatDurationCompact(event.Duration)
		filesStr := fmt.Sprintf("%d", event.FilesChanged)

		fmt.Fprintf(&out, "  %-12s %-8s %-10s %s\n",
			timeStr, typeStr, durationStr, filesStr)
	}

	return out.String()
}

// determineHealth calculates overall health status.
func determineHealth(status *StatusData) HealthStatus {
	// critical: many recent errors or sync very stale
	if status.RecentErrorCount >= 5 {
		return HealthCritical
	}

	if !status.LastSync.IsZero() {
		sinceLast := time.Since(status.LastSync)

		// stale sync is a warning, not critical — syncs can lag during
		// GC, indexing, or slow networks without indicating a real problem
		if sinceLast > status.SyncIntervalRead*4 {
			return HealthWarning
		}
	}

	// warning: some errors
	if status.RecentErrorCount > 0 {
		return HealthWarning
	}

	// healthy
	return HealthHealthy
}

// formatKV formats a key-value pair with consistent spacing.
func formatKV(key, value string) string {
	return fmt.Sprintf("  %s %s",
		styleLabel.Render(fmt.Sprintf("%-12s", key+":")),
		value,
	)
}

// formatDurationCompact formats a duration in compact form (e.g., "2h 30m").
func formatDurationCompact(d time.Duration) string {
	d = d.Round(time.Second)

	if d < time.Second {
		return "<1s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm %ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

// formatRelativeTime formats a duration as relative time (e.g., "5m ago").
func formatRelativeTime(d time.Duration) string {
	d = d.Round(time.Second)

	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", h)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1d ago"
		}
		return fmt.Sprintf("%dd ago", days)
	}
}

// shortenPath replaces home directory with ~ for display.
func shortenPath(path string) string {
	if path == "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// humanizeError translates raw git/system errors into concise user-friendly messages.
// Returns the original string if no translation matches.
func humanizeError(raw string) string {
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "resolving timed out") || strings.Contains(lower, "could not resolve host"):
		return "DNS resolution timed out (network may be offline or DNS is unreachable)"
	case strings.Contains(lower, "connection timed out") || strings.Contains(lower, "operation timed out"):
		return "Connection timed out (network may be offline or server is unreachable)"
	case strings.Contains(lower, "unable to access") && strings.Contains(lower, "timed out"):
		return "Git sync timed out (network may be offline or server is unreachable)"
	case strings.Contains(lower, "repository does not exist") || strings.Contains(lower, "repository not found"):
		// extract the path from "open local repo /path: repository does not exist"
		if idx := strings.Index(raw, "open local repo "); idx >= 0 {
			rest := raw[idx+len("open local repo "):]
			if colonIdx := strings.Index(rest, ":"); colonIdx >= 0 {
				path := rest[:colonIdx]
				return fmt.Sprintf("Project path no longer exists: %s (workspace may have moved)", filepath.Base(path))
			}
		}
		return "Repository path no longer exists (workspace may have moved)"
	case strings.Contains(lower, "no such host"):
		return "Server unreachable (DNS lookup failed)"
	case strings.Contains(lower, "connection refused"):
		return "Server unreachable (connection refused)"
	default:
		return raw
	}
}

// getErrorHint returns actionable hint for common error types.
func getErrorHint(err string) string {
	lower := strings.ToLower(err)
	switch {
	case strings.Contains(lower, "authentication"):
		return "Run: git config credential.helper store"
	case strings.Contains(lower, "permission denied"):
		return "Check SSH key: ssh -T git@github.com"
	case strings.Contains(lower, "push failed"):
		return "Pull may be needed. Run: ox ledger sync"
	case strings.Contains(lower, "timed out") || strings.Contains(lower, "no such host") || strings.Contains(lower, "connection refused"):
		return "Will retry automatically. Check network connectivity if this persists."
	case strings.Contains(lower, "repository does not exist") || strings.Contains(lower, "no longer exists"):
		return "The daemon will pick up the new workspace path on next heartbeat."
	default:
		return ""
	}
}

// FormatDaemonList formats a list of daemons for display.
func FormatDaemonList(daemons []DaemonInfo) string {
	if len(daemons) == 0 {
		return styleMuted.Render("No ox daemons running") + "\n"
	}

	var out strings.Builder

	out.WriteString(styleBold.Render(fmt.Sprintf("Running Daemons (%d)", len(daemons))))
	out.WriteString("\n")

	// header
	out.WriteString(styleLabel.Render(fmt.Sprintf("%-42s %-8s %-10s %s",
		"Workspace", "PID", "Version", "Uptime")))
	out.WriteString("\n")

	for _, d := range daemons {
		uptime := formatDurationCompact(time.Since(d.StartedAt))
		workspace := shortenPath(d.WorkspacePath)
		if len(workspace) > 40 {
			workspace = "..." + filepath.Base(d.WorkspacePath)
		}
		fmt.Fprintf(&out, "%-42s %-8d %-10s %s\n",
			workspace, d.PID, d.Version, uptime)
	}

	return out.String()
}
