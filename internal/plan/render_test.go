package plan

import (
	"strings"
	"testing"
)

func sampleInput() Input {
	raw := "# Sample Plan\n\nA short preamble.\n\n## Approach\n\nDo the thing.\n\n```mermaid\nflowchart LR\n  A[\"start\"] --> B[\"end\"]\n```\n\n## Verify\n\n| step | ok |\n|---|---|\n| build | yes |\n"
	return Parse(raw)
}

func TestRenderHTML_AttributionByConstruction(t *testing.T) {
	in := sampleInput()
	res := Result{
		Annotations: []Annotation{
			{Kind: "deterministic", Type: "collision", Why: "teammate murmured editing foo.go 1h ago", SourceURL: "murmur:abc"},
			{Kind: "judgment", Type: "rigor", Why: "thoughtful"}, // must NOT surface in the panel
		},
	}
	out, err := RenderHTML(in, res)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}

	// The render must satisfy the lint contract WITHOUT any hand-editing —
	// that is the whole point of generating attribution by construction.
	findings := LintBranding(out, res)
	if len(findings) != 0 {
		t.Fatalf("render failed lint contract by construction: %+v", findings)
	}

	s := string(out)
	for _, want := range []string{
		`aria-label="SageOx insight"`,      // OX marker
		"enriched by SageOx",               // footer credit
		`<pre class="mermaid">`,            // mermaid fence converted
		`<section id="sec-1"`,              // section + anchor id
		"teammate murmured editing foo.go", // the signal why (humanize strips the "1h ago" provenance)
		"<table>",                          // GFM table rendered
		"<title>Sample Plan</title>",       // H1 -> title
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered HTML missing %q", want)
		}
	}
	// judgment badges are agent-authored and must not appear in the deterministic panel
	if strings.Contains(s, "thoughtful") {
		t.Error("judgment badge leaked into the enrichment panel")
	}
}

func TestRenderHTML_NoSignalsNoMarker(t *testing.T) {
	in := sampleInput()
	res := Result{} // greenfield: no signals, no context

	out, err := RenderHTML(in, res)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(out)
	// No deterministic badges -> no OX marker (lint forbids overclaiming).
	if strings.Contains(s, `aria-label="SageOx insight"`) {
		t.Error("OX marker present with zero signals (overclaim)")
	}
	// Still a usable render.
	if !strings.Contains(s, `<section id="sec-1"`) {
		t.Error("sections not rendered")
	}
	// Every render carries a very subtle SageOx nod (a brand/tool credit, NOT an
	// enrichment claim) — so even a greenfield plan is recognizably SageOx.
	if !strings.Contains(s, `<code>ox plan</code> · SageOx`) {
		t.Error("subtle always-on SageOx footer nod missing on un-enriched render")
	}
	// ...but the un-enriched render must NOT carry the enrichment credit (overclaim).
	if strings.Contains(strings.ToLower(s), "enriched by sageox") {
		t.Error("un-enriched render claims enrichment credit (overclaim)")
	}
	if strings.Contains(s, "enrichment ran") {
		t.Error("un-enriched render claims enrichment ran")
	}
	if findings := LintBranding(out, res); len(findings) != 0 {
		t.Fatalf("no-signal render failed lint: %+v", findings)
	}
}

// TestRenderHTML_QuietFooterRequiresEnrichment distinguishes two otherwise
// similar outcomes: no deterministic signals because enrichment found only
// context, versus no signals because enrichment never ran / found nothing.
func TestRenderHTML_QuietFooterRequiresEnrichment(t *testing.T) {
	in := sampleInput()
	res := Result{Context: []ContextItem{{Kind: "adr", Title: "ADR-001", Score: 1}}}

	out, err := RenderHTML(in, res)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "Team context enriched by SageOx") {
		t.Fatal("context-only enrichment should earn footer credit")
	}
	if !strings.Contains(s, "enrichment ran") {
		t.Fatal("context-only enrichment with no signals should show the quiet no-signal note")
	}
}

