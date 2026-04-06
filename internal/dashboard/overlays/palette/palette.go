// Package palette implements the ⌘K command palette overlay for the dashboard TUI.
// Items are pre-populated with tea.Cmd closures at creation time (constructed
// in the app layer) to avoid an import cycle between app and overlays/palette.
package palette

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/dashboard/overlays"
	"github.com/sageox/ox/internal/dashboard/theme"
)

// Item is a single searchable entry in the command palette.
type Item struct {
	Icon  string  // 1-char icon prefix
	Label string  // primary display text
	Sub   string  // secondary label shown right-aligned (section name or category)
	Cmd   tea.Cmd // command executed when item is selected; nil for no-op
}

// Overlay is the command palette opened by Ctrl+K. Items are injected at
// construction time so this package has no dependency on the app layer.
type Overlay struct {
	query  string
	items  []Item
	cursor int
	keyUp  key.Binding
	keyDn  key.Binding
	keySel key.Binding
}

// New creates a command palette overlay pre-loaded with the given items.
func New(items []Item) *Overlay {
	return &Overlay{
		items:  items,
		keyUp:  key.NewBinding(key.WithKeys("k", "up")),
		keyDn:  key.NewBinding(key.WithKeys("j", "down")),
		keySel: key.NewBinding(key.WithKeys("enter")),
	}
}

func (o *Overlay) ID() overlays.OverlayID { return overlays.OverlayPalette }

func (o *Overlay) Update(msg tea.Msg) (overlays.Overlay, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return o, nil, false
	}

	filtered := o.filtered()

	switch km.String() {
	case "esc", "ctrl+c":
		return nil, nil, true

	case "backspace", "ctrl+h":
		runes := []rune(o.query)
		if len(runes) > 0 {
			o.query = string(runes[:len(runes)-1])
			o.cursor = 0
		}
		return o, nil, true
	}

	switch {
	case key.Matches(km, o.keyUp):
		if o.cursor > 0 {
			o.cursor--
		}
		return o, nil, true

	case key.Matches(km, o.keyDn):
		if o.cursor < len(filtered)-1 {
			o.cursor++
		}
		return o, nil, true

	case key.Matches(km, o.keySel):
		if o.cursor >= 0 && o.cursor < len(filtered) {
			return nil, filtered[o.cursor].Cmd, true
		}
		return nil, nil, true

	default:
		s := km.String()
		if utf8.RuneCountInString(s) == 1 && s != " " {
			o.query += s
			o.cursor = 0
		}
		return o, nil, true
	}
}

func (o *Overlay) filtered() []Item {
	q := strings.ToLower(o.query)
	if q == "" {
		return o.items
	}
	var out []Item
	for _, it := range o.items {
		if strings.Contains(strings.ToLower(it.Label), q) ||
			strings.Contains(strings.ToLower(it.Sub), q) {
			out = append(out, it)
		}
	}
	return out
}

