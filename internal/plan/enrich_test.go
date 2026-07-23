package plan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnrichEmptyRegistry verifies the orchestrator works with zero registered
// detectors/retrievers: empty annotations, empty context, non-material signals.
func TestEnrichEmptyRegistry(t *testing.T) {
	// Snapshot/restore the global registry so this test doesn't see (or leak)
	// detectors registered by Round 2 packages.
	registryMu.Lock()
	savedDetectors, savedRetrievers := detectors, retrievers
	detectors, retrievers = nil, nil
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		detectors, retrievers = savedDetectors, savedRetrievers
		registryMu.Unlock()
	})

	in := Parse("## Section\nbody")
	result := Enrich(context.Background(), in, "")

	if len(result.Annotations) != 0 {
		t.Errorf("expected no annotations, got %d", len(result.Annotations))
	}
	if len(result.Context) != 0 {
		t.Errorf("expected no context items, got %d", len(result.Context))
	}
	if result.Signals.Material {
		t.Errorf("expected non-material signals for empty registry")
	}
	if result.Signals.Collisions != 0 || result.Signals.PriorArt != 0 || result.Signals.ExpertRoutes != 0 {
		t.Errorf("expected zero signal counts, got %+v", result.Signals)
	}
	// a single section with no file refs is trivial: Files=0, Steps=1.
	if result.Signals.Files != 0 || result.Signals.Steps != 1 {
		t.Errorf("expected Files=0 Steps=1, got Files=%d Steps=%d", result.Signals.Files, result.Signals.Steps)
	}
	if result.Signals.NonTrivial {
		t.Errorf("expected single-section no-file plan to be trivial")
	}
}

// TestSummarizeNonTrivialDecoupledFromMaterial verifies the core decoupling:
// a structurally substantial plan is NonTrivial even with zero team-context
// signals (no registered detectors => Material is false). Without this, a large
// greenfield plan would never trigger the HTML-render nudge.
// Failure prevented: the render nudge stays coupled to team-context signals.
func TestSummarizeNonTrivialDecoupledFromMaterial(t *testing.T) {
	registryMu.Lock()
	savedDetectors, savedRetrievers := detectors, retrievers
	detectors, retrievers = nil, nil
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		detectors, retrievers = savedDetectors, savedRetrievers
		registryMu.Unlock()
	})

	t.Run("multi-file triggers NonTrivial without Material", func(t *testing.T) {
		in := Parse("## Plan\ntouch `internal/auth/session.go` and `cmd/ox/login.go`")
		res := Enrich(context.Background(), in, "")
		if res.Signals.Material {
			t.Fatalf("expected non-material (no detectors), got material")
		}
		if res.Signals.Files != 2 {
			t.Errorf("expected Files=2, got %d", res.Signals.Files)
		}
		if !res.Signals.NonTrivial {
			t.Errorf("expected multi-file plan to be NonTrivial")
		}
	})

	t.Run("same file across sections counts once", func(t *testing.T) {
		raw := "## A\nedit `internal/auth/session.go`\n\n## B\nalso `internal/auth/session.go`"
		res := Enrich(context.Background(), Parse(raw), "")
		if res.Signals.Files != 1 {
			t.Errorf("expected distinct Files=1 (cross-section dedup), got %d", res.Signals.Files)
		}
		if res.Signals.NonTrivial {
			t.Errorf("single distinct file across two sections is not multi-file; expected trivial on files")
		}
	})

	t.Run("five steps triggers NonTrivial, preamble excluded", func(t *testing.T) {
		// leading preamble before the first H2 must NOT count as a step.
		raw := "intro preamble\n\n## One\n## Two\n## Three\n## Four\n## Five"
		res := Enrich(context.Background(), Parse(raw), "")
		if res.Signals.Steps != 5 {
			t.Errorf("expected Steps=5 (preamble excluded), got %d", res.Signals.Steps)
		}
		if !res.Signals.NonTrivial {
			t.Errorf("expected 5-step plan to be NonTrivial")
		}
	})

	t.Run("four steps with preamble stays trivial", func(t *testing.T) {
		raw := "intro preamble\n\n## One\n## Two\n## Three\n## Four"
		res := Enrich(context.Background(), Parse(raw), "")
		if res.Signals.Steps != 4 {
			t.Errorf("expected Steps=4 (preamble excluded), got %d", res.Signals.Steps)
		}
		if res.Signals.NonTrivial {
			t.Errorf("4-step no-multi-file plan must stay trivial (preamble off-by-one guard)")
		}
	})
}

// --- topic-only (pre-draft consult) tests ---

