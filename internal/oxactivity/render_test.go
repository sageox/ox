package oxactivity

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fullCounts = Counts{Discussions: 3, Plans: 1, Docs: 2, Sessions: 4, Murmurs: 1}

// TestRender_Idle is byte-exact with color off. Failure prevented: a session
// where nothing has been used yet reads as anything but the bare mark, or
// escape bytes reach a coworker who asked for none.
func TestRender_Idle(t *testing.T) {
	t.Parallel()
	got := Render(Counts{}, false)
	assert.Equal(t, "● ox", got)
	line, width := RenderWidth(Counts{}, false, 80)
	assert.Equal(t, "● ox", line)
	assert.Equal(t, 4, width)
	assert.True(t, Counts{}.IsZero())
	assert.False(t, Counts{Murmurs: 1}.IsZero())
}

// TestRender_Pluralization names one item in the singular and many in the
// plural, in the fixed order. Failure prevented: "1 discussions", or buckets
// that reshuffle between refreshes.
func TestRender_Pluralization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		c    Counts
		want string
	}{
		{"all singular", Counts{1, 1, 1, 1, 1}, "● ox  1 discussion · 1 plan · 1 doc · 1 session · 1 murmur"},
		{"all plural", Counts{2, 3, 4, 5, 12}, "● ox  2 discussions · 3 plans · 4 docs · 5 sessions · 12 murmurs"},
		{"mixed", Counts{Discussions: 3, Plans: 1, Murmurs: 1}, "● ox  3 discussions · 1 plan · 1 murmur"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Render(tt.c, false)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, "\x1b")
			assert.NotContains(t, got, "\n")
			assert.Equal(t, ansi.StringWidth(got), Width(tt.c))
		})
	}
}

// TestRender_ZeroBucketsOmitted shows only what was used. Failure prevented:
// "0 plans" noise drawing the eye to nothing.
func TestRender_ZeroBucketsOmitted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		c    Counts
		want string
	}{
		{"only plans", Counts{Plans: 2}, "● ox  2 plans"},
		{"only murmurs", Counts{Murmurs: 1}, "● ox  1 murmur"},
		{"gap in the middle", Counts{Discussions: 1, Sessions: 2}, "● ox  1 discussion · 2 sessions"},
		{"negative treated as zero", Counts{Docs: -1, Plans: 1}, "● ox  1 plan"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, Render(tt.c, false))
			assert.NotContains(t, Render(tt.c, false), "0 ")
		})
	}
}

// TestRender_FitDropsSmallestFirst keeps the biggest signals when the bar is
// narrow and never cuts an item in half. Failure prevented: the line wraps,
// or a truncated "3 discu…" that tells the coworker nothing.
func TestRender_FitDropsSmallestFirst(t *testing.T) {
	t.Parallel()

	full := Render(fullCounts, false) // ● ox  3 discussions · 1 plan · 2 docs · 4 sessions · 1 murmur
	tests := []struct {
		name  string
		c     Counts
		width int
		want  string
	}{
		{"unlimited", fullCounts, 0, full},
		{"fits exactly", fullCounts, ansi.StringWidth(full), full},
		// smallest are plan(1) and murmur(1): the rightmost (murmur) goes first
		{"one over drops rightmost smallest", fullCounts, ansi.StringWidth(full) - 1, "● ox  3 discussions · 1 plan · 2 docs · 4 sessions"},
		{"then the other 1", fullCounts, 41, "● ox  3 discussions · 2 docs · 4 sessions"},
		{"then 2", fullCounts, 32, "● ox  3 discussions · 4 sessions"},
		{"then 3", fullCounts, 20, "● ox  4 sessions"},
		{"nothing fits but the mark", fullCounts, 10, "● ox"},
		{"mark is the floor", fullCounts, 2, "● ox"},
		{"tie keeps leftmost", Counts{Discussions: 2, Plans: 2}, 20, "● ox  2 discussions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, width := RenderWidth(tt.c, false, tt.width)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, ansi.StringWidth(got), width)
			if tt.width >= 4 {
				assert.LessOrEqual(t, width, tt.width)
			}
			colored, cw := RenderWidth(tt.c, true, tt.width)
			assert.Equal(t, got, ansi.Strip(colored), "color must not change fitting")
			assert.Equal(t, width, cw, "width is the plain width with color on")
			assert.Equal(t, got, Render(Fit(tt.c, tt.width), false))
		})
	}
}

// TestRender_Color: numbers green, nouns and separators dim, mark bold.
// Failure prevented: the line loses its green, uses 24-bit color that paints
// backgrounds on 256-color terminals, or styles differ from the plain words.
func TestRender_Color(t *testing.T) {
	t.Parallel()

	got := Render(Counts{Discussions: 3, Plans: 1}, true)
	require.Contains(t, got, "\x1b[", "color output must carry SGR sequences")
	assert.NotContains(t, got, "38;2;", "24-bit color is misread by 256-color terminals")
	assert.Contains(t, got, "38;5;", "foreground must be a 256-color index")
	assert.Contains(t, got, "\x1b[1;", "the ● ox mark is bold")
	assert.False(t, strings.HasSuffix(got, "\n"))
	assert.Equal(t, Render(Counts{Discussions: 3, Plans: 1}, false), ansi.Strip(got), "color only adds")

	greenPrefix, _, _ := strings.Cut(greenStyle.Render("x"), "x")
	dimPrefix, _, _ := strings.Cut(dimStyle.Render("x"), "x")
	require.NotEmpty(t, greenPrefix)
	assert.NotEqual(t, greenPrefix, dimPrefix)
	assert.Contains(t, got, greenPrefix+"3")
	assert.Contains(t, got, greenPrefix+"1")
	assert.Contains(t, got, dimPrefix+" discussions")
	assert.Contains(t, got, dimPrefix+" · ")
	assert.Contains(t, got, dimPrefix+" plan")

	idle := Render(Counts{}, true)
	assert.Equal(t, markStyle.Render("● ox"), idle)
}

func TestTruncate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{"fits", "hello", 5, "hello"},
		{"cut", "hello world", 6, "hello…"},
		{"multibyte runes", "héllo wörld", 6, "héllo…"},
		{"wide runes", "日本語テキスト", 5, "日本…"},
		{"zero", "hello", 0, ""},
		{"negative", "hello", -1, ""},
		{"empty", "", 3, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Truncate(tt.in, tt.width)
			assert.Equal(t, tt.want, got)
			assert.LessOrEqual(t, ansi.StringWidth(got), max(tt.width, 0))
		})
	}
}

// Failure prevented: NO_COLOR is ignored because ox's own stdout is a pipe and
// the status line decides color by environment alone.
func TestColorAllowed(t *testing.T) {
	tests := []struct {
		name, noColor, profile string
		want                   bool
	}{
		{"default", "", "", true},
		{"NO_COLOR=1", "1", "", false},
		{"NO_COLOR=true", "true", "", false},
		{"NO_COLOR=0", "0", "", true},
		{"profile ascii", "", "ascii", false},
		{"profile notty", "", "NoTTY", false},
		{"profile ansi256", "", "ansi256", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tt.noColor)
			t.Setenv("OX_COLOR_PROFILE", tt.profile)
			assert.Equal(t, tt.want, ColorAllowed())
		})
	}
}
