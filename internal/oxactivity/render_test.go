package oxactivity

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fullSegment = Segment{
	Action:         "posted murmur",
	WhisperFrom:    "160305533-ryan",
	WhisperTopic:   "wip",
	WhisperContent: "Refactoring auth middleware",
	Age:            3 * time.Minute,
}

// --- A. Plain rendering is byte-exact ---
// Failure prevented: a harness that does not want color (NO_COLOR) receives
// escape bytes, or the two harnesses drift in wording.
func TestRender_Plain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		seg  Segment
		want string
	}{
		{"full", fullSegment, "● ox  posted murmur · whisper from ryan [wip] Refactoring auth middleware 3m"},
		{"action only", Segment{Action: "loaded team context"}, "● ox  loaded team context"},
		{"action with age", Segment{Action: "saved plan", Age: 2 * time.Hour}, "● ox  saved plan 2h"},
		{"whisper only", Segment{WhisperFrom: "dedhaw", WhisperContent: "hi"}, "● ox  whisper from dedhaw hi"},
		{"whisper without sender", Segment{WhisperContent: "hi"}, "● ox  whisper hi"},
		{"empty", Segment{}, "● ox"},
		{"whitespace collapsed", Segment{Action: "  posted\n\tmurmur "}, "● ox  posted murmur"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Render(tt.seg, false)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, "\x1b")
			assert.NotContains(t, got, "\n")
		})
	}
}

// --- B. Color rendering ---
// Failure prevented: the line loses its green, uses 24-bit color that paints
// backgrounds on 256-color terminals, or styles differ from the plain words.
func TestRender_Color(t *testing.T) {
	t.Parallel()

	got := Render(fullSegment, true)
	require.Contains(t, got, "\x1b[", "color output must carry SGR sequences")
	assert.NotContains(t, got, "38;2;", "24-bit color is misread by 256-color terminals")
	assert.Contains(t, got, "38;5;", "foreground must be a 256-color index")
	assert.Contains(t, got, "\x1b[1;", "the ● ox mark is bold")
	assert.False(t, strings.HasSuffix(got, "\n"))

	// Stripping escapes reproduces the plain line exactly: color only adds.
	assert.Equal(t, Render(fullSegment, false), ansi.Strip(got))

	// The action and sender share the mark's green; detail text is a different (dim) color.
	greenPrefix, _, _ := strings.Cut(greenStyle.Render("x"), "x")
	dimPrefix, _, _ := strings.Cut(dimStyle.Render("x"), "x")
	require.NotEmpty(t, greenPrefix)
	assert.NotEqual(t, greenPrefix, dimPrefix)
	assert.Contains(t, got, greenPrefix+"posted murmur")
	assert.Contains(t, got, greenPrefix+"ryan")
	assert.Contains(t, got, dimPrefix+" Refactoring auth middleware")
}

// TestRender_StripsInjectedEscapes: whisper text comes from teammates.
// Failure prevented: a murmur containing ESC sequences rewrites the
// coworker's status line or terminal.
func TestRender_StripsInjectedEscapes(t *testing.T) {
	t.Parallel()

	seg := Segment{
		Action:         "posted\x1b[31m murmur",
		WhisperFrom:    "ev\x1b]0;pwned\x07il",
		WhisperTopic:   "w\x1bip",
		WhisperContent: "clear\x1b[2J\x9b2J\u202eme\r\n",
	}
	got := Render(seg, false)
	assert.NotContains(t, got, "\x1b")
	assert.NotContains(t, got, "\x07")
	assert.NotContains(t, got, "\x9b")
	assert.NotContains(t, got, "\u202e")
	assert.NotContains(t, got, "\r")
	assert.NotContains(t, got, "\n")
	assert.Equal(t, ansi.Strip(Render(seg, true)), got)
}

// --- C. Truncation ---
// Failure prevented: a long whisper overflows a narrow status line and wraps
// or pushes the action off screen.
func TestRenderWidth_Truncation(t *testing.T) {
	t.Parallel()

	full := Render(fullSegment, false)
	fullWidth := ansi.StringWidth(full)

	tests := []struct {
		name  string
		width int
		want  string
	}{
		{"unlimited", 0, full},
		{"fits exactly", fullWidth, full},
		{"content shortened first", fullWidth - 10, "● ox  posted murmur · whisper from ryan [wip] Refactoring auth… 3m"},
		{"content dropped when too short", 50, "● ox  posted murmur · whisper from ryan [wip] 3m"},
		{"hard cut after content gone", 30, "● ox  posted murmur · whisper…"},
		{"tiny", 3, "● …"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := RenderWidth(fullSegment, false, tt.width)
			assert.Equal(t, tt.want, got)
			if tt.width > 0 {
				assert.LessOrEqual(t, ansi.StringWidth(got), tt.width)
			}
			colored := RenderWidth(fullSegment, true, tt.width)
			assert.Equal(t, got, ansi.Strip(colored), "color must not change truncation")
		})
	}
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

func TestFormatAge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "now"},
		{59 * time.Second, "now"},
		{time.Minute, "1m"},
		{59 * time.Minute, "59m"},
		{time.Hour, "1h"},
		{23 * time.Hour, "23h"},
		{49 * time.Hour, "2d"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, FormatAge(tt.d), tt.d.String())
	}
}

// Failure prevented: whisper senders render as "160305533-dedhaw", an
// identifier no coworker recognizes.
func TestDisplayName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in, want string
	}{
		{"160305533-dedhaw", "dedhaw"},
		{"1-ryan-snodgrass", "ryan-snodgrass"},
		{"ryan", "ryan"},
		{"ryan-snodgrass", "ryan-snodgrass"},
		{"a1-ryan", "a1-ryan"},
		{"123", "123"},
		{"123-", "123-"},
		{"-ryan", "-ryan"},
		{"", ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, DisplayName(tt.in), tt.in)
	}
}

// Failure prevented: NO_COLOR is ignored because ox's own stdout is a pipe and
// the activity line decides color by environment alone.
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