// TestRenderHTML_ReviewLoopNod verifies the differentiated attribution: a page
// SERVED by the live review loop (ReviewEndpoint set) earns a slightly stronger
// SageOx nod, while a plain render does not — and an un-enriched live-loop page
// still does not overclaim enrichment.
// Failure prevented: the unique live-review-loop capability ships without the
// extra SageOx credit, or the stronger nod leaks onto every static render.
func TestRenderHTML_ReviewLoopNod(t *testing.T) {
	in := sampleInput()
	res := Result{} // un-enriched: proves the loop nod is independent of enrichment

	// plain render: no live-loop nod.
	plain, err := RenderHTML(in, res)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	if strings.Contains(string(plain), "powered by SageOx") {
		t.Error("live-loop nod leaked onto a plain (non-served) render")
	}

	// served-by-review-loop render: stronger nod present.
	served, err := RenderHTMLOpts(in, res, RenderOptions{
		Slug:           "sample-plan",
		ReviewEndpoint: "http://127.0.0.1:54321",
		ReviewToken:    "tok",
	})
	if err != nil {
		t.Fatalf("RenderHTMLOpts: %v", err)
	}
	ss := string(served)
	if !strings.Contains(ss, "Live review loop · powered by SageOx") {
		t.Error("live-review-loop render missing the stronger SageOx nod")
	}
	if !strings.Contains(ss, "foot-live") {
		t.Error("live-loop nod not styled (missing foot-live class)")
	}
	// even with the stronger nod, an un-enriched page must not overclaim enrichment.
	if strings.Contains(strings.ToLower(ss), "enriched by sageox") {
		t.Error("live-loop render overclaims enrichment on an un-enriched plan")
	}
	if findings := LintBranding(served, res); len(findings) != 0 {
		t.Fatalf("un-enriched live-loop render failed lint: %+v", findings)
	}
}

// TestRenderHTML_ContentPresentation verifies the deterministic presentation
// features that aid the reader: a TL;DR hero callout, a risk-flagged section,
// colored verdict cells, and a signal anchored to the section whose file it
// concerns. Failure prevented: a flat render where the decision, the risk, and
// the team signal are buried in undifferentiated prose.
func TestRenderHTML_ContentPresentation(t *testing.T) {
	raw := strings.Join([]string{
		"# Big Plan",
		"",
		"> TL;DR: ship the renderer; one risk is CDN availability.",
		"",
		"## Approach",
		"",
		"Edit `internal/plan/render.go` to add presentation.",
		"",
		"| step | ok |",
		"|---|---|",
		"| build | yes |",
		"| deploy | no |",
		"",
		"## Risks",
		"",
		"CDN could be unavailable at view time.",
		"",
	}, "\n")
	in := Parse(raw)
	res := Result{Annotations: []Annotation{
		// a collision on render.go must anchor to the "Approach" section
		{Kind: "deterministic", Type: "collision", Why: "teammate editing render.go", Files: []string{"internal/plan/render.go"}},
	}}

	out, err := RenderHTML(in, res)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(out)

	for _, want := range []string{
		`class="tldr"`,                     // TL;DR callout lifted out
		`<section id="sec-2" class="risk"`, // Risks section flagged
		`class="v-good"`,                   // "yes" verdict colored
		`class="v-bad"`,                    // "no" verdict colored
		`class="ox-rail"`,                  // per-section anchored signal rail
		"teammate editing render.go",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered HTML missing %q", want)
		}
	}

	// The anchored signal must sit inside the Approach section (sec-1), not only
	// the global panel.
	sec1 := between(s, `<section id="sec-1"`, `<section id="sec-2"`)
	if !strings.Contains(sec1, "ox-rail") {
		t.Error("collision on render.go should anchor to the Approach section's rail")
	}
}

func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	j := strings.Index(s[i:], end)
	if j < 0 {
		return s[i:]
	}
	return s[i : i+j]
}

// TestRenderHTML_HumanWhyCuratesNoise verifies the binary curates agent-only
// provenance noise out of the HUMAN render: an annotation's HumanWhy (decision-
// first) is shown, while the raw Why (commit count / last-touched date) and a
// "commit:<sha>" SourceURL are kept OUT of the page — yet the raw Why stays on
// res.Annotations for the --json/agent path (render-time projection, not a data
// change). Failure prevented: the poor render where chips carried "…12h ago",
// raw SHAs, and "touched by N workspaces" — info a human reviewer doesn't need.
func TestRenderHTML_HumanWhyCuratesNoise(t *testing.T) {
	in := sampleInput()
	rawWhy := "alice owns internal/plan/ (42 commits, last touched Jan 2026)"
	res := Result{Annotations: []Annotation{{
		Kind:      "deterministic",
		Type:      "expert-routing",
		Why:       rawWhy,
		HumanWhy:  "alice owns internal/plan/",
		SourceURL: "commit:7e49b224e0189ac4599645af32cbeec44b56c178",
		Files:     []string{"internal/plan/render.go"},
	}}}
	out, err := RenderHTML(in, res)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "alice owns internal/plan/") {
		t.Error("curated HumanWhy not shown in the render")
	}
	for _, noise := range []string{"42 commits", "last touched", "commit:7e49b22", "Jan 2026"} {
		if strings.Contains(s, noise) {
			t.Errorf("agent-only provenance noise leaked into the human render: %q", noise)
		}
	}
	// the raw Why must remain intact on the Result for the --json/agent path.
	if res.Annotations[0].Why != rawWhy {
		t.Error("raw Why was mutated — the --json/agent path must keep provenance")
	}
}

