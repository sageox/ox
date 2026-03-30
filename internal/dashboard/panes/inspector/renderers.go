// Package inspector implements the right-hand detail inspector pane for the dashboard TUI.
package inspector

import (
	"fmt"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/dashboard/theme"
)

// row renders a label: value line in the inspector style.
func row(label, value string) string {
	l := theme.InspectorLabelStyle.Render(label)
	v := theme.InspectorValueStyle.Render(value)
	return l + " " + v
}

// RenderSession renders a session.SessionInfo in the inspector pane.
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
	if sess.EntryCount > 0 {
		lines = append(lines, row("Entries", fmt.Sprintf("%d", sess.EntryCount)))
	}
	if sess.Recording {
		lines = append(lines, row("Status", "● recording"))
	}
	if sess.StopReason != "" {
		lines = append(lines, row("Ended", sess.StopReason))
	}

	if sess.Summary != "" {
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

// RenderIssue renders a DaemonIssue in the inspector pane.
func RenderIssue(target domain.InspectorTarget, width int) string {
	if target.Issue == nil {
		return theme.InspectorDimStyle.Render("no issue data")
	}
	issue := target.Issue

	severityStyle := theme.InspectorValueStyle
	switch issue.Severity {
	case "critical", "error":
		severityStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5555"))
	case "warning":
		severityStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFB86C"))
	}

	var lines []string
	lines = append(lines, theme.InspectorTitleStyle.Render("Issue"))
	lines = append(lines, "")
	lines = append(lines, row("Type", issue.Type))
	lines = append(lines,
		theme.InspectorLabelStyle.Render("Severity")+" "+severityStyle.Render(issue.Severity),
	)
	lines = append(lines, row("Since", humanTime(issue.Since)))
	if issue.Repo != "" {
		lines = append(lines, row("Repo", issue.Repo))
	}
	if issue.RequiresConfirm {
		lines = append(lines, row("Auth", "human confirmation required"))
	}

	if issue.Summary != "" {
		lines = append(lines, "")
		lines = append(lines, wrapText(issue.Summary, width-2)...)
	}

	return strings.Join(lines, "\n")
}

// RenderMurmur renders a MurmurEntry in the inspector pane.
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
	if m.Topic != "" {
		lines = append(lines, row("Topic", m.Topic))
	}
	lines = append(lines, row("When", humanTime(m.Timestamp)))

	if m.Content != "" {
		lines = append(lines, "")
		lines = append(lines, wrapText(m.Content, width-2)...)
	}

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

// RenderDefault renders the "nothing selected" hint shown when no target is active.
func RenderDefault(width int) string {
	_ = width // reserved for future centering
	return theme.InspectorHintStyle.Render("← select an item in the navigator")
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
	if max <= 0 || len(path) <= max {
		return path
	}
	return "…" + path[len(path)-max+1:]
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
		} else if len(current)+1+len(w) <= width {
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
