// Package inspector implements the right-hand detail inspector pane for the dashboard TUI.
package inspector

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/dashboard/theme"
	"github.com/sageox/ox/internal/ui"
)

// row renders a label: value line in the inspector style.
func row(label, value string) string {
	l := theme.InspectorLabelStyle.Render(label)
	v := theme.InspectorValueStyle.Render(value)
	return l + " " + v
}

// RenderSession renders a session.SessionInfo in the inspector pane.
// When a summary.md path is available on the SessionInfo, it renders the full
// markdown content using glamour for a richer review experience.
func RenderSession(target domain.InspectorTarget, width int) string {
	if target.Session == nil {
		return theme.InspectorDimStyle.Render("no session data")
	}
	sess := target.Session

	var lines []string
	lines = append(lines, theme.InspectorTitleStyle.Render("Session"))
	lines = append(lines, "")

	if sess.Username != "" {
		lines = append(lines, row("User", sess.Username))
	}
	if sess.AgentID != "" {
		lines = append(lines, row("Agent", sess.AgentID))
	}
	lines = append(lines, row("Created", humanTime(sess.CreatedAt)))
	if !sess.ModTime.IsZero() && !sess.CreatedAt.IsZero() && sess.ModTime.After(sess.CreatedAt) {
		dur := sess.ModTime.Sub(sess.CreatedAt).Round(time.Second)
		lines = append(lines, row("Duration", dur.String()))
	}
	if sess.EntryCount > 0 {
		lines = append(lines, row("Entries", fmt.Sprintf("%d", sess.EntryCount)))
	}
	if sess.Recording {
		lines = append(lines, row("Status", "● recording"))
	}
	if sess.StopReason != "" {
		lines = append(lines, row("Ended", sess.StopReason))
	}

	// Render summary.md content using glamour when available.
	// summary.md lives at <session-dir>/summary.md; FilePath is the raw.jsonl path.
	// Falls back to the plain Summary field when no markdown is present.
	summaryMD := loadSessionSummaryMD(sess.FilePath)
	if summaryMD != "" {
		lines = append(lines, "")
		lines = append(lines, theme.InspectorTitleStyle.Render("Summary"))
		rendered := ui.RenderMarkdown(summaryMD)
		// Trim trailing whitespace that glamour sometimes appends.
		rendered = strings.TrimRight(rendered, "\n")
		lines = append(lines, rendered)
	} else if sess.Summary != "" {
		lines = append(lines, "")
		lines = append(lines, theme.InspectorTitleStyle.Render("Summary"))
		for _, line := range wrapText(sess.Summary, width-2) {
			lines = append(lines, theme.InspectorDimStyle.Render(line))
		}
	}

	lines = append(lines, "")
	lines = append(lines, theme.InspectorHintStyle.Render("[enter] open  [r] refresh"))
	return strings.Join(lines, "\n")
}

// RenderWorkspace renders a WorkspaceSyncStatus in the inspector pane.
func RenderWorkspace(target domain.InspectorTarget, width int) string {
	if target.Workspace == nil {
		return theme.InspectorDimStyle.Render("no workspace data")
	}
	ws := target.Workspace

	label := ws.TeamName
	if label == "" {
		label = ws.ID
	}

	var lines []string
	lines = append(lines, theme.InspectorTitleStyle.Render(label))
	lines = append(lines, "")
	lines = append(lines, row("Type", ws.Type))
	lines = append(lines, row("Path", shortenPath(ws.Path, width-14)))

	if !ws.LastSync.IsZero() {
		lines = append(lines, row("Last sync", humanTime(ws.LastSync)))
	}

	syncStatus := "✓ ok"
	if ws.LastErr != "" {
		syncStatus = "✗ " + ws.LastErr
	}
	lines = append(lines, row("Status", syncStatus))

	if ws.Syncing {
		lines = append(lines, row("", "⟳ syncing…"))
	}

	return strings.Join(lines, "\n")
}

