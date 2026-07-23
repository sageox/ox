package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestPlanCommandSurface_AudienceTiers verifies the human/agent split: bare
// `ox plan --help` shows only the human verbs; agent/CI verbs are Hidden (still
// runnable, taught via prime). Failure prevented: an agent-plumbing command
// (viz/lint/save/feedback) leaks into the human help and clutters the surface.
func TestPlanCommandSurface_AudienceTiers(t *testing.T) {
	visible := map[string]bool{}
	hidden := map[string]bool{}
	for _, c := range planCmd.Commands() {
		if c.Hidden {
			hidden[c.Name()] = true
		} else {
			visible[c.Name()] = true
		}
	}
	for _, n := range []string{"enrich", "render", "review", "list", "view"} {
		if !visible[n] {
			t.Errorf("%q must be human-visible in `ox plan --help`", n)
		}
	}
	for _, n := range []string{"save", "lint", "viz", "feedback"} {
		if !hidden[n] {
			t.Errorf("%q must be Hidden (agent/CI tier, taught via prime)", n)
		}
	}
	// planCmd itself is a pure group — no default action.
	if planCmd.RunE != nil || planCmd.Run != nil {
		t.Error("planCmd must be a pure group (no RunE) so bare `ox plan` prints help")
	}
}

// TestPlanEnrich_DefaultsToJSON verifies the agent default: `ox plan enrich`
// emits JSON with no flags (no --json needed). Failure prevented: regressing to
// human-text default, which the agent-ux principles forbid.
func TestPlanEnrich_DefaultsToJSON(t *testing.T) {
	newPlanEnrichTestRepo(t) // hermetic: isolate cwd/HOME so findGitRoot() can't resolve to the real ox repo
	buf := &bytes.Buffer{}
	planEnrichCmd.SetOut(buf)
	planEnrichCmd.SetIn(strings.NewReader("# Title\n\n## Approach\n\nDo the thing.\n"))
	// default flags (text=false, persist=false) — the JSON path does NOT save.
	if err := planEnrichCmd.RunE(planEnrichCmd, nil); err != nil {
		t.Fatalf("enrich: %v", err)
	}
	out := strings.TrimSpace(buf.String())
	if !strings.HasPrefix(out, "{") {
		t.Errorf("default `ox plan enrich` output must be JSON, got: %.60s", out)
	}
	if !strings.Contains(out, `"signals"`) {
		t.Error("JSON output must carry the enrichment signals")
	}
}

// TestPlanFeedbackResolve_PositionalSlug verifies resolve takes <slug> <anchor>
// positionally (matches view/lint/review), not a --slug flag. Failure prevented:
// the lone command where slug is a flag, breaking the family's muscle memory.
func TestPlanFeedbackResolve_PositionalSlug(t *testing.T) {
	if !strings.Contains(planFeedbackResolveCmd.Use, "<slug> <anchor>") {
		t.Errorf("resolve Use should be `resolve <slug> <anchor>`, got %q", planFeedbackResolveCmd.Use)
	}
	if planFeedbackResolveCmd.Flags().Lookup("slug") != nil {
		t.Error("resolve must not have a --slug flag (slug is positional)")
	}
}
