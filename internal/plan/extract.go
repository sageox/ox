package plan

// extract.go derives searchable markdown from an authored HTML plan page —
// the inverse of render.go's markdown→HTML path. Once an HTML page is a
// plan's PRIMARY artifact (Meta.Primary == "html", see store.go), plan.md
// becomes a DERIVED convenience view: `ox plan view` terminal rendering,
// grep/search, and enrich.go's section parsing (which still splits markdown
// on "## " H2 headings — see input.go Parse()) all read the derived
// projection rather than the HTML.
//
// The projection is intentionally lossy and one-directional: interactive-only
// markup (spans wired up by page JS, tab chrome, decorative containers)
// degrades to its visible text rather than being reconstructed as structured
// markdown. That is a feature, not a gap — the authored HTML stays the plan
// of record, and this output only needs to be good enough to search and skim.

import (
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// headingLevel maps the heading atoms to their markdown "#" depth. A lookup
// table rather than atom arithmetic — x/net/html/atom values are hash-table
// offsets, not a contiguous H1..H6 enum, so atom.H2-atom.H1 is not safe.
var headingLevel = map[atom.Atom]int{
	atom.H1: 1, atom.H2: 2, atom.H3: 3, atom.H4: 4, atom.H5: 5, atom.H6: 6,
}

// ExtractMarkdown derives searchable markdown from an authored HTML plan
// document. It is a lossy, deterministic projection: headings, paragraphs,
// lists, tables, code blocks and links survive; interactive-only content
// degrades to its visible text. Never returns an error — a malformed
// document yields best-effort text (the derived md is a convenience view,
// the authored HTML stays the plan of record).
func ExtractMarkdown(htmlBytes []byte) string {
	doc, err := html.Parse(strings.NewReader(string(htmlBytes)))
	if err != nil {
		// x/net/html.Parse only errors on a Reader I/O failure, never on
		// malformed markup (that's the point of an HTML5 error-recovery
		// parser) — strings.Reader can't fail, but stay defensive since this
		// function's contract is "never panics, never errors".
		return ""
	}

	e := &extractor{}
	if body := firstDescendant(doc, atom.Body); body != nil {
		e.walkBlock(body)
	}
	e.flush() // catch any trailing bare text that never hit a block boundary

	if !e.sawH1 {
		if title := extractTitle(doc); title != "" {
			e.blocks = append([]string{"# " + title}, e.blocks...)
		}
	}

	if len(e.blocks) == 0 {
		return ""
	}
	return strings.Join(e.blocks, "\n\n") + "\n"
}

// extractTitle returns the flattened <head><title> text, or "" if absent.
func extractTitle(doc *html.Node) string {
	head := firstDescendant(doc, atom.Head)
	if head == nil {
		return ""
	}
	t := firstDescendant(head, atom.Title)
	if t == nil {
		return ""
	}
	return collapseSpace(textContent(t))
}

// extractor accumulates markdown blocks (headings, paragraphs, lists,
// tables, code fences) in document order. run is a SINGLE buffer shared
// across the entire walk — not one per container — because "transparent"
// elements (div/section/span/...) must let adjacent bare text merge across
// nesting levels (e.g. two sibling <span>s inside a wrapper <div> collapse
// into one paragraph). It is flushed immediately before any true block-level
// element and once more at the very end of the walk.
type extractor struct {
	blocks []string
	run    strings.Builder
	sawH1  bool
}

// flush closes out the current bare-text run as a paragraph block, if it has
// any non-whitespace content.
func (e *extractor) flush() {
	if text := collapseSpace(e.run.String()); text != "" {
		e.blocks = append(e.blocks, text)
	}
	e.run.Reset()
}

// isSkipped elements are dropped entirely — no text, no recursion. nav is
// here (not just button/select/...) because tab bars are label noise: their
// button text ("Overview", "Table", ...) has no informational value once the
// page's own headings already say the same thing.
func isSkipped(a atom.Atom) bool {
	switch a {
	case atom.Script, atom.Style, atom.Noscript, atom.Template, atom.Svg,
		atom.Iframe, atom.Canvas, atom.Button, atom.Select, atom.Input,
		atom.Textarea, atom.Nav:
		return true
	}
	return false
}

// walkBlock processes n's children in block context: text nodes and
// transparent containers feed the shared run buffer, true block-level
// elements flush the buffer and emit their own block.
func (e *extractor) walkBlock(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch c.Type {
		case html.TextNode:
			e.run.WriteString(c.Data)

		case html.ElementNode:
			if isSkipped(c.DataAtom) {
				continue
			}

			// data-ox-section is the authoring contract for pages whose
			// tabs/views are divs rather than headings (see render_tabbed
			// fixtures) — it always marks a section boundary regardless of
			// the element's tag.
			if name := attrVal(c, "data-ox-section"); name != "" {
				e.flush()
				e.emitSectionMarker(c, name)
				e.walkBlock(c)
				continue
			}

			switch c.DataAtom {
			case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
				e.flush()
				e.emitHeading(c)
			case atom.P:
				e.flush()
				e.emitParagraph(c)
			case atom.Ul, atom.Ol:
				e.flush()
				e.emitList(c)
			case atom.Table:
				e.flush()
				e.emitTable(c)
			case atom.Pre:
				e.flush()
				e.emitPre(c)
			case atom.Blockquote:
				e.flush()
				e.emitBlockquote(c)
			case atom.Hr:
				e.flush()
				e.blocks = append(e.blocks, "---")
			default:
				// div/section/span/article/header/footer/aside/anything else:
				// transparent. Recurse in the SAME block scope (no new run
				// buffer, no flush) so bare text flows into the current block.
				e.walkBlock(c)
			}
		}
	}
}

