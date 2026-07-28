package plan

// anchor.go — Go-side parity with the review layer's content anchors
// (assets/review.js). A review mark's anchor is a content hash of
// (section heading + element text), computed IN THE BROWSER as:
//
//	anchor = 'h' + fnv1a(norm(heading) + '\\u0000' + norm(text))
//
// where fnv1a runs over UTF-16 code units and — load-bearing detail — the JS
// multiply `h * 0x01000193 >>> 0` goes through an IEEE-754 float64, which
// LOSES LOW BITS once the product exceeds 2^53. That float behavior is part
// of the wire format now: every anchor already saved in every ledger was
// produced by it. Changing either side to exact integer FNV-1a would silently
// orphan all existing feedback, so this file replicates the float64 path
// bit-for-bit instead. Do not "fix" it.
//
// The server needs the same computation to REMAP feedback when a plan is
// updated: parse the freshly rendered HTML, enumerate the same elements the
// page makes markable (review.js SELECTOR), and compute each element's anchor.
// Known, accepted divergences (each degrades to "item stays orphaned but
// visible", never to data loss):
//   - Mermaid blocks: the browser hashes the post-render SVG text, the server
//     sees the raw diagram source, so marks on a diagram-bearing section don't
//     anchor-match server-side (fuzzy label matching may still rebind them).
//   - Exotic case mappings (e.g. U+0130) and astral-plane truncation differ
//     between JS toLowerCase/slice and Go's simple mappings.

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf16"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// jsFNV1a replicates review.js fnv1a(): FNV-1a over the string's UTF-16 code
// units, with the arithmetic routed through JS number semantics exactly:
// `h ^= u` yields a SIGNED Int32 (negative half the time), the multiply is an
// IEEE-754 float64 product of that signed value (rounded — lossy above 2^53),
// and `>>> 0` truncates toward zero then takes the result mod 2^32. Pinned
// against real-JS-engine goldens in anchor_test.go.
func jsFNV1a(s string) string {
	h := uint32(0x811c9dc5)
	for _, u := range utf16.Encode([]rune(s)) {
		h ^= uint32(u)
		// int32(h): the signed view ECMAScript ^ produces; float64 multiply
		// matches the engine's rounding; math.Trunc + uint32(int64) is ToUint32.
		h = uint32(int64(math.Trunc(float64(int32(h)) * 16777619)))
	}
	return fmt.Sprintf("%08x", h)
}

// jsWhitespace mirrors the ECMAScript /\s/ class (WhiteSpace + LineTerminator).
// Deliberately NOT unicode.IsSpace: JS includes U+FEFF and excludes U+0085.
func jsWhitespace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ',
		'\u00a0', // NBSP
		'\u1680', // ogham space mark
		'\u2028', // line separator
		'\u2029', // paragraph separator
		'\u202f', // narrow no-break space
		'\u205f', // medium mathematical space
		'\u3000', // ideographic space
		'\ufeff': // zero-width no-break space (JS \s includes it)
		return true
	}
	return r >= '\u2000' && r <= '\u200a' // en quad through hair space
}

// collapseJSWhitespace mirrors `.replace(/\s+/g, ' ').trim()`: runs of
// JS-whitespace become one space, leading/trailing whitespace drops.
func collapseJSWhitespace(s string) string {
	var b strings.Builder
	inWS := false
	for _, r := range s {
		if jsWhitespace(r) {
			inWS = true
			continue
		}
		if inWS && b.Len() > 0 {
			b.WriteByte(' ')
		}
		inWS = false
		b.WriteRune(r)
	}
	return b.String()
}

// jsNorm mirrors review.js norm(): collapse whitespace, trim, lowercase.
func jsNorm(s string) string {
	return strings.ToLower(collapseJSWhitespace(s))
}

// jsLabel mirrors review.js labelFor(): whitespace-collapsed, trimmed, and
// truncated to 70 UTF-16 units (69 + '…') — NOT lowercased.
func jsLabel(s string) string {
	t := collapseJSWhitespace(s)
	units := utf16.Encode([]rune(t))
	if len(units) <= 70 {
		return t
	}
	return string(utf16.Decode(units[:69])) + "…"
}

// AnchorFor computes the review anchor for an element, given its enclosing
// section heading and its full text content — matching review.js anchorFor().
func AnchorFor(heading, text string) string {
	return "h" + jsFNV1a(jsNorm(heading)+"\u0000"+jsNorm(text))
}

