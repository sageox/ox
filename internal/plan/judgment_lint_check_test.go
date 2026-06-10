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
	// A judgment-only plan earns the footer credit but renders no per-element OX
	// marker, so it must NOT trip the ox-marker finding — only deterministic
	// badges require a marker.
	// Failure prevented: LintBranding false-positives on skill-authored
	// judgment-only plans, blocking a clean `ox plan lint`.
	for _, f := range LintBranding(html, res) {
		if f.Rule == "branding.ox-marker" {
			t.Fatalf("judgment-only plan must not require an OX marker, got: %+v", f)
		}
	}
}
