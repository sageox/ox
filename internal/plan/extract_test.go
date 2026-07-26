package plan

import (
	"strings"
	"testing"
)

// tabbedFixtureHTML models a real hand-built tabbed plan page: a <head> with
// <title> + a <style> that must never leak, an <h1>, a <nav> tab bar whose
// button labels must never leak, and one <section class="view"> per tab
// exercising a table (with a "|" in a cell), a list, a fenced <pre>, and an
// interactive-only <div><span> pair. A trailing <script> must never leak.
const tabbedFixtureHTML = `<!DOCTYPE html>
<html>
<head>
<title>Turn vs Event</title>
<style>body{color:red}</style>
</head>
<body>
<header><h1>Turn vs Event: A Field Guide</h1></header>
<nav>
<button>TabOne</button>
<button>TabTwo</button>
<button>TabThree</button>
<button>TabFour</button>
<button>TabFive</button>
</nav>
<section class="view" id="overview">
<h2>Overview</h2>
<p>Some context about the difference.</p>
</section>
<section class="view" id="table">
<h2>Comparison Table</h2>
<table>
<tr><th>Concept</th><th>Scope</th></tr>
<tr><td>Turn</td><td>Single message | reply</td></tr>
<tr><td>Event</td><td>Anchor span</td></tr>
</table>
</section>
<section class="view" id="list">
<h2>Key Properties</h2>
<ul>
<li>Deterministic ordering</li>
<li>Stable identity</li>
</ul>
</section>
<section class="view" id="code">
<h2>Example</h2>
<pre>  step one
    step two</pre>
</section>
<section class="view" id="interactive">
<h2>Interactive Demo</h2>
<div class="code"><span>raw</span> <span>text</span></div>
</section>
<script>const MAP={a:1,b:2};</script>
</body>
</html>`

// TestExtractMarkdown_TabbedPage pins the full tabbed-page shape: h1 wins
// over <title>, every h2 survives, a table/list/code-block/interactive-div
// each degrade correctly, and script/style/nav content never leaks. Failure
// prevented: a plan-of-record HTML page whose derived markdown either loses
// searchable content or leaks chrome noise (tab labels, CSS, JS) into the
// grep/search-facing projection.
func TestExtractMarkdown_TabbedPage(t *testing.T) {
	md := ExtractMarkdown([]byte(tabbedFixtureHTML))

	if !strings.HasPrefix(md, "# Turn vs Event: A Field Guide") {
		t.Fatalf("expected md to start with the h1 (title dropped), got prefix %q", firstLine(md))
	}

	mustContain := []string{
		"## Overview",
		"## Comparison Table",
		"## Key Properties",
		"## Example",
		"## Interactive Demo",
		"| Concept | Scope |",
		"| --- | --- |",
		`Single message \| reply`, // "|" in cell text escaped
		"- Deterministic ordering",
		"- Stable identity",
		"```\n  step one\n    step two\n```", // verbatim pre content
	}
	for _, want := range mustContain {
		if !strings.Contains(md, want) {
			t.Errorf("ExtractMarkdown output missing %q\n--- output ---\n%s", want, md)
		}
	}

	// The interactive-only <div><span> pair must degrade to its own single
	// paragraph, not two, and not bleed into a neighboring block.
	if !hasBlock(md, "raw text") {
		t.Errorf("expected %q to appear as its own paragraph\n--- output ---\n%s", "raw text", md)
	}

	mustNotContain := []string{
		"const MAP",                                          // <script> body
		"color:red",                                          // <style> body
		"TabOne", "TabTwo", "TabThree", "TabFour", "TabFive", // nav button labels
	}
	for _, bad := range mustNotContain {
		if strings.Contains(md, bad) {
			t.Errorf("ExtractMarkdown output leaked skipped content %q\n--- output ---\n%s", bad, md)
		}
	}
}

// TestExtractMarkdown_DataOxSection covers the data-ox-section authoring
// contract for pages whose tabs/views are plain divs, not headings. Failure
// prevented: a heading-less view rendering as unlabelled prose with no
// searchable section boundary, or a view whose div AND its own <h2> both
// producing the same heading twice.
func TestExtractMarkdown_DataOxSection(t *testing.T) {
	const html = `<html><body>
<div data-ox-section="Verdict"><p>The verdict is clear.</p></div>
<div data-ox-section="Anatomy"><h2>Anatomy</h2><p>Breakdown follows.</p></div>
</body></html>`

	md := ExtractMarkdown([]byte(html))

	if !strings.Contains(md, "## Verdict") {
		t.Errorf("expected synthesized %q marker for a heading-less section\n--- output ---\n%s", "## Verdict", md)
	}
	if got := strings.Count(md, "## Anatomy"); got != 1 {
		t.Errorf("expected exactly one %q (marker suppressed in favor of the section's own h2), got %d\n--- output ---\n%s", "## Anatomy", got, md)
	}
}

