package plan

import (
	"html"
	"path"
	"regexp"
	"strings"
)

// Inline reference markers. Where a plan's prose names a reference ox surfaced
// team context for, the render wraps the first mention with a neutral OX marker
// whose tooltip is the surfaced context. It marks "SageOx has context on this,"
// NOT a verdict — aligns/conflicts/amends stay the agent's judgment to assert.
// Deterministic, network-free, and conservative: first mention only, skips
// code/links/headings, and only fires on references with an unambiguous token.

// oxAnnotMark is the small SageOx glyph appended inside an injected marker. It
// references the symbol defs the template inlines once (#ox-ico-d / #ox-ico-l),
// so each marker stays tiny and the page renders from file:// with no network.
const oxAnnotMark = `<svg class="ox-annot-mark" aria-hidden="true"><use href="#ox-ico-d" class="ico-d"></use><use href="#ox-ico-l" class="ico-l"></use></svg>`

// adrNumRe pulls the leading ADR number from an ADR doc-path basename
// (e.g. "051-consent-tooling-and-capture-posture.md" → "051").
var adrNumRe = regexp.MustCompile(`^(\d{1,4})[-_]`)

// contextMarker is a resolved inline reference: a compiled prose-token matcher
// and the tooltip text surfaced from team context.
type contextMarker struct {
	re      *regexp.Regexp
	tooltip string
}

// contextMarkers builds the inline-marker set from the enrichment context
// bundle. Only ADR references qualify today: their prose token ("ADR-051")
// derives deterministically from the doc path and matches unambiguously. Other
// kinds (sessions, murmurs) carry opaque ids that never appear in prose.
func contextMarkers(items []ContextItem) []contextMarker {
	var out []contextMarker
	seen := map[string]bool{}
	for _, it := range items {
		if it.Kind != "adr" || strings.TrimSpace(it.Ref) == "" {
			continue
		}
		m := adrNumRe.FindStringSubmatch(path.Base(it.Ref))
		if m == nil {
			continue
		}
		num := m[1]
		if seen[num] {
			continue
		}
		seen[num] = true
		// "ADR-051" / "ADR 051" / "adr051", word-bounded, exact digits.
		re := regexp.MustCompile(`(?i)\bADR[\s-]?` + regexp.QuoteMeta(num) + `\b`)
		out = append(out, contextMarker{re: re, tooltip: markerTooltip(it)})
	}
	return out
}

// markerTooltip composes the hover text from the surfaced doc title + snippet.
func markerTooltip(it ContextItem) string {
	t := strings.TrimSpace(it.Title)
	s := strings.TrimSpace(it.Snippet)
	switch {
	case t != "" && s != "":
		return "SageOx surfaced: " + t + " — " + s
	case t != "":
		return "SageOx surfaced: " + t
	case s != "":
		return "SageOx surfaced: " + s
	default:
		return "SageOx surfaced this reference"
	}
}

// injectMarkers wraps the first eligible prose mention of each marker's token in
// htmlStr. Each marker is placed at most once doc-wide: markers that find no
// home here are returned so the next section can try.
func injectMarkers(htmlStr string, markers []contextMarker) (string, []contextMarker) {
	if len(markers) == 0 {
		return htmlStr, markers
	}
	var remaining []contextMarker
	for _, m := range markers {
		if out, ok := injectOne(htmlStr, m); ok {
			htmlStr = out
		} else {
			remaining = append(remaining, m)
		}
	}
	return htmlStr, remaining
}

// tagOrText splits rendered HTML into tag tokens (<...>) and the text runs
// between them. Raw '<' only ever starts a tag (text '<' is escaped by goldmark),
// so this cleanly separates markup from prose.
var tagOrText = regexp.MustCompile(`<[^>]+>|[^<]+`)

// skipTag names elements whose text must never be wrapped: code/links/headings
// (already styled or already a reference) and pre/mermaid blocks.
var skipTag = map[string]bool{
	"code": true, "pre": true, "a": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
}

// injectOne wraps the FIRST eligible occurrence of marker m's token. Returns
// (htmlStr, false) unchanged when the token never appears in eligible prose.
func injectOne(htmlStr string, m contextMarker) (string, bool) {
	var b strings.Builder
	b.Grow(len(htmlStr) + 256)
	depth := 0 // nesting depth inside a skip-zone element
	done := false
	for _, seg := range tagOrText.FindAllString(htmlStr, -1) {
		if done {
			b.WriteString(seg)
			continue
		}
		if seg[0] == '<' {
			if name, open, self := parseTag(seg); skipTag[name] && !self {
				if open {
					depth++
				} else if depth > 0 {
					depth--
				}
			}
			b.WriteString(seg)
			continue
		}
		if depth > 0 {
			b.WriteString(seg)
			continue
		}
		loc := m.re.FindStringIndex(seg)
		if loc == nil {
			b.WriteString(seg)
			continue
		}
		b.WriteString(seg[:loc[0]])
		b.WriteString(`<span class="ox-annot" title="`)
		b.WriteString(html.EscapeString(m.tooltip))
		b.WriteString(`">`)
		b.WriteString(seg[loc[0]:loc[1]])
		b.WriteString(" ")
		b.WriteString(oxAnnotMark)
		b.WriteString(`</span>`)
		b.WriteString(seg[loc[1]:])
		done = true
	}
	if !done {
		return htmlStr, false
	}
	return b.String(), true
}

// parseTag returns an HTML tag's lowercased element name, whether it opens (vs a
// closing </…>), and whether it self-closes (<… />).
func parseTag(tag string) (name string, open, self bool) {
	inner := strings.TrimSpace(tag[1 : len(tag)-1]) // strip < and >
	self = strings.HasSuffix(inner, "/")
	inner = strings.TrimSpace(strings.TrimSuffix(inner, "/"))
	open = true
	if strings.HasPrefix(inner, "/") {
		open = false
		inner = strings.TrimSpace(inner[1:])
	}
	if i := strings.IndexAny(inner, " \t\n>"); i >= 0 {
		inner = inner[:i]
	}
	return strings.ToLower(inner), open, self
}