// reviewTarget is one markable element extracted from a rendered plan page:
// the anchor the browser would compute for it, plus the matching context the
// remapper scores against.
type reviewTarget struct {
	Anchor  string
	Section string // raw heading text of the enclosing section (review.js headingOf)
	Label   string // review.js labelFor — the ≤70-unit display label
	Norm    string // jsNorm of the element text, for fuzzy matching
}

// extractReviewTargets parses a rendered plan HTML document and returns every
// element the review layer makes markable — the same set review.js selects
// with `section[id], li, tr, .ox-chip, .stat, .bar-row` — with the anchors the
// page would compute for them. This is what lets the server re-anchor saved
// feedback after the plan's content changed.
func extractReviewTargets(htmlBytes []byte) ([]reviewTarget, error) {
	doc, err := html.Parse(strings.NewReader(string(htmlBytes)))
	if err != nil {
		return nil, fmt.Errorf("parse plan html: %w", err)
	}
	var out []reviewTarget
	var walk func(n *html.Node, section *html.Node)
	walk = func(n *html.Node, section *html.Node) {
		if n.Type == html.ElementNode {
			// scope mirrors review.js headingOf()'s
			// closest('section[id], [data-ox-section]'): the NEAREST
			// ancestor-or-self matching either — authored pages anchor sections
			// with data-ox-section, generated pages with section[id].
			if (n.DataAtom == atom.Section && attrVal(n, "id") != "") || hasAttr(n, "data-ox-section") {
				section = n
			}
			if matchesReviewSelector(n) {
				text := textContent(n)
				heading := sectionHeading(section)
				out = append(out, reviewTarget{
					Anchor:  AnchorFor(heading, text),
					Section: heading,
					Label:   jsLabel(text),
					Norm:    jsNorm(text),
				})
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, section)
		}
	}
	walk(doc, nil)
	return out, nil
}

// matchesReviewSelector mirrors review.js SELECTOR:
// 'section[id], li, tr, .ox-chip, .stat, .bar-row' — plus [data-ox-section],
// the authored-page anchor container chrome.js adds to the page-side selector
// (a mark on one must resolve to the same anchor server-side).
func matchesReviewSelector(n *html.Node) bool {
	switch n.DataAtom {
	case atom.Section:
		return attrVal(n, "id") != ""
	case atom.Li, atom.Tr:
		return true
	}
	if hasAttr(n, "data-ox-section") {
		return true
	}
	for _, c := range strings.Fields(attrVal(n, "class")) {
		if c == "ox-chip" || c == "stat" || c == "bar-row" {
			return true
		}
	}
	return false
}

// sectionHeading mirrors review.js headingOf() exactly: a non-empty
// data-ox-section attribute wins; else the first <h2> OR <h3> descendant in
// document order (querySelector('h2, h3')); else the scope's id; "" when the
// element sits outside any scope.
func sectionHeading(section *html.Node) string {
	if section == nil {
		return ""
	}
	if ds := attrVal(section, "data-ox-section"); ds != "" {
		return ds
	}
	if h := firstDescendantAny(section, atom.H2, atom.H3); h != nil {
		return textContent(h)
	}
	return attrVal(section, "id")
}

// firstDescendant returns the first descendant with the given atom in document
// order (extract.go uses it for body/head/title lookup).
func firstDescendant(n *html.Node, a atom.Atom) *html.Node {
	return firstDescendantAny(n, a)
}

// firstDescendantAny returns the first descendant matching ANY of the atoms in
// document order — the querySelector('h2, h3') semantics (document order, not
// per-atom priority).
func firstDescendantAny(n *html.Node, atoms ...atom.Atom) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			for _, a := range atoms {
				if c.DataAtom == a {
					return c
				}
			}
		}
		if hit := firstDescendantAny(c, atoms...); hit != nil {
			return hit
		}
	}
	return nil
}

// hasAttr reports attribute PRESENCE (the CSS [attr] selector semantics —
// present-but-empty still matches).
func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

// textContent mirrors DOM textContent: the concatenation of every text node in
// tree order (entities already decoded by the parser).
func textContent(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(m *html.Node) {
		if m.Type == html.TextNode {
			b.WriteString(m.Data)
		}
		for c := m.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func attrVal(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
