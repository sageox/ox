package usage

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session"
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
	lines = append(lines, sessionHeader(r, st, width))

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

	used := usedLines(r, width, st)
	lines = append(lines, used...)
	lines = append(lines, availableLines(r, width, st)...)
	lines = append(lines, coauthorLines(r.Attribution, width, st)...)
	if len(used) > 0 {
		lines = append(lines, st.dim(truncate(usedLegend, width)))
	}
	return finish(lines)
}

// usedLegend explains what earns an item a place under "used".
const usedLegend = "used = evidence from the transcript, a commit, or the AI coworker's report"

// maxUsedItems caps the used section so the brief stays one screen.
const maxUsedItems = 8

// usedLines lists each item with use evidence, oldest use first: its name, a
// dim phrase naming the evidence, and — for a discussion, plan or session —
// its sageox.ai link whole on its own line. Only used items carry links.
func usedLines(r Report, width int, st styler) []string {
	items := usedItems(r)
	if len(items) == 0 {
		return nil
	}
	label := st.label("used")
	linkPad := strings.Repeat(" ", labelCol+2)
	var lines []string
	for i, it := range items {
		if i == maxUsedItems {
			lines = append(lines, wrapItems("", []string{st.dim(fmt.Sprintf("+%d more", len(items)-maxUsedItems))}, " ", width)...)
			break
		}
		name := usedName(it, r.Surfaced, width)
		phrase := evidencePhrase(it, width-labelCol-ansi.StringWidth(name)-2, width-labelCol)
		lines = append(lines, wrapItems(label, []string{name, st.dim(phrase)}, "  ", width)...)
		label = ""
		if it.URL != "" {
			lines = append(lines, linkPad+st.dim(it.URL))
		}
	}
	return lines
}

// usedItems is the relevant items, or — when a report carries use marks but
// no item list — the surfaced items marked used.
func usedItems(r Report) []RelevantItem {
	if r.Relevant != nil && len(r.Relevant.Items) > 0 {
		return r.Relevant.Items
	}
	var out []RelevantItem
	for _, s := range r.Surfaced {
		if s.Used != nil {
			out = append(out, RelevantItem{Ref: s.Ref, RefType: s.RefType, Title: s.Title, URL: s.URL,
				Evidence: s.Used.Evidence, Detail: s.Used.Evidence, Seq: s.Used.Seq})
		}
	}
	return out
}

// usedName is "kind  title" for a titled item; a murmur by its sender and
// topic (joined from the surfaced whisper); anything else by its read name.
func usedName(it RelevantItem, surfaced []Surfaced, width int) string {
	s := Surfaced{Ref: it.Ref, RefType: it.RefType, Title: it.Title}
	for _, cand := range surfaced {
		if cand.Ref == it.Ref {
			s.From, s.FromName, s.Topic, s.Via = cand.From, cand.FromName, cand.Topic, cand.Via
			break
		}
	}
	if s.RefType == "murmur" && s.Title == "" {
		name := firstNonEmpty(s.FromName, s.From)
		if s.Topic != "" {
			name = strings.TrimSpace(name + " [" + s.Topic + "]")
		}
		return "murmur  " + firstNonEmpty(name, s.Ref)
	}
	// leave room beside the name for a short evidence phrase
	return readName(s, width-12)
}

// evidencePhrase says in words how a use was established. A reported --why
// is cut to fit beside the name when there is room (fit), else to a line of
// its own (line).
func evidencePhrase(it RelevantItem, fit, line int) string {
	switch firstNonEmpty(it.Detail, it.Evidence) {
	case "requery":
		return "re-opened"
	case EvidenceCited:
		return "cited"
	case "cited_commit":
		if it.Commit == "" {
			return "cited in a commit"
		}
		return "cited in commit " + it.Commit
	case EvidenceReported:
		if it.Why == "" {
			return "reported"
		}
		room := line
		if fit >= 24 {
			room = fit
		}
		return truncate("reported: "+it.Why, room)
	case EvidenceJudged:
		return "judged"
	}
	return "read"
}

// availableLines folds what was put in front of the model but not used into
// one line: docs by name, then counts by kind. No links — a link is earned
// by use. Counts come from the full per-type totals when the report has them,
// since the surfaced list is capped for display.
func availableLines(r Report, width int, st styler) []string {
	surfaced := r.Surfaced
	var docs []string
	for _, s := range surfaced {
		if s.Used == nil && s.RefType == "doc" {
			docs = append(docs, readName(s, width))
		}
	}
	kinds, counts := unusedCounts(r)
	items := docs
	for _, k := range kinds {
		n := counts[k]
		switch {
		case k == refTypeSkill && n > 1:
			items = append(items, fmt.Sprintf("%d team skills", n))
		case k == refTypeSkill:
			named := false
			for _, s := range surfaced {
				if s.RefType == refTypeSkill && s.Used == nil {
					items = append(items, surfacedName(s))
					named = true
				}
			}
			if !named {
				items = append(items, "1 team skill")
			}
		default:
			items = append(items, fmt.Sprintf("%d %s", n, kindPlural(k, n)))
		}
	}
	return wrapItems(st.label("available"), items, st.dim(" · "), width)
}

