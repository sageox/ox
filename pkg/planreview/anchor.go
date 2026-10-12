package planreview

// Parity with the review page's content anchors (review.js, at
// internal/plan/assets/review.js in the ox repository). A mark's anchor is a
// content hash of (section heading + element text), computed in the browser as
//
//	anchor = 'h' + fnv1a(norm(heading) + '\u0000' + norm(text))
//
// where fnv1a runs over UTF-16 code units and the JS multiply
// `h * 0x01000193 >>> 0` goes through an IEEE-754 float64, which loses low bits
// once the product exceeds 2^53. That float behavior is part of the wire
// format: every anchor saved in every Ledger was produced by it, and exact
// integer FNV-1a on either side would orphan all existing feedback. This file
// replicates the float64 path bit for bit. Do not "fix" it.
//
// A highlight's "q" anchor (the same hash over heading + quoted words) is
// never computed here: the page finds a highlight by searching for its words.
//
// Known divergences, each leaving a mark orphaned but visible, never lost:
//   - Mermaid blocks: the browser hashes the rendered SVG text, the server sees
//     the diagram source, so marks in a diagram-bearing section do not match
//     server-side (label matching may still rebind them).
//   - Exotic case mappings (U+0130) and astral-plane truncation differ between
//     JS toLowerCase/slice and Go's simple mappings.

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf16"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// jsFNV1a replicates review.js fnv1a(): FNV-1a over the string's UTF-16 code
// units with JS number semantics: `h ^= u` yields a signed Int32, the multiply
// is a rounded float64 product of that signed value, and `>>> 0` truncates
// toward zero then takes the result mod 2^32.
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
// Not unicode.IsSpace: JS includes U+FEFF and excludes U+0085.
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
		'\ufeff': // zero-width no-break space
		return true
	}
	return r >= '\u2000' && r <= '\u200a' // en quad through hair space
}

// collapseJSWhitespace mirrors `.replace(/\s+/g, ' ').trim()`.
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

// jsLabel mirrors review.js labelFor(): whitespace-collapsed and truncated to
// 70 UTF-16 units (69 + '…'), not lowercased.
func jsLabel(s string) string {
	t := collapseJSWhitespace(s)
	units := utf16.Encode([]rune(t))
	if len(units) <= 70 {
		return t
	}
	return string(utf16.Decode(units[:69])) + "…"
}

// AnchorFor computes the review anchor of an element from its enclosing
// section heading and its full text, as review.js anchorFor() does.
func AnchorFor(heading, text string) string {
	return "h" + jsFNV1a(jsNorm(heading)+"\u0000"+jsNorm(text))
}

// target is one markable element of a rendered plan page: the anchor the page
// computes for it, and the context a rebind scores against.
type target struct {
	Anchor  string
	Section string // raw heading text of the enclosing section (review.js headingOf)
	Label   string // review.js labelFor: the ≤70-unit display label
	Norm    string // jsNorm of the element text
}

// extractTargets parses a rendered plan page and returns every element the
// review layer makes markable, with the anchors the page computes for them.
func extractTargets(page []byte) ([]target, error) {
	doc, err := html.Parse(strings.NewReader(string(page)))
	if err != nil {
		return nil, fmt.Errorf("parse plan html: %w", err)
	}
	var out []target
	var walk func(n, section *html.Node)
	walk = func(n, section *html.Node) {
		if n.Type == html.ElementNode {
			// review.js headingOf() scopes by
			// closest('section[id], [data-ox-section]'): the nearest ancestor
			// or self matching either. Authored pages anchor sections with
			// data-ox-section, generated pages with section[id].
			if (n.DataAtom == atom.Section && attrVal(n, "id") != "") || hasAttr(n, "data-ox-section") {
				section = n
			}
			if markable(n) {
				text := textContent(n)
				heading := sectionHeading(section)
				out = append(out, target{
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

// markable mirrors the review.js SELECTOR
// 'section[id], li, tr, .ox-chip, .stat, .bar-row' plus [data-ox-section], the
// authored-page container chrome.js adds to it.
func markable(n *html.Node) bool {
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

// sectionHeading mirrors review.js headingOf(): a non-empty data-ox-section
// attribute wins; else the first <h2> or <h3> descendant in document order
// (querySelector('h2, h3')); else the scope's id; "" outside any scope.
func sectionHeading(section *html.Node) string {
	if section == nil {
		return ""
	}
	if ds := attrVal(section, "data-ox-section"); ds != "" {
		return ds
	}
	if h := firstHeading(section); h != nil {
		return textContent(h)
	}
	return attrVal(section, "id")
}

// firstHeading returns the first h2 or h3 descendant in document order.
func firstHeading(n *html.Node) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && (c.DataAtom == atom.H2 || c.DataAtom == atom.H3) {
			return c
		}
		if hit := firstHeading(c); hit != nil {
			return hit
		}
	}
	return nil
}

// hasAttr reports attribute presence: CSS [attr] matches a present but empty
// attribute too.
func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

// textContent mirrors DOM textContent: every text node in tree order.
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
