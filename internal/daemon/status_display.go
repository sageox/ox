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

// FormatStatus renders compact daemon status (Tufte-inspired: maximize data-ink ratio).
// cliVersion is the current CLI version for match comparison.
func FormatStatus(status *StatusData, cliVersion string) string {
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
	out.WriteString(formatWorkspaceGroups(status))

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
	out := FormatStatus(status, cliVersion)
	out += formatSparklineSection(history)

	// verbose internals
	out += "\n"
	out += styleBold.Render("Internals") + "\n"
	out += formatKV("PID", fmt.Sprintf("%d", status.Pid)) + "\n"
	if status.WorkspacePath != "" {
		out += formatKV("Workspace", shortenPath(status.WorkspacePath)) + "\n"
	}
	out += formatKV("Ledger", shortenPath(status.LedgerPath)) + "\n"
	if status.AuthenticatedUser != nil && status.AuthenticatedUser.Email != "" {
		out += formatKV("User", status.AuthenticatedUser.Email) + "\n"
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
		lastErrLine := "  " + styleCritical.Render("Last error: "+status.LastError)
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
func formatWorkspaceGroups(status *StatusData) string {
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

	// project section: ledger + primary team context shown as a tree
	projectCount := len(ledgers) + len(projectTCs)
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
				status: formatWSStatus(tc),
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
			out.WriteString(styleMuted.Render("    "+branch) + name + padding + formatWSStatus(tc) + "\n")
		}
	}

	return out.String()
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

		// if last sync is > 2x expected interval, something's wrong
		if sinceLast > status.SyncIntervalRead*2 {
			return HealthCritical
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

// getErrorHint returns actionable hint for common error types.
func getErrorHint(err string) string {
	switch {
	case strings.Contains(err, "authentication"):
		return "Run: git config credential.helper store"
	case strings.Contains(err, "permission denied"):
		return "Check SSH key: ssh -T git@github.com"
	case strings.Contains(err, "push failed"):
		return "Pull may be needed. Run: ox ledger sync"
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
