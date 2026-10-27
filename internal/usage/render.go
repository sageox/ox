package usage

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/theme"
)

// labelCol is the width of the left-hand section label ("context    ").
const labelCol = 11

// maxBar is the bar length of the largest context source.
const maxBar = 5

// maxDisplayLines bounds the display so it fits one screen in any harness.
const maxDisplayLines = 24

// styler colors pieces of the report. The plain styler is the identity, so
// Display (which both harnesses print verbatim) never carries an escape.
type styler struct {
	head, label, dim, accent, ok func(string) string
}

func ident(s string) string { return s }

var plain = styler{head: ident, label: ident, dim: ident, accent: ident, ok: ident}

func themed() styler {
	return styler{
		head:   func(s string) string { return cli.StyleBrand.Render(s) },
		label:  func(s string) string { return cli.StyleBold.Render(s) },
		dim:    func(s string) string { return cli.StyleDim.Render(s) },
		accent: func(s string) string { return cli.StyleAccent.Render(s) },
		ok:     func(s string) string { return cli.StyleSuccess.Render(s) },
	}
}

// Display renders the session report as plain text: no ANSI, at most 24
// lines, wrapped to width (0 → 80). It is what the AI coworker prints.
func Display(r Report, width int) string {
	return renderSession(r, width, plain)
}

// Human renders the session report for a bare terminal: the same text as
// Display, with theme colors when the terminal supports them.
func Human(r Report, width int) string {
	if !theme.ColorEnabled() {
		return Display(r, width)
	}
	return renderSession(r, width, themed())
}

func renderSession(r Report, width int, st styler) string {
	if width <= 0 {
		width = 80
	}
	var lines []string
	lines = append(lines, sessionHeader(r, st))

	if !r.Trace.Available {
		reason := r.Trace.Reason
		if reason == "" {
			reason = "nothing recorded yet"
		}
		lines = append(lines, wrapItems(st.label("trace"), []string{"no usage trace for this session: " + reason}, " ", width)...)
		lines = append(lines, wrapItems("", []string{st.dim("record the next one: ox session start")}, " ", width)...)
		return finish(lines)
	}

	// calls
	callItems := []string{st.accent(fmt.Sprintf("%d", r.Calls.Total)) + "  "}
	if r.Calls.Total == 0 {
		callItems = []string{st.dim("none yet")}
	}
	for _, c := range r.Calls.ByCommand {
		callItems = append(callItems, fmt.Sprintf("%s ×%d", c.Label, c.Count))
	}
	if r.Calls.Failed > 0 {
		callItems = append(callItems, st.dim(fmt.Sprintf("· %d failed", r.Calls.Failed)))
	}
	lines = append(lines, wrapItems(st.label("calls"), callItems, "  ", width)...)

	lines = append(lines, contextLines(r.Context, width, st)...)

	read, readChecked := readLines(r.Surfaced, width, st)
	murmurs, murmurChecked := murmurLines(r.Surfaced, width, st)
	lines = append(lines, read...)
	lines = append(lines, murmurs...)
	lines = append(lines, scoreLines(r, width, st)...)
	if readChecked || murmurChecked {
		lines = append(lines, wrapItems("", []string{st.ok("✓") + st.dim(" used by the AI coworker")}, " ", width)...)
	}
	return finish(lines)
}

// contextLines renders the estimated token volume and its notes.
func contextLines(cx Context, width int, st styler) []string {
	items := []string{st.accent("~"+FmtK(cx.Tokens)+" tokens") + "  "}
	maxTok := 0
	for _, s := range cx.BySource {
		maxTok = max(maxTok, s.Tokens)
	}
	for _, s := range cx.BySource {
		item := s.Source + " " + FmtK(s.Tokens)
		if n := barLen(s.Tokens, maxTok); n > 0 {
			item += " " + st.accent(strings.Repeat("▇", n))
		}
		items = append(items, item)
	}
	if len(cx.BySource) == 0 {
		items = []string{st.dim("none metered")}
	}
	lines := wrapItems(st.label("context"), items, "  ", width)

	var notes []string
	if p := cx.Prime; p != nil && p.FullBytes > p.EmittedBytes {
		note := fmt.Sprintf("prime trimmed %s→%s B", FmtK(p.FullBytes), FmtK(p.EmittedBytes))
		if len(p.Deferred) > 0 {
			note += " (deferred: " + strings.Join(p.Deferred, ", ") + ")"
		}
		notes = append(notes, note)
	}
	if len(cx.Unmetered) > 0 {
		notes = append(notes, "not metered: "+strings.Join(cx.Unmetered, ", "))
	}
	for i := range notes {
		notes[i] = st.dim(notes[i])
	}
	return append(lines, wrapItems("", notes, st.dim(" · "), width)...)
}

// readLines renders every non-murmur surfaced item by title (or name), team
// skills collapsed to a count. An item with a web link gets its own wrap so
// the URL lands whole on a continuation line instead of being cut. It reports
// whether any item carried a use check.
func readLines(surfaced []Surfaced, width int, st styler) ([]string, bool) {
	skills := 0
	for _, s := range surfaced {
		if s.RefType == refTypeSkill {
			skills++
		}
	}
	label := st.label("read")
	var lines, run []string
	checked, skillShown := false, false
	flush := func() {
		if len(run) > 0 {
			lines = append(lines, wrapItems(label, run, st.dim(" · "), width)...)
			label, run = "", nil
		}
	}
	for _, s := range surfaced {
		if s.RefType == "murmur" {
			continue
		}
		if s.RefType == refTypeSkill && skills > 1 {
			if !skillShown {
				run = append(run, fmt.Sprintf("%d team skills", skills))
				skillShown = true
			}
			if s.Used != nil {
				checked = true
			}
			continue
		}
		text := readName(s, width)
		if s.Used != nil {
			text += " " + st.ok("✓")
			checked = true
		}
		if s.URL == "" {
			run = append(run, text)
			continue
		}
		flush()
		lines = append(lines, wrapItems(label, []string{text, st.dim(s.URL)}, "  ", width)...)
		label = ""
	}
	flush()
	if len(lines) == 0 {
		lines = wrapItems(label, []string{st.dim("nothing by name")}, " ", width)
	}
	return lines, checked
}

