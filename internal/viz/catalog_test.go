package viz

import (
	"strings"
	"testing"
)

func TestCatalogMetadataComplete(t *testing.T) {
	patterns := Catalog()
	if len(patterns) == 0 {
		t.Fatal("catalog is empty")
	}
	seen := make(map[string]bool, len(patterns))
	for _, p := range patterns {
		seen[p.ID] = true
		if p.Category == "" || p.Authoring == "" || len(p.Tags) == 0 {
			t.Errorf("pattern %q has incomplete metadata: %+v", p.ID, p)
		}
	}
	for id := range metadataByID {
		if !seen[id] {
			t.Errorf("metadata exists for missing catalog pattern %q", id)
		}
	}
}

func TestDiagramDesignSubsetIsPortableAndAttributed(t *testing.T) {
	ids := []string{
		"architecture", "flowchart", "data-flow", "layer-stack",
		"sequence-diagram", "state-machine", "timeline", "loop",
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			p, ok := PatternByID(id)
			if !ok {
				t.Fatalf("catalog is missing %q", id)
			}
			if p.Origin != diagramDesignOrigin {
				t.Errorf("origin = %q, want %q", p.Origin, diagramDesignOrigin)
			}
			for _, want := range []string{`data-ox-viz="` + id + `"`, `role="img"`, "aria-labelledby", "<title", "<desc", "viewBox"} {
				if !strings.Contains(p.Body, want) {
					t.Errorf("portable recipe missing %q", want)
				}
			}
			fragment := firstHTMLFence(p.Body)
			if fragment == "" {
				t.Fatal("recipe has no HTML fence")
			}
			if findings := Lint([]byte(fragment), LintOptions{}); HasErrors(findings) {
				t.Errorf("catalog recipe has lint errors: %+v", findings)
			}
		})
	}
}

func TestSuggestDeterministicMatches(t *testing.T) {
	cases := []struct {
		intent string
		want   string
	}{
		{"request response between API and database", "sequence-diagram"},
		{"architecture trust boundaries between services", "architecture"},
		{"branching validation gates with a fallback", "flowchart"},
		{"reinforcing feedback cycle around shared memory", "loop"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			got := Suggest(tc.intent, 1)
			if len(got) != 1 || got[0].ID != tc.want {
				t.Fatalf("Suggest(%q) = %+v, want %q first", tc.intent, got, tc.want)
			}
		})
	}
	for _, got := range Suggest("notes and annotations for a decision record", 5) {
		if got.ID == "partition-map" || got.ID == "partition-bar" {
			t.Errorf("reviewed tags should prevent partition false positive: %+v", got)
		}
	}
	for _, got := range Suggest("feedback loop around shared memory", 5) {
		if got.ID == "donut" {
			t.Errorf("word-boundary matching must not treat shared as share: %+v", got)
		}
	}
}

func TestLintAccessibilitySafetyAndEditorialFindings(t *testing.T) {
	bad := `<svg data-ox-viz="bad" viewBox="0 0 100 100" aria-labelledby="dup missing">
		<title id="dup">Bad</title><desc id="dup">Duplicate</desc>
		<style>@import "https://example.com/theme.css";</style>
		<image href="https://example.com/remote.png"/>
		<line data-ox-connector x1="0" y1="0" x2="10" y2="10"/>
	</svg>`
	findings := Lint([]byte(bad), LintOptions{})
	for _, want := range []string{"viz.a11y.duplicate-id", "viz.a11y.role", "viz.a11y.labelledby", "viz.self-contained.external", "viz.connector.diagonal"} {
		if !hasRule(findings, want) {
			t.Errorf("missing %s in %+v", want, findings)
		}
	}
	if !HasErrors(findings) {
		t.Error("objective accessibility/safety failures must be errors")
	}

	warn := `<svg data-ox-viz="dense" role="img" aria-labelledby="t d"><title id="t">Dense</title><desc id="d">Dense diagram</desc>` +
		strings.Repeat(`<g data-ox-node data-ox-focus><text font-size="8px" fill="#fff">x</text></g>`, 13) + `</svg>`
	findings = Lint([]byte(warn), LintOptions{})
	for _, want := range []string{"viz.responsive.viewbox", "viz.type.too-small", "viz.theme.hard-color", "viz.density.nodes", "viz.focus.budget"} {
		if !hasRule(findings, want) {
			t.Errorf("missing advisory %s in %+v", want, findings)
		}
	}
}

func TestLintSupportsHTMLCatalogFragments(t *testing.T) {
	clean := `<div class="device" style="background:var(--panel,#111411)">Mockup</div>`
	if findings := Lint([]byte(clean), LintOptions{}); len(findings) != 0 {
		t.Errorf("portable HTML fragment should lint cleanly: %+v", findings)
	}
	bad := `<style>.hard{color:#abc}.themed{color:var(--ink,#fff)}</style><div onclick="go()" style="color:var(--ink,#fff);border-color:#fff"><img src="https://example.com/mock.png"></div>`
	findings := Lint([]byte(bad), LintOptions{})
	for _, want := range []string{"viz.self-contained.external", "viz.motion.inline-handler", "viz.theme.hard-color"} {
		if !hasRule(findings, want) {
			t.Errorf("missing %s in %+v", want, findings)
		}
	}
}

func TestLintChecksHTMLSurroundingSVG(t *testing.T) {
	fragment := `<div><script src="https://example.com/remote.js"></script>` +
		`<svg data-ox-viz="safe" viewBox="0 0 10 10" role="img" aria-labelledby="t d"><title id="t">Safe</title><desc id="d">Safe SVG</desc></svg></div>`
	findings := Lint([]byte(fragment), LintOptions{})
	if !hasRule(findings, "viz.self-contained.external") {
		t.Errorf("remote HTML wrapper must not evade SVG lint: %+v", findings)
	}
}

func firstHTMLFence(body string) string {
	const open = "```html\n"
	start := strings.Index(body, open)
	if start < 0 {
		return ""
	}
	rest := body[start+len(open):]
	end := strings.Index(rest, "\n```")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func hasRule(findings []Finding, rule string) bool {
	for _, f := range findings {
		if f.Rule == rule {
			return true
		}
	}
	return false
}
