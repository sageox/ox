package decision

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
)

// monoADR is a sageox-mono-template fixture: header metadata lines, numbered
// Decision subheads, an explicit D-anchor, a dated amendment, outbound refs.
const monoADR = `# ADR-047: Customer-Facing Env Var Namespace

**Status**: Accepted — merged via PR #1200
**Date**: 2026-03-14
**Decision Makers**: Person A, Person B

## Context

The namespace for customer-facing environment variables drifted across
subsystems, so operators could not predict which prefix a knob would use.

## Decision

### 1. SAGEOX_ prefix is canonical

All customer-facing vars use it.

### D4: OX_ is internal-only

Per ADR-046 D9 and adr 12, internal tooling keeps OX_.

**Amendment (2026-05-01):** additive clarification, decision unchanged.

## Consequences

Superseded by ADR-030 for the daemon subset.
`

func writeCorpus(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func defaultCorpusFiles() map[string]string {
	return map[string]string{
		"docs/adr/ADR-047-env-var-namespace.md": monoADR,
		"docs/adr/ADR-021-plan-context.md":      "# ADR-021: Plan Context Not Inference\n\n**Status**: Accepted\n**Date**: 2026-06-03\n\n## Context\n\nox provides context for plan enrichment, the client does inference.\n\n## Decision\n\n### 1. No LLM calls\n\nDeterministic only.\n",
		"docs/adr/002-daemon-architecture.md":   "# Daemon Architecture\n\n**Status**: Accepted\n**Date**: 2025-11-01\n\n## Context\n\nDaemon owns pulls.\n",
		"docs/adr/ADR-002-unix-socket.md":       "# ADR-002: Unix Socket IPC\n\n**Status**: Accepted\n**Date**: 2025-12-01\n\n## Context\n\nIPC over unix sockets.\n",
		"docs/adr/README.md":                    "# Index\n\n| ADR | Title |\n",
		"docs/adr/notes.md":                     "# Notes\n\nJust some markdown with no decision shape.\n",
	}
}

func TestParseContent_MonoTemplate(t *testing.T) {
	rec := ParseContent("docs/adr/ADR-047-env-var-namespace.md", monoADR)

	if rec.ID != "ADR-047" || rec.Prefix != "ADR" || rec.Number != 47 {
		t.Fatalf("id parse: got %q %q %d", rec.ID, rec.Prefix, rec.Number)
	}
	if rec.Title != "Customer-Facing Env Var Namespace" {
		t.Errorf("title: %q", rec.Title)
	}
	if !strings.HasPrefix(rec.Status, "Accepted") {
		t.Errorf("status: %q", rec.Status)
	}
	if rec.Date != "2026-03-14" {
		t.Errorf("date: %q", rec.Date)
	}
	if len(rec.Deciders) != 2 || rec.Deciders[0] != "Person A" {
		t.Errorf("deciders: %v", rec.Deciders)
	}
	anchorIDs := map[string]bool{}
	for _, d := range rec.DSections {
		anchorIDs[d.ID] = true
	}
	if !anchorIDs["D1"] || !anchorIDs["D4"] {
		t.Errorf("anchors: %v", rec.DSections)
	}
	if len(rec.Amendments) != 1 || rec.Amendments[0].Date != "2026-05-01" {
		t.Errorf("amendments: %v", rec.Amendments)
	}
	wantRefs := map[string]bool{"ADR-046": true, "ADR-012": true, "ADR-030": true}
	for _, r := range rec.Refs {
		if !wantRefs[r] {
			t.Errorf("unexpected ref %q", r)
		}
		delete(wantRefs, r)
	}
	for missing := range wantRefs {
		t.Errorf("missing ref %q", missing)
	}
	if rec.SupersededBy != "ADR-030" {
		t.Errorf("superseded_by: %q", rec.SupersededBy)
	}
	if rec.DRType != "adr" {
		t.Errorf("dr_type: %q", rec.DRType)
	}
	if !strings.Contains(rec.Excerpt, "namespace") {
		t.Errorf("excerpt: %q", rec.Excerpt)
	}
	if !rec.IsRecord() {
		t.Error("IsRecord = false")
	}
}

func TestParseContent_Variants(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		body     string
		wantID   string
		wantType string
		isRecord bool
	}{
		{"numeric-only filename", "002-daemon.md", "# Daemon\n\n**Status**: Accepted\n", "ADR-002", "adr", true},
		{"ddr prefix", "DDR-003-color-tokens.md", "# DDR-003: Color Tokens\n\n**Status**: Proposed\n", "DDR-003", "ddr", true},
		{"unnumbered with status", "adr-ephemeral-mode.md", "# Ephemeral Mode\n\n**Status**: Accepted\n", "", "adr", true},
		{"plain markdown", "notes.md", "# Notes\n\nnothing decision-shaped here\n", "", "other", false},
		{"stdin draft", "", "# ADR-099: Draft Thing\n\n**Status**: Proposed\n", "ADR-099", "adr", true},
		{"frontmatter title wins", "ADR-005-x.md", "---\ntitle: Frontmatter Title\nstatus: Accepted\n---\n# ADR-005: H1 Title\n", "ADR-005", "adr", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := ParseContent(tt.path, tt.body)
			if rec.ID != tt.wantID {
				t.Errorf("id: got %q want %q", rec.ID, tt.wantID)
			}
			if rec.DRType != tt.wantType {
				t.Errorf("dr_type: got %q want %q", rec.DRType, tt.wantType)
			}
			if rec.IsRecord() != tt.isRecord {
				t.Errorf("IsRecord: got %v want %v", rec.IsRecord(), tt.isRecord)
			}
			if tt.name == "frontmatter title wins" && rec.Title != "Frontmatter Title" {
				t.Errorf("title precedence: %q", rec.Title)
			}
		})
	}
}

