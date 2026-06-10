package plan

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

// mermaid_lint.go validates the Mermaid diagrams an agent authored into a plan.
//
// Why this exists: the rendered page bootstraps Mermaid with
// `try{mermaid.run(...)}catch(e){}` (assets/scaffold.js) — a SWALLOWED error. So
// a malformed or non-portable diagram renders as a BLANK BOX, silently. Neither
// the agent nor the reviewer sees a failure; the content is just gone. Nothing
// else in the pipeline catches it. These checks surface the high-confidence,
// unambiguous breakages (and GitHub-portability footguns from CLAUDE.md) so the
// diagram that ships actually renders. Advisory only — fail-open, never blocks a
// render or a save.

var (
	// ```mermaid fenced block in plan markdown (group 1 = body).
	mermaidMDFence = regexp.MustCompile("(?s)```mermaid\\s*\\n(.*?)```")
	// <pre class="mermaid">…</pre> in a rendered/saved page (group 1 = body,
	// HTML-entity-escaped by goldmark — unescaped before checking).
	mermaidHTMLFence = regexp.MustCompile(`(?s)<pre class="mermaid">(.*?)</pre>`)

	// an arrow-shaped substring inside a double-quoted label. GitHub's parser
	// (and occasionally mermaid.js) splits on these regardless of the quotes. The
	// char class excludes the bracket delimiters [ ] ( ) { } so the match can't
	// span across two adjacent nodes' quotes (e.g. A["x"] --> B["y"]).
	arrowInLabel = regexp.MustCompile(`"[^"\]\[(){}]*(-->|--?>|==>|<->|=>)[^"\]\[(){}]*"`)
	// a square-bracket node label that is NOT quoted and carries a char known to
	// break unquoted labels (@ : / ). Conservative on purpose.
	unquotedSpecialLabel = regexp.MustCompile(`\[[^"\]\n]*[@:/][^\]\n]*\]`)
	// a node id that collides with a reserved-ish keyword, used as a node decl
	// or edge endpoint. Renaming (DPR, DURL, …) is the fix.
	reservedNodeID = regexp.MustCompile(`(?m)(^|[\s])(PR|URL|IO|IS|AS|END)([\[({]|\s*--|\s*==|\s*-\.)`)
	// gantt with a numeric/placeholder dateFormat renders a meaningless axis.
	ganttNumericDateFormat = regexp.MustCompile(`(?i)dateFormat\s+X\b`)
)

// LintMermaid extracts every Mermaid diagram from a rendered/saved plan HTML and
// returns one Finding per high-confidence problem. Fail-open: no diagrams (or
// none broken) returns nil.
func LintMermaid(htmlBytes []byte) []Finding {
	return lintMermaidSources(extractMermaidFromHTML(string(htmlBytes)))
}

// LintMermaidMarkdown is the same check over raw plan markdown (```mermaid
// fences), for the render-time path where the source is in hand before the page
// is built.
func LintMermaidMarkdown(raw string) []Finding {
	return lintMermaidSources(extractMermaidFromMarkdown(raw))
}

func lintMermaidSources(diagrams []string) []Finding {
	var findings []Finding
	for i, d := range diagrams {
		findings = append(findings, lintOneDiagram(i+1, d)...)
	}
	return findings
}

func lintOneDiagram(n int, src string) []Finding {
	var out []Finding
	add := func(rule, msg string) {
		out = append(out, Finding{Rule: rule, Message: fmt.Sprintf("diagram %d: %s", n, msg)})
	}

	if loc := arrowInLabel.FindString(src); loc != "" {
		add("mermaid.arrow-in-label",
			fmt.Sprintf("label contains an arrow sequence (%s) — substitute \"to\", \"→\", or a comma; arrows inside labels break the parser", mermaidSnippet(loc)))
	}
	if loc := unquotedSpecialLabel.FindString(src); loc != "" {
		add("mermaid.unquoted-label",
			fmt.Sprintf("node label %s has a special char but is not quoted — wrap it: A[\"…\"]", mermaidSnippet(loc)))
	}
	if reservedNodeID.MatchString(src) {
		add("mermaid.reserved-id",
			"a node id uses a reserved-ish keyword (PR/URL/IO/IS/AS/END) — rename it (DPR, DURL, …)")
	}
	if strings.Contains(src, `\n`) {
		add("mermaid.literal-newline",
			`label contains a literal \n — use <br/> for line breaks (\n renders literally)`)
	}
	if ganttNumericDateFormat.MatchString(src) {
		add("mermaid.gantt-numeric-dateformat",
			"gantt uses `dateFormat X` (numeric) — it renders a meaningless axis; use real dates (YYYY-MM-DD) or a CSS swimlane")
	}
	return out
}

// extractMermaidFromMarkdown pulls the body of each ```mermaid fence.
func extractMermaidFromMarkdown(raw string) []string {
	var out []string
	for _, m := range mermaidMDFence.FindAllStringSubmatch(raw, -1) {
		out = append(out, m[1])
	}
	return out
}

// extractMermaidFromHTML pulls each <pre class="mermaid"> body and unescapes the
// HTML entities goldmark inserted, recovering the raw diagram source.
func extractMermaidFromHTML(h string) []string {
	var out []string
	for _, m := range mermaidHTMLFence.FindAllStringSubmatch(h, -1) {
		out = append(out, html.UnescapeString(m[1]))
	}
	return out
}

// snippet trims an offending fragment to a short, single-line form for messages.
func mermaidSnippet(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	const max = 48
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}
