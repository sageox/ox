package plan

import (
	"testing"
)

func TestLintBranding_JudgmentOnlyAnnotation(t *testing.T) {
	in := Parse("# Plan\n\n## Section\n\nbody text\n")
	res := Result{
		Annotations: []Annotation{
			{Kind: "judgment", Type: "rigor", Why: "well thought out"},
		},
	}
	html, err := RenderHTML(in, res)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	findings := LintBranding(html, res)
	t.Logf("findings: %+v", findings)
	// Document whether this is a bug: judgment-only plans should not require an OX marker
}
