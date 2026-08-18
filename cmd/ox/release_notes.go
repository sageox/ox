package main

import (
	_ "embed"
	"fmt"
	"io"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/sageox/ox/internal/theme"
	"github.com/spf13/cobra"
)

// release_notes.md is the canonical changelog file.
// CHANGELOG.md at repo root is a symlink to this file.
//
//go:embed release_notes.md
var releaseNotes string

var releaseNotesCmd = &cobra.Command{
	Use:   "release-notes",
	Short: "Display release notes for the current version",
	Long:  "Display formatted release notes showing what's new, changed, and fixed in the current version.",
	RunE: func(cmd *cobra.Command, args []string) error {
		raw, _ := cmd.Flags().GetBool("raw")
		latest, _ := cmd.Flags().GetBool("latest")

		content := releaseNotes
		if latest {
			content = extractLatestVersion(releaseNotes)
		}

		if raw {
			fmt.Fprintln(cmd.OutOrStdout(), content)
			return nil
		}

		// render with terminal formatting
		renderReleaseNotes(cmd.OutOrStdout(), content)
		return nil
	},
}

func init() {
	releaseNotesCmd.Flags().Bool("raw", false, "output raw markdown without formatting (default: false)")
	releaseNotesCmd.Flags().Bool("latest", false, "show only the latest version (default: false)")
}

// extractLatestVersion returns only the first version section from the changelog
func extractLatestVersion(content string) string {
	lines := strings.Split(content, "\n")
	var result strings.Builder
	inVersion := false
	versionCount := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// version header (## [0.0.1])
		if strings.HasPrefix(trimmed, "## [") {
			versionCount++
			if versionCount > 1 {
				// stop at second version header
				break
			}
			inVersion = true
		}

		if inVersion {
			result.WriteString(line)
			result.WriteString("\n")
		}
	}

	return strings.TrimSpace(result.String())
}

func renderReleaseNotes(w io.Writer, content string) {
	lines := strings.Split(content, "\n")

	for _, line := range lines {
		rendered := renderLine(line)
		fmt.Fprintln(w, rendered)
	}
}

func renderLine(line string) string {
	trimmed := strings.TrimSpace(line)

	// main title (# Changelog)
	if strings.HasPrefix(trimmed, "# ") && !strings.HasPrefix(trimmed, "##") {
		title := strings.TrimPrefix(trimmed, "# ")
		return fmt.Sprintf("\n%s\n", releaseNoteBoldStyle.Render(releaseNoteCyanStyle.Render(title)))
	}

	// version header (## [0.0.1])
	if strings.HasPrefix(trimmed, "## [") {
		version := strings.TrimPrefix(trimmed, "## ")
		return fmt.Sprintf("\n%s", releaseNoteBoldStyle.Render(releaseNoteYellowStyle.Render(version)))
	}

	// section header (### Added, ### Changed, etc.)
	if strings.HasPrefix(trimmed, "### ") {
		section := strings.TrimPrefix(trimmed, "### ")
		return fmt.Sprintf("\n  %s", releaseNoteGreenStyle.Render(section))
	}

	// subsection header (#### Core CLI)
	if strings.HasPrefix(trimmed, "#### ") {
		subsection := strings.TrimPrefix(trimmed, "#### ")
		return fmt.Sprintf("\n    %s", releaseNoteBlueStyle.Render(subsection))
	}

	// bullet points
	if strings.HasPrefix(trimmed, "- ") {
		item := strings.TrimPrefix(trimmed, "- ")
		// highlight markdown (bold and code)
		item = highlightMarkdown(item)
		return fmt.Sprintf("      %s %s", releaseNoteDimStyle.Render("•"), item)
	}

	// links at the bottom
	if strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, "]: ") {
		return releaseNoteDimStyle.Render(trimmed)
	}

	// empty lines
	if trimmed == "" {
		return ""
	}

	// other content
	return "  " + line
}

func highlightMarkdown(text string) string {
	// first pass: handle bold (**text**)
	text = highlightBold(text)
	// second pass: handle code (`text`)
	text = highlightCode(text)
	return text
}

func highlightBold(text string) string {
	var result strings.Builder
	i := 0

	for i < len(text) {
		// look for **
		if i+1 < len(text) && text[i] == '*' && text[i+1] == '*' {
			// find closing **
			end := strings.Index(text[i+2:], "**")
			if end != -1 {
				boldContent := text[i+2 : i+2+end]
				result.WriteString(releaseNoteBoldStyle.Render(boldContent))
				i = i + 2 + end + 2 // skip past closing **
				continue
			}
		}
		result.WriteByte(text[i])
		i++
	}

	return result.String()
}

func highlightCode(text string) string {
	var result strings.Builder
	var currentSegment strings.Builder
	inCode := false

	for i := 0; i < len(text); i++ {
		if text[i] == '`' {
			if inCode {
				// end of code segment - render accumulated text with cyan style
				result.WriteString(releaseNoteCyanStyle.Render(currentSegment.String()))
				currentSegment.Reset()
				inCode = false
			} else {
				// start of code segment - write accumulated plain text
				result.WriteString(currentSegment.String())
				currentSegment.Reset()
				inCode = true
			}
		} else {
			currentSegment.WriteByte(text[i])
		}
	}

	// write any remaining text
	if currentSegment.Len() > 0 {
		if inCode {
			result.WriteString(releaseNoteCyanStyle.Render(currentSegment.String()))
		} else {
			result.WriteString(currentSegment.String())
		}
	}

	return result.String()
}

// lipgloss styles for release notes
var (
	releaseNoteBoldStyle   = lipgloss.NewStyle().Bold(true)
	releaseNoteDimStyle    = lipgloss.NewStyle().Faint(true)
	releaseNoteCyanStyle   = lipgloss.NewStyle().Foreground(theme.Color("36"))
	releaseNoteGreenStyle  = lipgloss.NewStyle().Foreground(theme.Color("82"))
	releaseNoteYellowStyle = lipgloss.NewStyle().Foreground(theme.Color("214"))
	releaseNoteBlueStyle   = lipgloss.NewStyle().Foreground(theme.Color("39"))
)
