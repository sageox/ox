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

// GlobalKeyMap mirrors app.GlobalKeyMap to avoid a circular import.
// The fields must remain in sync with app.GlobalKeyMap.
type GlobalKeyMap struct {
	FocusNext   key.Binding
	FocusPrev   key.Binding
	Refresh     key.Binding
	Help        key.Binding
	Quit        key.Binding
	Palette     key.Binding
	OpenBrowser key.Binding
}

// PaneKeyMap mirrors app.PaneKeyMap to avoid a circular import.
type PaneKeyMap struct {
	Up     key.Binding
	Down   key.Binding
	Select key.Binding
	Expand key.Binding
}

// Overlay is the help keybinding modal.
type Overlay struct {
	globalKeys GlobalKeyMap
	paneKeys   PaneKeyMap
}

var _ overlays.Overlay = (*Overlay)(nil)

// New creates a new help overlay with the provided key maps.
// Callers (app.update) pass their own key maps so this package need not import app.
func New(globalKeys GlobalKeyMap, paneKeys PaneKeyMap) *Overlay {
	return &Overlay{
		globalKeys: globalKeys,
		paneKeys:   paneKeys,
	}
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
	type binding struct{ keys, desc string }

	sections := []struct {
		title    string
		bindings []binding
	}{
		{
			"Global",
			[]binding{
				{o.globalKeys.FocusNext.Help().Key, o.globalKeys.FocusNext.Help().Desc},
				{o.globalKeys.FocusPrev.Help().Key, o.globalKeys.FocusPrev.Help().Desc},
				{o.globalKeys.Refresh.Help().Key, o.globalKeys.Refresh.Help().Desc},
				{o.globalKeys.Palette.Help().Key, o.globalKeys.Palette.Help().Desc},
				{o.globalKeys.OpenBrowser.Help().Key, o.globalKeys.OpenBrowser.Help().Desc},
				{o.globalKeys.Help.Help().Key, o.globalKeys.Help.Help().Desc},
				{o.globalKeys.Quit.Help().Key, o.globalKeys.Quit.Help().Desc},
			},
		},
		{
			"Sections",
			[]binding{
				{"1-5", "jump to section"},
			},
		},
		{
			"Navigation",
			[]binding{
				{o.paneKeys.Up.Help().Key, o.paneKeys.Up.Help().Desc},
				{o.paneKeys.Down.Help().Key, o.paneKeys.Down.Help().Desc},
				{o.paneKeys.Select.Help().Key, o.paneKeys.Select.Help().Desc},
				{o.paneKeys.Expand.Help().Key, o.paneKeys.Expand.Help().Desc},
				{"esc", "close overlay"},
			},
		},
	}

	keyStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(cli.ColorSecondary).
		Width(14)
	descStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#AAAAAA"))

	var sb strings.Builder
	sb.WriteString(theme.HeaderStyle.Render(" Keyboard Shortcuts ") + "\n\n")

	for _, sec := range sections {
		sb.WriteString(theme.NavSectionStyle.Render(sec.title) + "\n")
		for _, b := range sec.bindings {
			sb.WriteString("  " + keyStyle.Render(b.keys) + descStyle.Render(b.desc) + "\n")
		}
		sb.WriteString("\n")
	}

	content := sb.String()

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
