package main

import (
	"strings"
	"testing"
)

// TestPlanEnrichmentGuidance_OneRuleDetailOnDemand verifies the full-tier
// prime block is one rule + a pointer, and that every detail it dropped is
// still reachable in `ox guide plan-enrichment`.
// Failure prevented: trimming prime silently deleting guidance (lint-before-
// save, --kind, the NOT SHARED verdict, legacy --plan + --html) instead of
// moving it on demand — or the block regrowing past its budget.
func TestPlanEnrichmentGuidance_OneRuleDetailOnDemand(t *testing.T) {
	var sb strings.Builder
	writePlanEnrichmentGuidance(&sb, "claude-code")
	block := sb.String()
	for _, want := range []string{"ox guide plan-enrichment", "--kind plan|mockup|review|evidence", "ox plan save --file plan.html", "ox plan enrich", "ox plan lint", "ox plan review"} {
		if !strings.Contains(block, want) {
			t.Errorf("prime block missing %q", want)
		}
	}
	// ~376 tokens measured after the trim (was ~590); fail on regrowth.
	if max := 1700; len(block) > max {
		t.Errorf("plan-enrichment-guidance is %d chars, budget %d — move detail to the guide", len(block), max)
	}

	guide, err := guidesFS.ReadFile("guides/plan-enrichment.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--kind plan|mockup|review|evidence", "ox plan lint --file plan.html", "NOT SHARED", "--plan + --html", "zero LLM cost", "strips `<script>`"} {
		if !strings.Contains(string(guide), want) {
			t.Errorf("guide missing moved detail %q", want)
		}
	}
}
