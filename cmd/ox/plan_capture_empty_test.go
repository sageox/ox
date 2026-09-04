package main

import (
	"testing"

	"github.com/sageox/ox/internal/plan"
)

// TestSavePlanArtifacts_TopicOnlyConsultNeverPersists pins the cmd-layer half
// of the consult-mode guard. `ox plan enrich --topic "…"` builds an Input with
// Raw deliberately empty (no document exists yet); before the guard, running
// it with --persist (or --text with plan.save on) walked the full save path
// and minted a real pln_ id around a zero-byte plan.md — an empty,
// creator-less entry in the plan gallery.
//
// Red-first: delete the empty-Raw guard at the top of savePlanArtifacts AND
// the ErrNothingToSave guard in plan.Save → this fails with a non-empty dir.
func TestSavePlanArtifacts_TopicOnlyConsultNeverPersists(t *testing.T) {
	root := newPlanCaptureTestRepo(t)

	in, err := plan.ResolveInput("Converge multi-room recordings", nil, "", nil)
	if err != nil {
		t.Fatalf("ResolveInput: %v", err)
	}

	if dir := savePlanArtifacts(root, in, plan.Result{}, nil, ""); dir != "" {
		t.Fatalf("savePlanArtifacts persisted a topic-only consult to %q, want skip", dir)
	}

	plans, err := plan.List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(plans) != 0 {
		t.Fatalf("topic-only consult left %d plan(s) in the ledger: %+v", len(plans), plans)
	}
}
