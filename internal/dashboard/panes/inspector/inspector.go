package inspector

import (
	"strings"

	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/dashboard/state"
	"github.com/sageox/ox/internal/dashboard/theme"
)

// Pane implements the slide-in inspector detail panel.
type Pane struct{}

// New creates an inspector Pane.
func New() *Pane { return &Pane{} }

// View renders the inspector content for the current selection.
// Implements app.InspectorRenderer.
func (p *Pane) View(store state.ReadOnlyStore, width, height, scroll int) string {
	if width < 4 || height < 2 {
		return ""
	}

	innerW := width - 2 // leave some padding

	target := store.Inspector()
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
		content = RenderDefault(store.GetDaemonStatus(), store.ActiveMurmurCoworkers(), innerW)
	}

	title := theme.InspectorTitleStyle.Render("✦ Inspector")

	// clip content to visible scroll window
	contentLines := strings.Split(content, "\n")
	visible := height - 1 // title takes 1 row
	if visible < 0 {
		visible = 0
	}
	start := scroll
	if start > len(contentLines) {
		start = len(contentLines)
	}
	end := start + visible
	if end > len(contentLines) {
		end = len(contentLines)
	}
	visibleContent := strings.Join(contentLines[start:end], "\n")

	// pad remaining lines
	rendered := end - start
	for rendered < visible {
		visibleContent += "\n"
		rendered++
	}

	return " " + title + "\n" + visibleContent
}
