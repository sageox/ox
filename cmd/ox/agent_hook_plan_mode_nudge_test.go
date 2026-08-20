package main

import (
	"bytes"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlanHintLines_PrimeCreed verifies both plan-mention steers lead with the
// plan creed. Failure prevented: the nudge primes the mechanics of planning but
// not its point — delight and education — so agents optimize for thrift alone.
func TestPlanHintLines_PrimeCreed(t *testing.T) {
	const creed = "delight them, educate them visually and crisply"
	assert.Contains(t, planModeHintLine(), creed, "plan-mode steer must prime the creed")
	assert.Contains(t, htmlPlanHintLine(), creed, "html steer must prime the creed")
}

// --- A. permission_mode extraction from UserPromptSubmit stdin ---

// TestExtractPermissionMode_BothSpellings verifies both the hook-stdin spelling
// (snake_case permission_mode) and the transcript spelling (camelCase
// permissionMode) are decoded. Failure prevented: the in-plan hint never fires
// because Claude Code's actual field name isn't the one we decode.
func TestExtractPermissionMode_BothSpellings(t *testing.T) {
	cases := map[string]struct {
		raw  string
		want string
	}{
		"snake plan":    {`{"permission_mode":"plan","prompt":"x"}`, "plan"},
		"camel plan":    {`{"permissionMode":"plan"}`, "plan"},
		"snake default": {`{"permission_mode":"default"}`, "default"},
		"camel accept":  {`{"permissionMode":"acceptEdits"}`, "acceptEdits"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, extractPermissionMode([]byte(tc.raw)))
		})
	}
}

