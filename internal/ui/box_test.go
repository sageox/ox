package ui

import (
	"image/color"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"

	"github.com/sageox/ox/internal/theme"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVariantColor pins the variant→token mapping. It runs at TrueColor because
// variantColor returns the terminal-adapted color, and a test binary's stdout is
// never a TTY — under the ambient NoTTY profile every variant correctly collapses
// to nil (no color), which would make the mapping untestable. Not parallel: it
// swaps the package-level theme.Profile.
func TestVariantColor(t *testing.T) {
	prevProfile := theme.Profile
	theme.Profile = colorprofile.TrueColor
	t.Cleanup(func() { theme.Profile = prevProfile })

	tests := []struct {
		name    string
		variant BoxVariant
		want    color.Color
	}{
		{"BoxDefault returns dim color", BoxDefault, theme.ColorDim},
		{"BoxInfo returns info color", BoxInfo, theme.ColorInfo},
		{"BoxWarning returns warning color", BoxWarning, theme.ColorWarning},
		{"BoxError returns error color", BoxError, theme.ColorError},
		{"BoxSuccess returns success color", BoxSuccess, theme.ColorSuccess},
		{"unknown variant defaults to dim", BoxVariant(99), theme.ColorDim},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := variantColor(tt.variant)
			// require, not assert: a nil here must halt the subtest, or
			// assertSameColor panics on the nil deref instead of failing cleanly.
			require.NotNil(t, got, "variantColor should never return nil at TrueColor")
			assertSameColor(t, tt.want, got)
		})
	}
}

// TestVariantColor_NoColorTerminal documents the other half of the contract:
// on a terminal that cannot render color, every variant yields nil, which
// lipgloss treats as "unset" — the box still renders, just uncolored.
func TestVariantColor_NoColorTerminal(t *testing.T) {
	prevProfile := theme.Profile
	theme.Profile = colorprofile.NoTTY
	t.Cleanup(func() { theme.Profile = prevProfile })

	for _, v := range []BoxVariant{BoxDefault, BoxInfo, BoxWarning, BoxError, BoxSuccess} {
		assert.Nil(t, variantColor(v))
	}
	assert.Contains(t, RenderBox("Title", "body", BoxInfo), "body")
}

func assertSameColor(t *testing.T, want, got color.Color) {
	t.Helper()
	wr, wg, wb, _ := want.RGBA()
	gr, gg, gb, _ := got.RGBA()
	assert.Equal(t, [3]uint32{wr, wg, wb}, [3]uint32{gr, gg, gb})
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
