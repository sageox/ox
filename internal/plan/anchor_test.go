package plan

import (
	"strings"
	"testing"
)

// TestAnchorFor_MatchesReviewJSEngine pins AnchorFor to goldens produced by
// running review.js's fnv1a/norm in a real JS engine (node). This is the wire
// format of every saved anchor: JS ^ yields a signed Int32 and the multiply is
// a rounded float64 product, so a "correct" integer FNV-1a diverges from what
// browsers actually computed. Failure prevented: the save-time remapper
// computes anchors the page never produced, so every rebind silently misses
// and updated plans orphan all feedback.
func TestAnchorFor_MatchesReviewJSEngine(t *testing.T) {
	cases := []struct {
		heading, text, want string
	}{
		{"Rollout Plan", "Ship the CLI first", "ha58cafdc"},
		{"", "hello world", "hbe8511f4"},
		{"Risks", "Feedback lost when the  server dies", "h45926978"}, // double space collapses
		{"Sécurité", "données perdues — état hors ligne", "h80cd4f5c"},
		{"", "", "h050c5d20"},
	}
	for _, c := range cases {
		if got := AnchorFor(c.heading, c.text); got != c.want {
			t.Errorf("AnchorFor(%q, %q) = %s, want %s (JS-engine golden)", c.heading, c.text, got, c.want)
		}
	}
	// normalization parity: case and whitespace shape must not change the anchor.
	if AnchorFor("Rollout Plan", "Ship the CLI first") != AnchorFor("  rollout   PLAN ", "\tship the CLI\nfirst ") {
		t.Error("anchor must be whitespace/case-insensitive like review.js norm()")
	}
}

// TestJSLabel_TruncatesAt70UTF16Units pins the label truncation to review.js
// labelFor (70 UTF-16 units, 69 + ellipsis). Failure prevented: exact-label
// remap matching never fires for long elements because the two sides truncate
// differently.
func TestJSLabel_TruncatesAt70UTF16Units(t *testing.T) {
	long := strings.Repeat("abcdefghij", 10) // 100 units
	got := jsLabel(long)
	if want := long[:69] + "…"; got != want {
		t.Errorf("jsLabel truncation = %q, want %q", got, want)
	}
	if jsLabel("short  text") != "short text" {
		t.Errorf("jsLabel must collapse whitespace, got %q", jsLabel("short  text"))
	}
}

// TestExtractReviewTargets_MatchesPageMarkableElements verifies the server-side
// extraction walks a real render and produces the exact anchors the page's
// review layer would compute for its markable elements. Failure prevented: the
// remapper scores against a different element set than the page shows, so
// rebinds land on anchors no browser will ever match.
func TestExtractReviewTargets_MatchesPageMarkableElements(t *testing.T) {
	md := "# T\n\n## Rollout Plan\n\n- Ship the CLI first\n- Then the daemon\n"
	html, err := RenderHTMLOpts(Parse(md), Result{}, RenderOptions{Slug: "t"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	targets, err := extractReviewTargets(html)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	byAnchor := map[string]reviewTarget{}
	for _, tg := range targets {
		byAnchor[tg.Anchor] = tg
	}
	// the li's anchor must equal what review.js computes: heading + li text.
	liAnchor := AnchorFor("Rollout Plan", "Ship the CLI first")
	tg, ok := byAnchor[liAnchor]
	if !ok {
		t.Fatalf("extraction missed the li anchor %s; got %d targets", liAnchor, len(targets))
	}
	if tg.Section != "Rollout Plan" {
		t.Errorf("li target section = %q, want the raw heading", tg.Section)
	}
	if tg.Label != "Ship the CLI first" {
		t.Errorf("li target label = %q", tg.Label)
	}
	// the section element itself is markable too (section[id] in the SELECTOR).
	found := false
	for _, x := range targets {
		if x.Section == "Rollout Plan" && strings.HasPrefix(x.Norm, "rollout plan") {
			found = true
			break
		}
	}
	if !found {
		t.Error("extraction must include the section element itself")
	}
}
