package plan

import (
	"strings"
	"testing"
)

// renderMarkdown is a small helper: parse raw plan markdown and render it with
// the given options, failing the test on error.
func renderMarkdown(t *testing.T, raw string, opts RenderOptions) string {
	t.Helper()
	out, err := RenderHTMLOpts(Parse(raw), Result{}, opts)
	if err != nil {
		t.Fatalf("RenderHTMLOpts: %v", err)
	}
	return string(out)
}

// --- A. Code-block syntax highlighting ---

// TestHighlight_KnownLanguageColorized verifies a fenced block with a known
// language is chroma-tokenized into class-based spans.
// Failure prevented: code blocks render flat monochrome (the original complaint).
func TestHighlight_KnownLanguageColorized(t *testing.T) {
	raw := "# P\n\n## Code\n\n```json\n{\n  \"version\": 1\n}\n```\n"
	s := renderMarkdown(t, raw, RenderOptions{})

	if !strings.Contains(s, `<pre class="ox-hl-chroma">`) {
		t.Error("known-language block was not chroma-highlighted")
	}
	if !strings.Contains(s, "ox-hl-") {
		t.Error("expected prefixed chroma token classes in output")
	}
	if !strings.Contains(s, "version") {
		t.Error("code content lost during highlighting")
	}
}

// TestHighlight_MermaidUntouched verifies mermaid fences are NOT chroma-tokenized
// — they must stay <pre class="mermaid"> for mermaid.run() to pick up.
// Failure prevented: highlighting breaks every Mermaid diagram (the #1 risk).
func TestHighlight_MermaidUntouched(t *testing.T) {
	raw := "# P\n\n## Flow\n\n```mermaid\nflowchart LR\n  A[\"x\"] --> B[\"y\"]\n```\n"
	s := renderMarkdown(t, raw, RenderOptions{})

	if !strings.Contains(s, `<pre class="mermaid">`) {
		t.Error("mermaid fence was not preserved")
	}
	// No code block should have been chroma-wrapped (only mermaid present).
	if strings.Contains(s, `<pre class="ox-hl-chroma">`) {
		t.Error("mermaid block was incorrectly chroma-tokenized")
	}
	if !strings.Contains(s, "flowchart LR") {
		t.Error("mermaid source lost")
	}
}

// TestHighlight_UnknownLanguageVerbatim verifies an unknown language is left as
// goldmark rendered it — never falling back to prose-tokenizing or panicking.
func TestHighlight_UnknownLanguageVerbatim(t *testing.T) {
	raw := "# P\n\n## Code\n\n```zzznotalang\nhello world\n```\n"
	s := renderMarkdown(t, raw, RenderOptions{})

	if strings.Contains(s, `<pre class="ox-hl-chroma">`) {
		t.Error("unknown language should not be chroma-wrapped")
	}
	if !strings.Contains(s, `class="language-zzznotalang"`) {
		t.Error("unknown-language block should be left verbatim")
	}
}

// TestHighlight_BareFenceUntouched verifies a fence with no language stays plain.
func TestHighlight_BareFenceUntouched(t *testing.T) {
	raw := "# P\n\n## Code\n\n```\nplain text\n```\n"
	s := renderMarkdown(t, raw, RenderOptions{})

	if strings.Contains(s, `<pre class="ox-hl-chroma">`) {
		t.Error("bare fence should not be highlighted")
	}
	if !strings.Contains(s, "plain text") {
		t.Error("bare fence content lost")
	}
}

// TestHighlight_EmbeddedCloseTagSafe verifies a code block whose content contains
// the literal string "</code></pre>" is not truncated — goldmark escapes the
// angle brackets, so the close-tag regex cannot match inside the block.
func TestHighlight_EmbeddedCloseTagSafe(t *testing.T) {
	raw := "# P\n\n## Code\n\n```html\n<div></code></pre>after</div>\n```\n"
	s := renderMarkdown(t, raw, RenderOptions{})

	if !strings.Contains(s, "after") {
		t.Error("content after an embedded close-tag was truncated")
	}
}

// TestHighlightCSS_ThemeScopedAndStable verifies both theme palettes are emitted,
// scoped under [data-theme], with chroma's panel background stripped so the
// scaffold's var(--panel) shows through; and that generation is stable.
// Failure prevented: highlight colors don't flip with the theme toggle, or chroma
// paints its own background over the design-token panel.
func TestHighlightCSS_ThemeScopedAndStable(t *testing.T) {
	css := highlightCSS()

	if !strings.Contains(css, `html[data-theme="dark"]`) {
		t.Error("missing dark-theme-scoped highlight CSS")
	}
	if !strings.Contains(css, `html[data-theme="light"]`) {
		t.Error("missing light-theme-scoped highlight CSS")
	}
	if strings.Contains(css, "background-color:") || strings.Contains(css, "background:") {
		t.Error("chroma background leaked; scaffold var(--panel) would be overpainted")
	}
	if highlightCSS() != css {
		t.Error("highlightCSS must be deterministic across calls")
	}
}

