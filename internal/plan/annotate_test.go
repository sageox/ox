package plan

import (
	"strings"
	"testing"
)

// --- Inline reference markers (annotate.go) ---

// adrCtx is an ADR context item ox would have surfaced via the bundle.
func adrCtx() []ContextItem {
	return []ContextItem{{
		Kind:    "adr",
		Title:   "Consent tooling and capture posture",
		Ref:     "docs/human/adr/051-consent-tooling-and-capture-posture.md",
		Snippet: "bundled voiceprint + recording consent; flagged the weaker BIPA position",
	}}
}

// TestContextMarkers_OnlyADRWithDerivableToken verifies the marker set is built
// only from references whose prose token is unambiguous (ADRs). Opaque-id kinds
// (sessions, murmurs) must not produce markers — their ids never appear in prose.
// Failure prevented: ox tries to wrap a session uuid and mangles unrelated text.
func TestContextMarkers_OnlyADRWithDerivableToken(t *testing.T) {
	items := []ContextItem{
		{Kind: "adr", Ref: "docs/human/adr/051-x.md", Title: "X"},
		{Kind: "session", Ref: "2026-06-12-some-session", Title: "S"},
		{Kind: "murmur", Ref: "01J-abc", Title: "M"},
		{Kind: "adr", Ref: "no-leading-number.md", Title: "Y"}, // no token → skipped
	}
	got := contextMarkers(items)
	if len(got) != 1 {
		t.Fatalf("want 1 marker (the ADR with a derivable token), got %d", len(got))
	}
	if !got[0].re.MatchString("see ADR-051 here") {
		t.Errorf("marker regex should match the ADR's prose token")
	}
}

// TestContextMarkers_Dedup verifies a reference surfaced twice yields one marker.
// Failure prevented: duplicate context items wrap the same token repeatedly.
func TestContextMarkers_Dedup(t *testing.T) {
	items := []ContextItem{
		{Kind: "adr", Ref: "docs/human/adr/058-a.md", Title: "A"},
		{Kind: "adr", Ref: "other/058-b.md", Title: "B"},
	}
	if got := contextMarkers(items); len(got) != 1 {
		t.Fatalf("want 1 deduped marker for ADR-058, got %d", len(got))
	}
}

// TestInjectMarkers_FirstMentionOnly verifies ox wraps only the FIRST prose
// mention of a surfaced reference, leaving later mentions untouched.
// Failure prevented: every occurrence gets a marker, turning prose into noise.
func TestInjectMarkers_FirstMentionOnly(t *testing.T) {
	m := contextMarkers(adrCtx())
	in := `<p>ADR-051 is relevant. Later we revisit ADR-051 again.</p>`
	out, remaining := injectMarkers(in, m)
	if len(remaining) != 0 {
		t.Errorf("marker should have been placed, %d remain", len(remaining))
	}
	if n := strings.Count(out, `class="ox-annot"`); n != 1 {
		t.Fatalf("want exactly 1 injected marker, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "weaker BIPA position") {
		t.Errorf("tooltip (snippet) missing from injected marker:\n%s", out)
	}
	if !strings.Contains(out, `<use href="#ox-ico-d"`) {
		t.Errorf("OX glyph missing from injected marker:\n%s", out)
	}
}

// TestInjectMarkers_SkipsCodeLinksHeadings verifies ox never wraps a reference
// that lives inside a code span, a link, or a heading — those are already styled
// or already a reference, and rewriting them would corrupt the markup.
// Failure prevented: a token inside <code>/<a>/<h2> gets a <span> spliced in,
// breaking the element.
func TestInjectMarkers_SkipsCodeLinksHeadings(t *testing.T) {
	m := contextMarkers(adrCtx())
	cases := map[string]string{
		"code":    `<p>the file <code>ADR-051</code> here</p>`,
		"link":    `<p>see <a href="x">ADR-051</a></p>`,
		"heading": `<h2>ADR-051 rollout</h2>`,
	}
	for name, in := range cases {
		out, remaining := injectMarkers(in, contextMarkers(adrCtx()))
		if strings.Contains(out, `class="ox-annot"`) {
			t.Errorf("%s: marker injected into a skip-zone:\n%s", name, out)
		}
		if len(remaining) != 1 {
			t.Errorf("%s: marker should remain unplaced, got %d remaining", name, len(remaining))
		}
	}
	_ = m
}

// TestInjectMarkers_NoFireWhenAbsent verifies a reference ox has NO context for
// gets no marker — ox only annotates what it actually surfaced.
// Failure prevented: ox overclaims by marking references it never retrieved.
func TestInjectMarkers_NoFireWhenAbsent(t *testing.T) {
	m := contextMarkers(adrCtx())
	in := `<p>this plan mentions ADR-099 but not the surfaced one</p>`
	out, remaining := injectMarkers(in, m)
	if out != in {
		t.Errorf("unrelated prose was modified:\n%s", out)
	}
	if len(remaining) != 1 {
		t.Errorf("unplaced marker should be returned, got %d", len(remaining))
	}
}

// TestRenderHTML_InlineMarkerAndCornerBadge verifies the end-to-end render: a
// plan citing an ADR ox surfaced context for gets the inline OX marker, and the
// enriched page carries the subtle corner wordmark badge.
// Failure prevented: the deterministic brand surfaces silently stop rendering.
func TestRenderHTML_InlineMarkerAndCornerBadge(t *testing.T) {
	in := Input{
		Raw: "# Plan\n\n## Approach\nWe build on ADR-051 here.\n",
		Sections: []Section{
			{Heading: "", Body: "# Plan\n"},
			{Heading: "Approach", Body: "We build on ADR-051 here.\n"},
		},
	}
	res := Result{Context: adrCtx()}
	out, err := RenderHTML(in, res)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `class="ox-annot"`) {
		t.Error("inline ADR marker not injected into an enriched render")
	}
	if !strings.Contains(s, `class="toc-brand"`) {
		t.Error("corner wordmark badge missing on an enriched render")
	}
	if !strings.Contains(s, `<symbol id="ox-ico-d"`) {
		t.Error("OX icon symbol defs missing from render")
	}
	// The footer credit must still be present (enriched) and the contract satisfied.
	if findings := LintBranding(out, res); len(findings) != 0 {
		t.Fatalf("enriched render failed branding lint: %+v", findings)
	}
}