func TestIsTopicOnly(t *testing.T) {
	tests := []struct {
		name string
		in   Input
		want bool
	}{
		{"topic only", Input{Topic: "x"}, true},
		{"topic with files", Input{Topic: "x", Files: []string{"a.go"}}, true},
		{"full doc via Parse (Topic always empty)", Parse("## A\nbody"), false},
		{"zero-value Input", Input{}, false},
		{"topic set but Raw also set is NOT topic-only (defensive)", Input{Topic: "x", Raw: "## A\nbody"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTopicOnly(tt.in); got != tt.want {
				t.Errorf("isTopicOnly(%+v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestNewTopicInput_FeedsFileKeyedHelpers proves the synthetic section built by
// newTopicInput actually reaches the EXISTING file/query-keyed helpers
// collision.go, expert.go, and priorart.go already implement — the mechanism
// the file-based signals (collision, expert-routing) and the keyword signal
// (prior-art) rely on, requiring ZERO changes to those files. Also documents
// the one accepted gap: the synthetic section's empty Heading (preamble
// semantics) means sectionHeadings excludes it, so the softer murmur
// topic-vs-heading match does not fire in topic-only mode.
func TestNewTopicInput_FeedsFileKeyedHelpers(t *testing.T) {
	in := newTopicInput("auth token refresh flow", []string{"internal/auth/token.go", "internal/auth/refresh.go"})
	want := []string{"internal/auth/refresh.go", "internal/auth/token.go"}

	if got := planFiles(in); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("planFiles = %v, want %v", got, want)
	}
	if got := expertPlanFiles(in); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("expertPlanFiles = %v, want %v", got, want)
	}

	q := deriveQuery(in)
	for _, kw := range []string{"auth", "token", "refresh", "flow"} {
		if !strings.Contains(q, kw) {
			t.Errorf("deriveQuery(%q) missing keyword %q", q, kw)
		}
	}

	if headings := sectionHeadings(in); len(headings) != 0 {
		t.Errorf("expected no headings from the synthetic preamble section (accepted gap), got %v", headings)
	}
}

// TestNewTopicInput_ExcludedFromDocStructuralHints is the belt-and-suspenders
// check: even called directly (bypassing the isTopicOnly early-return in
// Enrich), the synthetic section's empty Heading means the hint functions skip
// it, matching Parse's own "preamble is framing, not a step" convention. The
// topic text is loaded with trigger words that WOULD fire on a real section,
// so this proves the exclusion is structural, not an accident of wording.
func TestNewTopicInput_ExcludedFromDocStructuralHints(t *testing.T) {
	in := newTopicInput("retry timeout state transition rollout phase onboarding screen mockup", []string{"a.go", "b.go", "c.go"})
	if got := computeDiagramHints(in); got != nil {
		t.Errorf("computeDiagramHints on synthetic topic section = %+v, want nil", got)
	}
	if got := computeVizHints(in); got != nil {
		t.Errorf("computeVizHints on synthetic topic section = %+v, want nil", got)
	}
	if got := computeMockupExpectation(in); got != "" {
		t.Errorf("computeMockupExpectation on synthetic topic section = %q, want \"\"", got)
	}
}

func TestBuildTopicGuidance(t *testing.T) {
	t.Run("material branch names the fired signals", func(t *testing.T) {
		sum := SignalSummary{Collisions: 2, ExpertRoutes: 1, PriorArt: 3, Material: true}
		g := buildTopicGuidance(Input{Topic: "x", Files: []string{"a.go"}}, sum)
		for _, want := range []string{"2 collisions", "1 expert route", "3 prior-art hits", "team history"} {
			if !strings.Contains(g, want) {
				t.Errorf("guidance missing %q: %q", want, g)
			}
		}
		if strings.Contains(g, "Pass --files") {
			t.Errorf("files were already given; must not prompt for --files: %q", g)
		}
	})
	t.Run("zero-signal branch is honest, not silent, and prompts for --files", func(t *testing.T) {
		g := buildTopicGuidance(Input{Topic: "x"}, SignalSummary{})
		if !strings.Contains(g, "Draft from first principles") {
			t.Errorf("expected the honesty-first zero-signal lead: %q", g)
		}
		if !strings.Contains(g, "Pass --files") {
			t.Errorf("expected a --files nudge when none were given: %q", g)
		}
	})
	t.Run("always points at the full-doc follow-up", func(t *testing.T) {
		g := buildTopicGuidance(Input{Topic: "x"}, SignalSummary{})
		if !strings.Contains(g, "ox plan enrich --file") {
			t.Errorf("expected the full-doc follow-up reminder: %q", g)
		}
	})
	t.Run("never empty", func(t *testing.T) {
		if buildTopicGuidance(Input{Topic: "x"}, SignalSummary{}) == "" {
			t.Error("buildTopicGuidance must never return empty (unlike the full-doc buildGuidance)")
		}
	})
}

// TestEnrich_TopicOnlySkipsDocStructuralHints is the Enrich-level counterpart
// of TestNewTopicInput_ExcludedFromDocStructuralHints: it proves the isTopicOnly
// branch in Enrich itself takes the shortcut path (no hint computation, no
// full-doc buildGuidance call), not merely that the hint functions happen to
// no-op on this input.
func TestEnrich_TopicOnlySkipsDocStructuralHints(t *testing.T) {
	registryMu.Lock()
	savedDetectors, savedRetrievers := detectors, retrievers
	detectors, retrievers = nil, nil
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		detectors, retrievers = savedDetectors, savedRetrievers
		registryMu.Unlock()
	})

	in := newTopicInput("retry timeout state transition rollout phase onboarding screen mockup", nil)
	res := Enrich(context.Background(), in, "")

	if len(res.DiagramHints) != 0 {
		t.Errorf("expected no diagram hints for a topic-only consult, got %+v", res.DiagramHints)
	}
	if len(res.VizHints) != 0 {
		t.Errorf("expected no viz hints for a topic-only consult, got %+v", res.VizHints)
	}
	if res.MockupSection != "" {
		t.Errorf("expected no mockup section for a topic-only consult, got %q", res.MockupSection)
	}
	if res.Guidance == "" {
		t.Fatal("expected non-empty topic guidance")
	}
	if strings.Contains(res.Guidance, "Author in two layers") {
		t.Errorf("topic-only guidance must not be the full-doc authoring contract: %q", res.Guidance)
	}
	if !strings.Contains(res.Guidance, "Draft from first principles") {
		t.Errorf("expected the zero-signal topic guidance branch (empty registry fires nothing), got %q", res.Guidance)
	}
}

// TestEnrich_FullDocPathUnaffectedByTopicBranch is the explicit regression test
// for the isTopicOnly branch added to Enrich: any Input with Raw set — the
// entire pre-existing --file/stdin/auto-discovery surface, where Topic is
// always "" the zero value — must take the SAME full-doc path as before, byte-
// for-byte matching pre-change behavior. TestEnrichEmptyRegistry and
// TestSummarizeNonTrivialDecoupledFromMaterial already cover the full-doc
// Result shape; this test targets the NEW branch condition directly so a
// future change to isTopicOnly's predicate cannot silently swallow real plans.
func TestEnrich_FullDocPathUnaffectedByTopicBranch(t *testing.T) {
	registryMu.Lock()
	savedDetectors, savedRetrievers := detectors, retrievers
	detectors, retrievers = nil, nil
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		detectors, retrievers = savedDetectors, savedRetrievers
		registryMu.Unlock()
	})

	raw := "## Retry handling\nThe request retries with backoff on timeout, then transitions to idle.\n\n" +
		"## Rollout\nRoll out in phases over two weeks."
	in := Parse(raw)
	if in.Topic != "" {
		t.Fatalf("sanity: Parse must never set Topic, got %q", in.Topic)
	}
	if isTopicOnly(in) {
		t.Fatal("a parsed full document must never be classified topic-only")
	}

	res := Enrich(context.Background(), in, "")
	if len(res.DiagramHints) == 0 {
		t.Error("expected the full-doc path to still compute diagram hints for structured prose")
	}
	if res.Guidance == "" || !strings.Contains(res.Guidance, "Implementation notes") {
		t.Errorf("expected the full-doc two-layer authoring guidance (decision layer + Implementation notes), got %q", res.Guidance)
	}
}

