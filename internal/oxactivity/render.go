package oxactivity

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/sageox/ox/internal/theme"
)

// Segment is everything the activity line can show. Empty fields are omitted.
type Segment struct {
	// Action is an Activity label, e.g. "posted murmur".
	Action string
	// WhisperFrom is the raw sender of the latest whisper; Render shows it
	// through DisplayName ("160305533-dedhaw" → "dedhaw").
	WhisperFrom string
	// WhisperTopic is the murmur topic, shown as "[wip]".
	WhisperTopic string
	// WhisperContent is the whisper text. It is the first thing shortened
	// when the line exceeds its width.
	WhisperContent string
	// Age is shown last ("3m"); zero or negative omits it.
	Age time.Duration
}

// Ellipsis marks truncated text.
const Ellipsis = "…"

// mark is the fixed lead of every activity line.
const mark = "● ox"

type pieceStyle int

const (
	stylePlain pieceStyle = iota
	styleMark             // green + bold
	styleGreen            // action, whisper sender
	styleDim              // detail text
)

type piece struct {
	text  string
	style pieceStyle
}

// Render draws seg as a single line with no trailing newline, e.g.
//
//	● ox  posted murmur · whisper from ryan [wip] Refactoring auth… 3m
//
// "● ox" is green and bold, the action and whisper sender are green, and the
// rest is dim. With color false the output contains no escape bytes. All text
// is stripped of control characters first, so a teammate's whisper cannot
// inject escape sequences into a harness. With no action and no whisper the
// line is just "● ox".
func Render(seg Segment, color bool) string {
	return RenderWidth(seg, color, 0)
}

// RenderWidth is Render limited to width terminal cells (0 means unlimited).
// The whisper content is shortened first; if that is not enough the whole
// line is cut with an ellipsis. Widths are measured in display cells, so wide
// runes and emoji are counted correctly.
func RenderWidth(seg Segment, color bool, width int) string {
	pieces, content := layout(seg)
	if width > 0 {
		pieces = fit(pieces, content, width)
	}
	var b strings.Builder
	for _, p := range pieces {
		if color {
			b.WriteString(styleFor(p.style).Render(p.text))
		} else {
			b.WriteString(p.text)
		}
	}
	return b.String()
}

// layout builds the line's pieces and returns the index of the whisper-content
// piece (-1 when there is none), which fit shortens first.
func layout(seg Segment) ([]piece, int) {
	action := cleanText(seg.Action, 0)
	from := DisplayName(cleanText(seg.WhisperFrom, 0))
	topic := cleanText(seg.WhisperTopic, 0)
	content := cleanText(seg.WhisperContent, 0)

	ps := []piece{{mark, styleMark}}
	contentAt := -1
	if action != "" {
		ps = append(ps, piece{"  ", stylePlain}, piece{action, styleGreen})
	}
	if from != "" || content != "" {
		if action != "" {
			ps = append(ps, piece{" · ", styleDim})
		} else {
			ps = append(ps, piece{"  ", stylePlain})
		}
		if from != "" {
			ps = append(ps, piece{"whisper from ", styleDim}, piece{from, styleGreen})
		} else {
			ps = append(ps, piece{"whisper", styleDim})
		}
		if topic != "" {
			ps = append(ps, piece{" [" + topic + "]", styleDim})
		}
		if content != "" {
			contentAt = len(ps)
			ps = append(ps, piece{" " + content, styleDim})
		}
	}
	if seg.Age > 0 {
		ps = append(ps, piece{" " + FormatAge(seg.Age), styleDim})
	}
	return ps, contentAt
}

// fit shrinks pieces to width cells: the whisper content first, then a hard
// cut from the end.
func fit(ps []piece, content, width int) []piece {
	total := 0
	for _, p := range ps {
		total += ansi.StringWidth(p.text)
	}
	over := total - width
	if over <= 0 {
		return ps
	}

	if i := content; i >= 0 {
		w := ansi.StringWidth(ps[i].text)
		keep := w - over
		if keep >= 3 { // " x…" — anything shorter carries no meaning
			ps[i].text = Truncate(ps[i].text, keep)
			return ps
		}
		ps = append(ps[:i:i], ps[i+1:]...)
		over -= w
		if over <= 0 {
			return ps
		}
	}

	out := make([]piece, 0, len(ps))
	remaining := width
	for _, p := range ps {
		w := ansi.StringWidth(p.text)
		if w <= remaining {
			out = append(out, p)
			remaining -= w
			continue
		}
		if remaining > 0 {
			out = append(out, piece{Truncate(p.text, remaining), p.style})
		}
		break
	}
	return out
}

// Truncate shortens s to at most width display cells, ending in an ellipsis
// when anything was cut. It is rune- and width-aware; width <= 0 yields "".
func Truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, Ellipsis)
}

// FormatAge renders a duration compactly: "now", "3m", "2h", "5d".
func FormatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}

// ColorAllowed reports whether the environment permits color in the activity
// line. Harnesses capture ox's stdout through a pipe, so theme.ColorEnabled()
// is always false there even though the harness renders ANSI; this asks only
// the coworker's explicit opt-outs: NO_COLOR (sacred) and an
// OX_COLOR_PROFILE of ascii/none/notty.
func ColorAllowed() bool {
	if noColor, _ := strconv.ParseBool(os.Getenv("NO_COLOR")); noColor {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OX_COLOR_PROFILE"))) {
	case "ascii", "none", "notty":
		return false
	}
	return true
}

// Colors are fixed at 256-color depth rather than adapted to theme.Profile:
// ox's stdout is a pipe inside a harness, so the profile resolves to no color,
// yet the harness paints our bytes onto a real terminal of unknown depth.
// 24-bit "38;2;R;G;B" is misread as background colors by 256-color terminals
// (see internal/theme/profile.go); "38;5;N" is safe on both those terminals
// and Codex's ANSI parser.
var (
	greenColor = theme.AdaptTo(colorprofile.ANSI256, theme.ColorSuccess)
	dimColor   = theme.AdaptTo(colorprofile.ANSI256, theme.ColorDim)

	markStyle  = lipgloss.NewStyle().Foreground(greenColor).Bold(true)
	greenStyle = lipgloss.NewStyle().Foreground(greenColor)
	dimStyle   = lipgloss.NewStyle().Foreground(dimColor)
	plainStyle = lipgloss.NewStyle()
)

func styleFor(s pieceStyle) lipgloss.Style {
	switch s {
	case styleMark:
		return markStyle
	case styleGreen:
		return greenStyle
	case styleDim:
		return dimStyle
	default:
		return plainStyle
	}
}

// cleanText makes untrusted text safe for a single terminal line: control
// characters (including ESC) are removed, runs of whitespace collapse to one
// space, and the result is trimmed. max > 0 caps the width with an ellipsis.
func cleanText(s string, max int) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			space = b.Len() > 0
			continue
		case unicode.IsControl(r), r == '\u2028', r == '\u2029':
			continue
		case unicode.Is(unicode.Cf, r): // bidi overrides, zero-width joiners
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	out := b.String()
	if max > 0 {
		out = Truncate(out, max)
	}
	return out
}

// DisplayName turns a whisper sender into what a coworker reads: a leading
// numeric GitHub-id prefix is stripped ("160305533-dedhaw" → "dedhaw");
// anything else passes through unchanged.
func DisplayName(sender string) string {
	if i := strings.IndexByte(sender, '-'); i > 0 && i < len(sender)-1 && isDigits(sender[:i]) {
		return sender[i+1:]
	}
	return sender
}
