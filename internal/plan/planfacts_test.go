package plan

import (
	"strings"
	"testing"
)

// The fixtures below are excerpted from a REAL plan (the 2026-07-24
// conversation-model execution plan) — the shapes the detectors must catch in
// the wild: H3 track subsections with bold **Gate:** lines (not tables), a
// SHIPPED declaration in a status blockquote, a Risk|Mitigation table with no
// severity column, and a bead map. Synthetic minimal fixtures had let all of
// these regress to zero emissions.

const realTracksMD = `# Conversation model update — the execution plan

> **2026-07-25 status.** A1 + A2 + B* are SHIPPED on ` + "`ryan/some-branch`" + `
> (PR #2252): canonical Turn + published schema set.

## 3. The plan — five tracks, gated

Contract-lands-first discipline throughout.

### Track A — Converge the Turn *(the unanimous #1; blocks ingestion)*

**Goal:** one canonical turn record.

1. **A1 — the contract.** ` + "`packages/turns`" + ` the ruled schema. *Lands alone, first.*
2. **A2 — the provenance pass** (` + "`sageox-nrqu9`" + `, extended): the triple.
3. **A3 — encoders.** Each maps to/from the canonical record.
4. **A4 — turns-as-a-layer.** The canonical JSONL turn file.

**Gate:** A1 merged before A2; A2 before any ingest activity.

### Track B — The Layer folder + envelope upgrades

1. **B1 — folder-per-layer migration** (extends ` + "`sageox-6f288`" + `).
2. **B2 — envelope additions** (additive).
3. **B3 — hygiene:** CAS-on-update (` + "`sageox-228zl`" + `).

**Gate:** B1 is a format migration — it wants the folder decision recorded
and should land *before* the schema set publishes.

### Track C — Erasure's first writer *(both debate positions ranked it #2)*

Span-grain first: the escalation path (` + "`sageox-cd51f`" + `).

### Track D — Ingestion readiness (Scribe / Buzz)

Blocked on the ratification review (` + "`sageox-d7faf`" + ` routes the findings).

**Gate:** Track A2 lands first (provenance triple), else early corpus loses data.

## 6. Bead map

| Track | Beads |
|---|---|
| A | ` + "`sageox-nrqu9`" + ` (provenance), new: A1 contract |
| D | ` + "`sageox-d7faf`" + ` [HUMAN ratification], ` + "`sageox-ar85m`" + ` (signatures) |

## 8. Risks

| Risk | Mitigation |
|---|---|
| B1 migrates customer-committed files | additive sweep; never dual-write |
| A-track churn hits distillation | grammar untouched |
| Ratification stalls Track D | not started until ` + "`sageox-d7faf`" + ` clears — the [HUMAN] gate is real |
| Spec-vs-shipped gap widens | the moratorium shrinks the doc |
`

// TestParseTracks_H3ProseShape pins the prose-track detector against the real
// plan shape: H3 "Track X" subsections, numbered bold-ID items, bold gate
// lines, a prose-only track, and the SHIPPED declaration marking A1+A2+B*.
func TestParseTracks_H3ProseShape(t *testing.T) {
	in := Parse(realTracksMD)
	shipped := parseShippedSet(in.Raw)
	for _, tok := range []string{"A1", "A2", "B*"} {
		if !shipped[tok] {
			t.Errorf("shipped set missing %q", tok)
		}
	}
	if shipped["A3"] {
		t.Error("A3 must not be shipped")
	}

	lanes := parseTracks(in.Raw, shipped)
	if len(lanes) != 4 {
		t.Fatalf("expected 4 lanes, got %d: %+v", len(lanes), lanes)
	}
	a := lanes[0]
	if a.Name != "Track A" || len(a.Items) != 4 {
		t.Fatalf("Track A parse: %+v", a)
	}
	if !a.Items[0].Shipped || !a.Items[1].Shipped || a.Items[2].Shipped {
		t.Errorf("Track A shipped states wrong: %+v", a.Items)
	}
	if !strings.Contains(a.Gate, "A1 merged before A2") {
		t.Errorf("Track A gate = %q", a.Gate)
	}
	b := lanes[1]
	if !b.Shipped {
		t.Error("Track B (B*) must be whole-lane shipped")
	}
	for _, it := range b.Items {
		if !it.Shipped {
			t.Errorf("B* wildcard must ship item %s", it.ID)
		}
	}
	c := lanes[2]
	if len(c.Items) != 0 || c.Gate != "" || c.Shipped {
		t.Errorf("Track C must be a single pending prose lane: %+v", c)
	}
	d := lanes[3]
	if !strings.Contains(d.Gate, "Track A2 lands first") {
		t.Errorf("Track D gate = %q", d.Gate)
	}
	// gate text must be tooltip-clean: no markdown bold/code markers
	if strings.Contains(b.Gate, "*") || strings.Contains(b.Gate, "`") {
		t.Errorf("gate text carries markdown markers: %q", b.Gate)
	}
}

// TestParseShippedSet_IgnoresLowercaseProse guards the poison case: ordinary
// prose containing "shipped" ("spec-vs-shipped gap") must not mint tokens.
func TestParseShippedSet_IgnoresLowercaseProse(t *testing.T) {
	set := parseShippedSet("The spec-vs-shipped gap widens. B2 was shipped late.")
	if len(set) != 0 {
		t.Errorf("lowercase 'shipped' prose minted tokens: %v", set)
	}
}