// TestEnrich_TopicOnly_RealDecisionRetrieverFires proves a topic-only consult
// gets a REAL, non-trivial signal end-to-end — not just a differently-worded
// Guidance string — mirroring decision.TestEnrich_OnRealTempCorpus's rigor via
// the same easy-to-fixture mechanism (a plain docs/adr/*.md corpus needs no
// ledger/codedb setup) since plan's own decisionRetriever (decision_bundle.go)
// calls straight into internal/decision's corpus loader.
func TestEnrich_TopicOnly_RealDecisionRetrieverFires(t *testing.T) {
	root := t.TempDir()
	adrDir := filepath.Join(root, "docs", "adr")
	if err := os.MkdirAll(adrDir, 0o755); err != nil {
		t.Fatal(err)
	}
	drBody := "# ADR-001: Plan Context Not Inference\n\n**Status**: Accepted\n**Date**: 2026-06-03\n\n" +
		"## Context\n\nox provides context for plan enrichment; the client agent does inference.\n"
	if err := os.WriteFile(filepath.Join(adrDir, "ADR-001-plan-context.md"), []byte(drBody), 0o644); err != nil {
		t.Fatal(err)
	}

	in := newTopicInput("plan context inference", nil)
	res := Enrich(context.Background(), in, root)

	var found *ContextItem
	for i, c := range res.Context {
		if c.Kind == "adr" && strings.Contains(c.Ref, "ADR-001-plan-context.md") {
			found = &res.Context[i]
		}
	}
	if found == nil {
		t.Fatalf("expected ADR-001 surfaced as context for a matching topic-only consult, got %+v", res.Context)
	}
	if found.Score <= 0 {
		t.Errorf("expected a positive relevance score, got %v", found.Score)
	}
	// doc-structural fields must stay empty even though a real signal fired.
	if len(res.DiagramHints) != 0 || len(res.VizHints) != 0 || res.MockupSection != "" {
		t.Errorf("doc-structural fields must stay empty in topic-only mode: %+v", res)
	}
}
