package oxactivity

import (
	"os"
	"strconv"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/sageox/ox/internal/theme"
)

// Counts is what the status line shows: how many distinct items of each kind
// of team context influenced the harness session. Zero buckets are omitted.
type Counts struct {
	Discussions int
	Plans       int
	Docs        int
	Sessions    int
	Murmurs     int
}

// IsZero reports whether nothing has been counted yet.
func (c Counts) IsZero() bool { return c == Counts{} }

// bucket is one count with its singular and plural nouns.
type bucket struct {
	n              int
	singular, many string
}

// buckets returns the counts in display order: discussions, plans, docs,
// sessions, murmurs.
func (c Counts) buckets() []bucket {
	return []bucket{
		{c.Discussions, "discussion", "discussions"},
		{c.Plans, "plan", "plans"},
		{c.Docs, "doc", "docs"},
		{c.Sessions, "session", "sessions"},
		{c.Murmurs, "murmur", "murmurs"},
	}
}

// setBucket writes n into the i-th bucket of buckets' order.
func (c *Counts) setBucket(i, n int) {
	switch i {
	case 0:
		c.Discussions = n
	case 1:
		c.Plans = n
	case 2:
		c.Docs = n
	case 3:
		c.Sessions = n
	case 4:
		c.Murmurs = n
	}
}

// Ellipsis marks truncated text.
const Ellipsis = "…"

// Mark is the fixed lead of every ox status line.
const Mark = "● ox"

type pieceStyle int

const (
	stylePlain pieceStyle = iota
	styleMark             // green + bold
	styleGreen            // numbers
	styleDim              // nouns and separators
)

type piece struct {
	text  string
	style pieceStyle
}

// Render draws c as a single line with no trailing newline, e.g.
//
//	● ox  3 discussions · 1 plan · 1 murmur
//
// "● ox" is green and bold, the numbers are green, and the nouns and " · "
// separators are dim. With color false the output contains no escape bytes.
// With every count zero the line is just "● ox".
func Render(c Counts, color bool) string {
	line, _ := RenderWidth(c, color, 0)
	return line
}

// RenderWidth is Render limited to width terminal cells (0 means unlimited).
// While the line is too wide the smallest non-zero count is dropped (on a tie,
// the rightmost), so what remains is always whole "N noun" items; "● ox" is
// never dropped. It returns the line and its width in cells, which is the
// plain-text width whether or not color is on.
func RenderWidth(c Counts, color bool, width int) (string, int) {
	if width > 0 {
		c = Fit(c, width)
	}
	pieces := layout(c)
	var b strings.Builder
	cells := 0
	for _, p := range pieces {
		cells += ansi.StringWidth(p.text)
		if color {
			b.WriteString(styleFor(p.style).Render(p.text))
		} else {
			b.WriteString(p.text)
		}
	}
	return b.String(), cells
}

// Fit drops the smallest non-zero count (ties: the rightmost in display
// order) until the plain line fits width cells, and returns what is left.
// width <= 0 means unlimited. "● ox" alone is the floor, even if wider than
// width.
func Fit(c Counts, width int) Counts {
	if width <= 0 {
		return c
	}
	for Width(c) > width && !c.IsZero() {
		drop := -1
		for i, b := range c.buckets() {
			if b.n == 0 {
				continue
			}
			if drop < 0 || b.n <= c.buckets()[drop].n {
				drop = i
			}
		}
		c.setBucket(drop, 0)
	}
	return c
}

// Width is the plain-text width of Render(c, false) in terminal cells.
func Width(c Counts) int {
	w := 0
	for _, p := range layout(c) {
		w += ansi.StringWidth(p.text)
	}
	return w
}

// layout builds the line's pieces.
func layout(c Counts) []piece {
	ps := []piece{{Mark, styleMark}}
	first := true
	for _, b := range c.buckets() {
		if b.n <= 0 {
			continue
		}
		if first {
			ps = append(ps, piece{"  ", stylePlain})
			first = false
		} else {
			ps = append(ps, piece{" · ", styleDim})
		}
		noun := b.many
		if b.n == 1 {
			noun = b.singular
		}
		ps = append(ps, piece{strconv.Itoa(b.n), styleGreen}, piece{" " + noun, styleDim})
	}
	return ps
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

// ColorAllowed reports whether the environment permits color in the status
// line. Harnesses capture ox's stdout through a pipe, so theme.ColorEnabled()
// is always false there even though the harness renders ANSI; this asks only
// the coworker's explicit opt-outs: NO_COLOR (sacred) and an
// OX_COLOR_PROFILE of ascii/none/notty.
func ColorAllowed() bool {
	return ColorAllowedFor(os.Getenv("NO_COLOR"))
}

// Colors are fixed at 256-color depth rather than adapted to theme.Profile:
// ox's stdout is a pipe inside a harness, so the profile resolves to no color,
// yet the harness paints our bytes onto a real terminal of unknown depth.
// 24-bit "38;2;R;G;B" is misread as background colors by 256-color terminals
// (see internal/theme/profile.go); "38;5;N" is safe on all of them.
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
