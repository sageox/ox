package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/plan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// planNudgeProject builds a minimal initialized project root for the plan-exit
// nudge tests. Unlike the suspended-nudge tests, the plan nudge is independent
// of recording state, so no session is started here.
func planNudgeProject(t *testing.T) string {
	t.Helper()
	projectRoot := t.TempDir()
	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0o755))
	cfg := `{"config_version":"2","repo_id":"test-repo-plan-nudge","endpoint":"http://test.sageox.local","session_publishing":"manual"}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(cfg), 0o644))
	return projectRoot
}

// planNudgeProjectWithSaveOff mirrors planNudgeProject but sets plan.save=false
// in the project config — for the one test proving the background render is
// skipped when there is no ledger capture to render into.
func planNudgeProjectWithSaveOff(t *testing.T) string {
	t.Helper()
	projectRoot := t.TempDir()
	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0o755))
	cfg := `{"config_version":"2","repo_id":"test-repo-plan-nudge-save-off","endpoint":"http://test.sageox.local","session_publishing":"manual","plan":{"save":false}}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(cfg), 0o644))
	return projectRoot
}

// isolatePlanConfigEnv points OX_USER_CONFIG at a nonexistent file and clears
// SAGEOX_PLAN_HTML, so plan.save/plan.html resolution in these tests can never
// pick up the real developer machine's user-level config — mirrors
// internal/config/plan_test.go's isolateUserConfig for this package.
func isolatePlanConfigEnv(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv(config.EnvPlanHTML, "")
}

// planExitCtx builds a HookContext carrying a valid ExitPlanMode tool_input,
// for tests that drive handlePlanExit directly.
func planExitCtx(projectRoot, planText string) *HookContext {
	type toolInput struct {
		Plan string `json:"plan"`
	}
	type envelope struct {
		ToolName  string    `json:"tool_name"`
		ToolInput toolInput `json:"tool_input"`
	}
	body, _ := json.Marshal(envelope{ToolName: exitPlanModeToolName, ToolInput: toolInput{Plan: planText}})
	return &HookContext{
		ProjectRoot: projectRoot,
		Input:       &agentx.HookInput{RawBytes: body},
	}
}

// stubPlanSubprocesses swaps runPlanEnrichment / runPlanRenderNoOpen for the
// duration of the calling test (restored via t.Cleanup), and returns a
// counter that increments on every runPlanRenderNoOpen call — so a test can
// assert whether the background render was attempted at all, independent of
// spawning a real `ox` subprocess (os.Executable() inside `go test` resolves
// to the compiled TEST binary, not the real CLI — see runPlanEnrichment's doc).
func stubPlanSubprocesses(t *testing.T, enrich func(string) (planJSONResult, bool)) *int {
	t.Helper()
	origEnrich := runPlanEnrichment
	origRender := runPlanRenderNoOpen
	t.Cleanup(func() {
		runPlanEnrichment = origEnrich
		runPlanRenderNoOpen = origRender
	})

	renderCalls := 0
	runPlanEnrichment = enrich
	runPlanRenderNoOpen = func(string) bool {
		renderCalls++
		return true
	}
	return &renderCalls
}

// --- A. Plan text extraction from ExitPlanMode tool_input ---

// TestExtractExitPlanText_HappyPath verifies the plan markdown is pulled out of
// the nested tool_input.plan field of Claude Code's PostToolUse stdin.
// Failure prevented: nudge silently never fires because the plan text is lost.
func TestExtractExitPlanText_HappyPath(t *testing.T) {
	raw := []byte(`{"tool_name":"ExitPlanMode","tool_input":{"plan":"# Refactor auth\n- touch internal/auth/session.go"}}`)
	got := extractExitPlanText(raw)
	assert.Contains(t, got, "Refactor auth")
	assert.Contains(t, got, "internal/auth/session.go")
}

