package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// authoredFixture is a miniature authored HTML plan in the comparison-page
// register: title, tabbed sections with h2s, a table, a script (interactive).
const authoredFixture = `<!doctype html>
<html><head><title>Turn model rollout</title>
<meta name="ox-plan-slug" content="turn model rollout">
</head><body>
<header><h1>Turn model rollout</h1><nav><button>00 Anatomy</button></nav></header>
<section class="view" data-ox-section="Anatomy"><h2>Anatomy</h2><p>One atom table, never two.</p>
<table><tr><th>Field</th><th>Ours</th></tr><tr><td>id</td><td>source_ref</td></tr></table></section>
<section class="view"><h2>Sequencing</h2><ul><li>provenance first</li></ul></section>
<script>const MAP={secret:1};</script>
</body></html>`

// TestSave_HTMLPrimaryRoundTrip is the html-primary save round-trip: the
// authored page is stored canonical (meta primary=html, slug from the
// authoring-contract meta tag), plan.md is the DERIVED markdown that Load —
// and therefore `ox plan view` / search — reads, and a re-save keeps Primary.
func TestSave_HTMLPrimaryRoundTrip(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)

	authored := []byte(authoredFixture)
	derived := ExtractMarkdown(authored)
	in := Parse(derived)
	meta := Meta{
		Topic:     "Turn model rollout",
		Slug:      AuthoredSlug(authored),
		Primary:   PrimaryHTML,
		CreatedAt: time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC),
	}

	dir, _, err := Save("/fake/git/root", in, Result{}, authored, meta)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if base := filepath.Base(dir); base != "2026-07-25-turn-model-rollout" {
		t.Errorf("dir = %q — slug should come from the ox-plan-slug meta tag", base)
	}

	got, merr := LoadMeta(dir)
	if merr != nil {
		t.Fatalf("LoadMeta: %v", merr)
	}
	if got.Primary != PrimaryHTML {
		t.Errorf("meta.Primary = %q, want %q", got.Primary, PrimaryHTML)
	}

	// the terminal/search surface reads the DERIVED markdown
	md, _, _, lerr := Load("/fake/git/root", "turn-model-rollout")
	if lerr != nil {
		t.Fatalf("Load: %v", lerr)
	}
	for _, want := range []string{"# Turn model rollout", "## Anatomy", "## Sequencing", "- provenance first", "| Field | Ours |"} {
		if !strings.Contains(md, want) {
			t.Errorf("derived plan.md missing %q", want)
		}
	}
	if strings.Contains(md, "const MAP") {
		t.Error("script content leaked into the derived markdown")
	}

	// the canonical artifact is the authored page (plus the sanctioned <head>
	// identity stamp), not a generated render
	htmlBytes, rerr := os.ReadFile(filepath.Join(dir, "plan.html"))
	if rerr != nil {
		t.Fatalf("read stored plan.html: %v", rerr)
	}
	if !strings.Contains(string(htmlBytes), "const MAP={secret:1};") {
		t.Error("authored interactivity lost from the stored canonical page")
	}
	if !strings.Contains(string(htmlBytes), `name="sageox:plan"`) {
		t.Error("stored page missing the self-identifying head stamp")
	}

	// a markdown-shaped re-save (hook draft) must not demote Primary
	if _, _, err := Save("/fake/git/root", in, Result{}, nil, Meta{Topic: "Turn model rollout", Slug: "turn-model-rollout", CreatedAt: meta.CreatedAt}); err != nil {
		t.Fatalf("re-Save: %v", err)
	}
	got2, _ := LoadMeta(dir)
	if got2.Primary != PrimaryHTML {
		t.Errorf("re-save demoted Primary to %q", got2.Primary)
	}
}

// TestInjectChrome_OnAuthoredFixture ties injection to the authored register:
// the chrome bundle lands before </body>, the authored markup is untouched,
// and the review layer + enrichment data ride along.
func TestInjectChrome_OnAuthoredFixture(t *testing.T) {
	res := Result{Annotations: []Annotation{{Kind: BadgeDeterministic, Type: BadgeCollision, Why: "teammate editing store.go"}}}
	out := InjectChrome([]byte(authoredFixture), BuildChromeData(res, RenderOptions{Slug: "turn-model-rollout"}))
	s := string(out)
	if !strings.HasPrefix(s, authoredFixture[:strings.Index(authoredFixture, "</body>")]) {
		t.Error("authored bytes before </body> were modified by injection")
	}
	if !strings.Contains(s, ChromeMarkerStart) || !strings.Contains(s, "teammate editing store.go") {
		t.Error("chrome bundle or enrichment signal missing")
	}
	if strings.Index(s, ChromeMarkerEnd) > strings.LastIndex(s, "</body>") {
		t.Error("chrome bundle not placed before </body>")
	}
}
