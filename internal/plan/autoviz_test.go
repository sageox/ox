package plan

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

// TestAutoSwimlane_GatedTracks verifies the structure-keyed emission: a table
// with a Track column and a Gate column renders a .swim swimlane above the
// table (lanes in row order, ◆ gate markers carrying the gate text), and the
// table itself survives as the detail record. Failure prevented: a
// five-gated-track plan rendering as prose while ~700 lines of viz CSS ship
// unused.
func TestAutoSwimlane_GatedTracks(t *testing.T) {
	raw := strings.Join([]string{
		"# P", "",
		"## Sequencing", "",
		"| Track | Work | Gate |", "|---|---|---|",
		"| A. Turn provenance | schema | none |", // "none" still text — marker rendered with title
		"| B. Folder layout | readers | A ships |",
		"| C. Access seam | commit plan | B green |", "",
	}, "\n")
	out, err := RenderHTML(Parse(raw), Result{})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `class="swim auto-swim"`) {
		t.Fatal("gated-track table did not emit a swimlane")
	}
	if got := strings.Count(s, `class="lane-name"`); got != 3 {
		t.Errorf("expected 3 swimlane lanes, got %d", got)
	}
	if !strings.Contains(s, `title="gate: B green"`) {
		t.Error("gate marker missing its gate-condition tooltip")
	}
	if !strings.Contains(s, "<table>") {
		t.Error("the source table must survive below the swimlane")
	}
}

// TestAutoSwimlane_RequiresGateColumn pins the precision gate: a plain
// tracks table (no gate-ish header) must NOT sprout a swimlane.
func TestAutoSwimlane_RequiresGateColumn(t *testing.T) {
	raw := "# P\n\n## Sequencing\n\n| Track | Work |\n|---|---|\n| A | x |\n| B | y |\n"
	out, err := RenderHTML(Parse(raw), Result{})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	if strings.Contains(string(out), `class="swim auto-swim"`) {
		t.Error("swimlane emitted without a gate column")
	}
}

// TestAutoInspector_ComparisonTable verifies the click-to-inspect upgrade: a
// >=3-column table under a comparison heading gains class "inspect" and a
// docked explainer; the same table under a neutral heading stays plain.
func TestAutoInspector_ComparisonTable(t *testing.T) {
	table := "| Field | Ours | Theirs |\n|---|---|---|\n| id | source_ref | sha256 |\n| author | author_id | pubkey |\n| time | start_utc | created_at |\n"
	comparing := "# P\n\n## Format comparison\n\n" + table
	out, err := RenderHTML(Parse(comparing), Result{})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `<table class="inspect">`) {
		t.Error("comparison table did not gain the inspect class")
	}
	if !strings.Contains(s, `class="inspect-dock"`) {
		t.Error("comparison table missing its docked explainer")
	}

	neutral := "# P\n\n## Storage\n\n" + table
	out2, err := RenderHTML(Parse(neutral), Result{})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	if strings.Contains(string(out2), `class="inspect"`) {
		t.Error("non-comparison section table wrongly upgraded to inspector")
	}
}

// TestRenderHTML_TLDRAlwaysLede pins the mandatory hero: a plan with NO
// explicit TL;DR marker still opens on a plain-language lede — the preamble's
// first paragraph LIFTED (moved, not copied) into the .tldr hero.
func TestRenderHTML_TLDRAlwaysLede(t *testing.T) {
	lede := "Ship the conversation model this week so multi-mic capture lands."
	raw := "# P\n\n" + lede + "\n\nSecond paragraph stays put.\n\n## One\n\na\n"
	out, err := RenderHTML(Parse(raw), Result{})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `class="tldr"`) {
		t.Fatal("hero missing on a plan without an explicit TL;DR")
	}
	if got := strings.Count(s, lede); got != 1 {
		t.Errorf("lede must appear exactly once (lifted, not copied): %d", got)
	}
	tldrIdx := strings.Index(s, `class="tldr"`)
	if strings.Index(s, lede) < tldrIdx {
		t.Error("lede text appears before the hero — not lifted into it")
	}
	if !strings.Contains(s, "Second paragraph stays put.") {
		t.Error("non-lede preamble content lost")
	}
}

