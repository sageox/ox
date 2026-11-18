// Package flowlane draws the SageOx flow lane as one line of terminal text:
// what an ox call moved between SageOx and the AI coworker's session, in
// which direction, and how much.
//
//	SAGEOX ●┄┄•┄┄┄┄┄•┄┄┄┄┄•┄○ SESSION  searching team discussions
//	SAGEOX ●━━━━━━•━•━•━━━━━● SESSION  searched team memory · ~1.2k from 5 discussions · 0.8s
//	SAGEOX ●━━━━━━━━━━━━━━━━● SESSION  searched team memory · ~1.2k from 5 discussions · 0.8s
//	SAGEOX ●╶─              ● SESSION  failed while searching team discussions · 1.2s
//
// Context arriving travels left to right as filled dots; anything sent back
// travels right to left as hollow rings: marching along the dotted line while
// the call runs, then landing, then the lane settles to a still line. A call
// that failed leaves a red stub. It is the terminal twin of the SVG lane the Claude Code plugin draws
// (internal/usagepane/mod/hooks/scene.ts) and shares its grammar, cell for
// cell, with the plugin's own text lane (hooks/textlane.ts): the two test
// suites assert the same golden rows. design/sageox-flow/SPEC.md, "Terminal
// lane", is the design record.
//
// The package does no I/O and holds no clock: Render is a pure function of a
// Lane and how long ago it ended (or, for a running lane, started).
package flowlane

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/sageox/ox/internal/oxactivity"
	"github.com/sageox/ox/internal/theme"
)

const (
	// Track is the lane's width in cells, both end nodes included. It is the
	// same on every row so stacked rows read as small multiples.
	Track = 18
	// NarrowColumns is the width below which the end labels are dropped.
	NarrowColumns = 60
	// MaxDots is the most dots or rings shown in flight.
	MaxDots = 5
	// AnimDuration is how long after a call ends its lane is still moving.
	AnimDuration = 2400 * time.Millisecond

	drawDuration = 600 * time.Millisecond
	dotDuration  = 1400 * time.Millisecond
	dotStagger   = 180 * time.Millisecond
	tokensPerDot = 1000

	// A running lane's dots: one every marchPeriod cells, each stepping one
	// cell per marchStep. MarchMax is how long a running lane keeps moving: a
	// call whose end was never seen must not animate forever.
	marchPeriod = 6
	marchStep   = 120 * time.Millisecond
	// MarchMax is exported for the callers that bound a running call's age.
	MarchMax = 10 * time.Minute
	inner    = Track - 2

	labelSageox  = "SAGEOX "
	labelSession = " SESSION"
	digestGap    = "  "
	sep          = " · "
	ellipsis     = "…"

	node      = '●'
	ring      = '○'
	dotted    = '┄'
	thin      = '─'
	thick     = '━'
	stubStart = '╶'
	dotIn     = '•'
	dotOut    = '◦'
)

// Lane is one ox call's movement of team data.
type Lane struct {
	Direction oxactivity.Direction
	// Label is the finished wording ("searched team memory"); Step is the
	// wording while the call runs ("searching team discussions").
	Label, Step string
	// Tokens is the estimated size of what moved. Unsized marks an outbound
	// call whose payload ox could not see.
	Tokens  int
	Unsized bool
	// Items and ItemsNoun name what moved when the call's result counted it
	// ("5", "discussions"); zero Items shows the tokens alone.
	Items     int
	ItemsNoun string
	// Duration is how long the call ran; zero when not measured.
	Duration time.Duration
	// Running is a call still in progress. Failed is one that ended in error.
	Running, Failed bool
}

// Options controls one Render.
type Options struct {
	// Columns is the width to fit; 0 is unlimited.
	Columns int
	// Color adds ANSI color. Without it the output has no escape bytes and
	// the glyphs alone carry the meaning.
	Color bool
	// Still draws the settled frame whatever sinceEnd says.
	Still bool
}

type tone int

const (
	toneQuiet tone = iota
	toneStream
	toneBad
)

type cell struct {
	ch   rune
	tone tone
}

type seg struct {
	text   string
	number bool
	bad    bool
}

// ANSI256 for the reason oxactivity pins it: a status pane's terminal may
// misread 24-bit color as a background.
var (
	streamStyle = lipgloss.NewStyle().Foreground(theme.AdaptTo(colorprofile.ANSI256, theme.ColorSuccess))
	quietStyle  = lipgloss.NewStyle().Foreground(theme.AdaptTo(colorprofile.ANSI256, theme.ColorDim))
	// The status line's own red (its `● rec` dot): ColorError downsamples to
	// a salmon that reads as orange in a status bar.
	badStyle = lipgloss.NewStyle().Foreground(ansi.IndexedColor(196)) // theme.AnsiError
)

