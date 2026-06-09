package plan

import (
	"context"
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
