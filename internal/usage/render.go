package usage

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/theme"
)

// labelCol is the width of the left-hand section label ("injected   ").
const labelCol = 11

// maxBar is the bar length of the largest injected source.
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

	// injected
	inj := r.Injected
	injItems := []string{st.accent("~"+FmtK(inj.TotalTokens)+" tok") + "  "}
	maxTok := 0
	for _, s := range inj.BySource {
		maxTok = max(maxTok, s.Tokens)
	}
	for _, s := range inj.BySource {
		item := s.Source + " " + FmtK(s.Tokens)
		if n := barLen(s.Tokens, maxTok); n > 0 {
			item += " " + st.accent(strings.Repeat("▇", n))
		}
		injItems = append(injItems, item)
	}
	if len(inj.BySource) == 0 {
		injItems = []string{st.dim("none metered")}
	}
	lines = append(lines, wrapItems(st.label("injected"), injItems, "  ", width)...)

	var notes []string
	if p := inj.Prime; p != nil && p.FullBytes > p.EmittedBytes {
		note := fmt.Sprintf("prime trimmed %s→%s B", FmtK(p.FullBytes), FmtK(p.EmittedBytes))
		if len(p.Deferred) > 0 {
			note += " (deferred: " + strings.Join(p.Deferred, ", ") + ")"
		}
		notes = append(notes, note)
	}
	if len(inj.Unmetered) > 0 {
		notes = append(notes, "not metered: "+strings.Join(inj.Unmetered, ", "))
	}
	for i := range notes {
		notes[i] = st.dim(notes[i])
	}
	lines = append(lines, wrapItems("", notes, st.dim(" · "), width)...)

	// surfaced
	var surf []string
	for _, s := range r.Surfaced {
		surf = append(surf, surfacedName(s))
	}
	if len(surf) == 0 {
		surf = []string{st.dim("nothing by name")}
	}
	lines = append(lines, wrapItems(st.label("surfaced"), surf, st.dim(" · "), width)...)

	// used
	lines = append(lines, wrapItems(st.label("used"), usedItems(r.Used, st), st.dim(" · "), width)...)

	return finish(lines)
}

func sessionHeader(r Report, st styler) string {
	scope := "this session"
	if !r.Session.Recording && r.Session.Name != "" {
		scope = r.Session.Name
	}
	var parts []string
	if r.Session.AgentID != "" {
		parts = append(parts, r.Session.AgentID)
	}
	if r.Session.Harness != "" {
		parts = append(parts, r.Session.Harness)
	}
	if r.Session.Recording {
		parts = append(parts, st.ok("●")+" recording")
	}
	if r.Session.Turns > 0 {
		parts = append(parts, fmt.Sprintf("%d turn%s", r.Session.Turns, plural(r.Session.Turns, "", "s")))
	}
	h := st.head("ox usage") + st.dim(" · "+scope)
	if len(parts) > 0 {
		h += "  " + strings.Join(parts, st.dim(" · "))
	}
	return h
}

func surfacedName(s Surfaced) string {
	switch s.RefType {
	case "doc":
		return s.Ref
	case "murmur":
		name := "murmur"
		if s.From != "" {
			name += " " + s.From
		}
		if s.Topic != "" {
			name += " [" + s.Topic + "]"
		}
		if s.From == "" && s.Topic == "" {
			name += " " + s.Ref
		}
		return name
	}
	if s.Via == "retrieval" {
		return fmt.Sprintf("%s %q", s.RefType, truncate(s.Ref, 40))
	}
	return s.RefType + " " + s.Ref
}

func usedItems(u Used, st styler) []string {
	var items []string
	switch u.Status {
	case UsedReported:
		var refs []string
		for i, s := range u.SelfReported {
			if i == 3 {
				refs = append(refs, fmt.Sprintf("+%d more", len(u.SelfReported)-3))
				break
			}
			ref := s.Ref
			if s.Decision != "" {
				ref += fmt.Sprintf(" (%q)", truncate(s.Decision, 40))
			}
			refs = append(refs, ref)
		}
		items = append(items, "you reported: "+strings.Join(refs, ", "))
	case UsedNotYetReported:
		items = append(items, st.dim("not yet reported"))
	default:
		items = append(items, st.dim("not recording — no self-report"))
	}
	if n := len(u.Retrieval); n > 0 {
		items = append(items, fmt.Sprintf("ran %d retrieval", n)+plural(n, "", "s"))
	}
	if u.SageoxScore != nil {
		items = append(items, "score: "+u.SageoxScore.Category)
	}
	if u.ProducedCommits > 0 {
		items = append(items, fmt.Sprintf("%d commit%s", u.ProducedCommits, plural(u.ProducedCommits, "", "s")))
	}
	return items
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