// emitSectionMarker emits "## <name>" for a data-ox-section boundary, unless
// the section's own first h1-h3 already carries identical text — avoids
// double-emitting the heading when authors also (redundantly) put a matching
// <h2> inside the marked div.
func (e *extractor) emitSectionMarker(n *html.Node, name string) {
	label := collapseSpace(name)
	if label == "" {
		return
	}
	if heading, ok := firstHeadingText(n); ok && heading == label {
		return
	}
	e.blocks = append(e.blocks, "## "+label)
}

// firstHeadingText returns the flattened text of the first h1/h2/h3
// descendant of n in document order, skipping subtrees that are themselves
// skipped (script/nav/...).
func firstHeadingText(n *html.Node) (string, bool) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		switch c.DataAtom {
		case atom.H1, atom.H2, atom.H3:
			return collapseSpace(flattenInline(c)), true
		}
		if isSkipped(c.DataAtom) {
			continue
		}
		if text, ok := firstHeadingText(c); ok {
			return text, true
		}
	}
	return "", false
}

func (e *extractor) emitHeading(n *html.Node) {
	level, ok := headingLevel[n.DataAtom]
	if !ok {
		return
	}
	if n.DataAtom == atom.H1 {
		// Recorded even if the h1 turns out to be empty-text: an h1 ELEMENT
		// being present is what suppresses the <title> fallback, regardless
		// of whether it renders any visible content.
		e.sawH1 = true
	}
	text := collapseSpace(flattenInline(n))
	if text == "" {
		return
	}
	e.blocks = append(e.blocks, strings.Repeat("#", level)+" "+text)
}

func (e *extractor) emitParagraph(n *html.Node) {
	if text := collapseSpace(flattenInline(n)); text != "" {
		e.blocks = append(e.blocks, text)
	}
}

func (e *extractor) emitList(n *html.Node) {
	if lines := renderListLines(n, 0); len(lines) > 0 {
		e.blocks = append(e.blocks, strings.Join(lines, "\n"))
	}
}

// renderListLines renders one ul/ol's <li> children, indenting nested
// ul/ol two spaces per depth. A li's "own text" excludes any nested list
// (flattenInline already skips ul/ol) so the item line doesn't duplicate its
// children's text.
func renderListLines(n *html.Node, depth int) []string {
	ordered := n.DataAtom == atom.Ol
	indent := strings.Repeat("  ", depth)
	var lines []string
	idx := 0
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || c.DataAtom != atom.Li {
			continue
		}
		idx++
		marker := "- "
		if ordered {
			marker = strconv.Itoa(idx) + ". "
		}
		lines = append(lines, indent+marker+collapseSpace(flattenInline(c)))

		for cc := c.FirstChild; cc != nil; cc = cc.NextSibling {
			if cc.Type == html.ElementNode && (cc.DataAtom == atom.Ul || cc.DataAtom == atom.Ol) {
				lines = append(lines, renderListLines(cc, depth+1)...)
			}
		}
	}
	return lines
}

// emitTable renders a GFM table. The header row is the FIRST <tr> found
// anywhere in the table (thead or not), using th or td cells — some authored
// tables sloppily mark header cells as td. Every subsequent <tr> is a body
// row, on the same th-or-td acceptance.
func (e *extractor) emitTable(n *html.Node) {
	rows := collectTableRows(n)
	if len(rows) == 0 {
		return
	}
	header := tableCells(rows[0])
	if len(header) == 0 {
		return
	}
	lines := []string{tableRow(header), tableRow(dashRow(len(header)))}
	for _, r := range rows[1:] {
		lines = append(lines, tableRow(tableCells(r)))
	}
	e.blocks = append(e.blocks, strings.Join(lines, "\n"))
}

