package plan

import (
	"strings"
	"testing"
)

// enrichedResult is a Result that carries SageOx enrichment (one deterministic
// badge). un-enriched plans use the zero Result.
func enrichedResult() Result {
	// a deterministic, ox-computed badge — the kind that renders an anchored OX
	// marker (so a render without the marker is correctly flagged).
	return Result{Annotations: []Annotation{{Kind: BadgeDeterministic, Type: BadgeCollision, Why: "contended"}}}
}

// contextOnlyResult carries enrichment via the context bundle but no badges.
func contextOnlyResult() Result {
	return Result{Context: []ContextItem{{Kind: "session", Title: "prior work"}}}
}

// the canonical attribution fragments the html-plan skill is spec'd to emit.
const (
	footerCredit = `<footer>Team context enriched by SageOx</footer>`
	oxMarker     = `<button aria-label="SageOx insight">…</button>`
	inlineAvatar = `<img src="data:image/png;base64,AAAA">`
	remoteAvatar = `<img src="https://avatars.githubusercontent.com/u/224450799?s=64">`
)

// TestLintBranding_EarnedCreditRequired verifies the core guarantee: an
// enriched plan whose render omits the footer credit is flagged, and a fully
// attributed render is clean.
// Failure prevented: SageOx silently loses credit on a team-context-aware plan.
func TestLintBranding_EarnedCreditRequired(t *testing.T) {
	tests := []struct {
		name      string
		html      string
		res       Result
		wantRules []string
	}{
		{
			name: "enriched + credit + marker is clean",
			html: footerCredit + oxMarker + inlineAvatar,
			res:  enrichedResult(),
		},
		{
			name:      "enriched but no credit is flagged",
			html:      oxMarker + inlineAvatar,
			res:       enrichedResult(),
			wantRules: []string{"branding.footer-credit"},
		},
		{
			name:      "enriched with badges but no OX marker is flagged",
			html:      footerCredit,
			res:       enrichedResult(),
			wantRules: []string{"branding.ox-marker"},
		},
		{
			name: "context-only enrichment needs credit, not a marker",
			html: footerCredit,
			res:  contextOnlyResult(),
		},
		{
			name:      "context-only enrichment without credit is flagged",
			html:      "<p>plan</p>",
			res:       contextOnlyResult(),
			wantRules: []string{"branding.footer-credit"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertRules(t, LintBranding([]byte(tt.html), tt.res), tt.wantRules)
		})
	}
}

// TestLintBranding_NoOverclaim verifies an un-enriched plan must NOT credit
// SageOx — there is nothing to credit.
// Failure prevented: marketing-y credit appears on plans ox did not enrich.
func TestLintBranding_NoOverclaim(t *testing.T) {
	t.Run("un-enriched + no credit is clean", func(t *testing.T) {
		assertRules(t, LintBranding([]byte("<p>greenfield plan</p>"), Result{}), nil)
	})
	t.Run("un-enriched + credit is overclaim", func(t *testing.T) {
		assertRules(t, LintBranding([]byte(footerCredit), Result{}), []string{"branding.overclaim"})
	})
}

// TestLintBranding_RemoteAvatarBanned verifies the self-contained invariant: a
// live remote avatar is always flagged, even on an otherwise-correct render.
// Failure prevented: the page needs network to show the SageOx mark, breaking
// file:// rendering for the reviewer.
func TestLintBranding_RemoteAvatarBanned(t *testing.T) {
	html := footerCredit + `<button aria-label="SageOx insight">` + remoteAvatar + `</button>`
	got := LintBranding([]byte(html), enrichedResult())
	assertRules(t, got, []string{"branding.remote-avatar"})
}

// TestLintBranding_EmptyHTMLNoFindings verifies linting is a no-op when there is
// no render to check (fail-open: a save without --html must not be flagged).
func TestLintBranding_EmptyHTMLNoFindings(t *testing.T) {
	if got := LintBranding(nil, enrichedResult()); got != nil {
		t.Errorf("expected no findings on empty html, got %+v", got)
	}
}