func styleOf(t tone) lipgloss.Style {
	switch t {
	case toneStream:
		return streamStyle
	case toneBad:
		return badStyle
	default:
		return quietStyle
	}
}

func (l Lane) flowed() bool {
	return !l.Running && !l.Failed && (l.Tokens > 0 || l.Unsized)
}

// Moving reports whether a lane is still animating at since: a running call
// for MarchMax after it started, and a call that moved something for
// AnimDuration after it ended.
func Moving(l Lane, since time.Duration) bool {
	if l.Running {
		return since >= 0 && since < MarchMax
	}
	return l.flowed() && since >= 0 && since < AnimDuration
}

// DotCount is one dot per 1k tokens, at least one, at most MaxDots.
func DotCount(tokens int) int {
	n := (max(tokens, 0) + tokensPerDot - 1) / tokensPerDot
	return min(MaxDots, max(1, n))
}

func track(l Lane, sinceEnd time.Duration, still bool) []cell {
	along := make([]cell, 0, Track)
	quiet := func(r rune) cell { return cell{r, toneQuiet} }
	stream := func(r rune) cell { return cell{r, toneStream} }
	dot := dotIn
	if l.Direction == oxactivity.DirectionOut {
		dot = dotOut
	}
	switch {
	case l.Running:
		// sinceEnd is the time since the call started. Dots march from the
		// origin toward the destination for as long as it runs.
		march := !still && Moving(l, sinceEnd)
		step := int(sinceEnd / marchStep)
		along = append(along, quiet(node))
		for k := 1; k <= inner; k++ {
			if march && (k-1-step%marchPeriod+marchPeriod)%marchPeriod == 0 {
				along = append(along, stream(dot))
			} else {
				along = append(along, quiet(dotted))
			}
		}
		along = append(along, quiet(ring))
	case l.flowed():
		line := thin
		if l.Tokens >= tokensPerDot {
			line = thick
		}
		flight := !still && Moving(l, sinceEnd)
		reach := inner
		if flight && sinceEnd < drawDuration {
			reach = int(int64(inner) * int64(sinceEnd) / int64(drawDuration))
		}
		along = append(along, quiet(node))
		for k := 1; k <= inner; k++ {
			if k <= reach {
				along = append(along, stream(line))
			} else {
				along = append(along, quiet(dotted))
			}
		}
		along = append(along, stream(node))
		if flight {
			for i := range DotCount(l.Tokens) {
				ti := sinceEnd - time.Duration(i)*dotStagger
				if ti >= 0 && ti < dotDuration {
					k := 1 + min(inner-1, int(int64(inner)*int64(ti)/int64(dotDuration)))
					along[k] = stream(dot)
				}
			}
		}
	default:
		// failed or nothing moved: a short stub off the origin. A failure is
		// drawn in the error color, ends included; an empty result stays quiet.
		end := quiet
		if l.Failed {
			end = func(r rune) cell { return cell{r, toneBad} }
		}
		along = append(along, end(node), end(stubStart), end(thin))
		for k := 3; k <= inner; k++ {
			along = append(along, quiet(' '))
		}
		along = append(along, end(node))
	}
	if l.Direction == oxactivity.DirectionOut {
		for i, j := 0, len(along)-1; i < j; i, j = i+1, j-1 {
			along[i], along[j] = along[j], along[i]
		}
	}
	return along
}

// FormatTokens renders an estimate as `~80`, `~1.2k`, `~15k`.
func FormatTokens(n int) string {
	n = max(n, 0)
	if n < 1000 {
		return fmt.Sprintf("~%d", n)
	}
	k := float64(n) / 1000
	if k < 10 {
		return "~" + strings.TrimSuffix(fmt.Sprintf("%.1f", math.Round(k*10)/10), ".0") + "k"
	}
	return fmt.Sprintf("~%dk", int(math.Round(k)))
}

// FormatDuration renders `0.8s`, `3.4s`, `12s`, `1m 12s`.
func FormatDuration(d time.Duration) string {
	ms := float64(d) / float64(time.Millisecond)
	tenths := max(1, int(math.Round(ms/100)))
	if tenths < 100 {
		return fmt.Sprintf("%.1fs", float64(tenths)/10)
	}
	s := int(math.Round(ms / 1000))
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm %ds", s/60, s%60)
}

