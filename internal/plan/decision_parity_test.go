package plan

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/decision"
)

// TestDecisionPlanParity_SameCorpusSameADR is the cross-command proof for #823:
// `ox plan enrich` and `ox decision enrich` must surface the SAME repo Decision
// Record for the same input over the same corpus. They share one loader and one
// scorer; before the fix they diverged because `plan enrich` distilled the query
// while `decision enrich` scored the raw topic — so a real (long) plan/issue
// body found the ADR through one command and nothing through the other.
//
// Failure prevented: an agent that consults `decision enrich` before drafting a
// DR is told "no prior decision" for a topic that `plan enrich` clearly ties to
// an existing ADR — the two halves of the same promise disagree.
//
// The input is deliberately long-form prose (the regime that broke), not a
// two-word probe.
func TestDecisionPlanParity_SameCorpusSameADR(t *testing.T) {
	root := t.TempDir()
	writeDR(t, root, "docs/adr/ADR-002-feature-flags.md",
		"# ADR-002: Feature flags are added only at explicit user request\n\n"+
			"**Status**: Accepted\n**Date**: 2026-02-01\n\n"+
			"## Context\n\nFlags accrete and never get removed; each one is a branch in "+
			"every code path forever. We add a feature flag only when a coworker asks "+
			"for one by name, never speculatively for staged rollouts or kill switches.\n")

	// A normal, long way to describe the work — the case the two-word probe hides.
	topic := "we want to gate the new todo digest emailer behind a feature flag so " +
		"we can stage the rollout by percentage and keep a kill switch in case the " +
		"new sender misbehaves in production, wiring two flag-shaped environment " +
		"variables into the deployment for the rollout knob and the rollback knob"

	// --- decision enrich side: the repo ADR must appear as a related decision ---
	dres := decision.Enrich(context.Background(), decision.Input{Topic: topic}, root)
	decisionFound := false
	for _, a := range dres.Annotations {
		if a.Type == decision.BadgeRelatedDecision && a.Ref == "ADR-002" {
			decisionFound = true
		}
	}
	if !decisionFound {
		t.Errorf("decision enrich did not surface ADR-002 for a long real-world topic: annotations=%+v signals=%+v",
			dres.Annotations, dres.Signals)
	}

	// --- plan enrich side: the same repo ADR must appear as an adr context item ---
	in, err := ResolveInput(topic, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	pres := Enrich(context.Background(), in, root)
	planFound := false
	for _, c := range pres.Context {
		if c.Kind == "adr" && strings.Contains(c.Ref, "ADR-002") {
			planFound = true
		}
	}
	if !planFound {
		t.Errorf("plan enrich did not surface ADR-002 for the same topic: context=%+v", pres.Context)
	}
}

// TestDecisionPlanParity_CompetingRecordsCannotEvictMatch proves both commands
// preserve an earlier seed match under a bounded result set. The plan bundle
// also reports how many above-floor candidates its cap omitted.
func TestDecisionPlanParity_CompetingRecordsCannotEvictMatch(t *testing.T) {
	tests := []struct {
		name  string
		topic string
	}{
		{
			name:  "competing terms",
			topic: "socket session adapter context plan integration lifecycle security distribution",
		},
		{
			name: "ordinary prose extension",
			topic: "socket session adapter context plan integration lifecycle security distribution " +
				"implementation ownership compatibility rollout migration observability",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeDR(t, root, "docs/adr/ADR-002-socket.md",
				"# ADR-002: Unix Domain Socket IPC\n\n**Status**: Accepted\n**Date**: 2026-01-01\n")
			for i, title := range []string{
				"Session Adapter Context",
				"Plan Integration Lifecycle",
				"Security Adapter Distribution",
				"Context Session Lifecycle",
				"Integration Security Plan",
				"Adapter Lifecycle Context",
			} {
				writeDR(t, root, filepath.Join("docs/adr", fmt.Sprintf("ADR-%03d-distractor.md", i+3)),
					fmt.Sprintf("# ADR-%03d: %s\n\n**Status**: Accepted\n**Date**: 2026-01-01\n", i+3, title))
			}

			dres := decision.Enrich(context.Background(), decision.Input{Topic: tc.topic}, root)
			decisionFound := false
			for _, ann := range dres.Annotations {
				decisionFound = decisionFound || ann.Ref == "ADR-002"
			}
			if !decisionFound {
				t.Fatalf("decision enrich evicted ADR-002: %+v", dres.Annotations)
			}

			in, err := ResolveInput(tc.topic, nil, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			pres := Enrich(context.Background(), in, root)
			planFound := false
			for _, item := range pres.Context {
				planFound = planFound || (item.Kind == "adr" && strings.Contains(item.Ref, "ADR-002"))
			}
			if !planFound {
				t.Fatalf("plan enrich evicted ADR-002: %+v", pres.Context)
			}
			adrCount, omitted := 0, 0
			for _, item := range pres.Context {
				if item.Kind == "adr" {
					adrCount++
					omitted += item.Omitted
				}
			}
			if adrCount != planDecisionCap {
				t.Fatalf("plan DR context must stay capped at %d, got %d", planDecisionCap, adrCount)
			}
			if omitted != 2 {
				t.Fatalf("plan DR context must report two omitted candidates, got %d", omitted)
			}
		})
	}
}
