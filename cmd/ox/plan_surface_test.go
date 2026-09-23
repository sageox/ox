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

// TestPlanHelp_NounIsAnyPlanNotJustImplementation pins the framing of the plan
// surface: `ox plan save --kind` already accepts plan|mockup|review|evidence, so
// help text that says "implementation plans" tells a designer, a PMM, or anyone
// planning a rollout that Plans is not for them. Failure prevented (GH #1041):
// the copy silently re-narrows to engineering and the non-engineering kinds
// become a hidden capability again.
func TestPlanHelp_NounIsAnyPlanNotJustImplementation(t *testing.T) {
	for _, tc := range []struct {
		where string
		text  string
	}{
		{"planCmd.Short", planCmd.Short},
		{"planCmd.Long", planCmd.Long},
		{"planEnrichCmd.Short", planEnrichCmd.Short},
		{"planEnrichCmd.Long", planEnrichCmd.Long},
	} {
		if strings.Contains(strings.ToLower(tc.text), "implementation plan") {
			t.Errorf("%s calls a plan an \"implementation plan\" — a plan is any work the team executes (design, GTM, rollout, engineering): %q", tc.where, tc.text)
		}
	}

	// Dropping the adjective is not enough on its own: a bare "plans" with an
	// engineering-only body reads the same way. The Long must name the kinds.
	long := strings.ToLower(planCmd.Long)
	for _, kind := range []string{"design", "gtm", "rollout"} {
		if !strings.Contains(long, kind) {
			t.Errorf("planCmd.Long must name %q among the kinds of plan ox supports, so a non-engineering reader sees themselves in it", kind)
		}
	}
}