// TestExtractPermissionMode_FailOpen covers every fail-open path: empty,
// non-JSON, and a payload with no permission-mode field (non-Claude agents).
// Failure prevented: a malformed payload panics or mis-fires the hint.
func TestExtractPermissionMode_FailOpen(t *testing.T) {
	cases := map[string]string{
		"empty":      ``,
		"not json":   `not json`,
		"no field":   `{"prompt":"hello","session_id":"s1"}`,
		"wrong type": `{"permission_mode":123}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, extractPermissionMode([]byte(raw)))
		})
	}
}

// --- B. Plan-mode trigger: hint once per plan-mode entry ---

// TestEmitPlanHint_FiresOncePerEntry verifies the core throttle: the hint
// fires on the first plan-mode prompt, suppresses on subsequent plan-mode
// prompts (same entry), then re-fires after the agent leaves and re-enters plan
// mode. Failure prevented: the hint spams every prompt, or never re-fires.
func TestEmitPlanHint_FiresOncePerEntry(t *testing.T) {
	projectRoot := planNudgeProject(t)
	agentID := "Oxmode1"
	planPrompt := []byte(`{"permission_mode":"plan","prompt":"plan it"}`)
	normalPrompt := []byte(`{"permission_mode":"default","prompt":"go"}`)

	// 1. first plan-mode prompt: hint fires
	var buf bytes.Buffer
	emitPlanHint(&buf, projectRoot, agentID, planPrompt)
	got := buf.String()
	assert.Contains(t, got, "<system-reminder>")
	assert.Contains(t, got, "[ox]")
	assert.Contains(t, got, "ox plan enrich --json")
	assert.Contains(t, got, "ox plan render --open")
	assert.Contains(t, got, "SageOx team-context-optimized plan")
	assert.NotContains(t, got, "\n<", "hint must be a single system-reminder line")

	// 2. second plan-mode prompt, same entry: suppressed
	var buf2 bytes.Buffer
	emitPlanHint(&buf2, projectRoot, agentID, planPrompt)
	assert.Empty(t, buf2.String(), "must not re-hint within the same plan-mode entry")

	// 3. agent leaves plan mode: stamp cleared, nothing emitted
	var buf3 bytes.Buffer
	emitPlanHint(&buf3, projectRoot, agentID, normalPrompt)
	assert.Empty(t, buf3.String(), "non-plan prompt emits nothing")
	assert.NoFileExists(t, planModeHintPath(projectRoot, agentID), "leaving plan mode clears the stamp")

	// 4. re-enters plan mode: hint fires again
	var buf4 bytes.Buffer
	emitPlanHint(&buf4, projectRoot, agentID, planPrompt)
	assert.Contains(t, buf4.String(), "ox plan enrich --json", "re-entering plan mode must re-hint")
}

// TestEmitPlanHint_NonClaudeNoOpOnNeutralPrompt verifies that a payload with no
// permission-mode field and no HTML-plan intent (the common non-Claude case)
// produces no hint and writes no stamp. Failure prevented: the plan-mode steer
// leaks to agents/prompts that didn't trigger it.
func TestEmitPlanHint_NonClaudeNoOpOnNeutralPrompt(t *testing.T) {
	projectRoot := planNudgeProject(t)
	agentID := "Oxsilver"
	var buf bytes.Buffer
	emitPlanHint(&buf, projectRoot, agentID, []byte(`{"prompt":"do work"}`))
	assert.Empty(t, buf.String())
	assert.NoFileExists(t, planModeHintPath(projectRoot, agentID))
}

// TestEmitPlanHint_FiresOnBlindPlanModePrompt is the behavioral guarantee that
// matters most: a user who NEVER mentions "ox", "SageOx", "plan", "enrich", or
// "render" — who only enters plan mode — is still steered into the SageOx
// enrichment workflow. The steer keys on permission_mode, not on the prompt naming
// the tool, so an unsuspecting prompt gets the team-context on-ramp. Failure
// prevented: enrichment only ever reaches users who already knew to ask for it.
//
// This is the UNIT tier — it proves the nudge fires. The true end-to-end (a real
// agent then actually invokes `ox plan enrich` and SageOx context appears in the
// output) lives in sageox/ox-test-harness; see .claude/rules/testing.md.
func TestEmitPlanHint_FiresOnBlindPlanModePrompt(t *testing.T) {
	projectRoot := planNudgeProject(t)
	agentID := "Oxblind"
	// No ox / SageOx / plan / enrich / render / html word anywhere in the prompt.
	blind := []byte(`{"permission_mode":"plan","prompt":"okay, let's begin building the new feature"}`)

	var buf bytes.Buffer
	emitPlanHint(&buf, projectRoot, agentID, blind)
	got := buf.String()
	require.Contains(t, got, "ox plan enrich --json", "a blind plan-mode prompt must still steer to enrich")
	assert.Contains(t, got, "Plan mode —", "must use the in-draft plan-mode lead")
	assert.FileExists(t, planModeHintPath(projectRoot, agentID))
}

// TestEmitPlanHint_EmptyArgs verifies path/emit are safe with empty inputs.
func TestEmitPlanHint_EmptyArgs(t *testing.T) {
	assert.Empty(t, planModeHintPath("", "Oxa"))
	assert.Empty(t, planModeHintPath("/tmp", ""))

	var buf bytes.Buffer
	emitPlanHint(&buf, "", "Oxa", []byte(`{"permission_mode":"plan"}`))
	emitPlanHint(&buf, "/tmp", "", []byte(`{"permission_mode":"plan"}`))
	assert.Empty(t, buf.String())
}

// TestEmitPlanHint_StampPersistsAcrossEntry verifies the stamp file exists
// while in plan mode (so repeat prompts stay suppressed) and is keyed per agent.
func TestEmitPlanHint_StampPersistsAcrossEntry(t *testing.T) {
	projectRoot := planNudgeProject(t)
	agentA := "OxA"
	agentB := "OxB"
	planPrompt := []byte(`{"permission_mode":"plan"}`)

	var buf bytes.Buffer
	emitPlanHint(&buf, projectRoot, agentA, planPrompt)
	require.FileExists(t, planModeHintPath(projectRoot, agentA))

	// agent B is independent — its first plan-mode prompt still hints
	var bufB bytes.Buffer
	emitPlanHint(&bufB, projectRoot, agentB, planPrompt)
	assert.Contains(t, bufB.String(), "ox plan enrich --json", "per-agent stamp must not bleed across agents")
}

// --- C. HTML-plan intent trigger (any permission mode) ---

// TestEmitPlanHint_FiresOnHTMLIntentOutsidePlanMode verifies the gap fix: a user
// asking to render an HTML plan OUTSIDE plan mode still gets the just-in-time
// steer toward `ox plan render` + the viz catalog. Failure prevented: agents in
// default/acceptEdits mode hand-roll context-blind orphan renders because the
// only just-in-time steer was gated on plan mode.
func TestEmitPlanHint_FiresOnHTMLIntentOutsidePlanMode(t *testing.T) {
	t.Setenv(config.EnvPlanHTML, config.PlanHTMLRecommend) // deterministic: not opted out
	projectRoot := planNudgeProject(t)
	agentID := "Oxhtml1"
	prompt := []byte(`{"permission_mode":"default","prompt":"make me an html plan for the auth refactor"}`)

	var buf bytes.Buffer
	emitPlanHint(&buf, projectRoot, agentID, prompt)
	got := buf.String()
	assert.Contains(t, got, "<system-reminder>")
	assert.Contains(t, got, "[ox]")
	assert.Contains(t, got, "ox plan render --open")
	assert.Contains(t, got, "ox viz", "must point at the visualization catalog")
	assert.NotContains(t, got, "Plan mode —", "outside plan mode uses the render lead, not the plan-mode lead")
	assert.NotContains(t, got, "\n<", "hint must be a single system-reminder line")
}

// TestEmitPlanHint_HTMLIntentThrottled verifies the once-per-episode throttle on
// the HTML-intent path. Failure prevented: the steer spams every prompt while
// the user keeps discussing the plan.
func TestEmitPlanHint_HTMLIntentThrottled(t *testing.T) {
	t.Setenv(config.EnvPlanHTML, config.PlanHTMLRecommend)
	projectRoot := planNudgeProject(t)
	agentID := "Oxhtml2"
	prompt := []byte(`{"permission_mode":"default","prompt":"render this plan as an html page"}`)

	var buf1, buf2 bytes.Buffer
	emitPlanHint(&buf1, projectRoot, agentID, prompt)
	emitPlanHint(&buf2, projectRoot, agentID, prompt)
	assert.Contains(t, buf1.String(), "ox plan render --open")
	assert.Empty(t, buf2.String(), "must not re-hint within the same episode")
}

// TestEmitPlanHint_PlanModeLeadWinsWhenBoth verifies that when the agent is in
// plan mode AND the prompt names an html plan, the in-draft plan-mode lead
// (enrich WHILE drafting) wins — it's the superset message. Failure prevented:
// the render-first lead overrides the more useful in-draft enrich steer.
func TestEmitPlanHint_PlanModeLeadWinsWhenBoth(t *testing.T) {
	t.Setenv(config.EnvPlanHTML, config.PlanHTMLRecommend)
	projectRoot := planNudgeProject(t)
	agentID := "Oxboth"
	prompt := []byte(`{"permission_mode":"plan","prompt":"draft and render an html plan"}`)

	var buf bytes.Buffer
	emitPlanHint(&buf, projectRoot, agentID, prompt)
	got := buf.String()
	assert.Contains(t, got, "Plan mode —")
	assert.Contains(t, got, "ox plan enrich --json")
}

// TestEmitPlanHint_RespectsPlanHTMLOff verifies the opt-out: a user who set
// plan.html=off does NOT get the HTML-intent steer. Failure prevented: the steer
// nags users who explicitly disabled HTML plan rendering.
func TestEmitPlanHint_RespectsPlanHTMLOff(t *testing.T) {
	t.Setenv(config.EnvPlanHTML, config.PlanHTMLOff)
	projectRoot := planNudgeProject(t)
	agentID := "Oxoff"
	prompt := []byte(`{"permission_mode":"default","prompt":"make me an html plan"}`)

	var buf bytes.Buffer
	emitPlanHint(&buf, projectRoot, agentID, prompt)
	assert.Empty(t, buf.String(), "plan.html=off must silence the HTML-intent steer")
	assert.NoFileExists(t, planModeHintPath(projectRoot, agentID))
}

// TestEmitPlanHint_PlanModeUnaffectedByPlanHTMLOff documents the deliberate
// asymmetry: plan.html=off silences the render-first HTML-intent steer but NOT
// the plan-mode in-draft steer, which leads with `ox plan enrich` (team context
// valuable regardless of whether the human renders HTML).
func TestEmitPlanHint_PlanModeUnaffectedByPlanHTMLOff(t *testing.T) {
	t.Setenv(config.EnvPlanHTML, config.PlanHTMLOff)
	projectRoot := planNudgeProject(t)
	agentID := "Oxoffplan"
	prompt := []byte(`{"permission_mode":"plan","prompt":"plan it"}`)

	var buf bytes.Buffer
	emitPlanHint(&buf, projectRoot, agentID, prompt)
	assert.Contains(t, buf.String(), "ox plan enrich --json", "plan-mode steer fires regardless of plan.html=off")
}

// TestEmitPlanHint_NoTriggerResetsAfterHTMLIntent verifies the stamp resets when
// a neutral prompt follows an HTML-intent hint, so a later HTML-plan request
// re-hints. Failure prevented: the throttle permanently suppresses after one
// episode.
func TestEmitPlanHint_NoTriggerResetsAfterHTMLIntent(t *testing.T) {
	t.Setenv(config.EnvPlanHTML, config.PlanHTMLRecommend)
	projectRoot := planNudgeProject(t)
	agentID := "Oxreset"
	htmlPrompt := []byte(`{"permission_mode":"default","prompt":"render this plan as html"}`)
	neutralPrompt := []byte(`{"permission_mode":"default","prompt":"fix the failing test"}`)

	var buf1 bytes.Buffer
	emitPlanHint(&buf1, projectRoot, agentID, htmlPrompt)
	require.Contains(t, buf1.String(), "ox plan render --open")

	var buf2 bytes.Buffer
	emitPlanHint(&buf2, projectRoot, agentID, neutralPrompt)
	assert.Empty(t, buf2.String())
	assert.NoFileExists(t, planModeHintPath(projectRoot, agentID), "neutral prompt clears the stamp")

	var buf3 bytes.Buffer
	emitPlanHint(&buf3, projectRoot, agentID, htmlPrompt)
	assert.Contains(t, buf3.String(), "ox plan render --open", "a later HTML-plan request must re-hint")
}

// TestEmitPlanHint_PlanModeUpgradesAfterHTMLIntent verifies the higher-value
// plan-mode (draft-first) steer still fires when the user gets an HTML-intent
// hint and THEN enters plan mode with no neutral prompt between — the shared
// stamp must not swallow it. Failure prevented: the render-first html-intent
// hint suppresses the "enrich WHILE you draft" steer for the whole episode.
func TestEmitPlanHint_PlanModeUpgradesAfterHTMLIntent(t *testing.T) {
	t.Setenv(config.EnvPlanHTML, config.PlanHTMLRecommend)
	projectRoot := planNudgeProject(t)
	agentID := "Oxupgrade"
	htmlPrompt := []byte(`{"permission_mode":"default","prompt":"make me an html plan"}`)
	planPrompt := []byte(`{"permission_mode":"plan","prompt":"plan it"}`)

	var buf1 bytes.Buffer
	emitPlanHint(&buf1, projectRoot, agentID, htmlPrompt)
	require.Contains(t, buf1.String(), "Rendering an HTML plan", "html-intent hint fires first")

	// enter plan mode directly, no neutral prompt between: draft-first steer upgrades
	var buf2 bytes.Buffer
	emitPlanHint(&buf2, projectRoot, agentID, planPrompt)
	assert.Contains(t, buf2.String(), "Plan mode —", "plan-mode steer upgrades over an html-intent stamp")

	// once upgraded, a second plan-mode prompt in the same entry is suppressed
	var buf3 bytes.Buffer
	emitPlanHint(&buf3, projectRoot, agentID, planPrompt)
	assert.Empty(t, buf3.String(), "plan-mode hint fires once after the upgrade")
}

// TestEmitPlanHint_HTMLIntentDoesNotUpgradePlanMode verifies the reverse is NOT
// an upgrade: after a plan-mode hint, an html-intent prompt in the same episode
// stays suppressed (the plan-mode lead already covers rendering). Failure
// prevented: an episode double-hints when the user toggles plan mode off then
// asks to render.
func TestEmitPlanHint_HTMLIntentDoesNotUpgradePlanMode(t *testing.T) {
	t.Setenv(config.EnvPlanHTML, config.PlanHTMLRecommend)
	projectRoot := planNudgeProject(t)
	agentID := "Oxnoupgrade"
	planPrompt := []byte(`{"permission_mode":"plan","prompt":"plan it"}`)
	htmlPrompt := []byte(`{"permission_mode":"acceptEdits","prompt":"now render this plan as html"}`)

	var buf1 bytes.Buffer
	emitPlanHint(&buf1, projectRoot, agentID, planPrompt)
	require.Contains(t, buf1.String(), "Plan mode —", "plan-mode hint fires first")

	var buf2 bytes.Buffer
	emitPlanHint(&buf2, projectRoot, agentID, htmlPrompt)
	assert.Empty(t, buf2.String(), "html-intent must not re-hint after a plan-mode hint in the same episode")
}

// --- D. HTML-plan intent detection ---

// TestPromptRequestsHTMLPlan covers the phrase detector that drives the any-mode
// trigger. Failure prevented: real HTML-plan requests slip through (no steer) or
// unrelated prompts false-fire (nagging).
func TestPromptRequestsHTMLPlan(t *testing.T) {
	positives := []string{
		`{"prompt":"make me an html plan for X"}`,
		`{"prompt":"use the /html-plan skill"}`,
		`{"prompt":"render the plan as a page"}`,
		`{"prompt":"render this plan please"}`,
		`{"prompt":"can you visualize the plan"}`,
		`{"prompt":"visualize this plan as html"}`,
		`{"prompt":"show the PLAN AS HTML"}`, // case-insensitive
	}
	for _, raw := range positives {
		assert.True(t, promptRequestsHTMLPlan([]byte(raw)), "should detect: %s", raw)
	}

	negatives := []string{
		``,
		`not json`,
		`{"prompt":"fix the failing test"}`,
		`{"prompt":"plan the sprint"}`,                 // plan, but no render/html cue
		`{"prompt":"render the login page"}`,           // render, but not a plan
		`{"prompt":"what does this html do"}`,          // html, but not a plan
		`{"prompt":"render the planned route"}`,        // word boundary: "plan" != "planned"
		`{"prompt":"visualize the planning timeline"}`, // word boundary: "plan" != "planning"
	}
	for _, raw := range negatives {
		assert.False(t, promptRequestsHTMLPlan([]byte(raw)), "should NOT detect: %s", raw)
	}
}
