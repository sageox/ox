// Package statusbar implements the single-line status bar shown at the bottom
// of the dashboard TUI. It summarizes overall health, active issue count, and
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

func countIssueNodes(nodes []domain.NavNode) int {
	n := 0
	for _, node := range nodes {
		if node.Kind == domain.NavNodeIssue {
			n++
		}
	}
	return n
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

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
// override is set it takes the entire bar; otherwise a left/right layout shows
// health + issues on the left and coworker count + key hints on the right.
func (p *Pane) View(ctx panes.Context) string {
	w := ctx.Width
	if w < 1 {
		w = 80
	}

	bg := lipgloss.Color("#111518")
	barStyle := lipgloss.NewStyle().Width(w).Background(bg)
	padStyle := lipgloss.NewStyle().Padding(0, 1).Background(bg)

	// An explicit override message takes the full bar width.
	if msg := ctx.Store.StatusMessage(); msg != "" {
		return barStyle.Render(padStyle.Render(theme.StatusDim.Render(msg)))
	}

	health := ctx.Store.Health()
	navNodes := ctx.NavNodes
	daemonStatus := ctx.Store.GetDaemonStatus()

	// ── Left segment: daemon health + issues ──────────────────────────────
	var leftParts []string

	switch health {
	case domain.HealthUnknown:
		leftParts = append(leftParts, theme.StatusDim.Render("● loading…"))
	case domain.HealthError:
		if daemonStatus == nil || !daemonStatus.Running {
			leftParts = append(leftParts, theme.StatusError.Render("⬡ daemon offline"))
			leftParts = append(leftParts, theme.StatusDim.Render("ox daemon start"))
		} else {
			issueCount := countIssueNodes(navNodes)
			leftParts = append(leftParts, theme.StatusError.Render(fmt.Sprintf("⚠ %d issue%s", issueCount, pluralS(issueCount))))
		}
	case domain.HealthOK:
		leftParts = append(leftParts, theme.StatusHealthy.Render("● online"))
	case domain.HealthWarn:
		leftParts = append(leftParts, theme.StatusWarning.Render("◐ warning"))
	}

	if health != domain.HealthError {
		if n := countIssueNodes(navNodes); n > 0 {
			leftParts = append(leftParts, theme.StatusWarning.Render(fmt.Sprintf("⚠ %d issue%s", n, pluralS(n))))
		}
	}

	if daemonStatus != nil {
		for _, issue := range daemonStatus.Issues {
			if issue.Type == "auth_expiring" || issue.Type == "auth_expired" {
				leftParts = append(leftParts, theme.StatusWarning.Render("⚠ auth expiring"))
				break
			}
		}
	}

	sep := theme.StatusSeparator.Render("  ·  ")
	left := strings.Join(leftParts, sep)

	// ── Right segment: coworker count + key hints ──────────────────────────
	var rightParts []string

	if coworkers := ctx.Store.ActiveMurmurCoworkers(); coworkers > 0 {
		rightParts = append(rightParts, theme.StatusHealthy.Render(fmt.Sprintf("◈ %d active", coworkers)))
	}
	rightParts = append(rightParts, theme.StatusDim.Render("tab · j/k · ? help · q quit"))

	right := strings.Join(rightParts, sep)

	// Assemble left + padding + right, right-aligned within the bar width.
	leftRendered := left
	rightRendered := right
	// Pad left text to push right segment flush to the right edge.
	leftLen := lipgloss.Width(leftRendered)
	rightLen := lipgloss.Width(rightRendered)
	padding := w - leftLen - rightLen - 2 // -2 for left/right pad
	if padding < 1 {
		padding = 1
	}
	line := " " + leftRendered + strings.Repeat(" ", padding) + rightRendered + " "

	return barStyle.Render(line)
}
