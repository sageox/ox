package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	assert.Contains(t, line, "ox plan")
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

// --- C. Stash + emit roundtrip (deliver-once via UserPromptSubmit channel) ---

// TestPlanNudge_StashThenEmit verifies the full deliver path: a stashed nudge is
// emitted as a <system-reminder> on the next prompt and then removed (deliver-once).
// Failure prevented: nudge never reaches the model, or reaches it on every prompt.
func TestPlanNudge_StashThenEmit(t *testing.T) {
	projectRoot := planNudgeProject(t)
	agentID := "Oxplan1"

	require.NoError(t, stashPlanNudge(projectRoot, agentID, "Your plan touches 1 collision. Run `ox plan`."))

	var buf bytes.Buffer
	emitPlanNudge(&buf, projectRoot, agentID)
	got := buf.String()
	assert.Contains(t, got, "<system-reminder>")
	assert.Contains(t, got, "[ox]")
	assert.Contains(t, got, "ox plan")

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