// RenderIssue renders a DaemonIssue in the inspector pane with severity badge.
func RenderIssue(target domain.InspectorTarget, width int) string {
	if target.Issue == nil {
		return theme.InspectorDimStyle.Render("no issue data")
	}
	issue := target.Issue

	var severityStyle lipgloss.Style
	switch issue.Severity {
	case "critical":
		severityStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF5555"))
	case "error":
		severityStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5555"))
	case "warning":
		severityStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFB86C"))
	default:
		severityStyle = theme.InspectorValueStyle
	}

	badge := severityStyle.Render("[" + strings.ToUpper(issue.Severity) + "]")

	var lines []string
	lines = append(lines, theme.InspectorTitleStyle.Render("Issue"))
	lines = append(lines, "")
	lines = append(lines, theme.InspectorLabelStyle.Render("Severity")+" "+badge)
	lines = append(lines, row("Type", issue.Type))
	lines = append(lines, row("Since", humanTime(issue.Since)))
	if issue.Repo != "" {
		lines = append(lines, row("Repo", issue.Repo))
	}
	if issue.RequiresConfirm {
		lines = append(lines, row("Confirm", "human confirmation required"))
	}

	if issue.Summary != "" {
		lines = append(lines, "")
		lines = append(lines, theme.InspectorTitleStyle.Render("Details"))
		lines = append(lines, wrapText(issue.Summary, width-2)...)
	}

	if issue.RequiresConfirm {
		lines = append(lines, "")
		lines = append(lines, theme.InspectorHintStyle.Render("[enter] resolve  [r] refresh"))
	}

	return strings.Join(lines, "\n")
}

// RenderAuth renders authentication status and token expiry information.
func RenderAuth(target domain.InspectorTarget, width int) string {
	status := target.Auth

	var lines []string
	lines = append(lines, theme.InspectorTitleStyle.Render("Authentication"))
	lines = append(lines, "")

	if status == nil || !status.Running {
		lines = append(lines, theme.InspectorDimStyle.Render("daemon offline — auth status unavailable"))
		lines = append(lines, "")
		lines = append(lines, theme.InspectorHintStyle.Render("[r] ox login"))
		return strings.Join(lines, "\n")
	}

	if status.AuthenticatedUser != nil {
		lines = append(lines, row("Email", status.AuthenticatedUser.Email))
		if status.AuthenticatedUser.ID != "" {
			lines = append(lines, row("User ID", status.AuthenticatedUser.ID))
		}
	} else {
		lines = append(lines, theme.InspectorDimStyle.Render("not authenticated"))
		lines = append(lines, "")
		lines = append(lines, theme.InspectorHintStyle.Render("[r] run: ox login"))
		return strings.Join(lines, "\n")
	}

	// Show auth-related issues (expiry warnings).
	var authIssues []string
	for _, issue := range status.Issues {
		if issue.Type == "auth_expiring" || issue.Type == "auth_expired" {
			authIssues = append(authIssues, issue.Summary)
		}
	}
	if len(authIssues) > 0 {
		lines = append(lines, "")
		warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFB86C"))
		lines = append(lines, warnStyle.Render("⚠ Token expiry warning"))
		for _, msg := range authIssues {
			for _, line := range wrapText(msg, width-2) {
				lines = append(lines, theme.InspectorDimStyle.Render(line))
			}
		}
		lines = append(lines, "")
		lines = append(lines, theme.InspectorHintStyle.Render("[r] run: ox login to refresh"))
	}

	return strings.Join(lines, "\n")
}

