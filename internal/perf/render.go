package perf

import (
	"fmt"
	"io"
	"strings"
	"time"

	"go.opentelemetry.io/otel/codes"

	"github.com/sageox/ox/internal/ui"
)

// RenderTree writes the node tree to w using dotted-leader alignment so
// durations line up in a single column.
//
//	pre-push total ........................ 4.2s
//	├─ resolve_push_settings ............. 0.1s
//	├─ git_push .......................... 1.9s
//	└─ cleanup ........................... 0.1s
//
// The dot column width is computed once over the whole tree so deeply
// nested children stay aligned with shallow ones.
func RenderTree(w io.Writer, root *Node) {
	if root == nil {
		return
	}
	lines := flattenForRender(root, "", true, true)
	maxLabel := 0
	for _, ln := range lines {
		if w := displayWidth(ln.label); w > maxLabel {
			maxLabel = w
		}
	}
	dotCol := maxLabel + 4 // breathing room before the duration column
	for _, ln := range lines {
		writeLine(w, ln, dotCol)
	}
}

type renderLine struct {
	label    string // prefix + name (visible text only, no color)
	duration time.Duration
	failed   bool
}

func flattenForRender(n *Node, prefix string, isLast, isRoot bool) []renderLine {
	if n == nil {
		return nil
	}
	var label string
	switch {
	case isRoot:
		label = n.Name
	case isLast:
		label = prefix + ui.TreeLast + n.Name
	default:
		label = prefix + ui.TreeBranch + n.Name
	}
	out := []renderLine{{
		label:    label,
		duration: n.Duration,
		failed:   n.Status.Code == codes.Error,
	}}
	// child prefix carries vertical bars for non-last ancestors.
	var childPrefix string
	if !isRoot {
		if isLast {
			childPrefix = prefix + ui.TreeIndent
		} else {
			childPrefix = prefix + ui.TreeVertical
		}
	}
	for i, c := range n.Children {
		out = append(out, flattenForRender(c, childPrefix, i == len(n.Children)-1, false)...)
	}
	return out
}

func writeLine(w io.Writer, ln renderLine, dotCol int) {
	gap := dotCol - displayWidth(ln.label)
	if gap < 2 {
		gap = 2
	}
	// leading + trailing space around the dots: " ......... " reads
	// cleaner than abutting them to the label/duration.
	dots := strings.Repeat(".", gap-2)
	dur := formatDuration(ln.duration)
	statusMark := ""
	if ln.failed {
		statusMark = " " + ui.FailStyle.Render(ui.IconFail)
	}
	dotsStyled := ui.MutedStyle.Render(" " + dots + " ")
	fmt.Fprintf(w, "%s%s%s%s\n", ln.label, dotsStyled, dur, statusMark)
}

// displayWidth returns the visible column count for s. Tree glyphs are
// single-width; ASCII is single-width; we don't render colored prefixes
// in label so no ANSI stripping is needed.
func displayWidth(s string) int {
	// internal/ui tree constants are UTF-8 multi-byte glyphs (├, │, └, ─)
	// but render as single columns. Count runes, not bytes.
	n := 0
	for range s {
		n++
	}
	return n
}

// formatDuration renders d in a compact, human-readable form suitable for
// a single column. Examples:
//
//	850µs   < 1ms (rare; only when forced)
//	42ms    1ms ≤ d < 1s
//	1.9s    1s  ≤ d < 60s
//	1m 12s  60s ≤ d
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		s := float64(d) / float64(time.Second)
		return fmt.Sprintf("%.1fs", s)
	default:
		m := int(d / time.Minute)
		s := int((d - time.Duration(m)*time.Minute) / time.Second)
		return fmt.Sprintf("%dm %ds", m, s)
	}
}
