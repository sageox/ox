package viz

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	xhtml "golang.org/x/net/html"
)

type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

type Finding struct {
	Rule     string   `json:"rule"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
}

type LintOptions struct {
	// TaggedOnly limits checks to SVGs explicitly authored through the ox viz
	// contract. Plan lint uses it to avoid policing legacy Mermaid output.
	TaggedOnly bool
}

var hardColorRE = regexp.MustCompile(`(?i)#[0-9a-f]{3,8}\b|rgba?\(`)
var cssVarRE = regexp.MustCompile(`(?i)var\([^)]*\)`)
var externalCSSRE = regexp.MustCompile(`(?i)(?:@import\s+|url\(\s*["']?)(?:https?:)?//`)

// Lint validates portable diagram fragments without rewriting them. Errors are
// objective accessibility/self-containment failures; editorial taste remains a
// warning unless the caller opts into strict enforcement.
func Lint(data []byte, opts LintOptions) []Finding {
	wrapped := "<!doctype html><html><body>" + string(data) + "</body></html>"
	doc, err := xhtml.Parse(strings.NewReader(wrapped))
	if err != nil {
		return []Finding{{Rule: "viz.parse", Severity: SeverityError, Message: err.Error()}}
	}
	var svgs []*xhtml.Node
	contentElements := 0
	walk(doc, func(n *xhtml.Node) {
		if n.Type != xhtml.ElementNode {
			return
		}
		if n.Data != "html" && n.Data != "head" && n.Data != "body" {
			contentElements++
		}
		if n.Data == "svg" {
			if !opts.TaggedOnly || hasAttr(n, "data-ox-viz") {
				svgs = append(svgs, n)
			}
		}
	})
	if len(svgs) == 0 {
		if opts.TaggedOnly {
			return nil
		}
		if contentElements == 0 {
			return []Finding{{Rule: "viz.missing", Severity: SeverityError, Message: "no SVG or HTML visualization found"}}
		}
		return lintHTML(doc, false)
	}

	var findings []Finding
	if !opts.TaggedOnly {
		findings = lintHTML(doc, true)
	}
	ids := map[string]int{}
	for _, svg := range svgs {
		walk(svg, func(n *xhtml.Node) {
			if id := attr(n, "id"); id != "" {
				ids[id]++
			}
		})
	}
	for id, count := range ids {
		if count > 1 {
			findings = append(findings, Finding{Rule: "viz.a11y.duplicate-id", Severity: SeverityError, Message: fmt.Sprintf("id %q appears %d times", id, count)})
		}
	}

	for i, svg := range svgs {
		label := attr(svg, "data-ox-viz")
		if label == "" {
			label = fmt.Sprintf("diagram %d", i+1)
		}
		findings = append(findings, lintSVG(svg, label, ids)...)
	}
	return findings
}

func lintHTML(doc *xhtml.Node, skipSVG bool) []Finding {
	var findings []Finding
	add := func(rule string, severity Severity, message string) {
		findings = append(findings, Finding{Rule: rule, Severity: severity, Message: "HTML fragment: " + message})
	}
	visit := func(n *xhtml.Node) {
		if n.Type != xhtml.ElementNode {
			return
		}
		for _, a := range n.Attr {
			name, value := strings.ToLower(a.Key), strings.TrimSpace(a.Val)
			if name == "src" && externalURL(value) {
				add("viz.self-contained.external", SeverityError, fmt.Sprintf("external asset %q is not portable", value))
			}
			if n.Data == "link" && name == "href" && externalURL(value) {
				add("viz.self-contained.external", SeverityError, fmt.Sprintf("external stylesheet %q is not portable", value))
			}
			if strings.HasPrefix(name, "on") {
				add("viz.motion.inline-handler", SeverityWarning, fmt.Sprintf("inline event handler %q makes static output harder to review", a.Key))
			}
			if name == "style" && hasHardColor(value) {
				add("viz.theme.hard-color", SeverityWarning, "inline style uses a hard-coded color instead of a SageOx theme token")
			}
		}
		if n.Data == "style" && externalCSSRE.MatchString(nodeText(n)) {
			add("viz.self-contained.external", SeverityError, "external CSS is not portable")
		}
		if n.Data == "style" && hasHardColor(nodeText(n)) {
			add("viz.theme.hard-color", SeverityWarning, "CSS uses a hard-coded color instead of a SageOx theme token")
		}
		if n.Data == "script" {
			if externalURL(attr(n, "src")) {
				return // already reported as an external src
			}
			add("viz.motion.script", SeverityWarning, "static output is the default; inline motion needs an explicit reason and reduced-motion fallback")
		}
	}
	if skipSVG {
		walkOutsideSVG(doc, visit)
	} else {
		walk(doc, visit)
	}
	return findings
}

func lintSVG(svg *xhtml.Node, label string, ids map[string]int) []Finding {
	var findings []Finding
	errFinding := func(rule, message string) {
		findings = append(findings, Finding{Rule: rule, Severity: SeverityError, Message: label + ": " + message})
	}
	warnFinding := func(rule, message string) {
		findings = append(findings, Finding{Rule: rule, Severity: SeverityWarning, Message: label + ": " + message})
	}

	if attr(svg, "role") != "img" {
		errFinding("viz.a11y.role", `SVG must declare role="img"`)
	}
	title, desc := directChild(svg, "title"), directChild(svg, "desc")
	if title == nil || strings.TrimSpace(nodeText(title)) == "" {
		errFinding("viz.a11y.title", "SVG needs a non-empty first-level <title>")
	}
	if desc == nil || strings.TrimSpace(nodeText(desc)) == "" {
		errFinding("viz.a11y.desc", "SVG needs a non-empty first-level <desc>")
	}
	labelledBy := strings.Fields(attr(svg, "aria-labelledby"))
	if len(labelledBy) == 0 {
		errFinding("viz.a11y.labelledby", "SVG needs aria-labelledby resolving to its title and description")
	} else {
		for _, id := range labelledBy {
			if ids[id] != 1 {
				errFinding("viz.a11y.labelledby", fmt.Sprintf("aria-labelledby reference %q does not resolve exactly once", id))
			}
		}
	}
	if strings.TrimSpace(attr(svg, "viewBox")) == "" {
		warnFinding("viz.responsive.viewbox", "SVG has no viewBox")
	}

	nodes, focus := 0, 0
	walk(svg, func(n *xhtml.Node) {
		if hasAttr(n, "data-ox-node") {
			nodes++
		}
		if hasAttr(n, "data-ox-focus") {
			focus++
		}
		for _, a := range n.Attr {
			name, value := strings.ToLower(a.Key), strings.TrimSpace(a.Val)
			if (name == "href" || name == "src" || name == "xlink:href") && externalURL(value) {
				errFinding("viz.self-contained.external", fmt.Sprintf("external asset %q is not portable", value))
			}
			if strings.HasPrefix(name, "on") {
				warnFinding("viz.motion.inline-handler", fmt.Sprintf("inline event handler %q makes static output harder to review", a.Key))
			}
			if name == "fill" || name == "stroke" || name == "color" || name == "style" {
				if hasHardColor(value) {
					warnFinding("viz.theme.hard-color", fmt.Sprintf("%s uses a hard-coded color instead of a SageOx theme token", a.Key))
				}
			}
			if name == "font-size" {
				if px, ok := pixelSize(value); ok && px < 10 {
					warnFinding("viz.type.too-small", fmt.Sprintf("font-size %s is below the 10px readability floor", value))
				}
			}
		}
		if n.Type == xhtml.ElementNode && n.Data == "script" {
			if externalURL(attr(n, "src")) {
				errFinding("viz.self-contained.external", "external script is not portable")
			} else {
				warnFinding("viz.motion.script", "static output is the default; inline motion needs an explicit reason and reduced-motion fallback")
			}
		}
		if n.Type == xhtml.ElementNode && n.Data == "style" && externalCSSRE.MatchString(nodeText(n)) {
			errFinding("viz.self-contained.external", "external CSS is not portable")
		}
		if n.Type == xhtml.ElementNode && n.Data == "style" && hasHardColor(nodeText(n)) {
			warnFinding("viz.theme.hard-color", "CSS uses a hard-coded color instead of a SageOx theme token")
		}
		if n.Type == xhtml.ElementNode && n.Data == "line" && hasAttr(n, "data-ox-connector") {
			x1, x1ok := numberAttr(n, "x1")
			x2, x2ok := numberAttr(n, "x2")
			y1, y1ok := numberAttr(n, "y1")
			y2, y2ok := numberAttr(n, "y2")
			if x1ok && x2ok && y1ok && y2ok && x1 != x2 && y1 != y2 {
				warnFinding("viz.connector.diagonal", "tagged connectors must be orthogonal; use a rounded path")
			}
		}
	})
	if nodes > 12 {
		warnFinding("viz.density.nodes", fmt.Sprintf("%d nodes exceeds the balanced-detail budget of 12; split overview and detail", nodes))
	}
	if focus > 2 {
		warnFinding("viz.focus.budget", fmt.Sprintf("%d focal elements exceeds the editorial maximum of 2", focus))
	}
	return findings
}

func hasHardColor(css string) bool {
	return hardColorRE.MatchString(cssVarRE.ReplaceAllString(css, ""))
}

func HasErrors(findings []Finding) bool {
	for _, f := range findings {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}

func walk(n *xhtml.Node, fn func(*xhtml.Node)) {
	fn(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, fn)
	}
}

func walkOutsideSVG(n *xhtml.Node, fn func(*xhtml.Node)) {
	if n.Type == xhtml.ElementNode && n.Data == "svg" {
		return
	}
	fn(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkOutsideSVG(c, fn)
	}
}

func attr(n *xhtml.Node, name string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, name) {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *xhtml.Node, name string) bool {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, name) {
			return true
		}
	}
	return false
}

func directChild(n *xhtml.Node, name string) *xhtml.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == xhtml.ElementNode && c.Data == name {
			return c
		}
	}
	return nil
}

func nodeText(n *xhtml.Node) string {
	var b strings.Builder
	walk(n, func(c *xhtml.Node) {
		if c.Type == xhtml.TextNode {
			b.WriteString(c.Data)
		}
	})
	return b.String()
}

func externalURL(value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "//")
}

func numberAttr(n *xhtml.Node, name string) (float64, bool) {
	v := attr(n, name)
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	return f, err == nil
}

func pixelSize(value string) (float64, bool) {
	v := strings.TrimSpace(strings.TrimSuffix(strings.ToLower(value), "px"))
	f, err := strconv.ParseFloat(v, 64)
	return f, err == nil
}