// TestCountHolds_NearestBeadDeduped: a [HUMAN] marker attributes to the
// nearest PRECEDING bead on its line (not every bead on the line), and the
// same held bead cited twice is one hold.
func TestCountHolds_NearestBeadDeduped(t *testing.T) {
	if got := countHolds(realTracksMD); got != 1 {
		t.Errorf("holds = %d, want 1 (sageox-d7faf held twice, deduped)", got)
	}
	if got := countHolds("do the thing [HUMAN] with no bead"); got != 1 {
		t.Errorf("bare [HUMAN] line = %d, want 1", got)
	}
}

// TestDetectBeadNamespaces_Threshold: a prefix with >=3 distinct suffixes is a
// bead namespace; one-off hyphenated code spans are not.
func TestDetectBeadNamespaces_Threshold(t *testing.T) {
	re := detectBeadNamespaces(realTracksMD)
	if re == nil {
		t.Fatal("sageox-* namespace not detected")
	}
	if !re.MatchString("<code>sageox-nrqu9</code>") {
		t.Error("namespace regexp must match a rendered bead ref")
	}
	if re.MatchString("<code>git-file</code>") {
		t.Error("one-off `git-file` must not read as a bead")
	}
	if got := detectBeadNamespaces("uses `git-file` and `co-record` once"); got != nil {
		t.Errorf("below-threshold prefixes minted a namespace: %v", got)
	}
}

// TestBuildTrackSwim_Markup pins the swim figure: done/todo item states, the
// state legend in the figcaption, and the gate diamond with its tooltip.
func TestBuildTrackSwim_Markup(t *testing.T) {
	in := Parse(realTracksMD)
	lanes := parseTracks(in.Raw, parseShippedSet(in.Raw))
	s := buildTrackSwim(lanes)
	for _, want := range []string{
		`class="swim auto-swim track-swim"`,
		`■ shipped`, `□ pending`,
		`class="bar done"`, `class="bar todo"`,
		`title="gate: A1 merged before A2; A2 before any ingest activity."`,
		`>A1</span>`, `>Track C</span>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("track swim missing %q in:\n%s", want, s)
		}
	}
}

// TestRenderHTML_StructurePrimitives_RealPlanShapes is the end-to-end gate: the
// real-shaped markdown must emit the swimlane, the riskm severity table, bead
// chips, the structure-derived hero chips, and the jump bar with keyboard map
// and legend. This is exactly the render that used to come out primitive-free.
func TestRenderHTML_StructurePrimitives_RealPlanShapes(t *testing.T) {
	out, err := RenderHTML(Parse(realTracksMD), Result{})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		// swimlane above the tracks prose
		`class="swim auto-swim track-swim"`,
		`class="bar done"`,
		`class="bar todo"`,
		`class="gate"`,
		// risk matrix: inferred severity, glyph+word, blocker-first sort disclosed
		`<table class="riskm">`,
		`severity inferred from wording`,
		`class="sev-high"`,
		`▲ High`,
		// bead chips in the bead map
		`<span class="ox-chip bead"><code>sageox-nrqu9</code></span>`,
		`<span class="ox-chip bead hold">[HUMAN ratification]</span>`,
		`<table class="bead-map">`,
		// structure hero chips
		`<span class="hstat-v">4</span><span class="hstat-l">tracks</span>`,
		`<span class="hstat-v">3</span><span class="hstat-l">gates</span>`,
		`<span class="hstat-v">5</span><span class="hstat-l">shipped</span>`,
		`<span class="hstat-v">1</span><span class="hstat-l">human hold</span>`,
		`<span class="hstat-v">4</span><span class="hstat-l">risks</span>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("real-shaped render missing %q", want)
		}
	}
	// the risk rows must be sorted: first data row is a high (customer files),
	// the medium rows follow
	hi := strings.Index(s, `class="sev-high"`)
	med := strings.Index(s, `class="sev-medium"`)
	if hi < 0 || med < 0 || hi > med {
		t.Errorf("risk rows not sorted blocker-first: high@%d medium@%d", hi, med)
	}
}

// TestAutoRiskTable_ExplicitSeverityColumn: a table that ALREADY carries a
// severity column uses the authored values (no inference disclosure) and still
// sorts + glyphs them.
func TestAutoRiskTable_ExplicitSeverityColumn(t *testing.T) {
	md := "## Risks\n\n| Risk | Severity | Mitigation |\n|---|---|---|\n| minor thing | Low | shrug |\n| the big one | Critical | pray |\n"
	out, err := RenderHTML(Parse("# T\n\n"+md+"\n## Two\n\nx.\n"), Result{})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `class="sev-blocker"`) {
		t.Error("Critical must canonicalize to blocker")
	}
	if strings.Contains(s, "severity inferred") {
		t.Error("authored severity must not claim inference")
	}
	// blocker sorts above low
	if b, l := strings.Index(s, `class="sev-blocker"`), strings.Index(s, `class="sev-low"`); b > l {
		t.Errorf("blocker@%d must sort before low@%d", b, l)
	}
}

// TestHeaderCellRe_DoesNotMatchThead is the regression pin for the root-cause
// bug that broke every table detector on real goldmark output: `<th[^>]*>`
// also matched `<thead>` and swallowed markup up to the first real `</th>`.
func TestHeaderCellRe_DoesNotMatchThead(t *testing.T) {
	table := "<table>\n<thead>\n<tr>\n<th>Track</th>\n<th>Gate</th>\n</tr>\n</thead>\n<tbody></tbody></table>"
	got := headerCellRe.FindAllStringSubmatch(table, -1)
	if len(got) != 2 || stripTags(got[0][1]) != "Track" || stripTags(got[1][1]) != "Gate" {
		t.Fatalf("header cells parsed wrong: %+v", got)
	}
}
