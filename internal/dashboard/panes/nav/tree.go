package nav

import (
	"strings"
	"unicode/utf8"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/dashboard/theme"
)

// Indent returns a string of spaces for the given depth level.
func Indent(depth int) string {
	return strings.Repeat("  ", depth)
}

// ExpandIcon returns a collapse/expand chevron for section nodes, empty string for leaves.
func ExpandIcon(node domain.NavNode) string {
	if !node.Expandable {
		return ""
	}
	if node.Expanded {
		return "▼ "
	}
	return "▶ "
}

// KindIcon returns a small decorative icon for a nav node's kind.
func KindIcon(kind domain.NavNodeKind) string {
	switch kind {
	case domain.NavNodeLedger:
		return "◈ "
	case domain.NavNodeSession:
		return "◎ "
	case domain.NavNodeWorkspace:
		return "⬡ "
	case domain.NavNodeIssue:
		return "⚠ "
	case domain.NavNodeCodeIndex:
		return "⊛ "
	case domain.NavNodeMurmur:
		return "◈ "
	case domain.NavNodeDiscussion:
		return "◇ "
	case domain.NavNodeAuth:
		return "● "
	case domain.NavNodeSyncHealth:
		return "⟲ "
	case domain.NavNodeSOUL:
		return "✦ "
	case domain.NavNodeAICoworker:
		return "● "
	case domain.NavNodeDaemon:
		return "◉ "
	case domain.NavNodeDaemonErrors:
		return "⚠ "
	case domain.NavNodeAgentWork:
		return "◎ "
	case domain.NavNodeCallers:
		return "⬡ "
	case domain.NavNodeTeamContext:
		return "◇ "
	case domain.NavNodeWhisper:
		return "↘ "
	case domain.NavNodeTeamFeedSection:
		return "◈ "
	default:
		return ""
	}
}

// RenderNode renders a single nav node row to a fixed width.
// When selected is true the cursor/highlight style is applied.
func RenderNode(node domain.NavNode, selected bool, width int) string {
	prefix := Indent(node.Depth)

	// Hint nodes are non-interactive empty-state labels — always rendered dim,
	// never highlighted even when the cursor happens to land on them.
	if node.Kind == domain.NavNodeHint {
		row := prefix + "— " + node.Label
		if width > 0 && utf8.RuneCountInString(row) > width {
			row = string([]rune(row)[:width-1]) + "…"
		}
		s := theme.NavDimStyle
		if width > 0 {
			s = s.Width(width)
		}
		return s.Render(row)
	}

	var label string
	if node.Kind == domain.NavNodeSection {
		label = ExpandIcon(node) + strings.ToUpper(node.Label)
	} else {
		label = KindIcon(node.Kind) + node.Label
	}

	row := prefix + label
	// Truncate long rows to avoid wrapping inside the pane.
	if width > 0 && utf8.RuneCountInString(row) > width {
		row = string([]rune(row)[:width-1]) + "…"
	}

	var s lipgloss.Style
	switch {
	case selected:
		s = theme.NavSelectedStyle
	case node.Kind == domain.NavNodeSection:
		s = theme.NavSectionStyle
	default:
		s = theme.NavItemStyle
	}

	// Fill the full row width so the selection highlight extends to the pane edge.
	if width > 0 {
		s = s.Width(width)
	}
	return s.Render(row)
}