// collectTableRows gathers <tr> descendants in document order, not
// descending into a nested table's own rows.
func collectTableRows(n *html.Node) []*html.Node {
	var rows []*html.Node
	var walk func(*html.Node)
	walk = func(m *html.Node) {
		for c := m.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode {
				continue
			}
			switch c.DataAtom {
			case atom.Tr:
				rows = append(rows, c)
			case atom.Table:
				// a nested table's rows belong to it, not this one
			default:
				walk(c)
			}
		}
	}
	walk(n)
	return rows
}

// tableCells flattens a row's th/td cells, escaping "|" so it can't be
// mistaken for a column delimiter. Newline collapsing is a side effect of
// collapseSpace (all whitespace, including embedded newlines, becomes one
// space).
func tableCells(tr *html.Node) []string {
	var cells []string
	for c := tr.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		if c.DataAtom == atom.Th || c.DataAtom == atom.Td {
			text := collapseSpace(flattenInline(c))
			cells = append(cells, strings.ReplaceAll(text, "|", "\\|"))
		}
	}
	return cells
}

func tableRow(cells []string) string {
	return "| " + strings.Join(cells, " | ") + " |"
}

func dashRow(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "---"
	}
	return out
}

// emitPre fences the pre's raw text content verbatim — no whitespace
// collapsing — trimming only trailing newlines so the closing fence doesn't
// float behind a stray blank line.
func (e *extractor) emitPre(n *html.Node) {
	code := strings.TrimRight(textContent(n), "\n")
	e.blocks = append(e.blocks, "```\n"+code+"\n```")
}

// emitBlockquote runs an isolated sub-extractor over the blockquote's
// content (reusing the full block machinery — nested lists/tables/etc. all
// work for free) and prefixes every resulting line with "> ".
func (e *extractor) emitBlockquote(n *html.Node) {
	inner := &extractor{}
	inner.walkBlock(n)
	inner.flush()
	if inner.sawH1 {
		e.sawH1 = true
	}
	for _, block := range inner.blocks {
		lines := strings.Split(block, "\n")
		for i, line := range lines {
			lines[i] = "> " + line
		}
		e.blocks = append(e.blocks, strings.Join(lines, "\n"))
	}
}

// flattenInline flattens n's children into inline markdown text: <code>,
// <strong>/<b>, <em>/<i>, <a href>, and <br> get their markdown form;
// everything else (span, nested div, ...) contributes bare text. Block-level
// ul/ol content never leaks into a heading/paragraph/cell/li's flattened
// text — lists render as their own block via emitList.
func flattenInline(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch c.Type {
		case html.TextNode:
			b.WriteString(c.Data)

		case html.ElementNode:
			if isSkipped(c.DataAtom) {
				continue
			}
			switch c.DataAtom {
			case atom.Ul, atom.Ol:
				continue
			case atom.Br:
				b.WriteString(" ")
			case atom.Code:
				b.WriteString(inlineWrap("`", flattenInline(c), "`"))
			case atom.Strong, atom.B:
				b.WriteString(inlineWrap("**", flattenInline(c), "**"))
			case atom.Em, atom.I:
				b.WriteString(inlineWrap("*", flattenInline(c), "*"))
			case atom.A:
				href := attrVal(c, "href")
				text := collapseSpace(flattenInline(c))
				if text == "" {
					continue
				}
				if href != "" && !strings.HasPrefix(href, "#") && !unsafeLinkScheme(href) {
					b.WriteString("[" + text + "](" + href + ")")
				} else {
					b.WriteString(text)
				}
			default:
				b.WriteString(flattenInline(c))
			}
		}
	}
	return b.String()
}

// unsafeLinkScheme reports whether href uses a scheme that must never survive
// into derived markdown as a clickable link.
func unsafeLinkScheme(href string) bool {
	h := strings.ToLower(strings.TrimSpace(href))
	for _, scheme := range []string{"javascript:", "data:", "vbscript:"} {
		if strings.HasPrefix(h, scheme) {
			return true
		}
	}
	return false
}

// inlineWrap trims/collapses s and wraps it in pre/suf, or returns "" for
// whitespace-only content — an empty **strong** or `code` span would just be
// visual noise in the derived markdown.
func inlineWrap(pre, s, suf string) string {
	t := collapseSpace(s)
	if t == "" {
		return ""
	}
	return pre + t + suf
}

// collapseSpace collapses any run of whitespace (including newlines) to a
// single space and trims the ends — the single whitespace rule applied
// everywhere except inside a <pre>.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
