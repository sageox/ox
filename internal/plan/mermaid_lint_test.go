package plan

import (
	"strings"
	"testing"
)

// TestLintMermaidMarkdown_CatchesBreakage verifies each high-confidence Mermaid
// problem is flagged. Failure prevented: the page swallows mermaid errors, so a
// broken diagram renders as a silent blank box that nobody notices.
func TestLintMermaidMarkdown_CatchesBreakage(t *testing.T) {
	cases := []struct {
		name string
		src  string
		rule string
	}{
		{
			name: "arrow inside quoted label",
			src:  "flowchart TB\n  A[\"build -> ship\"] --> B[\"done\"]",
			rule: "mermaid.arrow-in-label",
		},
		{
			name: "unquoted label with special char",
			src:  "flowchart LR\n  A[internal/plan/render.go] --> B[ok]",
			rule: "mermaid.unquoted-label",
		},
		{
			name: "reserved node id",
			src:  "flowchart TB\n  PR[open] --> M[merge]",
			rule: "mermaid.reserved-id",
		},
		{
			name: "literal newline in label",
			src:  "flowchart TB\n  A[\"line1\\nline2\"] --> B[\"x\"]",
			rule: "mermaid.literal-newline",
		},
		{
			name: "numeric gantt dateformat",
			src:  "gantt\n  dateFormat X\n  task :0, 5",
			rule: "mermaid.gantt-numeric-dateformat",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			md := "```mermaid\n" + tc.src + "\n```\n"
			findings := LintMermaidMarkdown(md)
			if !hasRule(findings, tc.rule) {
				t.Errorf("expected rule %q, got %+v", tc.rule, findings)
			}
		})
	}
}

// TestLintMermaid_CleanDiagramPasses verifies a well-formed diagram produces no
// findings (no false positives that would erode trust in the advisory).
func TestLintMermaid_CleanDiagramPasses(t *testing.T) {
	md := "```mermaid\nflowchart TB\n  A[\"build\"] --> B[\"ship\"]\n  B --> C[\"done\"]\n```\n"
	if f := LintMermaidMarkdown(md); len(f) != 0 {
		t.Errorf("clean diagram should pass, got %+v", f)
	}
}

// TestLintMermaid_FromRenderedHTML verifies the HTML path unescapes entities and
// still catches breakage — the saved-render lint route used by `ox plan lint`.
func TestLintMermaid_FromRenderedHTML(t *testing.T) {
	// goldmark would emit the arrow as &gt;; the linter must unescape first.
	html := `<pre class="mermaid">flowchart TB
  A["build --&gt; ship"] --&gt; B["done"]</pre>`
	if !hasRule(LintMermaid([]byte(html)), "mermaid.arrow-in-label") {
		t.Error("expected arrow-in-label finding from rendered HTML after unescape")
	}
}

func hasRule(fs []Finding, rule string) bool {
	for _, f := range fs {
		if f.Rule == rule {
			return true
		}
	}
	return false
}

// TestLintRender_Aggregates verifies the single entrypoint returns both
// attribution and mermaid findings.
func TestLintRender_Aggregates(t *testing.T) {
	html := []byte(`<html><body><pre class="mermaid">flowchart TB
  PR[x] --> Y[z]</pre></body></html>`)
	// res has an annotation -> branding lint will want a footer/marker (absent here)
	res := Result{Annotations: []Annotation{{Kind: "deterministic", Type: "collision", Why: "x"}}}
	fs := LintRender(html, res)
	if !hasRule(fs, "mermaid.reserved-id") {
		t.Errorf("expected mermaid finding in aggregate, got %+v", fs)
	}
	if !strings.Contains(joinRules(fs), "branding.") {
		t.Errorf("expected a branding finding in aggregate, got %+v", fs)
	}
}

func joinRules(fs []Finding) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString(f.Rule)
		b.WriteByte(' ')
	}
	return b.String()
}