// --- B. Prior-art crisp clickable links ---

func priorArtResult() Result {
	return Result{Annotations: []Annotation{{
		Kind: BadgeDeterministic, Type: BadgePriorArt,
		Why:       "alice · 2026-02-13 · cache warming path",
		SourceURL: "2026-02-13T14-56-alice-OxAb12",
		Expert:    "alice", Date: "2026-02-13", RefKind: "session",
	}}}
}

// TestPriorArt_SessionLinks verifies a session prior-art entry becomes a crisp
// linked label plus a "view" affordance, opening in a new tab.
// Failure prevented: prior art stays dead, repetitive text (the complaint).
func TestPriorArt_SessionLinks(t *testing.T) {
	in := Parse("# P\n\n## Approach\n\nDo it.\n")
	resolver := func(refKind, ref string) string {
		if refKind == "session" {
			return "https://ex.test/repo/r1/sessions/" + ref + "/view"
		}
		return ""
	}
	out, err := RenderHTMLOpts(in, priorArtResult(), RenderOptions{PriorArtURL: resolver})
	if err != nil {
		t.Fatalf("RenderHTMLOpts: %v", err)
	}
	s := string(out)

	for _, want := range []string{
		`class="src-link"`,
		`href="https://ex.test/repo/r1/sessions/2026-02-13T14-56-alice-OxAb12/view"`,
		`target="_blank"`,
		`rel="noopener noreferrer"`,
		"view ↗",
		"alice · 2026-02-13 · cache warming path",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("linked prior-art missing %q", want)
		}
	}
	// the slug must NOT be printed as a trailing plain-text .src span — that
	// visible double-print (slug in prose AND in the span) was the original bug.
	// It legitimately appears inside the two anchor hrefs only.
	if strings.Contains(s, `<span class="src">`) {
		t.Error("linked prior-art must not also print the slug as a plain .src span")
	}
}

// TestPriorArt_NoLinkWhenResolverEmpty verifies plans/murmurs (resolver returns
// "") render as crisp text with no anchor and no empty href.
func TestPriorArt_NoLinkWhenResolverEmpty(t *testing.T) {
	in := Parse("# P\n\n## Approach\n\nDo it.\n")
	resolver := func(refKind, ref string) string { return "" } // e.g. plan/murmur
	out, err := RenderHTMLOpts(in, priorArtResult(), RenderOptions{PriorArtURL: resolver})
	if err != nil {
		t.Fatalf("RenderHTMLOpts: %v", err)
	}
	s := string(out)

	if strings.Contains(s, `class="src-link"`) {
		t.Error("entry should not be linked when resolver yields no URL")
	}
	if !strings.Contains(s, "alice · 2026-02-13 · cache warming path") {
		t.Error("crisp label missing in the no-link fallback")
	}
}

// TestPriorArt_NilResolverNoPanic verifies a nil PriorArtURL (the zero
// RenderOptions, e.g. enrich --json paths) renders crisp text without linking.
func TestPriorArt_NilResolverNoPanic(t *testing.T) {
	in := Parse("# P\n\n## Approach\n\nDo it.\n")
	out, err := RenderHTMLOpts(in, priorArtResult(), RenderOptions{}) // nil resolver
	if err != nil {
		t.Fatalf("RenderHTMLOpts: %v", err)
	}
	if strings.Contains(string(out), `class="src-link"`) {
		t.Error("nil resolver must not produce links")
	}
}

// --- C. relevanceSummary cleaning ---

// TestRelevanceSummary verifies snippet cleaning: whitespace collapse, edge
// markdown stripping, word-boundary cap, and the too-short → "" fallback.
func TestRelevanceSummary(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"collapses whitespace", "the   cache\n\nwarming  path", "the cache warming path"},
		{"strips edge markdown", "## the cache warming path ##", "the cache warming path"},
		{"strips quote and list markers", ">- the cache warming path -", "the cache warming path"},
		{"caps at ten words", "one two three four five six seven eight nine ten eleven twelve", "one two three four five six seven eight nine ten"},
		{"too short -> empty", "ok", ""},
		{"empty -> empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := relevanceSummary(tt.in); got != tt.want {
				t.Errorf("relevanceSummary(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