// TestExtractExitPlanText_Malformed covers every fail-open path: empty input,
// non-JSON, missing tool_input, missing plan field.
func TestExtractExitPlanText_Malformed(t *testing.T) {
	cases := map[string]string{
		"empty":           ``,
		"not json":        `not json at all`,
		"no tool_input":   `{"tool_name":"ExitPlanMode"}`,
		"no plan field":   `{"tool_input":{"other":"x"}}`,
		"plan not string": `{"tool_input":{"plan":123}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, extractExitPlanText([]byte(raw)))
		})
	}
}

// --- B. Nudge line formatting ---

// TestFormatPlanNudgeLine_MentionsOnlyFiredSignals verifies the one-line nudge
// names only the signal classes that actually fired, with correct pluralization.
// Failure prevented: nudge claims signals (e.g. "0 collisions") that didn't fire.
func TestFormatPlanNudgeLine_MentionsOnlyFiredSignals(t *testing.T) {
	var res planJSONResult
	res.Signals.Collisions = 2
	res.Signals.PriorArt = 1
	res.Signals.ExpertRoutes = 0
	res.Signals.Material = true

	line := formatPlanNudgeLine(res, config.PlanOpenAsk)
	assert.Contains(t, line, "2 collisions")
	assert.Contains(t, line, "1 prior-art match")
	assert.NotContains(t, line, "expert route", "expert routes did not fire — must not be mentioned")
	assert.Contains(t, line, "ox plan render --open")
	assert.Contains(t, line, "SageOx team-context-optimized plan")
	assert.Contains(t, line, "ox plan review", "exit nudge must offer the live review loop")
	assert.Contains(t, line, "ox plan review await", "nudge must point to the agent-side await loop")
	assert.Contains(t, line, "confirm", "nudge must carry the ask-before-blocking guardrail")
	assert.NotContains(t, line, "ox plan --open")
	// single line — grepability invariant
	assert.NotContains(t, line, "\n")
	assertNudgeNeverAutoOpensBrowser(t, line)
}

func TestFormatPlanNudgeLine_SingularCollision(t *testing.T) {
	var res planJSONResult
	res.Signals.Collisions = 1
	line := formatPlanNudgeLine(res, config.PlanOpenAsk)
	assert.Contains(t, line, "1 collision in")
	assert.NotContains(t, line, "1 collisions")
	assertNudgeNeverAutoOpensBrowser(t, line)
}

// TestFormatPlanNudgeLine_NonTrivialOnly verifies the render-focused line used
// when no team-context signals fired but the plan is structurally non-trivial.
// Failure prevented: a large greenfield plan gets a line claiming team-context
// signals that didn't fire, or no HTML-render framing at all.
func TestFormatPlanNudgeLine_NonTrivialOnly(t *testing.T) {
	t.Run("files and steps", func(t *testing.T) {
		var res planJSONResult
		res.Signals.NonTrivial = true
		res.Signals.Files = 7
		res.Signals.Steps = 6

		line := formatPlanNudgeLine(res, config.PlanOpenAsk)
		assert.Contains(t, line, "7 files")
		assert.Contains(t, line, "6 steps")
		assert.Contains(t, line, "SageOx team-context-optimized plan")
		assert.Contains(t, line, "ox plan render --open")
		assert.Contains(t, line, "ox plan review await", "non-trivial nudge must also point to await")
		assert.Contains(t, line, "confirm", "ask-before-blocking guardrail on the non-trivial path too")
		assert.NotContains(t, line, "collision", "no team-context signal fired — must not be mentioned")
		assert.NotContains(t, line, "\n", "single line — grepability invariant")
		assertNudgeNeverAutoOpensBrowser(t, line)
	})

	t.Run("files only (steps below threshold)", func(t *testing.T) {
		var res planJSONResult
		res.Signals.NonTrivial = true
		res.Signals.Files = 4
		res.Signals.Steps = 2 // below nonTrivialMinStepsHook — must not be named

		line := formatPlanNudgeLine(res, config.PlanOpenAsk)
		assert.Contains(t, line, "4 files")
		assert.NotContains(t, line, "step", "steps below threshold must not be named")
		assert.Contains(t, line, "SageOx team-context-optimized plan")
		assert.NotContains(t, line, "\n")
		assertNudgeNeverAutoOpensBrowser(t, line)
	})

	t.Run("steps only (files below threshold)", func(t *testing.T) {
		var res planJSONResult
		res.Signals.NonTrivial = true
		res.Signals.Files = 1 // below nonTrivialMinFilesHook — must not be named
		res.Signals.Steps = 7

		line := formatPlanNudgeLine(res, config.PlanOpenAsk)
		assert.Contains(t, line, "7 steps")
		assert.NotContains(t, line, "file", "files below threshold must not be named")
		assert.Contains(t, line, "SageOx team-context-optimized plan")
		assert.NotContains(t, line, "\n")
		assertNudgeNeverAutoOpensBrowser(t, line)
	})
}

// --- B2. Never-auto-open invariant (ox-mj0s CRITICAL correction) ---
//
// Ryan's correction on ox-mj0s: devs would be furious if ox opened a browser
// without permission. So no nudge this file produces may ever instruct the
// agent to open one before it has asked. These checks are structural (string
// order), not a fuzzy prose match, specifically so a future wording edit can't
// quietly regress the guarantee while still "reading" as gated to a human
// skimming the diff.

// assertNudgeNeverAutoOpensBrowser is the hard regression guard: every mention
// of a browser-opening command (--open, or the `ox plan review` loop, which
// itself launches a browser — see runPlanReview) must appear strictly AFTER
// the instruction to call AskUserQuestion, and the line must name an explicit
// "yes" gate.
func assertNudgeNeverAutoOpensBrowser(t *testing.T, line string) {
	t.Helper()
	askIdx := strings.Index(line, "AskUserQuestion")
	require.GreaterOrEqualf(t, askIdx, 0, "nudge must direct the agent to AskUserQuestion before any browser action: %q", line)
	assert.Contains(t, line, "explicit yes", "nudge must gate opening behind an explicit yes")

	if openIdx := strings.Index(line, "--open"); openIdx >= 0 {
		assert.Greaterf(t, openIdx, askIdx, "nudge mentions --open before AskUserQuestion — reads as an unconditional auto-open: %q", line)
	}
	if reviewIdx := strings.Index(line, "ox plan review"); reviewIdx >= 0 {
		assert.Greaterf(t, reviewIdx, askIdx, "nudge mentions the (browser-launching) review loop before AskUserQuestion: %q", line)
	}
}

// TestFormatPlanNudgeLine_NeverAutoOpensBrowser is the dedicated regression
// test for the invariant above, across every branch formatPlanNudgeLine can
// take. Failure prevented: a future wording edit silently reintroducing
// "render it ... with `ox plan render --open`" as the FIRST instruction
// (the pre-ox-mj0s wording) instead of gating it behind a human's yes.
func TestFormatPlanNudgeLine_NeverAutoOpensBrowser(t *testing.T) {
	cases := []struct {
		name string
		res  func() planJSONResult
	}{
		{"material all three signals", func() planJSONResult {
			var r planJSONResult
			r.Signals.Collisions = 2
			r.Signals.PriorArt = 1
			r.Signals.ExpertRoutes = 3
			r.Signals.Material = true
			return r
		}},
		{"material single collision", func() planJSONResult {
			var r planJSONResult
			r.Signals.Collisions = 1
			r.Signals.Material = true
			return r
		}},
		{"non-trivial files and steps", func() planJSONResult {
			var r planJSONResult
			r.Signals.NonTrivial = true
			r.Signals.Files = 7
			r.Signals.Steps = 6
			return r
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			line := formatPlanNudgeLine(tt.res(), config.PlanOpenAsk)
			assertNudgeNeverAutoOpensBrowser(t, line)
		})
	}
}

// TestFormatPlanNudgeLine_OpenPolicy pins the plan.open behavior contract across
// all three policies, for both the material and non-trivial nudge branches.
// Failure prevented: (1) plan.open becomes a dead setting the nudge ignores;
// (2) the auto-open escape hatch (always) leaks into ask/never, seizing a
// browser without the user's standing consent.
func TestFormatPlanNudgeLine_OpenPolicy(t *testing.T) {
	material := func() planJSONResult {
		var r planJSONResult
		r.Signals.Collisions = 2
		r.Signals.Material = true
		return r
	}
	nonTrivial := func() planJSONResult {
		var r planJSONResult
		r.Signals.NonTrivial = true
		r.Signals.Files = 7
		r.Signals.Steps = 6
		return r
	}
	for _, branch := range []struct {
		name string
		res  func() planJSONResult
	}{{"material", material}, {"non-trivial", nonTrivial}} {
		t.Run(branch.name, func(t *testing.T) {
			// ask (default): gated behind AskUserQuestion, and offers to persist.
			ask := formatPlanNudgeLine(branch.res(), config.PlanOpenAsk)
			assertNudgeNeverAutoOpensBrowser(t, ask)
			assert.Contains(t, ask, "ox config set plan.open", "ask mode must offer to persist the preference from the prompt")

			// always: the ONE sanctioned auto-open — names --open, no ask required.
			always := formatPlanNudgeLine(branch.res(), config.PlanOpenAlways)
			assert.Contains(t, always, "plan.open=always")
			assert.Contains(t, always, "ox plan render --open", "always mode must instruct a direct open")
			assert.NotContains(t, always, "AskUserQuestion", "always mode is the user's standing consent — it must NOT re-prompt")

			// never: no prompt, no open instruction — any open is user-initiated.
			never := formatPlanNudgeLine(branch.res(), config.PlanOpenNever)
			assert.Contains(t, never, "plan.open=never")
			assert.Contains(t, never, "do NOT prompt to open", "never mode must tell the agent not to prompt or open")
			assert.NotContains(t, never, "AskUserQuestion", "never mode must not prompt at all")
		})
	}
}

// --- B3. Subprocess arg shape (structural, not just runtime, guarantees) ---

// TestPlanRenderArgs_NeverOpensBrowser guards the render step's args at the
// slice level — planRenderArgs must never include --open or -o/--output. This
// is stronger than a runtime check: because runPlanRenderNoOpen builds its
// command exclusively from planRenderArgs(), this test proves BY CONSTRUCTION
// that the hook's background render cannot open a browser, independent of
// anything formatPlanNudgeLine's wording claims.
func TestPlanRenderArgs_NeverOpensBrowser(t *testing.T) {
	args := planRenderArgs()
	assert.Equal(t, []string{"plan", "render"}, args, "no slug/flags — the fresh/stdin render path, matching the plan text already on hand")
	for _, a := range args {
		assert.NotEqual(t, "--open", a)
		assert.NotEqual(t, "-o", a)
		assert.NotEqual(t, "--output", a)
	}
}

// TestPlanEnrichArgs_Shape guards the enrichment step's args: --persist must
// stay (durability — a draft lands on the ledger the moment plan mode exits)
// and --json must stay (the hook parses stdout as planJSONResult).
func TestPlanEnrichArgs_Shape(t *testing.T) {
	assert.Equal(t, []string{"plan", "enrich", "--json", "--persist"}, planEnrichArgs())
}

// TestPlanNudgeThresholds_MatchPlanPackage guards the deliberately-duplicated
// non-triviality thresholds: the hook keeps local copies for wording, but they
// must never silently diverge from internal/plan's authoritative values (a
// divergence would make planScopePhrase mis-word the nudge with no other signal).
// Failure prevented: the plan package changes a threshold and the hook's wording
// gate drifts out of sync unnoticed.
func TestPlanNudgeThresholds_MatchPlanPackage(t *testing.T) {
	assert.Equal(t, plan.NonTrivialMinFiles, nonTrivialMinFilesHook, "hook file-threshold mirror drifted from plan package")
	assert.Equal(t, plan.NonTrivialMinSteps, nonTrivialMinStepsHook, "hook step-threshold mirror drifted from plan package")
}

// --- B4. handlePlanExit end-to-end (stubbed subprocess seams) ---
//
// runPlanEnrichment / runPlanRenderNoOpen are package-level vars specifically
// so these tests can drive the full decision path — trivial / material /
// non-trivial × render / no-render × plan.html mode — without spawning a real
// `ox` subprocess.

// TestHandlePlanExit_Material_BackgroundRendersAndNudges verifies the earned
// aggression path: a Material plan (real team-context signals) triggers
// exactly one background render call (no --open) AND stashes a nudge that
// gates opening behind AskUserQuestion. Failure prevented: a material plan's
// SageOx-enriched view never gets materialized, so the human only ever sees a
// hand-authored plan and the signals silently drop on the floor.
func TestHandlePlanExit_Material_BackgroundRendersAndNudges(t *testing.T) {
	projectRoot := planNudgeProject(t)
	isolatePlanConfigEnv(t)
	agentID := "OxplanMaterial"

	var res planJSONResult
	res.Signals.Collisions = 2
	res.Signals.PriorArt = 1
	res.Signals.Material = true
	renderCalls := stubPlanSubprocesses(t, func(string) (planJSONResult, bool) { return res, true })

	planText := "# Refactor auth\n- touch internal/auth/session.go"
	handlePlanExit(planExitCtx(projectRoot, planText), agentID)

	assert.Equal(t, 1, *renderCalls, "material plan must trigger exactly one background render")

	var buf bytes.Buffer
	emitPlanNudge(&buf, projectRoot, agentID)
	got := buf.String()
	assert.Contains(t, got, "<system-reminder>")
	assert.Contains(t, got, "AskUserQuestion")
	assert.Contains(t, got, "2 collisions")
	assertNudgeNeverAutoOpensBrowser(t, got)
}

// TestHandlePlanExit_NonTrivialOnly_NudgesWithoutRender verifies the earned
// gate on the render step specifically: a structurally-substantial plan with
// NO team-context signals still gets nudged (kept behavior) but must NOT
// trigger the background render — that aggression is reserved for Material,
// where real signals earned it (ox-mj0s).
func TestHandlePlanExit_NonTrivialOnly_NudgesWithoutRender(t *testing.T) {
	projectRoot := planNudgeProject(t)
	isolatePlanConfigEnv(t)
	agentID := "OxplanNonTrivial"

	var res planJSONResult
	res.Signals.NonTrivial = true
	res.Signals.Files = 7
	res.Signals.Steps = 6
	renderCalls := stubPlanSubprocesses(t, func(string) (planJSONResult, bool) { return res, true })

	handlePlanExit(planExitCtx(projectRoot, "# Big greenfield plan\nstep one\nstep two"), agentID)

	assert.Equal(t, 0, *renderCalls, "non-trivial-only plans must not trigger the background render")

	var buf bytes.Buffer
	emitPlanNudge(&buf, projectRoot, agentID)
	got := buf.String()
	assert.Contains(t, got, "7 files")
	assert.Contains(t, got, "AskUserQuestion")
	assertNudgeNeverAutoOpensBrowser(t, got)
}

// TestHandlePlanExit_Trivial_NoNudgeNoRender verifies the earned gate holds at
// the top: a plan with neither team-context signals nor structural substance
// gets no nudge (the pre-existing invariant) and, by extension, no render
// attempt either.
func TestHandlePlanExit_Trivial_NoNudgeNoRender(t *testing.T) {
	projectRoot := planNudgeProject(t)
	isolatePlanConfigEnv(t)
	agentID := "OxplanTrivial"

	var res planJSONResult // all zero — trivial
	renderCalls := stubPlanSubprocesses(t, func(string) (planJSONResult, bool) { return res, true })

	handlePlanExit(planExitCtx(projectRoot, "# Tiny plan\nfix a typo"), agentID)

	assert.Equal(t, 0, *renderCalls, "trivial plan must not trigger the background render")

	var buf bytes.Buffer
	emitPlanNudge(&buf, projectRoot, agentID)
	assert.Empty(t, buf.String(), "trivial plan must not stash a nudge")
}

// TestHandlePlanExit_PlanHTMLOff_NoNudgeNoRender verifies plan.html=off keeps
// its documented meaning ("never render, never nudge") even for a Material
// plan that would otherwise earn the background render.
func TestHandlePlanExit_PlanHTMLOff_NoNudgeNoRender(t *testing.T) {
	projectRoot := planNudgeProject(t)
	isolatePlanConfigEnv(t)
	t.Setenv(config.EnvPlanHTML, config.PlanHTMLOff)
	agentID := "OxplanOff"

	var res planJSONResult
	res.Signals.Collisions = 3
	res.Signals.Material = true
	renderCalls := stubPlanSubprocesses(t, func(string) (planJSONResult, bool) { return res, true })

	handlePlanExit(planExitCtx(projectRoot, "# Material but silenced\ntouches shared code"), agentID)

	assert.Equal(t, 0, *renderCalls, "plan.html=off must suppress the render, even for a material plan")

	var buf bytes.Buffer
	emitPlanNudge(&buf, projectRoot, agentID)
	assert.Empty(t, buf.String(), "plan.html=off must suppress the nudge")
}

// TestHandlePlanExit_PlanSaveOff_SkipsRenderStillNudges verifies the
// plan.save guard on the render step: with capture disabled there is no
// ledger to render into, so the hook must not burn a subprocess on a
// guaranteed no-op — but the nudge (a pure stash, independent of capture)
// still fires so the human still gets the ask.
func TestHandlePlanExit_PlanSaveOff_SkipsRenderStillNudges(t *testing.T) {
	projectRoot := planNudgeProjectWithSaveOff(t)
	isolatePlanConfigEnv(t)
	agentID := "OxplanSaveOff"

	var res planJSONResult
	res.Signals.ExpertRoutes = 1
	res.Signals.Material = true
	renderCalls := stubPlanSubprocesses(t, func(string) (planJSONResult, bool) { return res, true })

	handlePlanExit(planExitCtx(projectRoot, "# Material, capture off\ntouches shared code"), agentID)

	assert.Equal(t, 0, *renderCalls, "plan.save=false must skip the render attempt (nothing to render into)")

	var buf bytes.Buffer
	emitPlanNudge(&buf, projectRoot, agentID)
	assert.NotEmpty(t, buf.String(), "the nudge itself does not depend on plan.save")
}

// --- C. Stash + emit roundtrip (deliver-once via UserPromptSubmit channel) ---

// TestPlanNudge_StashThenEmit verifies the full deliver path: a stashed nudge is
// emitted as a <system-reminder> on the next prompt and then removed (deliver-once).
// Failure prevented: nudge never reaches the model, or reaches it on every prompt.
func TestPlanNudge_StashThenEmit(t *testing.T) {
	projectRoot := planNudgeProject(t)
	agentID := "Oxplan1"

	require.NoError(t, stashPlanNudge(projectRoot, agentID, "Your plan touches 1 collision. Run `ox plan render --open`."))

	var buf bytes.Buffer
	emitPlanNudge(&buf, projectRoot, agentID)
	got := buf.String()
	assert.Contains(t, got, "<system-reminder>")
	assert.Contains(t, got, "[ox]")
	assert.Contains(t, got, "ox plan render --open")

	// deliver-once: file is gone, a second prompt emits nothing
	assert.NoFileExists(t, planNudgePath(projectRoot, agentID))
	var buf2 bytes.Buffer
	emitPlanNudge(&buf2, projectRoot, agentID)
	assert.Empty(t, buf2.String(), "nudge must deliver exactly once")
}

// TestPlanNudge_NoPendingNudge verifies emit is a clean no-op with nothing stashed.
func TestPlanNudge_NoPendingNudge(t *testing.T) {
	projectRoot := planNudgeProject(t)
	var buf bytes.Buffer
	emitPlanNudge(&buf, projectRoot, "Oxnone")
	assert.Empty(t, buf.String())
}

// TestPlanNudge_StaleDiscarded verifies a nudge older than planNudgeMaxAge is
// discarded (and removed) rather than surfaced on an unrelated later prompt.
// Failure prevented: a day-old plan nudge resurfaces mid-unrelated-task.
func TestPlanNudge_StaleDiscarded(t *testing.T) {
	projectRoot := planNudgeProject(t)
	agentID := "Oxstale"
	require.NoError(t, stashPlanNudge(projectRoot, agentID, "stale nudge"))

	// backdate the file mtime well past the max age
	path := planNudgePath(projectRoot, agentID)
	old := time.Now().Add(-2 * planNudgeMaxAge)
	require.NoError(t, os.Chtimes(path, old, old))

	var buf bytes.Buffer
	emitPlanNudge(&buf, projectRoot, agentID)
	assert.Empty(t, buf.String(), "stale nudge must not surface")
	assert.NoFileExists(t, path, "stale nudge must be removed so it never resurfaces")
}

// TestPlanNudge_LatestWins verifies a second stash overwrites the first — the
// most recent plan exit is the one delivered.
func TestPlanNudge_LatestWins(t *testing.T) {
	projectRoot := planNudgeProject(t)
	agentID := "Oxlatest"
	require.NoError(t, stashPlanNudge(projectRoot, agentID, "first nudge"))
	require.NoError(t, stashPlanNudge(projectRoot, agentID, "second nudge"))

	var buf bytes.Buffer
	emitPlanNudge(&buf, projectRoot, agentID)
	got := buf.String()
	assert.Contains(t, got, "second nudge")
	assert.NotContains(t, got, "first nudge")
}

// TestPlanNudge_EmptyArgs verifies path/emit/stash are safe with empty inputs.
func TestPlanNudge_EmptyArgs(t *testing.T) {
	assert.Empty(t, planNudgePath("", "Oxa"))
	assert.Empty(t, planNudgePath("/tmp", ""))

	var buf bytes.Buffer
	emitPlanNudge(&buf, "", "Oxa")
	emitPlanNudge(&buf, "/tmp", "")
	assert.Empty(t, buf.String())

	assert.Error(t, stashPlanNudge("", "Oxa", "x"))
}