// RenderCodeDB renders code index statistics in a stats grid.
func RenderCodeDB(target domain.InspectorTarget, width int) string {
	cdb := target.CodeDB
	if cdb == nil {
		return theme.InspectorDimStyle.Render("code index not available")
	}

	var lines []string
	title := "Code Index"
	if cdb.IndexingNow {
		title = "⟳ Code Index  (indexing…)"
	}
	lines = append(lines, theme.InspectorTitleStyle.Render(title))
	lines = append(lines, "")

	if !cdb.LastIndexed.IsZero() {
		age := time.Since(cdb.LastIndexed)
		ageStr := humanAge(age)
		stalenessIndicator := ""
		switch {
		case age > 24*time.Hour:
			stalenessIndicator = " (stale)"
		case age > 6*time.Hour:
			stalenessIndicator = " (aging)"
		}
		lines = append(lines, row("Indexed", ageStr+stalenessIndicator))
	}
	if cdb.LastError != "" {
		errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5555"))
		lines = append(lines, theme.InspectorLabelStyle.Render("Error")+" "+errStyle.Render(cdb.LastError))
	}
	lines = append(lines, "")

	// Stats grid — 2-column layout.
	lines = append(lines, theme.InspectorTitleStyle.Render("Counts"))
	lines = append(lines, row("Commits", fmt.Sprintf("%d", cdb.Commits)))
	lines = append(lines, row("Blobs", fmt.Sprintf("%d", cdb.Blobs)))
	lines = append(lines, row("Symbols", fmt.Sprintf("%d", cdb.Symbols)))
	lines = append(lines, row("Comments", fmt.Sprintf("%d", cdb.Comments)))
	lines = append(lines, row("PRs", fmt.Sprintf("%d", cdb.PRs)))
	lines = append(lines, row("Issues", fmt.Sprintf("%d", cdb.Issues)))

	// Per-repo breakdown when multiple repos are indexed.
	if len(cdb.Repos) > 0 {
		lines = append(lines, "")
		lines = append(lines, theme.InspectorTitleStyle.Render("Repos"))
		for _, r := range cdb.Repos {
			name := r.Name
			if name == "" {
				name = shortenPath(r.Path, width-20)
			}
			detail := fmt.Sprintf("%s  commits:%d  blobs:%d", name, r.Commits, r.Blobs)
			for _, line := range wrapText(detail, width-2) {
				lines = append(lines, theme.InspectorDimStyle.Render(line))
			}
		}
	}

	// Disk usage from DataDir.
	if cdb.DataDir != "" {
		var storageLines []string
		if fi, err := os.Stat(filepath.Join(cdb.DataDir, "metadata.db")); err == nil {
			storageLines = append(storageLines, row("SQLite", formatBytes(fi.Size())))
		}
		bleveDir := filepath.Join(cdb.DataDir, "bleve")
		for _, idx := range []string{"code", "diff", "comment"} {
			sz := dirSize(filepath.Join(bleveDir, idx))
			if sz > 0 {
				storageLines = append(storageLines, row("Bleve/"+idx, formatBytes(sz)))
			}
		}
		if len(storageLines) > 0 {
			lines = append(lines, "")
			lines = append(lines, theme.InspectorTitleStyle.Render("Storage"))
			lines = append(lines, storageLines...)
		}
	}

	return strings.Join(lines, "\n")
}

// RenderSyncHealth renders daemon sync health with per-workspace timing.
func RenderSyncHealth(target domain.InspectorTarget, width int) string {
	status := target.SyncHealth
	if status == nil || !status.Running {
		return theme.InspectorDimStyle.Render("daemon offline — sync status unavailable")
	}

	var lines []string
	lines = append(lines, theme.InspectorTitleStyle.Render("Sync Health"))
	lines = append(lines, "")

	// Global daemon sync stats.
	lines = append(lines, row("Total syncs", fmt.Sprintf("%d", status.TotalSyncs)))
	lines = append(lines, row("Last hour", fmt.Sprintf("%d", status.SyncsLastHour)))
	if status.AvgSyncTime > 0 {
		lines = append(lines, row("Avg sync", status.AvgSyncTime.Round(time.Millisecond).String()))
	}

	// Per-workspace breakdown.
	if len(status.Workspaces) > 0 {
		lines = append(lines, "")
		lines = append(lines, theme.InspectorTitleStyle.Render("Workspaces"))

		for wsType, wsList := range status.Workspaces {
			for _, ws := range wsList {
				label := ws.TeamName
				if label == "" {
					label = ws.ID
				}
				lines = append(lines, "")
				lines = append(lines, theme.InspectorTitleStyle.Render(label+" ("+wsType+")"))
				if !ws.LastSync.IsZero() {
					age := time.Since(ws.LastSync)
					ageStyle := syncAgeStyle(age)
					lines = append(lines,
						theme.InspectorLabelStyle.Render("Last sync")+" "+ageStyle.Render(humanAge(age)),
					)
				} else {
					lines = append(lines, row("Last sync", "never"))
				}
				if ws.Syncing {
					lines = append(lines, row("", "⟳ syncing now…"))
				}
				if ws.LastErr != "" {
					errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5555"))
					lines = append(lines,
						theme.InspectorLabelStyle.Render("Error")+" "+errStyle.Render(ws.LastErr),
					)
				}
				if !ws.LastGCTime.IsZero() {
					gcDays := ws.GCIntervalDays
					if gcDays == 0 {
						gcDays = 7 // default
					}
					lines = append(lines, row("Last GC", humanTime(ws.LastGCTime)))
					lines = append(lines, row("GC cadence", fmt.Sprintf("every %d days", gcDays)))
				}
			}
		}
	}

	return strings.Join(lines, "\n")
}