func TestLintRender_IncludesTaggedOxVizFindings(t *testing.T) {
	html := []byte(`<svg data-ox-viz="architecture"><title>Architecture</title></svg>`)
	findings := LintRender(html, Result{})
	for _, rule := range []string{"viz.a11y.role", "viz.a11y.desc", "viz.a11y.labelledby"} {
		found := false
		for _, f := range findings {
			if f.Rule == rule {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("plan lint missing %s from ox viz integration: %+v", rule, findings)
		}
	}

	// Legacy/unowned SVGs are outside the ox viz contract and remain fail-open.
	for _, f := range LintRender([]byte(`<svg><title>Legacy</title></svg>`), Result{}) {
		if strings.HasPrefix(f.Rule, "viz.") {
			t.Errorf("plan lint should ignore untagged SVG, got %+v", f)
		}
	}
}

// assertRules checks the finding rule-ids match exactly (order-independent).
func assertRules(t *testing.T, got []BrandingFinding, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d finding(s) %v, want %d %v", len(got), ruleIDs(got), len(want), want)
	}
	have := make(map[string]bool, len(got))
	for _, f := range got {
		have[f.Rule] = true
		// every message must be non-empty and name something actionable.
		if strings.TrimSpace(f.Message) == "" {
			t.Errorf("finding %s has empty message", f.Rule)
		}
	}
	for _, r := range want {
		if !have[r] {
			t.Errorf("missing expected finding %q; got %v", r, ruleIDs(got))
		}
	}
}

func ruleIDs(fs []BrandingFinding) []string {
	ids := make([]string, len(fs))
	for i, f := range fs {
		ids[i] = f.Rule
	}
	return ids
}

// judgmentOnlyResult carries enrichment as a judgment badge and nothing else —
// the input class where LintBranding and lintWordmark used to disagree.
func judgmentOnlyResult() Result {
	return Result{Annotations: []Annotation{{Kind: BadgeJudgment, Type: BadgeCollision, Why: "rigor"}}}
}

const wordmarkMark = `<div class="toc-brand" data-ox-wordmark><svg></svg></div>`

// TestLintWordmark_EnrichedRendersMustCarryTheMark covers the gate and the
// marker match together.
// Failure prevented: the mark silently stops rendering on enriched plans, or
// the rule nags plans that never earned SageOx credit in the first place.
func TestLintWordmark_EnrichedRendersMustCarryTheMark(t *testing.T) {
	tests := []struct {
		name      string
		html      string
		res       Result
		wantRules []string
	}{
		{"enriched render without the mark is flagged", footerCredit, enrichedResult(), []string{"branding.wordmark-missing"}},
		{"enriched render with the mark is clean", footerCredit + wordmarkMark, enrichedResult(), nil},
		{"context-only enrichment still requires the mark", footerCredit, contextOnlyResult(), []string{"branding.wordmark-missing"}},
		// The gate that used to diverge from LintBranding: a judgment-only plan
		// earns the footer credit, so it must earn the wordmark check too.
		{"judgment-only enrichment requires the mark", footerCredit, judgmentOnlyResult(), []string{"branding.wordmark-missing"}},
		{"un-enriched render is never nagged", "<p>plain plan</p>", Result{}, nil},
		// Prose naming the mark must not satisfy the rule — only the emitted marker.
		{"prose mentioning the wordmark does not satisfy the rule", footerCredit + `<p>add the SageOx Wordmark here</p>`, enrichedResult(), []string{"branding.wordmark-missing"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertRules(t, lintWordmark([]byte(tt.html), tt.res), tt.wantRules)
		})
	}
}

// TestLintWordmark_EmptyHTMLNoFindings mirrors the guarantee LintBranding
// already makes. This is live, not hypothetical: `ox plan save` leaves html nil
// when --html is not passed, so without the guard a markdown-only save is
// nagged for a missing wordmark on a plan that has no render at all.
func TestLintWordmark_EmptyHTMLNoFindings(t *testing.T) {
	if got := lintWordmark(nil, enrichedResult()); got != nil {
		t.Errorf("expected no findings on empty html, got %+v", got)
	}
}

const googleFontLink = `<link href="https://fonts.googleapis.com/css2?family=Inter" rel="stylesheet"/>`