// TestExtractMarkdown_NoH1FallsBackToTitle covers the fallback path: when the
// body never produces an h1, <title> becomes the first line so a plan
// missing a body heading still has a searchable top-level label. Failure
// prevented: a plan with no h1 losing its identifying title entirely from the
// derived markdown.
func TestExtractMarkdown_NoH1FallsBackToTitle(t *testing.T) {
	const html = `<html><head><title>Fallback Title</title></head><body><p>First paragraph.</p><p>Second paragraph.</p></body></html>`

	md := ExtractMarkdown([]byte(html))
	if !strings.HasPrefix(md, "# Fallback Title") {
		t.Fatalf("expected md to start with %q, got %q", "# Fallback Title", firstLine(md))
	}
}

// TestExtractMarkdown_MalformedInputNeverPanics is the hard contract:
// ExtractMarkdown must never error or panic, only degrade. Failure
// prevented: a malformed authored page (or a nil/empty read) crashing
// whatever background job is deriving plan.md from plan.html.
func TestExtractMarkdown_MalformedInputNeverPanics(t *testing.T) {
	tests := []struct {
		name         string
		in           []byte
		wantContains string
	}{
		{"nil input", nil, ""},
		{"unclosed tag", []byte("<div>unclosed"), "unclosed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("ExtractMarkdown(%q) panicked: %v", tt.in, r)
				}
			}()
			got := ExtractMarkdown(tt.in)
			if tt.wantContains != "" && !strings.Contains(got, tt.wantContains) {
				t.Errorf("ExtractMarkdown(%q) = %q, want contains %q", tt.in, got, tt.wantContains)
			}
		})
	}
}

// TestExtractMarkdown_Deterministic pins document-order-only output: no map
// iteration or other nondeterminism sneaking into the walk. Failure
// prevented: a flaky derived plan.md that rewrites itself differently on
// every save/regenerate cycle, producing noisy diffs and unstable search.
func TestExtractMarkdown_Deterministic(t *testing.T) {
	t.Parallel()
	a := ExtractMarkdown([]byte(tabbedFixtureHTML))
	b := ExtractMarkdown([]byte(tabbedFixtureHTML))
	if a != b {
		t.Fatalf("ExtractMarkdown is non-deterministic:\n--- run 1 ---\n%s\n--- run 2 ---\n%s", a, b)
	}
}

// TestExtractMarkdown_DropsUnsafeLinkSchemes pins the markdown projection's
// link safety floor: schemes browsers can execute as active content degrade to
// plain text instead of becoming live anchors when plan.md is rendered later.
func TestExtractMarkdown_DropsUnsafeLinkSchemes(t *testing.T) {
	const html = `<html><body><p>
		<a href=" javascript:alert(1)">js</a>
		<a href="DATA:text/html,evil">data</a>
		<a href="vbscript:evil">vb</a>
		<a href="https://example.com/ok">ok</a>
	</p></body></html>`

	md := ExtractMarkdown([]byte(html))
	for _, bad := range []string{"javascript:", "DATA:", "vbscript:"} {
		if strings.Contains(md, bad) {
			t.Fatalf("unsafe href survived into markdown: %q\n--- output ---\n%s", bad, md)
		}
	}
	if !strings.Contains(md, "[ok](https://example.com/ok)") {
		t.Fatalf("safe http link missing from markdown\n--- output ---\n%s", md)
	}
	for _, want := range []string{"js", "data", "vb"} {
		if !strings.Contains(md, want) {
			t.Errorf("rejected link text %q should remain as plain text\n--- output ---\n%s", want, md)
		}
	}
}

// hasBlock reports whether md contains a block (a "\n\n"-delimited unit)
// whose trimmed content equals want exactly — stronger than strings.Contains
// alone, which would also pass if want only ever showed up as a substring of
// some larger, incorrectly-merged block.
func hasBlock(md, want string) bool {
	for _, block := range strings.Split(strings.TrimSuffix(md, "\n"), "\n\n") {
		if block == want {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