// TestRenderHTML_NoProvenanceLeaks is the DURABLE floor: no deterministic badge —
// including ones that never set HumanWhy — may leak agent-only provenance (commit
// counts, SHAs, relative timestamps, workspace counts) into a rendered chip.
// humanize() is the default projection that guarantees it, so a future badge that
// forgets HumanWhy still can't leak. Failure prevented: the regression where a new
// detector's raw Why ("…12h ago", a SHA, "touched by N workspaces") reaches the
// human render because nobody remembered to author a parallel HumanWhy.
func TestRenderHTML_NoProvenanceLeaks(t *testing.T) {
	in := sampleInput()
	res := Result{Annotations: []Annotation{
		// expert badge, NO HumanWhy — humanize must strip the parenthetical.
		{Kind: BadgeDeterministic, Type: BadgeExpertRoute,
			Why:       "alice owns internal/auth/ (42 commits, last touched Jan 2026)",
			SourceURL: "commit:7e49b224e0189ac4599645af32cbeec44b56c178"},
		// contention badge, NO HumanWhy — strip the workspace-count clause.
		{Kind: BadgeDeterministic, Type: BadgeCollision,
			Why: "internal/auth/login.go is contended — touched by 3 workspaces recently"},
		// murmur badge, NO HumanWhy — strip the relative-time tail.
		{Kind: BadgeDeterministic, Type: BadgeCollision,
			Why: "bob murmured editing internal/auth/login.go 12h ago"},
		// open-PR badge — PR number/author are human-fine; only assert no SHA.
		{Kind: BadgeDeterministic, Type: BadgeCollision,
			Why: "internal/auth/login.go is in open PR #42 (carol)", SourceURL: "https://example.com/mr/42"},
	}}
	out, err := RenderHTML(in, res)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(out)
	for _, leak := range []string{"42 commits", "last touched", "Jan 2026", "workspaces", "12h ago", "7e49b224"} {
		if strings.Contains(s, leak) {
			t.Errorf("provenance leaked into the human render without a HumanWhy: %q", leak)
		}
	}
	// humanize must strip the noise, not the point — the curated remainder stays.
	for _, keep := range []string{"alice owns internal/auth/", "is contended", "murmured editing", "open PR #42"} {
		if !strings.Contains(s, keep) {
			t.Errorf("humanize over-stripped — expected chip text %q missing", keep)
		}
	}
}

// TestHumanize pins the provenance-strip projection used by the render.
func TestHumanize(t *testing.T) {
	cases := map[string]string{
		"alice owns internal/plan/ (42 commits, last touched Jan 2026)": "alice owns internal/plan/",
		"x.go is contended — touched by 3 workspaces recently":          "x.go is contended",
		"bob murmured editing x.go 12h ago":                             "bob murmured editing x.go",
		"y.go is in open PR #42 (carol)":                                "y.go is in open PR #42 (carol)",
		"clean phrasing":                                                "clean phrasing",
	}
	for in, want := range cases {
		if got := humanize(in); got != want {
			t.Errorf("humanize(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestFilesIntersect proves the basename-collision fix: two different files that
// share a basename must NOT intersect, while a bare ref still matches a fuller
// path. Failure prevented: a signal citing one client.go anchors to a section
// citing a different client.go and inflates the broad-anchor count.
func TestFilesIntersect(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want bool
	}{
		{"bare basename matches full path", []string{"render.go:42"}, []string{"internal/plan/render.go"}, true},
		{"same basename different dirs do NOT match", []string{"internal/auth/client.go"}, []string{"internal/lfs/client.go"}, false},
		{"identical full paths match", []string{"internal/plan/x.go"}, []string{"internal/plan/x.go"}, true},
		{"disjoint", []string{"a.go"}, []string{"b.go"}, false},
		{"empty side", nil, []string{"a.go"}, false},
	}
	for _, tc := range cases {
		if got := filesIntersect(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: filesIntersect(%v,%v)=%v want %v", tc.name, tc.a, tc.b, got, tc.want)
		}
	}
}

// TestTooBroadToAnchor pins the proportional cap: a signal in 3 of 12 sections is
// section-specific (anchors), one in 3 of 3 is cross-cutting (globalizes), and 1-2
// hits always anchor. Failure prevented: the old fixed cap of 2 globalized any
// signal in 3 narrowly-related sections of a large plan.
func TestTooBroadToAnchor(t *testing.T) {
	cases := []struct {
		hits, sections int
		broad          bool
	}{
		{1, 1, false},  // single-section plan: anchor
		{2, 4, false},  // narrow: 2 of 4
		{3, 12, false}, // section-specific in a big plan: anchor
		{3, 3, true},   // cross-cutting: globalize
		{3, 5, true},   // majority of a small plan
		{4, 8, false},  // exactly half is not "more than half": anchor
		{5, 8, true},   // more than half: globalize
	}
	for _, tc := range cases {
		if got := tooBroadToAnchor(tc.hits, tc.sections); got != tc.broad {
			t.Errorf("tooBroadToAnchor(%d,%d)=%v want %v", tc.hits, tc.sections, got, tc.broad)
		}
	}
}
