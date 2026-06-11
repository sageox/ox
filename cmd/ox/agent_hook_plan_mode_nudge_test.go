package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// --- B. Hint lifecycle: once per plan-mode entry ---

// TestEmitPlanModeHint_FiresOncePerEntry verifies the core throttle: the hint
// fires on the first plan-mode prompt, suppresses on subsequent plan-mode
// prompts (same entry), then re-fires after the agent leaves and re-enters plan
// mode. Failure prevented: the hint spams every prompt, or never re-fires.
func TestEmitPlanModeHint_FiresOncePerEntry(t *testing.T) {
	projectRoot := planNudgeProject(t)
	agentID := "Oxmode1"
	planPrompt := []byte(`{"permission_mode":"plan","prompt":"plan it"}`)
	normalPrompt := []byte(`{"permission_mode":"default","prompt":"go"}`)

	// 1. first plan-mode prompt: hint fires
	var buf bytes.Buffer
	emitPlanModeHint(&buf, projectRoot, agentID, planPrompt)
	got := buf.String()
	assert.Contains(t, got, "<system-reminder>")
	assert.Contains(t, got, "[ox]")
	assert.Contains(t, got, "ox plan enrich --json")
	assert.Contains(t, got, "ox plan render --open")
	assert.Contains(t, got, "SageOx team-context-optimized plan")
	assert.NotContains(t, got, "\n<", "hint must be a single system-reminder line")

	// 2. second plan-mode prompt, same entry: suppressed
	var buf2 bytes.Buffer
	emitPlanModeHint(&buf2, projectRoot, agentID, planPrompt)
	assert.Empty(t, buf2.String(), "must not re-hint within the same plan-mode entry")

	// 3. agent leaves plan mode: stamp cleared, nothing emitted
	var buf3 bytes.Buffer
	emitPlanModeHint(&buf3, projectRoot, agentID, normalPrompt)
	assert.Empty(t, buf3.String(), "non-plan prompt emits nothing")
	assert.NoFileExists(t, planModeHintPath(projectRoot, agentID), "leaving plan mode clears the stamp")

	// 4. re-enters plan mode: hint fires again
	var buf4 bytes.Buffer
	emitPlanModeHint(&buf4, projectRoot, agentID, planPrompt)
	assert.Contains(t, buf4.String(), "ox plan enrich --json", "re-entering plan mode must re-hint")
}

// TestEmitPlanModeHint_NonClaudeNoOp verifies that a payload with no
// permission-mode field (every non-Claude agent) produces no hint and writes no
// stamp. Failure prevented: the Gold-only feature leaks to agents that can't
// deliver it.
func TestEmitPlanModeHint_NonClaudeNoOp(t *testing.T) {
	projectRoot := planNudgeProject(t)
	agentID := "Oxsilver"
	var buf bytes.Buffer
	emitPlanModeHint(&buf, projectRoot, agentID, []byte(`{"prompt":"do work"}`))
	assert.Empty(t, buf.String())
	assert.NoFileExists(t, planModeHintPath(projectRoot, agentID))
}

// TestEmitPlanModeHint_EmptyArgs verifies path/emit are safe with empty inputs.
func TestEmitPlanModeHint_EmptyArgs(t *testing.T) {
	assert.Empty(t, planModeHintPath("", "Oxa"))
	assert.Empty(t, planModeHintPath("/tmp", ""))

	var buf bytes.Buffer
	emitPlanModeHint(&buf, "", "Oxa", []byte(`{"permission_mode":"plan"}`))
	emitPlanModeHint(&buf, "/tmp", "", []byte(`{"permission_mode":"plan"}`))
	assert.Empty(t, buf.String())
}

// TestEmitPlanModeHint_StampPersistsAcrossEntry verifies the stamp file exists
// while in plan mode (so repeat prompts stay suppressed) and is keyed per agent.
func TestEmitPlanModeHint_StampPersistsAcrossEntry(t *testing.T) {
	projectRoot := planNudgeProject(t)
	agentA := "OxA"
	agentB := "OxB"
	planPrompt := []byte(`{"permission_mode":"plan"}`)

	var buf bytes.Buffer
	emitPlanModeHint(&buf, projectRoot, agentA, planPrompt)
	require.FileExists(t, planModeHintPath(projectRoot, agentA))

	// agent B is independent — its first plan-mode prompt still hints
	var bufB bytes.Buffer
	emitPlanModeHint(&bufB, projectRoot, agentB, planPrompt)
	assert.Contains(t, bufB.String(), "ox plan enrich --json", "per-agent stamp must not bleed across agents")
}