// digest is the words beside the lane, by outcome. The duration is left off
// when it was not measured.
func digest(l Lane) []seg {
	var out []seg
	withDuration := func() []seg {
		if l.Duration > 0 {
			out = append(out, seg{text: sep}, seg{text: FormatDuration(l.Duration), number: true})
		}
		return out
	}
	nothing := "nothing came back"
	if l.Direction == oxactivity.DirectionOut {
		nothing = "nothing sent"
	}
	switch {
	case l.Running:
		return []seg{{text: l.Step}}
	case l.Failed:
		out = []seg{{text: "failed", bad: true}, {text: " while " + l.Step}}
	case l.Unsized:
		out = []seg{{text: l.Label}, {text: sep}, {text: "size unknown"}}
	case l.Tokens <= 0:
		out = []seg{{text: l.Label}, {text: sep}, {text: nothing}}
	default:
		out = []seg{{text: l.Label}, {text: sep}, {text: FormatTokens(l.Tokens), number: true}}
		if l.Items > 0 {
			noun := l.ItemsNoun
			if noun == "" {
				noun = "items"
			}
			prep := " from "
			if l.Direction == oxactivity.DirectionOut {
				prep = " in "
			}
			out = append(out, seg{text: fmt.Sprintf("%s%d %s", prep, l.Items, noun)})
		}
	}
	return withDuration()
}

func cellsOf(segs []seg) int {
	n := 0
	for _, s := range segs {
		n += utf8.RuneCountInString(s.text)
	}
	return n
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if n < len(r) {
		r = r[:n]
	}
	return strings.TrimRight(string(r), " ")
}

// fit trims the digest to room cells in the SVG lane's order: shrink the
// label, then drop it (the row above already names the call), then cut the
// tail. The numbers go last.
func fit(segs []seg, room int) []seg {
	if len(segs) == 0 || cellsOf(segs) <= room {
		return segs
	}
	first, rest := segs[0], segs[1:]
	plain := !first.number && !first.bad
	if chars := room - cellsOf(rest) - 1; chars >= 12 && plain {
		return append([]seg{{text: firstRunes(first.text, chars) + ellipsis}}, rest...)
	}
	if plain && len(rest) > 0 && rest[0].text == sep && cellsOf(rest[1:]) <= room {
		return rest[1:]
	}
	var out []seg
	used := 1
	for _, s := range segs {
		w := utf8.RuneCountInString(s.text)
		if used+w > room {
			if left := room - used; left > 0 {
				out = append(out, seg{text: firstRunes(s.text, left), number: s.number, bad: s.bad})
			}
			break
		}
		out = append(out, s)
		used += w
	}
	if room > 0 {
		out = append(out, seg{text: ellipsis})
	}
	return out
}

// Render draws the lane as one line with no trailing newline. sinceEnd is how
// long ago the call ended, or for a running lane how long ago it started.
// Anything at or past AnimDuration after the end (or opts.Still) draws the
// settled frame; opts.Still draws a running lane without its marching dots.
// A width too narrow for the track draws nothing: a wrapped lane would break
// a pane that redraws in place.
func Render(l Lane, sinceEnd time.Duration, opts Options) string {
	if opts.Columns > 0 && opts.Columns < Track {
		return ""
	}
	labels := opts.Columns <= 0 || opts.Columns >= NarrowColumns
	room := math.MaxInt32
	if opts.Columns > 0 {
		prefix := Track + len(digestGap)
		if labels {
			prefix += len(labelSageox) + len(labelSession)
		}
		room = max(0, opts.Columns-prefix)
	}
	paint := func(t tone, s string) string {
		if !opts.Color || strings.TrimSpace(s) == "" {
			return s
		}
		return styleOf(t).Render(s)
	}

	var b strings.Builder
	if labels {
		b.WriteString(paint(toneQuiet, labelSageox))
	}
	// Runs of one tone are painted together, so a colored line stays short.
	cells := track(l, sinceEnd, opts.Still)
	for i := 0; i < len(cells); {
		j := i
		var run strings.Builder
		for j < len(cells) && cells[j].tone == cells[i].tone {
			run.WriteRune(cells[j].ch)
			j++
		}
		b.WriteString(paint(cells[i].tone, run.String()))
		i = j
	}
	if labels {
		b.WriteString(paint(toneQuiet, labelSession))
	}
	segs := fit(digest(l), room)
	if len(segs) > 0 {
		b.WriteString(digestGap)
		for _, s := range segs {
			t := toneQuiet
			if s.bad {
				t = toneBad
			}
			b.WriteString(paint(t, s.text))
		}
	}
	return b.String()
}
