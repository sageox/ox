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

func (p *Pane) ID() panes.PaneID              { return panes.PaneStatusBar }
func (p *Pane) SetSize(r panes.Rect)          { p.rect = r }
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

	var parts []string

	// Daemon status derived from the computed health level.
	switch health {
	case domain.HealthOK:
		parts = append(parts, theme.StatusHealthy.Render("Daemon ✓"))
	case domain.HealthWarn:
		parts = append(parts, theme.StatusWarning.Render("Daemon ⚠"))
	case domain.HealthError:
		parts = append(parts, theme.StatusError.Render("Daemon ✗"))
	default: // HealthUnknown — initial state before first data fetch
		parts = append(parts, theme.StatusDim.Render("Daemon …"))
	}

	// Count daemon-flagged issues surfaced as nav nodes so we reuse the same
	// filtering logic that already exists in the nav builder.
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
