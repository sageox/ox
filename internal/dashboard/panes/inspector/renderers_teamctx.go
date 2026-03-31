package inspector

import (
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/dashboard/domain"
	"github.com/sageox/ox/internal/dashboard/theme"
)

// RenderTeamContext renders a TeamContextEntry in the inspector pane.
// Shows team identity, path, SOUL preview, and knowledge counts.
func RenderTeamContext(target domain.InspectorTarget, width int) string {
	if target.TeamContext == nil {
		return theme.InspectorDimStyle.Render("no team context data")
	}
	tc := target.TeamContext

	var lines []string
	title := tc.TeamName
	if title == "" {
		title = tc.TeamSlug
	}
	if title == "" {
		title = "Team Context"
	}
	lines = append(lines, theme.InspectorTitleStyle.Render(title))
	lines = append(lines, "")

	if tc.Path != "" {
		lines = append(lines, row("Path", shortenPath(tc.Path, width-8)))
	}
	if tc.TeamSlug != "" && tc.TeamSlug != tc.TeamName {
		lines = append(lines, row("Slug", tc.TeamSlug))
	}

	lines = append(lines, "")
	lines = append(lines, theme.InspectorTitleStyle.Render("Knowledge"))
	if tc.MemoryCount > 0 {
		lines = append(lines, row("Memory entries", fmt.Sprintf("%d", tc.MemoryCount)))
	} else {
		lines = append(lines, row("Memory entries", "0"))
	}
	if tc.DocsCount > 0 {
		lines = append(lines, row("Docs", fmt.Sprintf("%d", tc.DocsCount)))
	} else {
		lines = append(lines, row("Docs", "0"))
	}

	if tc.SOULPreview != "" {
		lines = append(lines, "")
		lines = append(lines, theme.InspectorTitleStyle.Render("Team Identity (SOUL)"))
		for _, line := range wrapText(tc.SOULPreview, width-2) {
			lines = append(lines, theme.InspectorDimStyle.Render(line))
		}
	}

	lines = append(lines, "")
	lines = append(lines, theme.InspectorHintStyle.Render("[r] refresh"))
	return strings.Join(lines, "\n")
}