// RenderSOUL renders a SOUL.md document using glamour markdown.
func RenderSOUL(target domain.InspectorTarget, width int) string {
	soul := target.SOUL
	if soul == nil || soul.Content == "" {
		return theme.InspectorDimStyle.Render("SOUL.md not found for this team")
	}

	var lines []string
	title := "SOUL · " + soul.TeamName
	if soul.TeamName == "" {
		title = "SOUL.md"
	}
	lines = append(lines, theme.InspectorTitleStyle.Render(title))
	lines = append(lines, "")

	rendered := ui.RenderMarkdown(soul.Content)
	rendered = strings.TrimRight(rendered, "\n")
	lines = append(lines, rendered)

	return strings.Join(lines, "\n")
}

// syncAgeStyle returns a colored style based on how long ago the last sync occurred.
func syncAgeStyle(age time.Duration) lipgloss.Style {
	switch {
	case age < 10*time.Minute:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#5faf5f")) // green
	case age < time.Hour:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#FFB86C")) // yellow
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5555")) // red
	}
}

// humanAge formats a duration as a short human-readable string.
func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// RenderMurmur renders a MurmurEntry in the inspector pane with full content
// (no truncation), author info, and related-session navigation hint.
func RenderMurmur(target domain.InspectorTarget, width int) string {
	if target.Murmur == nil {
		return theme.InspectorDimStyle.Render("no murmur data")
	}
	m := target.Murmur

	var lines []string
	lines = append(lines, theme.InspectorTitleStyle.Render("Murmur"))
	lines = append(lines, "")

	if m.Author != "" {
		lines = append(lines, row("From", m.Author))
	}
	if m.AgentID != "" && m.AgentID != m.Author {
		// Show last 12 chars of AgentID to give enough context without overwhelming.
		displayID := m.AgentID
		if len(displayID) > 12 {
			displayID = "…" + displayID[len(displayID)-12:]
		}
		lines = append(lines, row("Agent", displayID))
	}
	if m.Topic != "" {
		lines = append(lines, row("Topic", m.Topic))
	}
	lines = append(lines, row("When", humanTime(m.Timestamp)))

	if m.Content != "" {
		lines = append(lines, "")
		lines = append(lines, theme.InspectorTitleStyle.Render("Content"))
		// Full content — no truncation. Word-wrap to pane width.
		lines = append(lines, wrapText(m.Content, width-2)...)
	}

	lines = append(lines, "")
	lines = append(lines, theme.InspectorHintStyle.Render("[o] open session on sageox.ai"))
	return strings.Join(lines, "\n")
}

// RenderDiscussion renders a TeamDiscussion in the inspector pane.
func RenderDiscussion(target domain.InspectorTarget, width int) string {
	if target.Discussion == nil {
		return theme.InspectorDimStyle.Render("no discussion data")
	}
	d := target.Discussion

	var lines []string
	lines = append(lines, theme.InspectorTitleStyle.Render(d.Title))
	lines = append(lines, "")
	lines = append(lines, row("Updated", humanTime(d.ModTime)))

	if d.Preview != "" {
		lines = append(lines, "")
		for _, line := range wrapText(d.Preview, width-2) {
			lines = append(lines, theme.InspectorDimStyle.Render(line))
		}
	}

	return strings.Join(lines, "\n")
}

