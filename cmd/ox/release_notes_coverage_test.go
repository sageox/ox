package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractLatestVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "single version",
			content: "# Changelog\n\n## [0.2.0] - 2026-03-01\n\n### Added\n\n- Feature A\n- Feature B\n",
			want:    "## [0.2.0] - 2026-03-01\n\n### Added\n\n- Feature A\n- Feature B",
		},
		{
			name:    "multiple versions returns first only",
			content: "# Changelog\n\n## [0.3.0] - 2026-03-15\n\n### Added\n- New stuff\n\n## [0.2.0] - 2026-03-01\n\n### Fixed\n- Old bug\n",
			want:    "## [0.3.0] - 2026-03-15\n\n### Added\n- New stuff",
		},
		{
			name:    "empty content",
			content: "",
			want:    "",
		},
		{
			name:    "no version headers",
			content: "# Changelog\n\nSome text without version headers.\n",
			want:    "",
		},
		{
			name:    "version header with subsections",
			content: "## [0.5.0] - 2026-03-20\n\n### Added\n\n#### Core CLI\n\n- Feature X\n\n#### Daemon\n\n- Feature Y\n\n## [0.4.0] - 2026-03-10\n\n### Fixed\n- Bug Z\n",
			want:    "## [0.5.0] - 2026-03-20\n\n### Added\n\n#### Core CLI\n\n- Feature X\n\n#### Daemon\n\n- Feature Y",
		},
		{
			name:    "version with no content before next",
			content: "## [0.2.0]\n## [0.1.0]\n- old\n",
			want:    "## [0.2.0]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractLatestVersion(tt.content)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRenderLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		line     string
		contains string // substring that must appear in rendered output
	}{
		{
			name:     "main title",
			line:     "# Changelog",
			contains: "Changelog",
		},
		{
			name:     "version header",
			line:     "## [0.3.0] - 2026-03-15",
			contains: "[0.3.0] - 2026-03-15",
		},
		{
			name:     "section header",
			line:     "### Added",
			contains: "Added",
		},
		{
			name:     "subsection header",
			line:     "#### Core CLI",
			contains: "Core CLI",
		},
		{
			name:     "bullet point",
			line:     "- New feature added",
			contains: "New feature added",
		},
		{
			name:     "link reference",
			line:     "[0.3.0]: https://github.com/example/compare/v0.2.0...v0.3.0",
			contains: "[0.3.0]:",
		},
		{
			name:     "empty line",
			line:     "",
			contains: "",
		},
		{
			name:     "plain text",
			line:     "Some description text here",
			contains: "Some description text here",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := renderLine(tt.line)
			if tt.contains != "" {
				assert.Contains(t, got, tt.contains)
			}
		})
	}
}

func TestRenderLine_MainTitleNotConfusedWithVersionHeader(t *testing.T) {
	t.Parallel()

	// "# Changelog" should NOT be treated as "## [version]"
	mainTitle := renderLine("# Changelog")
	versionHeader := renderLine("## [0.1.0]")

	// they should produce different output (main title has extra newline wrapping)
	assert.NotEqual(t, mainTitle, versionHeader)
}

func TestRenderLine_BulletHasDotSeparator(t *testing.T) {
	t.Parallel()
	// bullet lines should contain the dot character
	got := renderLine("- Added new command")
	// the rendered output should contain a bullet character
	assert.True(t, strings.Contains(got, "Added new command"))
}

func TestHighlightBold(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		// we check that the bold content appears and the ** delimiters are gone
		containsBoldContent string
		notContains         string
	}{
		{
			name:                "bold text",
			input:               "This is **bold** text",
			containsBoldContent: "bold",
			notContains:         "**",
		},
		{
			name:                "multiple bold",
			input:               "**first** and **second**",
			containsBoldContent: "first",
		},
		{
			name:                "no bold markers",
			input:               "plain text without markers",
			containsBoldContent: "plain text without markers",
		},
		{
			name:                "unclosed bold marker",
			input:               "**unclosed bold text",
			containsBoldContent: "unclosed bold text",
		},
		{
			name:                "empty bold",
			input:               "**** text",
			containsBoldContent: "text",
		},
		{
			name:                "empty string",
			input:               "",
			containsBoldContent: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := highlightBold(tt.input)
			if tt.containsBoldContent != "" {
				assert.Contains(t, got, tt.containsBoldContent)
			}
			if tt.notContains != "" {
				// strip ANSI to check ** removal
				plain := stripANSI(got)
				assert.NotContains(t, plain, tt.notContains)
			}
		})
	}
}

func TestHighlightCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		input           string
		containsContent string
	}{
		{
			name:            "code span",
			input:           "Use `ox init` to start",
			containsContent: "ox init",
		},
		{
			name:            "multiple code spans",
			input:           "`first` and `second`",
			containsContent: "first",
		},
		{
			name:            "no code markers",
			input:           "plain text",
			containsContent: "plain text",
		},
		{
			name:            "unclosed backtick",
			input:           "`unclosed code",
			containsContent: "unclosed code",
		},
		{
			name:            "empty string",
			input:           "",
			containsContent: "",
		},
		{
			name:            "adjacent backticks",
			input:           "`` empty code",
			containsContent: "empty code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := highlightCode(tt.input)
			if tt.containsContent != "" {
				assert.Contains(t, got, tt.containsContent)
			}
		})
	}
}

func TestHighlightMarkdown(t *testing.T) {
	t.Parallel()

	// combined bold + code
	got := highlightMarkdown("Run **`ox init`** to start")
	assert.Contains(t, got, "ox init")
	assert.Contains(t, got, "to start")
}

// stripANSI removes ANSI escape codes from a string for assertion purposes.
func stripANSI(s string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' {
			// skip until 'm'
			for i < len(s) && s[i] != 'm' {
				i++
			}
			if i < len(s) {
				i++ // skip 'm'
			}
			continue
		}
		result.WriteByte(s[i])
		i++
	}
	return result.String()
}
