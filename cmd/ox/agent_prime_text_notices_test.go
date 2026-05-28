package main

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ADR-019/020 text-mode notice rendering (PR #627 follow-up) ---
// Failure prevented: outputAgentPrimeText previously rendered only the
// typed notice boxes (HooksInstalled, SupportNotice, PrimeExcessiveNotice,
// UpdateAvailable). UserNotices of types "clear-boundary" and
// "session-skipped" had no dedicated renderer, so under --text mode the
// /clear lifecycle boundary handoff and the paused-parent subagent skip
// notice both vanished — the JSON path saw them, the text path did not.

// TestOutputAgentPrimeText_RendersClearBoundaryNotice — Failure prevented:
// the clear-boundary notice (post-/clear session transition) is silently
// dropped under --text.
func TestOutputAgentPrimeText_RendersClearBoundaryNotice(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	output := agentPrimeOutput{
		AgentID: "Oxtest",
		Status:  "fresh",
		UserNotices: []UserNotice{
			{Type: "clear-boundary", Message: "Prior session 2026-05-28-foo finalized. Recording: paused."},
		},
	}
	require.NoError(t, outputAgentPrimeText(cmd, output))

	got := buf.String()
	assert.Contains(t, got, "Prior session 2026-05-28-foo finalized",
		"clear-boundary notice must appear in --text output; previously it was silently dropped")
}

// TestOutputAgentPrimeText_RendersSessionSkippedNotice — Failure
// prevented: paused-parent subagent skip notice silently dropped under
// --text. The subagent agent ends up thinking recording is "available"
// when it was just intentionally skipped.
func TestOutputAgentPrimeText_RendersSessionSkippedNotice(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	output := agentPrimeOutput{
		AgentID: "OxsubA",
		Status:  "fresh",
		UserNotices: []UserNotice{
			{Type: "session-skipped", Message: "[ox] Parent session suspended. Recording skipped for this subagent."},
		},
	}
	require.NoError(t, outputAgentPrimeText(cmd, output))

	got := buf.String()
	assert.Contains(t, got, "Recording skipped for this subagent",
		"session-skipped notice must appear in --text output")
}

// TestOutputAgentPrimeText_FilterSkipsAlreadyRenderedTypes — Failure
// prevented: appending to UserNotices for a type that already has a
// dedicated renderer (upgrade box, hooks-restart box, support box) double-
// renders under --text via the new UserNotices loop. The loop must skip
// those types and only emit unrendered ones (clear-boundary, session-
// skipped, future additions).
func TestOutputAgentPrimeText_FilterSkipsAlreadyRenderedTypes(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	output := agentPrimeOutput{
		AgentID:         "Oxfilt",
		Status:          "fresh",
		UpdateAvailable: true,
		UpdateHint:      "v1.2.3 -> v1.2.4 upgrade hint",
		UserNotices: []UserNotice{
			{Type: "upgrade", Message: "UPGRADE_MSG_SENTINEL"},
			{Type: "support", Message: "SUPPORT_MSG_SENTINEL"},
			{Type: "restart", Message: "RESTART_MSG_SENTINEL"},
			{Type: "clear-boundary", Message: "BOUNDARY_MSG_SENTINEL"},
		},
	}
	require.NoError(t, outputAgentPrimeText(cmd, output))

	got := buf.String()
	// dedicated-box notices route to stderr, not to cmd.OutOrStdout; the
	// filter MUST keep them out of the loop too so we don't double-render.
	assert.NotContains(t, got, "UPGRADE_MSG_SENTINEL", "upgrade notice must not appear in the UserNotices loop output")
	assert.NotContains(t, got, "SUPPORT_MSG_SENTINEL", "support notice must not appear in the UserNotices loop output")
	assert.NotContains(t, got, "RESTART_MSG_SENTINEL", "restart notice must not appear in the UserNotices loop output")
	// clear-boundary has no dedicated renderer — MUST appear.
	assert.Contains(t, got, "BOUNDARY_MSG_SENTINEL", "clear-boundary notice must appear via the new UserNotices loop")
}