// RenderDefault renders a dashboard health summary when nothing is selected.
// Shows daemon status, auth status, active coworkers, top issue, and key hints
// so the pane is immediately informative on first open.
func RenderDefault(status *daemon.StatusData, activeCoworkers int, width int) string {
	var lines []string
	lines = append(lines, theme.InspectorTitleStyle.Render("ox dashboard"))
	lines = append(lines, "")

	if status == nil || !status.Running {
		lines = append(lines, theme.InspectorDimStyle.Render("⬡ daemon offline"))
		lines = append(lines, "")
		lines = append(lines, theme.InspectorHintStyle.Render("Run: ox daemon start"))
	} else {
		daemonLine := "✓ daemon running"
		if status.Version != "" {
			daemonLine += "  v" + status.Version
		}
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("#5faf5f")).Render(daemonLine))

		if status.AuthenticatedUser != nil {
			lines = append(lines, row("Signed in as", status.AuthenticatedUser.Email))
		} else {
			lines = append(lines, theme.InspectorDimStyle.Render("⚠ not authenticated — run: ox login"))
		}

		if activeCoworkers > 0 {
			lines = append(lines, row("Active AI coworkers", fmt.Sprintf("%d", activeCoworkers)))
		}

		if len(status.Issues) > 0 {
			lines = append(lines, "")
			lines = append(lines, theme.InspectorTitleStyle.Render("Issues"))
			shown := status.Issues
			if len(shown) > 3 {
				shown = shown[:3]
			}
			for _, issue := range shown {
				sev := issue.Severity
				var issStyle lipgloss.Style
				switch sev {
				case "critical", "error":
					issStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5555"))
				case "warning":
					issStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFB86C"))
				default:
					issStyle = theme.InspectorDimStyle
				}
				lines = append(lines, issStyle.Render("["+strings.ToUpper(sev)+"]")+" "+theme.InspectorDimStyle.Render(issue.Type))
			}
		}
	}

	lines = append(lines, "")
	lines = append(lines, theme.InspectorTitleStyle.Render("Keys"))
	lines = append(lines, theme.InspectorHintStyle.Render("tab/shift+tab  focus pane"))
	lines = append(lines, theme.InspectorHintStyle.Render("j/k ↑/↓        navigate"))
	lines = append(lines, theme.InspectorHintStyle.Render("enter          select / inspect"))
	lines = append(lines, theme.InspectorHintStyle.Render("o              open in browser"))
	lines = append(lines, theme.InspectorHintStyle.Render("r              refresh data"))
	lines = append(lines, theme.InspectorHintStyle.Render("?              help overlay"))
	lines = append(lines, theme.InspectorHintStyle.Render("q              quit"))

	return strings.Join(lines, "\n")
}

// --- helpers ---

func humanTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	age := time.Since(t)
	switch {
	case age < time.Minute:
		return fmt.Sprintf("%ds ago", int(age.Seconds()))
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(age.Hours()))
	default:
		return t.Format("Jan 2")
	}
}

// shortenPath truncates path from the left if it exceeds max columns.
func shortenPath(path string, max int) string {
	runes := []rune(path)
	if max <= 0 || len(runes) <= max {
		return path
	}
	return "…" + string(runes[len(runes)-max+1:])
}

// loadSessionSummaryMD reads summary.md from the session directory.
// The session directory contains the session files (raw.jsonl, summary.md, etc.).
// filePath is the path to raw.jsonl or another session file; its parent is the session dir.
// Returns empty string when summary.md is not available or cannot be read.
//
// NOTE: This performs disk I/O, but is acceptable because it only runs on
// user-initiated inspector opens (selecting a specific session), not per-frame.
func loadSessionSummaryMD(filePath string) string {
	if filePath == "" {
		return ""
	}
	dir := filepath.Dir(filePath)
	summaryPath := filepath.Join(dir, "summary.md")
	data, err := os.ReadFile(summaryPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// formatBytes returns a human-readable byte size string.
func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// dirSize returns the total size of all files under path (non-recursive errors silently skipped).
// NOTE: This performs disk I/O, but is acceptable because it only runs on
// user-initiated inspector opens (e.g. inspecting a CodeDB target), not per-frame.
func dirSize(path string) int64 {
	var total int64
	_ = filepath.Walk(path, func(_ string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !fi.IsDir() {
			total += fi.Size()
		}
		return nil
	})
	return total
}

// wrapText breaks s into lines of at most width columns, splitting on word boundaries.
func wrapText(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	current := ""
	for _, w := range words {
		if current == "" {
			current = w
		} else if utf8.RuneCountInString(current)+1+utf8.RuneCountInString(w) <= width {
			current += " " + w
		} else {
			lines = append(lines, current)
			current = w
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}