func TestLikelyDecisionRecord(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
		want bool
	}{
		{name: "descriptive malformed decision", path: "deployment-strategy.md", body: "# Deployment Strategy\n", want: true},
		{name: "README support file", path: "README.md", body: "# ADR-000: Decision Index\n", want: false},
		{name: "notes support file", path: "notes.md", body: "# Notes\n", want: false},
		{name: "template support file", path: "TEMPLATE.md", body: "# ADR-000: Title\n", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := likelyDecisionRecord(tc.path, tc.body); got != tc.want {
				t.Fatalf("likelyDecisionRecord(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestLoadCorpus(t *testing.T) {
	root := t.TempDir()
	writeCorpus(t, root, defaultCorpusFiles())

	corpus := LoadCorpus(root, nil)
	if len(corpus) != 4 {
		t.Fatalf("corpus size: got %d want 4 (README + non-DR excluded): %+v", len(corpus), corpus)
	}
	// sorted by number: 2, 2, 21, 47
	if corpus[0].Number != 2 || corpus[3].Number != 47 {
		t.Errorf("sort order: %d..%d", corpus[0].Number, corpus[3].Number)
	}
	for _, r := range corpus {
		if r.Corpus != "repo" || r.RelPath == "" {
			t.Errorf("record metadata: %+v", r)
		}
	}
}

func TestLoadCorpus_ConfiguredGlob(t *testing.T) {
	root := t.TempDir()
	writeCorpus(t, root, map[string]string{
		"eng/decisions/ADR-001-a.md": "# ADR-001: A\n\n**Status**: Accepted\n",
		"eng/other/skip.md":          "# ADR-002: B\n\n**Status**: Accepted\n",
	})
	cfg := &config.DecisionConfig{Paths: []string{"eng/decisions/**/*.md"}}
	corpus := LoadCorpus(root, cfg)
	if len(corpus) != 1 || corpus[0].ID != "ADR-001" {
		t.Fatalf("glob corpus: %+v", corpus)
	}
}

func TestCorpusDetected(t *testing.T) {
	empty := t.TempDir()
	if CorpusDetected(empty) {
		t.Error("detected corpus in empty repo")
	}
	withDir := t.TempDir()
	writeCorpus(t, withDir, map[string]string{"docs/adr/ADR-001-x.md": "# ADR-001: X\n"})
	if !CorpusDetected(withDir) {
		t.Error("default-dir corpus not detected")
	}
	if CorpusDetected("") {
		t.Error("detected corpus for empty root")
	}
}

func TestInvalidConfiguredPathsIgnored(t *testing.T) {
	root := t.TempDir()
	cfg := &config.DecisionConfig{Paths: []string{"../outside/**/*.md"}}
	if got := LoadCorpus(root, cfg); len(got) != 0 {
		t.Fatalf("invalid configured path should not load corpus: %+v", got)
	}
	if got := SearchPathPatterns(root); len(got) != 0 {
		t.Fatalf("empty repo search patterns: %+v", got)
	}

	cfgDir := filepath.Join(root, ".sageox")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{
  "config_version": "2",
  "update_frequency_hours": 24,
  "decision": {"paths": ["../outside/**/*.md"]}
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if CorpusDetected(root) {
		t.Error("invalid configured path should not count as a detected corpus")
	}
	if PathMatcher(root)("outside/ADR-001-x.md") {
		t.Error("invalid configured path should not match code-search results")
	}
	if got := SearchPathPatterns(root); len(got) != 0 {
		t.Fatalf("invalid configured path should not produce search patterns: %+v", got)
	}
}

// PathMatcher powers the `ox code search --decisions` filter and the
// doc_type:"decision" result tag — a wrong match either hides DRs from the
// filter or mislabels code as a decision.
func TestPathMatcher(t *testing.T) {
	root := t.TempDir()
	writeCorpus(t, root, map[string]string{
		"docs/adr/ADR-001-x.md": "# ADR-001: X\n**Status**: Accepted\n",
	})
	m := PathMatcher(root)

	tests := []struct {
		rel  string
		want bool
	}{
		{"docs/adr/ADR-001-x.md", true},
		{"docs/adr/nested/deep.md", true},  // dir match is prefix-recursive
		{"docs/adr/notes.txt", false},      // not markdown
		{"docs/other/ADR-002-y.md", false}, // outside the corpus
		{"internal/decision/parse.go", false},
	}
	for _, tt := range tests {
		if got := m(tt.rel); got != tt.want {
			t.Errorf("match(%q) = %v want %v", tt.rel, got, tt.want)
		}
	}

	// no corpus → always-false predicate, never nil
	if PathMatcher(t.TempDir())("docs/adr/x.md") {
		t.Error("empty corpus should match nothing")
	}
	if PathMatcher("")("docs/adr/x.md") {
		t.Error("empty root should match nothing")
	}
}

func TestSearchPathPatterns(t *testing.T) {
	root := t.TempDir()
	writeCorpus(t, root, map[string]string{
		"docs/adr/ADR-001-x.md": "# ADR-001: X\n**Status**: Accepted\n",
	})
	got := SearchPathPatterns(root)
	if len(got) != 1 || got[0] != "docs/adr/*.md" {
		t.Fatalf("default dir search patterns: %v", got)
	}
}

func TestScoreCorpus(t *testing.T) {
	corpus := []Record{
		{ID: "ADR-001", Number: 1, Title: "Unix socket IPC transport", Date: "2026-01-01", Excerpt: "sockets everywhere"},
		{ID: "ADR-002", Number: 2, Title: "Unrelated thing", Date: "2026-01-02", Excerpt: "socket mentioned in excerpt only, transport too"},
	}

	t.Run("exact id short-circuits", func(t *testing.T) {
		for _, q := range []string{"ADR-001", "adr 1", "001", "ADR-001 with appended rationale"} {
			got := scoreCorpus(corpus, q)
			if len(got) == 0 || got[0].rec.ID != "ADR-001" || got[0].score != 1.0 {
				t.Errorf("query %q: %+v", q, got)
			}
		}
	})

	t.Run("title beats excerpt", func(t *testing.T) {
		got := scoreCorpus(corpus, "socket transport")
		if len(got) < 2 {
			t.Fatalf("want both records matched: %+v", got)
		}
		if got[0].rec.ID != "ADR-001" {
			t.Errorf("title match should rank first: %+v", got)
		}
	})

	t.Run("weak excerpt-only match scores below the floor", func(t *testing.T) {
		// ADR-002 matches only in the excerpt ("socket"), so it must rank below
		// minDRScore — excluded by the floor, NOT zeroed by query length. The
		// distinction matters: a strong TITLE match must survive a long query
		// (see TestScoreCorpus_MonotonicInQueryLength), a weak excerpt one need not.
		got := scoreCorpus(corpus, "socket quantum blockchain kubernetes")
		found := false
		for _, s := range got {
			if s.rec.ID == "ADR-002" {
				found = true
				if s.score >= minDRScore {
					t.Errorf("excerpt-only match should score below floor %.2f: %+v", minDRScore, s)
				}
			}
		}
		if !found {
			t.Fatalf("ADR-002 must remain a scored below-floor candidate: %+v", got)
		}
	})
}

// TestScoreCorpus_MonotonicInQueryLength is the #823 regression: adding words to
// a query must NEVER drop a record that a shorter subset query matched. The old
// coverage=matched/len(terms) scorer zeroed a strong title match as ordinary
// prose lengthened, so a real plan/issue body found less than a two-word probe.
// Failure prevented: an agent passing a full plan silently loses the ADR that
// its two-word title would have surfaced.
func TestScoreCorpus_MonotonicInQueryLength(t *testing.T) {
	corpus := []Record{{
		ID: "ADR-002", Number: 2, Date: "2026-01-01",
		Title:   "Feature flags are added only at explicit user request",
		Excerpt: "gate rollout kill switch staged percentage",
	}}

	scoreOf := func(q string) float64 {
		for _, s := range scoreCorpus(corpus, q) {
			if s.rec.ID == "ADR-002" {
				return s.score
			}
		}
		return 0 // dropped entirely
	}

	// Same subject, growing query. Each is a superset of the record's own words
	// plus increasing off-topic prose — the normal case, not an edge case.
	short := "feature flags"
	title := "feature flags are added only at explicit user request"
	prose := "we want to gate the new todo digest emailer behind a feature flag " +
		"so we can stage the rollout by percentage and keep a kill switch in " +
		"case the new sender misbehaves in production"

	sShort, sTitle, sProse := scoreOf(short), scoreOf(title), scoreOf(prose)

	if sTitle < sShort {
		t.Errorf("exact-title query scored below its two-word subset: title=%.3f short=%.3f", sTitle, sShort)
	}
	if sProse < sShort {
		t.Errorf("#823: longer prose query dropped the record below the short query: short=%.3f prose=%.3f", sShort, sProse)
	}
	if sProse < minDRScore {
		t.Errorf("#823: record fell below the floor %.2f on ordinary prose input: %.3f", minDRScore, sProse)
	}
}

// TestRelatedDetector_MonotonicAcrossCompetingRecords guards the second half of
// the monotonicity contract: appended terms may add stronger-scoring records,
// but the bounded output must retain the shorter prefix query's seed match and
// report that additional candidates were omitted.
func TestRelatedDetector_MonotonicAcrossCompetingRecords(t *testing.T) {
	corpus := []Record{{ID: "ADR-002", Title: "Unix Domain Socket IPC"}}
	for i, title := range []string{
		"Session Adapter Context",
		"Plan Integration Lifecycle",
		"Security Adapter Distribution",
		"Context Session Lifecycle",
		"Integration Security Plan",
		"Adapter Lifecycle Context",
	} {
		corpus = append(corpus, Record{ID: fmt.Sprintf("ADR-%03d", i+3), Title: title})
	}

	detector := relatedDetector{}
	containsTarget := func(topic string) (found bool, related, overflow int) {
		anns, err := detector.Detect(context.Background(), &Env{Corpus: corpus}, Input{Topic: topic})
		if err != nil {
			t.Fatal(err)
		}
		for _, ann := range anns {
			if ann.Ref == "ADR-002" {
				found = true
			}
			if ann.Type == BadgeRelatedDecision {
				related++
			}
			if ann.Rule == RuleRelatedOverflow {
				overflow++
			}
		}
		return found, related, overflow
	}

	if found, _, _ := containsTarget("socket"); !found {
		t.Fatal("short query must surface ADR-002")
	}
	long := "socket session adapter context plan integration lifecycle security distribution"
	if found, related, overflow := containsTarget(long); !found {
		t.Fatalf("adding competing terms evicted ADR-002 from %d related matches", related)
	} else if related != relatedCap {
		t.Fatalf("related results must stay capped at %d, got %d", relatedCap, related)
	} else if overflow != 1 {
		t.Fatalf("bounded results must report overflow once, got %d", overflow)
	}
}

func TestResolveInput(t *testing.T) {
	t.Run("topic wins", func(t *testing.T) {
		in, err := ResolveInput("my topic", "/nonexistent", strings.NewReader("ignored"))
		if err != nil || in.Topic != "my topic" || in.Raw != "" {
			t.Fatalf("in=%+v err=%v", in, err)
		}
		if in.Terms() != "my topic" {
			t.Errorf("terms: %q", in.Terms())
		}
	})
	t.Run("file mode", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "ADR-009-x.md")
		if err := os.WriteFile(p, []byte("# ADR-009: File Mode\n\n**Status**: Accepted\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		in, err := ResolveInput("", p, nil)
		if err != nil || in.Path != p || in.Record.ID != "ADR-009" {
			t.Fatalf("in=%+v err=%v", in, err)
		}
	})
	t.Run("stdin draft", func(t *testing.T) {
		in, err := ResolveInput("", "", strings.NewReader("# ADR-011: Stdin Draft\n"))
		if err != nil || in.Record.ID != "ADR-011" {
			t.Fatalf("in=%+v err=%v", in, err)
		}
	})
	t.Run("empty stdin", func(t *testing.T) {
		in, err := ResolveInput("", "", strings.NewReader("  \n"))
		if err != nil || in.Raw != "" || in.Topic != "" {
			t.Fatalf("in=%+v err=%v", in, err)
		}
	})
}

func TestSourceRefsAndCredits(t *testing.T) {
	body := `# ADR-050: X

Per Person A's discussion, surfaced by SageOx.
<!-- SOURCE: sageox discussion:2026-05-28-1423-a#ch-2 -->
<!-- SOURCE: sageox adr:docs/adr/ADR-001-x.md#D5 -->
Guided by SageOx.
<!-- this comment says surfaced by SageOx but is invisible -->
`
	in := Input{Raw: body}
	refs := in.SourceRefs()
	if len(refs) != 2 || refs[0] != "discussion:2026-05-28-1423-a#ch-2" || refs[1] != "adr:docs/adr/ADR-001-x.md#D5" {
		t.Errorf("refs: %v", refs)
	}
	// two visible credits; the one inside an HTML comment must not count
	if n := in.VisibleSageoxCredits(); n != 2 {
		t.Errorf("visible credits: got %d want 2", n)
	}
}

// panicDetector / stubDetector exercise the fail-open orchestrator. The
// swapRegistry replaces the global detector/retriever registry for one test and
// restores it on cleanup. The registry is package-global and additive, so a test
// that leaves a fault-injection detector registered would silently degrade every
// later test's Enrich — masking whether the degraded/annotation logic under test
// actually fired. Isolating the registry keeps each test proving its own cause.
func swapRegistry(t *testing.T, ds []Detector, rs []Retriever) {
	t.Helper()
	registryMu.Lock()
	savedD, savedR := detectors, retrievers
	detectors, retrievers = ds, rs
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		detectors, retrievers = savedD, savedR
		registryMu.Unlock()
	})
}

type panicDetector struct{}

func (panicDetector) Name() string { return "test-panic" }
func (panicDetector) Detect(context.Context, *Env, Input) ([]Annotation, error) {
	panic("boom")
}

type stubDetector struct{}

func (stubDetector) Name() string { return "test-stub" }
func (stubDetector) Detect(context.Context, *Env, Input) ([]Annotation, error) {
	return []Annotation{{Kind: BadgeDeterministic, Type: BadgeDiagnostic, Rule: "test-stub", Why: "stub fired"}}, nil
}

func TestEnrich_FailOpenAndSchema(t *testing.T) {
	swapRegistry(t, []Detector{panicDetector{}, stubDetector{}}, nil)

	res := Enrich(context.Background(), Input{Topic: "anything at all"}, "")
	if res.SchemaVersion != SchemaVersion {
		t.Errorf("schema: %q", res.SchemaVersion)
	}
	found := false
	for _, a := range res.Annotations {
		if a.Rule == "test-stub" {
			found = true
		}
	}
	if !found {
		t.Error("stub detector output lost — panic in sibling detector aborted the run")
	}
	// a panicking detector is a failed source: the run must be marked degraded
	// so its emptiness is never read as a verified absence (#823).
	if !res.Signals.Degraded {
		t.Error("a panicking detector must mark the result degraded")
	}
}

func TestEnrich_OnRealTempCorpus(t *testing.T) {
	root := t.TempDir()
	writeCorpus(t, root, defaultCorpusFiles())

	res := Enrich(context.Background(), Input{Topic: "plan context inference"}, root)

	if res.Decision.SuggestedID != "ADR-048" {
		t.Errorf("suggested id: %q (next after 47)", res.Decision.SuggestedID)
	}
	if res.Conventions.NextNumber != 48 {
		t.Errorf("next number: %d", res.Conventions.NextNumber)
	}
	if len(res.Conventions.NumberCollisions) != 1 || res.Conventions.NumberCollisions[0] != "002" {
		t.Errorf("collisions: %v", res.Conventions.NumberCollisions)
	}
	var related *Annotation
	for i, a := range res.Annotations {
		if a.Type == BadgeRelatedDecision && a.Ref == "ADR-021" {
			related = &res.Annotations[i]
		}
	}
	if related == nil {
		t.Fatalf("ADR-021 not surfaced as related: %+v", res.Annotations)
	}
	if related.Relation != RelationCandidate && related.Relation != VariantSupersedeCandidate {
		t.Errorf("relation: %q", related.Relation)
	}
	if res.Signals.Degraded {
		t.Errorf("known support markdown must not degrade a readable corpus: %+v", res.Annotations)
	}
	if !res.Signals.Material {
		t.Error("material should be true with a related decision")
	}
	if res.Signals.Degraded {
		t.Error("ordinary README/notes files must not degrade an otherwise readable corpus")
	}
	if res.Guidance == "" || !strings.Contains(res.Guidance, "ox code search") {
		t.Errorf("guidance: %q", res.Guidance)
	}
	// context items for decisions must carry paste-ready cites
	for _, c := range res.Context {
		if c.Kind == "decision" && (c.Cite == nil || !strings.Contains(c.Cite.Comment, "SOURCE: sageox adr:")) {
			t.Errorf("decision item without cite: %+v", c)
		}
		if c.Kind == "murmur" && c.Cite != nil {
			t.Errorf("murmur must not carry a cite: %+v", c)
		}
	}
}

func TestRefsDetector(t *testing.T) {
	root := t.TempDir()
	writeCorpus(t, root, defaultCorpusFiles())
	env := &Env{GitRoot: root, Corpus: LoadCorpus(root, nil)}

	body := `# ADR-090: Draft

Per ADR-021 this is fine. But ADR-999 is phantom, and ADR-047 D9 names a
missing anchor (ADR-047 defines D1 and D4).
<!-- SOURCE: sageox adr:docs/adr/ADR-021-plan-context.md -->
<!-- SOURCE: sageox adr:docs/adr/nope.md -->
surfaced by SageOx · surfaced by SageOx · surfaced by SageOx
`
	in := Input{Raw: body, Record: ParseContent("draft.md", body)}
	anns, err := refsDetector{}.Detect(context.Background(), env, in)
	if err != nil {
		t.Fatal(err)
	}

	rules := map[string]int{}
	var whys []string
	for _, a := range anns {
		rules[a.Rule]++
		whys = append(whys, a.Ref+": "+a.Why)
	}
	if rules[RuleDanglingRef] != 3 {
		t.Errorf("want 3 dangling (ADR-999, D9 anchor, bad SOURCE path), got %d: %v", rules[RuleDanglingRef], whys)
	}
	if rules[RuleSageoxCreditOverflow] != 1 {
		t.Errorf("credit overflow not flagged: %v", whys)
	}
	joined := strings.Join(whys, "\n")
	if !strings.Contains(joined, "D9") || !strings.Contains(joined, "D1") {
		t.Errorf("anchor diagnostics should name missing + available anchors: %s", joined)
	}
	// valid refs (ADR-021 token + its SOURCE line) must produce no annotations
	if strings.Contains(joined, "ADR-021:") {
		t.Errorf("valid ref flagged: %s", joined)
	}
}

func TestConventionsDetector_TakenNumber(t *testing.T) {
	root := t.TempDir()
	writeCorpus(t, root, defaultCorpusFiles())
	env := &Env{GitRoot: root, Corpus: LoadCorpus(root, nil)}

	body := "# ADR-021: Colliding Draft\n\n## Context\n\nx\n"
	in := Input{Raw: body, Record: ParseContent("draft.md", body)}
	anns, err := conventionsDetector{}.Detect(context.Background(), env, in)
	if err != nil {
		t.Fatal(err)
	}
	taken := false
	for _, a := range anns {
		if a.Type == BadgeNumbering && a.Rule == RuleDuplicateNumber && strings.Contains(a.Why, "ADR-021-plan-context.md") {
			taken = true
		}
	}
	if !taken {
		t.Errorf("taken-number not flagged with holder path: %+v", anns)
	}
}

func TestNormalizeStatus(t *testing.T) {
	tests := map[string]string{
		"Accepted — merged via PR #621":  "Accepted",
		"Draft (Proposed) — awaiting":    "Draft",
		"Proposed":                       "Proposed",
		"  Accepted —  ":                 "Accepted",
		"Not adopted — deferred pending": "Not adopted",
	}
	for in, want := range tests {
		if got := normalizeStatus(in); got != want {
			t.Errorf("normalizeStatus(%q) = %q want %q", in, got, want)
		}
	}
}

func TestDriftDetector(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root                 // NEVER touch global git config
		cmd.Env = append(os.Environ(), // safe: git subprocess in a t.TempDir repo, identity via env only
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "commit.gpgsign", "false") // repo-local; host config may require a passphrase-protected key
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "main.go")
	run("commit", "-q", "-m", "one")
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main // changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "main.go")
	run("commit", "-q", "-m", "two")

	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	drBody := "# ADR-001: Uses Main\n\n**Status**: Accepted\n**Date**: " + yesterday + "\n\n## Context\n\nSee `main.go` for details.\n"
	drPath := filepath.Join(root, "docs", "adr", "ADR-001-x.md")
	if err := os.MkdirAll(filepath.Dir(drPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(drPath, []byte(drBody), 0o644); err != nil {
		t.Fatal(err)
	}

	in := Input{Path: drPath, Raw: drBody, Record: ParseContent(drPath, drBody)}
	anns, err := driftDetector{}.Detect(context.Background(), &Env{GitRoot: root}, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(anns) != 1 || anns[0].Type != BadgeDrift {
		t.Fatalf("drift annotations: %+v", anns)
	}
	if !strings.HasPrefix(anns[0].SourceURL, "commit:") || len(anns[0].Files) != 1 {
		t.Errorf("drift citation: %+v", anns[0])
	}

	// topic mode / missing date → fail-open nil
	if anns, _ := (driftDetector{}).Detect(context.Background(), &Env{GitRoot: root}, Input{Topic: "x"}); anns != nil {
		t.Errorf("topic mode should be nil: %+v", anns)
	}
}

func TestGuidanceBranches(t *testing.T) {
	conv := Conventions{NextNumber: 48, AmendmentMarker: "**Amendment (YYYY-MM-DD):**"}

	t.Run("zero context", func(t *testing.T) {
		g := buildGuidance(Input{Topic: "x"}, SignalSummary{}, conv, nil, nil)
		if !strings.Contains(g, "gap admitted beats a citation invented") {
			t.Errorf("zero-context lead missing: %q", g)
		}
	})
	t.Run("unresolved refs lead", func(t *testing.T) {
		g := buildGuidance(Input{Path: "a.md"}, SignalSummary{UnresolvedRefs: 2}, conv, nil, nil)
		if !strings.Contains(g, "do not resolve") {
			t.Errorf("unresolved lead missing: %q", g)
		}
	})
	t.Run("drift lead", func(t *testing.T) {
		anns := []Annotation{{Type: BadgeDrift}}
		g := buildGuidance(Input{Path: "a.md"}, SignalSummary{}, conv, anns, nil)
		if !strings.Contains(g, "drifted") {
			t.Errorf("drift lead missing: %q", g)
		}
	})
	t.Run("rich context + accepted amendment rule", func(t *testing.T) {
		body := "# ADR-001: X\n\n**Status**: Accepted\n"
		in := Input{Path: "a.md", Raw: body, Record: ParseContent("a.md", body)}
		items := []ContextItem{{Kind: "decision", Cite: &Cite{Comment: "<!-- SOURCE: sageox adr:x -->"}}}
		g := buildGuidance(in, SignalSummary{Related: 1}, conv, nil, items)
		if !strings.Contains(g, "team history") || !strings.Contains(g, "Amendment") || !strings.Contains(g, "VERBATIM") {
			t.Errorf("rich/update guidance incomplete: %q", g)
		}
	})
	t.Run("degraded suppresses the verified-absence claim", func(t *testing.T) {
		// #823: a swallowed source error must NOT be reported as a checked
		// absence. The verifiable-claim wording is reserved for a genuine,
		// fully-read empty result.
		g := buildGuidance(Input{Topic: "x"}, SignalSummary{Degraded: true}, conv, nil, nil)
		if strings.Contains(g, "verifiable claim") {
			t.Errorf("degraded run must not present absence as verified: %q", g)
		}
		if !strings.Contains(g, "DEGRADED") {
			t.Errorf("degraded run must warn the source was unreadable: %q", g)
		}
	})
	t.Run("degraded warning accompanies related history", func(t *testing.T) {
		g := buildGuidance(Input{Topic: "x"}, SignalSummary{Related: 1, PriorSessions: 2, Degraded: true}, conv, nil, nil)
		if !strings.Contains(g, "team history") || !strings.Contains(g, "DEGRADED") {
			t.Errorf("degraded rich result must include both history and incompleteness guidance: %q", g)
		}
		if strings.Contains(g, "verifiable claim") {
			t.Errorf("degraded rich result must not present any absence as verified: %q", g)
		}
	})
	// the credit cap is a standing rule in every branch
	g := buildGuidance(Input{Topic: "x"}, SignalSummary{}, conv, nil, nil)
	if !strings.Contains(g, "max 2 per DR") {
		t.Errorf("credit cap missing: %q", g)
	}
}

// --- #823: honesty (error-vs-empty) and --explain ---

// errDetector always errors — exercises the degraded-source path end to end.
type errDetector struct{}

func (errDetector) Name() string { return "test-err" }
func (errDetector) Detect(context.Context, *Env, Input) ([]Annotation, error) {
	return nil, fmt.Errorf("simulated source failure")
}

// TestEnrich_DegradedOnSourceError: a retrieval source that errors must mark the
// Result degraded and flip guidance away from asserting a verified absence.
// Failure prevented (#823): a swallowed index/lookup error is otherwise reported
// as "no prior decision found — a verifiable claim", so an agent drafts a
// duplicate/contradicting DR with false confidence. The clean-registry arm
// proves the flip is caused by the error, not by ambient pollution.
func TestEnrich_DegradedOnSourceError(t *testing.T) {
	in := Input{Topic: "gate rollout behind a flag"}

	swapRegistry(t, nil, nil) // no sources at all
	clean := Enrich(context.Background(), in, "")
	if clean.Signals.Degraded {
		t.Fatal("an all-clean run (no sources, no corpus) must NOT be degraded")
	}
	if !strings.Contains(clean.Guidance, "verifiable claim") {
		t.Errorf("a genuinely-empty run should still state absence as verifiable: %q", clean.Guidance)
	}

	swapRegistry(t, []Detector{errDetector{}}, nil) // exactly one erroring source
	res := Enrich(context.Background(), in, "")
	if !res.Signals.Degraded {
		t.Fatal("a source error must set Signals.Degraded")
	}
	if strings.Contains(res.Guidance, "verifiable claim") {
		t.Errorf("degraded result must not present absence as verified: %q", res.Guidance)
	}
	if !strings.Contains(res.Guidance, "DEGRADED") {
		t.Errorf("degraded result must warn: %q", res.Guidance)
	}
}

// TestMurmursRetriever_ReportsCorruptSource proves the production retriever,
// not only a synthetic test double, returns an error for corrupt local data so
// runRetriever can mark enrichment degraded.
func TestMurmursRetriever_ReportsCorruptSource(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	dir := filepath.Join(root, "data", "murmurs", now.Format("2006-01-02"), now.Format("15"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	valid := fmt.Sprintf(`{"id":"valid","timestamp":%q,"topic":"authentication","content":"authentication authentication authentication authentication"}`, now.Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(dir, "a-valid.json"), []byte(valid), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "z-broken.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	items, errored := runRetriever(context.Background(), murmursRetriever{}, &Env{LedgerPath: root}, Input{Topic: "authentication"})
	if !errored {
		t.Fatal("corrupt production murmur source must report an error")
	}
	if len(items) != 1 || items[0].Ref != "valid" {
		t.Fatalf("valid partial hits must survive a corrupt sibling: %+v", items)
	}
}

func TestSessionsRetriever_IgnoresCorruptMurmurSibling(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "current-session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "summary.md"), []byte("authentication authentication authentication authentication authentication"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	murmurDir := filepath.Join(root, "data", "murmurs", now.Format("2006-01-02"), now.Format("15"))
	if err := os.MkdirAll(murmurDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(murmurDir, "broken.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	items, err := sessionsRetriever{}.Retrieve(context.Background(), &Env{LedgerPath: root}, Input{Topic: "authentication"})
	if err != nil {
		t.Fatalf("durable retriever must not inherit murmur source errors: %v", err)
	}
	if len(items) != 1 || items[0].Kind != "session" {
		t.Fatalf("valid durable hit lost because a sibling murmur was corrupt: %+v", items)
	}
}

// TestEnrich_CorpusPresentButUnparsed: a decision dir full of markdown that does
// not parse as records is present-but-unreadable, NOT "no decisions". Enrich
// must flag it (diagnostic + degraded) instead of silently returning empty.
// Failure prevented (#823): a user's docs/adr whose files miss a number and a
// Status/Date line yields related=0 that reads as a verified absence.
// Empty registry: degraded here can ONLY come from the corpus, not a source error.
func TestEnrich_CorpusPresentButUnparsed(t *testing.T) {
	swapRegistry(t, nil, nil)
	root := t.TempDir()
	writeCorpus(t, root, map[string]string{
		"docs/adr/ADR-thoughts.md": "# Some Title\n\nProse with no status, no date, no number.\n",
		"docs/adr/DDR-more.md":     "# Another Thought\n\nAlso not a record.\n",
	})
	res := Enrich(context.Background(), Input{Topic: "some title"}, root)
	if !res.Signals.Degraded {
		t.Error("present-but-unparsed corpus must mark the result degraded")
	}
	found := false
	for _, a := range res.Annotations {
		if a.Rule == RuleUnreadableCorpus {
			found = true
		}
	}
	if !found {
		t.Errorf("unreadable-corpus diagnostic missing: %+v", res.Annotations)
	}
	if strings.Contains(res.Guidance, "verifiable claim") {
		t.Errorf("must not assert verified absence for an unreadable corpus: %q", res.Guidance)
	}
}

func TestEnrich_DescriptiveMalformedRecordDegraded(t *testing.T) {
	swapRegistry(t, nil, nil)
	root := t.TempDir()
	writeCorpus(t, root, map[string]string{
		"docs/adr/ADR-001-visible.md":     "# ADR-001: Visible Decision\n\n**Status**: Accepted\n",
		"docs/adr/deployment-strategy.md": "# Deployment Strategy\n\nProse without DR metadata.\n",
	})

	res := Enrich(context.Background(), Input{Topic: "deployment strategy"}, root)
	if !res.Signals.Degraded {
		t.Fatal("a titled, unparseable markdown file in the decision corpus must mark retrieval degraded")
	}
	if strings.Contains(res.Guidance, "verifiable claim") {
		t.Errorf("malformed decision must prevent verified-absence guidance: %q", res.Guidance)
	}
}

// TestEnrich_CorpusPartiallyUnparsed prevents a valid neighboring DR from
// masking a markdown file retrieval could not catalog. A query about the hidden
// file must not be reported as a verified absence merely because another record
// in the same directory parsed successfully.
func TestEnrich_CorpusPartiallyUnparsed(t *testing.T) {
	swapRegistry(t, nil, nil)
	root := t.TempDir()
	writeCorpus(t, root, map[string]string{
		"docs/adr/ADR-001-visible.md": "# ADR-001: Visible Decision\n\n**Status**: Accepted\n",
		"docs/adr/ADR-hidden.md":      "# Hidden Authentication Decision\n\nProse without DR metadata.\n",
	})

	res := Enrich(context.Background(), Input{Topic: "hidden authentication"}, root)
	if !res.Signals.Degraded {
		t.Fatal("a partially uncataloged corpus must mark the result degraded")
	}
	if res.Signals.Diagnostics != 1 {
		t.Fatalf("want one unreadable-corpus diagnostic, got %+v", res.Annotations)
	}
	if strings.Contains(res.Guidance, "verifiable claim") {
		t.Errorf("partial corpus visibility must not produce a verified-absence claim: %q", res.Guidance)
	}
}

func TestEnrich_CorpusPartiallyUnreadable(t *testing.T) {
	swapRegistry(t, nil, nil)
	root := t.TempDir()
	writeCorpus(t, root, map[string]string{
		"docs/adr/ADR-001-visible.md": "# ADR-001: Visible Decision\n\n**Status**: Accepted\n",
	})
	broken := filepath.Join(root, "docs/adr/ADR-002-broken.md")
	if err := os.Symlink(filepath.Join(root, "missing-target"), broken); err != nil {
		t.Fatal(err)
	}

	res := Enrich(context.Background(), Input{Topic: "authentication"}, root)
	if !res.Signals.Degraded {
		t.Fatal("an unreadable DR beside a valid record must mark retrieval degraded")
	}
	if res.Signals.Diagnostics != 1 {
		t.Fatalf("want one unreadable-corpus diagnostic, got %+v", res.Annotations)
	}
}

// TestEnrich_InvalidDecisionConfigDegraded pins the production buildEnv path:
// invalid configured paths are a source failure, not an empty readable corpus.
func TestEnrich_InvalidDecisionConfigDegraded(t *testing.T) {
	swapRegistry(t, nil, nil)
	root := t.TempDir()
	configDir := filepath.Join(root, ".sageox")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configBody := "config_version: \"2\"\ndecision:\n  paths:\n    - ../outside\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	res := Enrich(context.Background(), Input{Topic: "authentication"}, root)
	if !res.Signals.Degraded {
		t.Fatal("an invalid decision.paths source must mark the result degraded")
	}
	if strings.Contains(res.Guidance, "verifiable claim") {
		t.Errorf("invalid decision config must not produce a verified-absence claim: %q", res.Guidance)
	}
}

func TestEnrich_MalformedGlobDegraded(t *testing.T) {
	swapRegistry(t, nil, nil)
	root := t.TempDir()
	configDir := filepath.Join(root, ".sageox")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configBody := "config_version: \"2\"\ndecision:\n  paths:\n    - docs/adr/[\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	res := Enrich(context.Background(), Input{Topic: "authentication"}, root)
	if !res.Signals.Degraded {
		t.Fatal("a malformed configured glob must mark retrieval degraded")
	}
}

func TestEnrich_MissingConfiguredDirectoryDegraded(t *testing.T) {
	swapRegistry(t, nil, nil)
	root := t.TempDir()
	configDir := filepath.Join(root, ".sageox")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configBody := "config_version: \"2\"\ndecision:\n  paths:\n    - docs/decisions\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	res := Enrich(context.Background(), Input{Topic: "authentication"}, root)
	if !res.Signals.Degraded {
		t.Fatal("a missing explicit decision directory must mark retrieval degraded")
	}
}

// TestEnrich_ExplainSurfacesDroppedCandidates: --explain must list records that
// matched but fell below the floor, so a caller can tell "nothing relevant" from
// "found and dropped". Failure prevented (#823 ask 4): a silent floor hides
// near-misses, so a user cannot distinguish a true gap from a discarded match.
func TestEnrich_ExplainSurfacesDroppedCandidates(t *testing.T) {
	swapRegistry(t, nil, nil)
	root := t.TempDir()
	writeCorpus(t, root, map[string]string{
		// "kubernetes" appears only in the Context excerpt, never the title —
		// an excerpt-only match scores below minDRScore under the saturating scorer.
		"docs/adr/ADR-070-widget.md": "# ADR-070: Widget Rendering Pipeline\n\n**Status**: Accepted\n**Date**: 2026-02-02\n\n## Context\n\nThe kubernetes cluster hosts the renderer.\n",
	})
	topic := "kubernetes"

	off := Enrich(context.Background(), Input{Topic: topic}, root)
	if len(off.Dropped) != 0 {
		t.Errorf("dropped must be empty without --explain: %+v", off.Dropped)
	}

	on := Enrich(context.Background(), Input{Topic: topic}, root, WithExplain(true))
	if len(on.Dropped) == 0 {
		t.Fatalf("--explain should surface the sub-floor candidate: %+v", on)
	}
	if on.Dropped[0].Ref != "ADR-070" || on.Dropped[0].Score >= minDRScore {
		t.Errorf("dropped candidate wrong (want ADR-070 below %.2f): %+v", minDRScore, on.Dropped[0])
	}
}

func TestEnrich_ExplainSurfacesCapOmissions(t *testing.T) {
	swapRegistry(t, nil, nil)
	root := t.TempDir()
	files := make(map[string]string)
	for i := 1; i <= relatedCap+1; i++ {
		files[fmt.Sprintf("docs/adr/ADR-%03d-socket.md", i)] = fmt.Sprintf("# ADR-%03d: Socket Transport %d\n\n**Status**: Accepted\n", i, i)
	}
	writeCorpus(t, root, files)

	res := Enrich(context.Background(), Input{Topic: "socket"}, root, WithExplain(true))
	if len(res.Dropped) != 1 {
		t.Fatalf("want the one cap-omitted candidate explained, got %+v", res.Dropped)
	}
	if !strings.Contains(res.Dropped[0].Reason, "annotation cap") {
		t.Fatalf("cap omission reason missing: %+v", res.Dropped[0])
	}
}

func TestDroppedCandidates_ExcludesInputRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "docs", "adr", "ADR-070-widget.md")
	env := &Env{Corpus: []Record{{
		ID:      "ADR-070",
		Path:    path,
		RelPath: "docs/adr/ADR-070-widget.md",
		Title:   "Widget Rendering Pipeline",
		Excerpt: "The kubernetes cluster hosts the renderer.",
	}}}

	got := droppedCandidates(env, Input{Path: path, Topic: "kubernetes"})
	if len(got) != 0 {
		t.Fatalf("the input DR must never be reported as a dropped related candidate: %+v", got)
	}
}
