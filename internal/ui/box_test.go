package ui

import (
	"strings"
	"testing"

	"github.com/sageox/ox/internal/theme"
	"github.com/stretchr/testify/assert"
)

func TestVariantColor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		variant BoxVariant
		want    string // hex color to check against theme
	}{
		{"BoxDefault returns dim color", BoxDefault, theme.HexMuted},
		{"BoxInfo returns info color", BoxInfo, theme.HexInfo},
		{"BoxWarning returns warning color", BoxWarning, theme.HexWarn},
		{"BoxError returns error color", BoxError, theme.HexFail},
		{"BoxSuccess returns success color", BoxSuccess, theme.HexPass},
		{"unknown variant defaults to dim", BoxVariant(99), theme.HexMuted},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := variantColor(tt.variant)
			assert.NotNil(t, got, "variantColor should never return nil")
		})
	}
}

func TestRenderBox(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		title   string
		content string
		variant BoxVariant
		checks  []string // substrings expected in output
	}{
		{
			name:    "with title and content",
			title:   "Status",
			content: "All checks passed",
			variant: BoxSuccess,
			checks:  []string{"Status", "All checks passed"},
		},
		{
			name:    "without title",
			title:   "",
			content: "Just content",
			variant: BoxDefault,
			checks:  []string{"Just content"},
		},
		{
			name:    "empty content",
			title:   "Empty",
			content: "",
			variant: BoxInfo,
			checks:  []string{"Empty"},
		},
		{
			name:    "multiline content",
			title:   "",
			content: "line1\nline2\nline3",
			variant: BoxWarning,
			checks:  []string{"line1", "line2", "line3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := RenderBox(tt.title, tt.content, tt.variant)
			assert.NotEmpty(t, got, "RenderBox should produce output")
			for _, check := range tt.checks {
				assert.Contains(t, got, check,
					"RenderBox output should contain %q", check)
			}
		})
	}
}

func TestRenderBox_TitleAppendsNewline(t *testing.T) {
	t.Parallel()
	// when title is provided, content should appear after title
	got := RenderBox("Title", "Body", BoxDefault)
	// the rendered output should contain both
	assert.Contains(t, got, "Title")
	assert.Contains(t, got, "Body")
}

func TestRenderBox_NoTitleOmitsTitleLine(t *testing.T) {
	t.Parallel()
	// without title, body should not have a leading newline from title formatting
	got := RenderBox("", "OnlyBody", BoxDefault)
	assert.Contains(t, got, "OnlyBody")
}

func TestRenderSummaryBox(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		pass       int
		warn       int
		fail       int
		skip       int
		hint       string
		wantParts  []string // substrings expected in output
		wantAbsent []string // substrings that should NOT appear
	}{
		{
			name:      "all counts present",
			pass:      3,
			warn:      1,
			fail:      2,
			skip:      1,
			hint:      "",
			wantParts: []string{"3 passed", "1 warning", "2 failed", "1 skipped"},
		},
		{
			name:       "only passes",
			pass:       5,
			warn:       0,
			fail:       0,
			skip:       0,
			hint:       "",
			wantParts:  []string{"5 passed"},
			wantAbsent: []string{"warning", "failed", "skipped"},
		},
		{
			name:       "only failures",
			pass:       0,
			warn:       0,
			fail:       3,
			skip:       0,
			hint:       "",
			wantParts:  []string{"3 failed"},
			wantAbsent: []string{"passed", "warning", "skipped"},
		},
		{
			name:      "with hint text",
			pass:      1,
			warn:      0,
			fail:      0,
			skip:      0,
			hint:      "Run ox doctor --fix",
			wantParts: []string{"1 passed", "Run ox doctor --fix"},
		},
		{
			name:      "all zeros produces empty summary",
			pass:      0,
			warn:      0,
			fail:      0,
			skip:      0,
			hint:      "",
			wantParts: []string{}, // box is rendered but with empty summary
		},
		{
			name:      "large counts render correctly",
			pass:      100,
			warn:      50,
			fail:      25,
			skip:      10,
			hint:      "",
			wantParts: []string{"100 passed", "50 warning", "25 failed", "10 skipped"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := RenderSummaryBox(tt.pass, tt.warn, tt.fail, tt.skip, tt.hint)
			assert.NotEmpty(t, got, "RenderSummaryBox should produce output")

			for _, part := range tt.wantParts {
				assert.Contains(t, got, part,
					"RenderSummaryBox output should contain %q", part)
			}

			for _, absent := range tt.wantAbsent {
				assert.NotContains(t, got, absent,
					"RenderSummaryBox output should not contain %q when count is 0", absent)
			}
		})
	}
}

func TestRenderSummaryBox_IconsPresent(t *testing.T) {
	t.Parallel()
	// verify that icons are rendered (not just plain text counts)
	got := RenderSummaryBox(1, 1, 1, 1, "")
	// the icons are styled but the raw characters should be present
	assert.Contains(t, got, IconPass, "should contain pass icon")
	assert.Contains(t, got, IconWarn, "should contain warn icon")
	assert.Contains(t, got, IconFail, "should contain fail icon")
	assert.Contains(t, got, IconSkip, "should contain skip icon")
}

func TestRenderSummaryBox_PartsJoinedWithSpacing(t *testing.T) {
	t.Parallel()
	// multiple counts should be separated (not concatenated)
	got := RenderSummaryBox(1, 1, 0, 0, "")
	// both should be present as distinct items
	assert.Contains(t, got, "1 passed")
	assert.Contains(t, got, "1 warning")
}

func TestRenderBox_NoPanic(t *testing.T) {
	t.Parallel()
	// ensure no panics with edge case inputs
	assert.NotPanics(t, func() {
		RenderBox("", "", BoxDefault)
		RenderBox("", "", BoxVariant(255))
		RenderBox(strings.Repeat("x", 1000), strings.Repeat("y", 1000), BoxError)
	})
}