// TestLintMermaidFontRace covers the three-way signal and, crucially, both
// directions of the scoping bug: visible prose must not be able to trigger the
// finding or to suppress it.
// Failure prevented: diagrams ship with labels clipped mid-word, or the rule
// misfires on pages that merely talk about Mermaid and fonts.
func TestLintMermaidFontRace(t *testing.T) {
	tests := []struct {
		name      string
		html      string
		wantRules []string
	}{
		{
			"diagram + webfont with no font-ready re-render is flagged",
			googleFontLink + `<div class="mermaid">graph TD;</div><script>mermaid.run({});</script>`,
			[]string{"mermaid.font-race"},
		},
		{
			"font-ready re-render clears it",
			googleFontLink + `<div class="mermaid">graph TD;</div><script>document.fonts.ready.then(function(){mermaid.run({});});</script>`,
			nil,
		},
		{
			"no webfont means no race",
			`<div class="mermaid">graph TD;</div><script>mermaid.run({});</script>`,
			nil,
		},
		{
			"no diagram means no race",
			googleFontLink + `<script>mermaid.run({});</script>`,
			nil,
		},
		{
			// the false-positive half of the scoping bug
			"prose about mermaid and fonts does not trigger it",
			googleFontLink + `<p>We use mermaid diagrams; the mermaid class renders them.</p>`,
			nil,
		},
		{
			// the false-negative half: quoting the fix must not suppress the finding
			"prose quoting document.fonts.ready does not suppress it",
			googleFontLink + `<div class="mermaid">graph TD;</div><p>add document.fonts.ready.then(render)</p><script>mermaid.run({});</script>`,
			[]string{"mermaid.font-race"},
		},
		{
			// caught while mutation-testing this very rule: ox's own scaffold
			// carries a comment naming the fix, which silenced the finding even
			// with the code removed.
			"a JS comment naming fonts.ready does not suppress it",
			googleFontLink + `<div class="mermaid">graph TD;</div><script>// re-render on document.fonts.ready
mermaid.run({});</script>`,
			[]string{"mermaid.font-race"},
		},
		{
			"a block comment naming fonts.ready does not suppress it",
			googleFontLink + `<div class="mermaid">graph TD;</div><script>/* see document.fonts.ready */ mermaid.run({});</script>`,
			[]string{"mermaid.font-race"},
		},
		{
			// the comment stripper must not eat a URL inside a string literal
			"a https:// URL in script source is not treated as a comment",
			googleFontLink + `<div class="mermaid">graph TD;</div><script>var u="https://x/y";document.fonts.ready.then(function(){mermaid.run({});});</script>`,
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertRules(t, lintMermaidFontRace([]byte(tt.html)), tt.wantRules)
		})
	}
}

// TestLintRender_GoRendererOutputIsClean is the guard that would have caught
// this PR's own regression: ox must never ship a lint rule its OWN renderer
// fails. Before the scaffold.js font-ready fix, every non-artifact render with
// a diagram emitted mermaid.font-race against itself, and `ox plan lint
// --strict` exited non-zero on ox's own output.
// Failure prevented: a new advisory rule that nags every plan ox produces.
func TestLintRender_GoRendererOutputIsClean(t *testing.T) {
	in := Input{
		Raw: "# Plan\n\n## Approach\nWe build on ADR-051 here.\n\n```mermaid\ngraph TD;\n  A-->B;\n```\n",
		Sections: []Section{
			{Heading: "", Body: "# Plan\n"},
			{Heading: "Approach", Body: "We build on ADR-051 here.\n\n```mermaid\ngraph TD;\n  A-->B;\n```\n"},
		},
	}
	res := Result{Context: adrCtx()}
	out, err := RenderHTML(in, res)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	if !strings.Contains(string(out), `class="mermaid"`) {
		t.Fatal("fixture did not produce a mermaid diagram — the test cannot prove anything")
	}
	if findings := LintRender(out, res); len(findings) != 0 {
		t.Errorf("the Go renderer's own output must lint clean, got %v", ruleIDs(findings))
	}
}