// TestRenderHTML_TLDRSkipsBannerLiftsSection pins the guard: a preamble that
// OPENS with a blockquote (a status banner) is not a lede — the hero lifts the
// first section's opening paragraph instead, and the banner stays intact.
func TestRenderHTML_TLDRSkipsBannerLiftsSection(t *testing.T) {
	raw := "# P\n\n> **Status.** shipped on branch X.\n\n## Context\n\nThe model must serve every capture surface.\n\nMore detail.\n"
	out, err := RenderHTML(Parse(raw), Result{})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `class="tldr"`) {
		t.Fatal("hero missing")
	}
	if strings.Count(s, "The model must serve every capture surface.") != 1 {
		t.Error("section lede not lifted exactly once")
	}
	if !strings.Contains(s, "shipped on branch X.") {
		t.Error("status banner blockquote must survive untouched")
	}
}

// TestRenderHTML_ContextStripAndJudgment verifies the enrichment plumbing the
// judge flagged as unshipped: retrieved context items surface as the
// alignment strip, and agent-authored judgment badges (aligns/conflicts)
// render as chips — while rigor stays excluded.
func TestRenderHTML_ContextStripAndJudgment(t *testing.T) {
	in := sampleInput()
	res := Result{
		Annotations: []Annotation{
			{Kind: BadgeJudgment, Type: BadgeAligns, Why: "matches ADR-016 retention rule", SourceURL: "docs/adr/016.md", Files: []string{"store.go"}},
			{Kind: BadgeJudgment, Type: BadgeConflicts, Why: "clashes with ADR-024 tiering", SourceURL: "docs/adr/024.md"},
			{Kind: BadgeJudgment, Type: BadgeRigor, Why: "thoughtful-collab"},
		},
		Context: []ContextItem{
			{Kind: "adr", Title: "ADR-016 flags", Score: 0.9},
			{Kind: "session", Title: "2026-05-12 render rework", Score: 0.8},
		},
	}
	out, err := RenderHTML(in, res)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		`class="ox-ctx"`, "ADR-016 flags", "2026-05-12 render rework",
		"ox-sig-aligns", "matches ADR-016 retention rule",
		"ox-sig-conflicts", "clashes with ADR-024 tiering",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("enrichment layer missing %q", want)
		}
	}
	if strings.Contains(s, "thoughtful-collab") {
		t.Error("rigor judgment leaked into the render")
	}
}

// TestRenderHTML_QuietZeroSignalLine pins the silence-vs-never-ran rule: an
// un-enriched render does not claim enrichment ran, so a missing rail stays
// readable as "no enrichment input" rather than "checked, clean".
func TestRenderHTML_QuietZeroSignalLine(t *testing.T) {
	out, err := RenderHTML(sampleInput(), Result{})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	if strings.Contains(string(out), "no collisions or prior art surfaced") {
		t.Error("zero-signal un-enriched render claimed enrichment ran")
	}
}

// --- WCAG contrast gate over the corrected tokens (perceptual-ux spec) ---

func relLum(hex string) float64 {
	c := func(i int) float64 {
		v, _ := strconv.ParseInt(hex[i:i+2], 16, 32)
		f := float64(v) / 255
		if f <= 0.03928 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	return 0.2126*c(1) + 0.7152*c(3) + 0.0722*c(5)
}

func contrast(a, b string) float64 {
	la, lb := relLum(a), relLum(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// TestContrastTokens is the acceptance gate from the perceptual-ux review:
// corrected text tokens clear WCAG AA (4.5:1) on the surfaces they sit on —
// including the chrome's own near-opaque surfaces on white AND #0b0d0b hosts.
func TestContrastTokens(t *testing.T) {
	cases := []struct{ name, fg, bg string }{
		{"dark faint on panel2 (BUG1 fix)", "#7b857a", "#171b17"},
		{"dark faint on canvas", "#7b857a", "#0b0d0b"},
		{"light amber on canvas (BUG2 fix)", "#9a620a", "#eef2ee"},
		{"light copper on canvas (BUG3 fix)", "#9f5c38", "#eef2ee"},
		{"light violet on canvas", "#755acb", "#eef2ee"},
		{"chrome ink on chrome dark surface", "#e8ede7", "#111411"},
		{"chrome light ink on chrome light surface", "#16201c", "#ffffff"},
	}
	for _, tc := range cases {
		if got := contrast(tc.fg, tc.bg); got < 4.5 {
			t.Errorf("%s: contrast %.2f < 4.5", tc.name, got)
		}
	}
}
