package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

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

	line := formatPlanNudgeLine(res)
	assert.Contains(t, line, "2 collisions")
	assert.Contains(t, line, "1 prior-art match")
	assert.NotContains(t, line, "expert route", "expert routes did not fire — must not be mentioned")
	assert.Contains(t, line, "ox plan render --open")
	assert.NotContains(t, line, "ox plan --open")
	// single line — grepability invariant
	assert.NotContains(t, line, "\n")
}

func TestFormatPlanNudgeLine_SingularCollision(t *testing.T) {
	var res planJSONResult
	res.Signals.Collisions = 1
	line := formatPlanNudgeLine(res)
	assert.Contains(t, line, "1 collision in")
	assert.NotContains(t, line, "1 collisions")
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

		line := formatPlanNudgeLine(res)
		assert.Contains(t, line, "7 files")
		assert.Contains(t, line, "6 steps")
		assert.Contains(t, line, "HTML page")
		assert.Contains(t, line, "ox plan render --open")
		assert.NotContains(t, line, "collision", "no team-context signal fired — must not be mentioned")
		assert.NotContains(t, line, "\n", "single line — grepability invariant")
	})

	t.Run("files only (steps below threshold)", func(t *testing.T) {
		var res planJSONResult
		res.Signals.NonTrivial = true
		res.Signals.Files = 4
		res.Signals.Steps = 2 // below nonTrivialMinStepsHook — must not be named

		line := formatPlanNudgeLine(res)
		assert.Contains(t, line, "4 files")
		assert.NotContains(t, line, "step", "steps below threshold must not be named")
		assert.Contains(t, line, "HTML page")
		assert.NotContains(t, line, "\n")
	})

	t.Run("steps only (files below threshold)", func(t *testing.T) {
		var res planJSONResult
		res.Signals.NonTrivial = true
		res.Signals.Files = 1 // below nonTrivialMinFilesHook — must not be named
		res.Signals.Steps = 7

		line := formatPlanNudgeLine(res)
		assert.Contains(t, line, "7 steps")
		assert.NotContains(t, line, "file", "files below threshold must not be named")
		assert.Contains(t, line, "HTML page")
		assert.NotContains(t, line, "\n")
	})
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
