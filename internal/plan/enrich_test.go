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
}
