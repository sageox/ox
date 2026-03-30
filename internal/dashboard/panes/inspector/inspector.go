package inspector

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/dashboard/panes"
	"github.com/sageox/ox/internal/dashboard/theme"
)

// Pane implements the right-hand detail inspector panel.
type Pane struct {
	rect      panes.Rect
	scrollTop int
}

// compile-time interface check
var _ panes.Pane = (*Pane)(nil)

// New creates an initialized inspector Pane.
func New() *Pane { return &Pane{} }

func (p *Pane) ID() panes.PaneID { return panes.PaneInspector }

func (p *Pane) SetSize(r panes.Rect) { p.rect = r }

func (p *Pane) Update(msg tea.Msg, ctx panes.Context) (panes.Pane, tea.Cmd) {
	return p, nil
}

func (p *Pane) View(ctx panes.Context) string {
	w, h := ctx.Width, ctx.Height
	if w < 2 || h < 2 {
		return ""
	}

	// Border consumes 1 column on each side and 1 row top and bottom.
	innerW := w - 2
	innerH := h - 2

	title := theme.PaneTitle("✦ Inspector", ctx.Focused)

	target := ctx.Store.Inspector()
	var content string
	switch target.Kind {
	case domain.TargetSession:
		content = RenderSession(target, innerW)
	case domain.TargetWorkspace:
		content = RenderWorkspace(target, innerW)
	case domain.TargetIssue:
		content = RenderIssue(target, innerW)
	case domain.TargetMurmur:
		content = RenderMurmur(target, innerW)
	case domain.TargetTeamDiscussion:
		content = RenderDiscussion(target, innerW)
	default:
		content = RenderDefault(innerW)
	}

	// Clip content to the visible scroll window.
	contentLines := strings.Split(content, "\n")
	// Reserve one row for the title line.
	visible := innerH - 1
	if visible < 0 {
		visible = 0
	}
	start := p.scrollTop
	if start > len(contentLines) {
		start = len(contentLines)
	}
	end := start + visible
	if end > len(contentLines) {
		end = len(contentLines)
	}
	visibleContent := strings.Join(contentLines[start:end], "\n")

	full := title + "\n" + visibleContent

	borderStyle := theme.PaneBorderUnfocused
	if ctx.Focused {
		borderStyle = theme.PaneBorderFocused
	}
	return borderStyle.Width(innerW).Height(innerH).Render(full)
}