// refTypeSkill is the surfaced ref_type of a team skill.
const refTypeSkill = "skill"

// readName is "kind  title" when the item has a title, else its name.
func readName(s Surfaced, width int) string {
	if s.Title == "" {
		return surfacedName(s)
	}
	kind := s.RefType
	room := max(12, width-labelCol-ansi.StringWidth(kind)-4)
	return kind + "  " + truncate(s.Title, room)
}

// murmurLines lists only the murmurs with use evidence; a murmur that was
// merely received is not value and is not shown.
func murmurLines(surfaced []Surfaced, width int, st styler) ([]string, bool) {
	var items []string
	for _, s := range surfaced {
		if s.RefType != "murmur" || s.Used == nil {
			continue
		}
		name := firstNonEmpty(s.FromName, s.From)
		if s.Topic != "" {
			name = strings.TrimSpace(name + " [" + s.Topic + "]")
		}
		if name == "" {
			name = s.Ref
		}
		items = append(items, name+" "+st.ok("✓"))
	}
	if len(items) == 0 {
		return nil, false
	}
	return wrapItems(st.label("murmurs"), items, st.dim(" · "), width), true
}

// scoreLines is the session's SageOx score and the commits it produced.
func scoreLines(r Report, width int, st styler) []string {
	var items []string
	switch {
	case r.Used.SageoxScore != nil && r.Used.SageoxScore.Category != "":
		items = append(items, r.Used.SageoxScore.Category)
	case r.Relevant != nil && r.Relevant.Derived != nil && r.Relevant.Derived.Category != "":
		items = append(items, r.Relevant.Derived.Category)
	}
	if n := r.Used.ProducedCommits; n > 0 {
		items = append(items, fmt.Sprintf("%d commit%s", n, plural(n, "", "s")))
	}
	return wrapItems(st.label("score"), items, st.dim(" · "), width)
}

func sessionHeader(r Report, st styler) string {
	scope := "this session"
	past := !r.Session.Recording && r.Session.Name != ""
	if past {
		scope = "past session"
	}
	var parts []string
	switch {
	case r.Session.Recording:
		parts = append(parts, st.ok("●")+" recording")
	case !past:
		parts = append(parts, st.dim("○")+" not recording")
	}
	if r.Session.Turns > 0 {
		parts = append(parts, fmt.Sprintf("%d turn%s", r.Session.Turns, plural(r.Session.Turns, "", "s")))
	}
	if rel := r.Relevant; rel != nil {
		parts = append(parts, fmt.Sprintf("%d of %d used", rel.Count, rel.SurfacedCount))
	}
	h := st.head("ox brief") + st.dim(" · "+scope)
	if len(parts) > 0 {
		h += "  " + strings.Join(parts, st.dim(" · "))
	}
	return h
}

// surfacedName is the untitled fallback for a read item: a doc by its path,
// a retrieval by its query, anything else as "kind ref".
func surfacedName(s Surfaced) string {
	switch s.RefType {
	case "doc":
		return s.Ref
	}
	if s.Via == "retrieval" {
		return fmt.Sprintf("%s %q", s.RefType, truncate(s.Ref, 40))
	}
	return s.RefType + " " + s.Ref
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// wrapItems lays items out after a fixed-width label, joined by sep, wrapping
// onto continuation lines indented to the label column.
func wrapItems(label string, items []string, sep string, width int) []string {
	if len(items) == 0 {
		return nil
	}
	pad := strings.Repeat(" ", labelCol)
	first := pad
	if label != "" {
		first = label + strings.Repeat(" ", max(1, labelCol-ansi.StringWidth(label)))
	}
	var lines []string
	cur := first
	curEmpty := true
	for _, it := range items {
		if !curEmpty && ansi.StringWidth(cur)+ansi.StringWidth(sep)+ansi.StringWidth(it) > width {
			lines = append(lines, strings.TrimRight(cur, " "))
			cur, curEmpty = pad, true
		}
		if !curEmpty && !strings.HasSuffix(cur, " ") {
			cur += sep
		}
		cur += it
		curEmpty = false
	}
	return append(lines, strings.TrimRight(cur, " "))
}

func finish(lines []string) string {
	if len(lines) > maxDisplayLines {
		lines = append(lines[:maxDisplayLines-1], "…")
	}
	return strings.Join(lines, "\n") + "\n"
}

// barLen scales n against the largest source; zero-length for tiny sources so
// small buckets do not read as larger than they are.
func barLen(n, maxN int) int {
	if maxN <= 0 || n <= 0 {
		return 0
	}
	return (n*maxBar + maxN/2) / maxN
}

// FmtK formats a count compactly: 87, 0.2k, 6.4k, 142k.
func FmtK(n int) string {
	switch {
	case n < 100:
		return fmt.Sprintf("%d", n)
	case n < 100_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%.0fk", float64(n)/1000)
	}
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
