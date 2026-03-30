// Package statusbar implements the single-line status bar shown at the bottom
// of the dashboard TUI. It summarises overall health, active issue count, and
// transient status messages without consuming more than one terminal row.
package statusbar

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/dashboard/panes"
	"github.com/sageox/ox/internal/dashboard/theme"
)

// compile-time interface check
var _ panes.Pane = (*Pane)(nil)

// Pane implements the bottom status bar — a single-line health summary.
// It intentionally holds no state beyond its allocated rect because all
// meaningful state lives in the read-only store supplied via Context.
type Pane struct {
	rect panes.Rect
}

// New returns an initialized status bar Pane.
func New() *Pane { return &Pane{} }

func (p *Pane) ID() panes.PaneID                                            { return panes.PaneStatusBar }
func (p *Pane) SetSize(r panes.Rect)                                        { p.rect = r }
func (p *Pane) Update(msg tea.Msg, ctx panes.Context) (panes.Pane, tea.Cmd) { return p, nil }

// View renders the status bar as a single full-width line. If a StatusMessage
// override is set it takes the entire bar; otherwise segments are assembled
// from health level, issue count, and loading state.
func (p *Pane) View(ctx panes.Context) string {
	w := ctx.Width
	if w < 1 {
		w = 80
	}

	// An explicit override message takes the full bar width — useful for
	// ephemeral notifications (e.g. "Syncing…", "Session saved").
	if msg := ctx.Store.StatusMessage(); msg != "" {
		return lipgloss.NewStyle().
			Width(w).
			Background(lipgloss.Color("#111518")).
			Render(theme.StatusBarBase.Render(theme.StatusDim.Render(msg)))
	}

	health := ctx.Store.Health()
	navNodes := ctx.Store.Nav()
	daemonStatus := ctx.Store.GetDaemonStatus()

	var parts []string

	// Daemon status line: vary message based on health and why it's unhealthy.
	switch health {
	case domain.HealthUnknown:
		// Daemon status nil or still loading — show a dim loading hint.
		parts = append(parts, theme.StatusDim.Render("Loading…"))
	case domain.HealthError:
		// Distinguish between "daemon not running" and "daemon up but has errors".
		if daemonStatus == nil || !daemonStatus.Running {
			parts = append(parts, theme.StatusError.Render("daemon offline  ·  run: ox daemon start"))
		} else {
			// Count issues so we can surface the number inline.
			issueCount := 0
			for _, node := range navNodes {
				if node.Kind == domain.NavNodeIssue {
					issueCount++
				}
			}
			label := fmt.Sprintf("⚠ %d issue", issueCount)
			if issueCount != 1 {
				label += "s"
			}
			parts = append(parts, theme.StatusError.Render(label))
		}
	case domain.HealthOK:
		parts = append(parts, theme.StatusHealthy.Render("Daemon ✓"))
	case domain.HealthWarn:
		parts = append(parts, theme.StatusWarning.Render("Daemon ⚠"))
	}

	// Count daemon-flagged issues when health is not already showing them inline.
	if health != domain.HealthError {
		issueCount := 0
		for _, node := range navNodes {
			if node.Kind == domain.NavNodeIssue {
				issueCount++
			}
		}
		if issueCount > 0 {
			label := fmt.Sprintf("⚠ %d issue", issueCount)
			if issueCount > 1 {
				label += "s"
			}
			parts = append(parts, theme.StatusWarning.Render(label))
		}
	}

	// Auth expiry warning — shown prominently when a token is about to expire.
	if daemonStatus != nil {
		for _, issue := range daemonStatus.Issues {
			if issue.Type == "auth_expiring" || issue.Type == "auth_expired" {
				parts = append(parts, theme.StatusWarning.Render("⚠ auth expiring · run: ox login"))
				break
			}
		}
	}

	// Show a spinner-style indicator while the initial data load is in flight.
	if ctx.Store.Loading() {
		parts = append(parts, theme.StatusDim.Render("loading…"))
	}

	sep := theme.StatusSeparator.Render(" · ")
	line := strings.Join(parts, sep)

	return lipgloss.NewStyle().
		Width(w).
		Background(lipgloss.Color("#111518")).
		Render(theme.StatusBarBase.Render(line))
}
