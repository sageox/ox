package daemon

import (
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/sageox/ox/internal/teamconverge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sessionHeld is the outcome teamconverge produces for a skill a live AI
// coworker session is holding stable.
func sessionHeld(name string) teamconverge.Outcome {
	return teamconverge.Outcome{
		Kind: teamconverge.KindSkill, Name: name, State: teamconverge.StatePending,
		Detail:   "active AI coworker session keeps the current skill snapshot stable",
		Deferral: teamconverge.DeferralActiveSession,
	}
}

func attrsOf(r slog.Record) map[string]string {
	attrs := map[string]string{}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	return attrs
}

// A pending artifact is a deferral by design, not an incident: it must not be
// a WARN. Failure prevented (2026-10-08): 29 skills held stable by an active
// coworker session produced 87 WARN lines in half an hour, burying the real
// warnings around them.
func TestLogConvergenceOutcomes_PendingIsNotAWarning(t *testing.T) {
	h := &recordingHandler{}
	report := teamconverge.Report{Outcomes: []teamconverge.Outcome{
		sessionHeld("scan-first"),
		{Kind: "skill", Name: "needs-ok", State: teamconverge.StatePendingApproval},
		{Kind: "skill", Name: "broken", State: teamconverge.StateError, Detail: "boom"},
		{Kind: "skill", Name: "clash", State: teamconverge.StateConflict},
		{Kind: "skill", Name: "fine", State: teamconverge.StateApplied},
	}}

	counts := logConvergenceOutcomes(slog.New(h), "/repo", report)

	var warned []string
	for _, r := range h.records {
		if r.Level >= slog.LevelWarn {
			warned = append(warned, attrsOf(r)["name"])
		}
	}
	assert.ElementsMatch(t, []string{"broken", "clash"}, warned, "only error and conflict warrant a WARN")
	require.Equal(t, 2, counts[teamconverge.StatePending]+counts[teamconverge.StatePendingApproval], "pending artifacts are still counted for the cycle summary")
	assert.Equal(t, 1, counts[teamconverge.StateApplied])
}

// N skills held by an active session are ONE INFO line per pass, while a skill
// pending for any other reason is still a WARN.
// Failure prevented (ox#1242): one pass wrote 90 identical
// `team artifact not converged` WARN lines, one per skill, for the designed
// steady state — and a real stall in the same pass looked exactly like them.
func TestLogConvergenceOutcomes_SessionHoldIsOneInfoSummary(t *testing.T) {
	h := &recordingHandler{}
	outcomes := make([]teamconverge.Outcome, 0, 91)
	for i := range 90 {
		outcomes = append(outcomes, sessionHeld(fmt.Sprintf("skill-%02d", i)))
	}
	outcomes = append(outcomes, teamconverge.Outcome{
		Kind: teamconverge.KindSkill, Name: "stalled", State: teamconverge.StatePending,
		Detail: "skill reconcile already in progress",
	})

	counts := logConvergenceOutcomes(slog.New(h), "/repo", teamconverge.Report{
		Snapshot: teamconverge.Snapshot{Commit: "abc123"}, Outcomes: outcomes,
	})

	summaries := h.byMessage("team_artifacts_pending")
	require.Len(t, summaries, 1, "the session hold must be one summary line per pass")
	assert.Equal(t, slog.LevelInfo, summaries[0].Level)
	attrs := attrsOf(summaries[0])
	assert.Equal(t, "90", attrs["count"])
	assert.Equal(t, "active-session", attrs["reason"])

	var warnings []slog.Record
	for _, r := range h.records {
		if r.Level >= slog.LevelWarn {
			warnings = append(warnings, r)
		}
	}
	require.Len(t, warnings, 1, "only the artifact pending for another reason warrants a WARN")
	stalled := attrsOf(warnings[0])
	assert.Equal(t, "skill reconcile already in progress", stalled["reason"])
	assert.Equal(t, "skill/stalled", stalled["artifacts"])
	assert.Len(t, h.records, 2, "nothing per-artifact may be logged for the held skills")
	assert.Equal(t, 91, counts[teamconverge.StatePending])
}

// Skills a repository cannot receive share their reason; that is one WARN naming
// them, not one per skill.
func TestLogConvergenceOutcomes_SkippedIsOneWarningPerReason(t *testing.T) {
	h := &recordingHandler{}
	report := teamconverge.Report{Outcomes: []teamconverge.Outcome{
		{Kind: teamconverge.KindSkill, Name: "a", State: teamconverge.StateUnsupported, Detail: "no native skill target"},
		{Kind: teamconverge.KindSkill, Name: "b", State: teamconverge.StateUnsupported, Detail: "no native skill target"},
	}}
	logConvergenceOutcomes(slog.New(h), "/repo", report)
	skipped := h.byMessage("team artifacts skipped")
	require.Len(t, skipped, 1)
	assert.Equal(t, slog.LevelWarn, skipped[0].Level)
	assert.Equal(t, "skill/a,skill/b", attrsOf(skipped[0])["artifacts"])
	assert.Len(t, h.records, 1)
}

// A whole pass the session held (retiring the last skill or rule while a session
// runs) has no outcome rows to summarize; it is still a deferral, not a failure.
func TestLogConvergenceError_SessionHoldIsInfo(t *testing.T) {
	h := &recordingHandler{}
	held := fmt.Errorf("converge: %w", teamconverge.ErrSessionHold)
	logConvergenceError(slog.New(h), "/repo", held)
	logConvergenceError(slog.New(h), "/repo", errors.New("team context snapshot is busy"))

	require.Len(t, h.byMessage("team context convergence deferred"), 1)
	assert.Equal(t, slog.LevelInfo, h.byMessage("team context convergence deferred")[0].Level)
	failed := h.byMessage("team context convergence failed")
	require.Len(t, failed, 1)
	assert.Equal(t, slog.LevelWarn, failed[0].Level)
}