// unusedCounts counts the unused non-doc items by kind ("" reads "item"), in
// the order the surfaced list first shows each kind. With SurfacedByType a
// kind's count is its full total less the used items of that kind — used
// items always survive the display cap, so the list holds all of them.
func unusedCounts(r Report) ([]string, map[string]int) {
	var kinds []string
	counts := map[string]int{}
	note := func(kind string, n int) {
		if n <= 0 {
			return
		}
		if _, seen := counts[kind]; !seen {
			kinds = append(kinds, kind)
		}
		counts[kind] += n
	}
	if r.SurfacedByType == nil {
		for _, s := range r.Surfaced {
			if s.Used == nil && s.RefType != "doc" {
				note(firstNonEmpty(s.RefType, "item"), 1)
			}
		}
		return kinds, counts
	}
	usedOf := map[string]int{}
	var order []string
	seen := map[string]bool{}
	for _, s := range r.Surfaced {
		if s.Used != nil {
			usedOf[s.RefType]++
		}
		if !seen[s.RefType] {
			seen[s.RefType] = true
			order = append(order, s.RefType)
		}
	}
	var rest []string
	for t := range r.SurfacedByType {
		if !seen[t] {
			rest = append(rest, t)
		}
	}
	sort.Strings(rest)
	for _, t := range append(order, rest...) {
		if t != "doc" {
			note(firstNonEmpty(t, "item"), r.SurfacedByType[t]-usedOf[t])
		}
	}
	return kinds, counts
}

// kindPlural names n items of one kind.
func kindPlural(kind string, n int) string {
	if n == 1 {
		return kind
	}
	if kind == "query" {
		return "queries"
	}
	return kind + "s"
}

// coauthorLines says whether SageOx would be credited as a co-author and
// why. Display-only: it never changes commit trailers. A derived "none" is
// the absence of evidence, not a verdict, and is omitted.
func coauthorLines(a Attribution, width int, st styler) []string {
	if a.Category == "" || (a.Category == string(session.ScoreNone) && a.Source != AttributionSelfReported) {
		return nil
	}
	verdict := " · below threshold"
	if a.WouldAttribute {
		verdict = " · would attribute"
	}
	if a.Threshold > 0 {
		verdict += fmt.Sprintf(" (threshold %g)", a.Threshold)
	}
	lines := wrapItems(st.label("co-author"), []string{a.Category + st.dim(verdict)}, " ", width)
	why := a.Reason
	if a.Source == AttributionSelfReported {
		why = "reported by the AI coworker"
		if a.Reason != "" {
			why += ": " + a.Reason
		}
	}
	if why != "" {
		lines = append(lines, wrapItems("", []string{st.dim(truncate(why, width-labelCol))}, " ", width)...)
	}
	return lines
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

// sessionHeader is the report's first line. When it would overflow width it
// drops the scope, then trailing status parts, so the title always fits.
func sessionHeader(r Report, st styler, width int) string {
	scope := "this session"
	past := !r.Session.Recording && r.Session.Name != ""
	if past {
		scope = "past session"
	}
	var parts []string
	switch {
	case r.Session.Recording && r.Session.Paused:
		parts = append(parts, st.dim("⏸")+" paused")
	case r.Session.Recording:
		parts = append(parts, st.ok("●")+" recording")
	case !past:
		parts = append(parts, st.dim("○")+" not recording")
	}
	if r.Session.Turns > 0 {
		parts = append(parts, fmt.Sprintf("%d turn%s", r.Session.Turns, plural(r.Session.Turns, "", "s")))
	}
	switch rel := r.Relevant; {
	case rel == nil:
	case rel.ReportedNone:
		parts = append(parts, "none used")
	default:
		parts = append(parts, fmt.Sprintf("%d of %d used", rel.Count, rel.SurfacedCount))
	}
	build := func(scope string, parts []string) string {
		h := st.head("ox session report")
		if scope != "" {
			h += st.dim(" · " + scope)
		}
		if len(parts) > 0 {
			h += "  " + strings.Join(parts, st.dim(" · "))
		}
		return h
	}
	h := build(scope, parts)
	if ansi.StringWidth(h) <= width {
		return h
	}
	for n := len(parts); n >= 0; n-- {
		if h = build("", parts[:n]); ansi.StringWidth(h) <= width {
			return h
		}
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