func (o *Overlay) View(width, height int) string {
	boxW := 60
	if boxW > width-4 {
		boxW = width - 4
	}
	if boxW < 24 {
		boxW = 24
	}
	innerW := boxW - 4 // RoundedBorder(2) + Padding(1 each side)

	filtered := o.filtered()

	var sb strings.Builder

	// ── Header ────────────────────────────────────────────────────────────
	titleL := lipgloss.NewStyle().Bold(true).Foreground(cli.ColorSecondary).Render("⌘ Quick Search")
	titleR := theme.InspectorHintStyle.Render("[esc] close")
	gapW := innerW - lipgloss.Width(titleL) - lipgloss.Width(titleR)
	if gapW < 1 {
		gapW = 1
	}
	sb.WriteString(titleL + strings.Repeat(" ", gapW) + titleR + "\n")
	sb.WriteString(theme.NavDimStyle.Render(strings.Repeat("─", innerW)) + "\n")

	// ── Query input ───────────────────────────────────────────────────────
	prompt := lipgloss.NewStyle().Bold(true).Foreground(cli.ColorPrimary).Render("▸ ")
	cursor := lipgloss.NewStyle().Foreground(cli.ColorSecondary).Render("█")
	queryLine := prompt + lipgloss.NewStyle().
		Foreground(lipgloss.Color("#C8D0C8")).
		Width(innerW-2).
		Render(o.query) + cursor
	sb.WriteString(queryLine + "\n")
	sb.WriteString(theme.NavDimStyle.Render(strings.Repeat("─", innerW)) + "\n")

	// ── Item list ─────────────────────────────────────────────────────────
	if len(filtered) == 0 {
		noMatch := lipgloss.NewStyle().Foreground(cli.ColorDim).Italic(true).Width(innerW).Render("  no matches")
		sb.WriteString(noMatch + "\n")
	} else {
		shown := filtered
		if len(shown) > 10 {
			shown = shown[:10]
		}
		for i, it := range shown {
			icon := lipgloss.NewStyle().Foreground(cli.ColorPrimary).Render(it.Icon)
			sub := lipgloss.NewStyle().Foreground(cli.ColorDim).Render(it.Sub)
			subW := lipgloss.Width(sub)

			// Truncate label to leave room for icon + sub + spacing.
			maxLabelRunes := innerW - 2 - 2 - subW - 1 // 2=icon+space, 2=left margin, 1=gap
			if maxLabelRunes < 1 {
				maxLabelRunes = 1
			}
			label := it.Label
			if utf8.RuneCountInString(label) > maxLabelRunes && maxLabelRunes > 1 {
				runes := []rune(label)
				label = string(runes[:maxLabelRunes-1]) + "…"
			}
			labelW := utf8.RuneCountInString(label)
			gap := innerW - 2 - 2 - labelW - subW - 1
			if gap < 0 {
				gap = 0
			}

			line := icon + " " + label + strings.Repeat(" ", gap) + sub

			var rowStyle lipgloss.Style
			if i == o.cursor {
				rowStyle = lipgloss.NewStyle().
					Bold(true).
					Foreground(lipgloss.Color("#1A1D1C")).
					Background(cli.ColorPrimary).
					Width(innerW)
			} else {
				rowStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("#C8D0C8")).
					Width(innerW)
			}
			sb.WriteString(rowStyle.Render(line) + "\n")
		}
		if len(filtered) > 10 {
			more := lipgloss.NewStyle().Foreground(cli.ColorDim).Italic(true).Width(innerW).Render(
				fmt.Sprintf("  +%d more — keep typing to filter", len(filtered)-10),
			)
			sb.WriteString(more + "\n")
		}
	}

	// ── Footer ────────────────────────────────────────────────────────────
	sb.WriteString(theme.NavDimStyle.Render(strings.Repeat("─", innerW)) + "\n")
	footer := theme.InspectorHintStyle.Width(innerW).Render("j/k navigate  ·  enter select  ·  type to filter")
	sb.WriteString(footer)

	// ── Box ───────────────────────────────────────────────────────────────
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(cli.ColorSecondary).
		Padding(0, 1).
		Render(sb.String())

	boxRenderedW := lipgloss.Width(box)
	boxRenderedH := lipgloss.Height(box)

	leftPad := (width - boxRenderedW) / 2
	topPad := (height - boxRenderedH) / 3
	if leftPad < 0 {
		leftPad = 0
	}
	if topPad < 0 {
		topPad = 0
	}

	// Assemble full-screen output on a near-black background.
	bgLine := lipgloss.NewStyle().Background(lipgloss.Color("#111518")).Width(width).Render("")
	leftPadStr := strings.Repeat(" ", leftPad)
	var out strings.Builder

	for i := 0; i < topPad; i++ {
		out.WriteString(bgLine + "\n")
	}
	for _, line := range strings.Split(box, "\n") {
		out.WriteString(leftPadStr + line + "\n")
	}
	for rendered := topPad + boxRenderedH; rendered < height; rendered++ {
		out.WriteString(bgLine + "\n")
	}

	return out.String()
}

// KindIcon returns a display icon for a nav node kind.
func KindIcon(kind domain.NavNodeKind) string {
	switch kind {
	case domain.NavNodeSession:
		return "◎"
	case domain.NavNodeWorkspace:
		return "⬡"
	case domain.NavNodeMurmur:
		return "◈"
	case domain.NavNodeDiscussion:
		return "◇"
	case domain.NavNodeIssue:
		return "⚠"
	case domain.NavNodeAICoworker:
		return "●"
	case domain.NavNodeAuth:
		return "●"
	case domain.NavNodeSyncHealth:
		return "⟲"
	case domain.NavNodeSOUL:
		return "✦"
	case domain.NavNodeCodeIndex:
		return "⊛"
	case domain.NavNodeLedger:
		return "◈"
	case domain.NavNodeTeamContext:
		return "◇"
	case domain.NavNodeWhisper:
		return "↘"
	case domain.NavNodeDaemonErrors:
		return "⚠"
	case domain.NavNodeAgentWork:
		return "◎"
	case domain.NavNodeCallers:
		return "⬡"
	default:
		return "◦"
	}
}

// KindSection returns a section category name for a nav node kind.
func KindSection(kind domain.NavNodeKind) string {
	switch kind {
	case domain.NavNodeSession:
		return "sessions"
	case domain.NavNodeWorkspace:
		return "workspaces"
	case domain.NavNodeMurmur:
		return "murmurs"
	case domain.NavNodeDiscussion:
		return "discussions"
	case domain.NavNodeAICoworker:
		return "coworkers"
	case domain.NavNodeIssue:
		return "issues"
	case domain.NavNodeAuth:
		return "auth"
	case domain.NavNodeSyncHealth:
		return "sync"
	case domain.NavNodeSOUL:
		return "SOUL"
	case domain.NavNodeCodeIndex:
		return "code index"
	case domain.NavNodeLedger:
		return "ledger"
	case domain.NavNodeTeamContext:
		return "team knowledge"
	case domain.NavNodeWhisper:
		return "whispers"
	case domain.NavNodeDaemonErrors, domain.NavNodeAgentWork, domain.NavNodeCallers:
		return "daemon"
	default:
		return ""
	}
}
