package inspector

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/sageox/ox/internal/dashboard/app"
	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/dashboard/panes"
	"github.com/sageox/ox/internal/dashboard/theme"
)

// Pane implements the right-hand detail inspector panel.
type Pane struct {
	rect      panes.Rect
	scrollTop int
	keys      app.PaneKeyMap
	// lastTarget tracks the previously rendered target kind; scrollTop resets on target change.
	lastTarget domain.InspectorTargetKind
}

// compile-time interface check
var _ panes.Pane = (*Pane)(nil)

// New creates an initialized inspector Pane.
func New() *Pane { return &Pane{keys: app.DefaultPaneKeys()} }

func (p *Pane) ID() panes.PaneID { return panes.PaneInspector }

func (p *Pane) SetSize(r panes.Rect) { p.rect = r }

func (p *Pane) Update(msg tea.Msg, ctx panes.Context) (panes.Pane, tea.Cmd) {
	// Reset scroll when the inspector target changes.
	target := ctx.Store.Inspector()
	if target.Kind != p.lastTarget {
		p.scrollTop = 0
		p.lastTarget = target.Kind
	}

	if !ctx.Focused {
		return p, nil
	}

	if m, ok := msg.(tea.KeyMsg); ok {
		// Reserve one row for the title; compute max scroll from pane height.
		innerH := p.rect.Height - 2
		visible := innerH - 1
		if visible < 1 {
			visible = 1
		}
		switch {
		case key.Matches(m, p.keys.Down):
			p.scrollTop++
		case key.Matches(m, p.keys.Up):
			if p.scrollTop > 0 {
				p.scrollTop--
			}
		}
		_ = visible // scroll clamping happens in View
	}

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
	case domain.TargetAuth:
		content = RenderAuth(target, innerW)
	case domain.TargetCodeDB:
		content = RenderCodeDB(target, innerW)
	case domain.TargetSyncHealth:
		content = RenderSyncHealth(target, innerW)
	case domain.TargetSOUL:
		content = RenderSOUL(target, innerW)
	case domain.TargetInstance:
		content = RenderInstance(target, innerW)
	case domain.TargetDaemonErrors:
		content = RenderDaemonErrors(target, innerW)
	case domain.TargetAgentWork:
		content = RenderAgentWork(target, innerW)
	case domain.TargetCallers:
		content = RenderCallers(target, innerW)
	case domain.TargetTeamContext:
		content = RenderTeamContext(target, innerW)
	case domain.TargetWhisperHistory:
		content = RenderWhisperHistory(target, innerW)
	default:
		content = RenderDefault(ctx.Store.GetDaemonStatus(), ctx.Store.ActiveMurmurCoworkers(), innerW)
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
