package help

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/dashboard/overlays"
	"github.com/sageox/ox/internal/dashboard/theme"
)

// Section is one titled group of rows in the help view. Rows render from the
// bindings the caller passes, never from hand-written key text, so relabeling
// a binding relabels its row.
type Section struct {
	Title    string
	Bindings []key.Binding
}

// Overlay is the help keybinding modal.
type Overlay struct {
	sections []Section
}

var _ overlays.Overlay = (*Overlay)(nil)

// New creates a help overlay listing the given sections in order.
// Callers (dashboard.Run) pass their own bindings so this package need not import app.
func New(sections []Section) *Overlay {
	return &Overlay{sections: sections}
}

func (o *Overlay) ID() overlays.OverlayID { return overlays.OverlayHelp }

func (o *Overlay) Update(msg tea.Msg) (overlays.Overlay, tea.Cmd, bool) {
	if m, ok := msg.(tea.KeyMsg); ok {
		switch m.String() {
		case "esc", "q", "?":
			// nil signals the stack to close this overlay
			return nil, nil, true
		}
		// consume all keys while help is open so they don't reach panes
		return o, nil, true
	}
	return o, nil, false
}

func (o *Overlay) View(width, height int) string {
	keyStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(cli.ColorSecondary).
		Width(14)
	descStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#AAAAAA"))

	var sb strings.Builder
	sb.WriteString(theme.HeaderStyle.Render(" Keyboard Shortcuts ") + "\n\n")

	for _, sec := range o.sections {
		sb.WriteString(theme.NavSectionStyle.Render(sec.Title) + "\n")
		for _, b := range sec.Bindings {
			sb.WriteString("  " + keyStyle.Render(b.Help().Key) + descStyle.Render(b.Help().Desc) + "\n")
		}
		sb.WriteString("\n")
	}

	// trailing blank rows are padding only; dropping them offsets some of the
	// height that listing every binding adds
	content := strings.TrimRight(sb.String(), "\n")

	// Size the modal box, clamping to terminal width with padding.
	boxW := 44
	if boxW > width-4 {
		boxW = width - 4
	}
	lineCount := strings.Count(content, "\n")
	boxH := lineCount + 2 // +2 for border

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#7a8f78")). // muted sage border
		Padding(0, 1).
		Width(boxW - 2). // -2 for left+right border chars
		Render(content)

	// Center the modal within the terminal dimensions.
	x := (width - boxW) / 2
	y := (height - boxH) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	indent := strings.Repeat(" ", x)
	var out strings.Builder
	for i := 0; i < y; i++ {
		out.WriteString("\n")
	}
	for _, line := range strings.Split(box, "\n") {
		out.WriteString(indent + line + "\n")
	}
	return out.String()
}
