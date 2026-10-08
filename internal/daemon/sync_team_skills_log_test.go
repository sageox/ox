package daemon

import (
	"log/slog"
	"testing"

	"github.com/sageox/ox/internal/teamconverge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A pending artifact is a deferral by design, not an incident: it must not be
// a WARN. Failure prevented (2026-10-08): 29 skills held stable by an active
// coworker session produced 87 WARN lines in half an hour, burying the real
// warnings around them.
func TestLogConvergenceOutcomes_PendingIsNotAWarning(t *testing.T) {
	h := &recordingHandler{}
	report := teamconverge.Report{Outcomes: []teamconverge.Outcome{
		{Kind: "skill", Name: "scan-first", State: teamconverge.StatePending, Detail: "active AI coworker session keeps the current skill snapshot stable"},
		{Kind: "skill", Name: "needs-ok", State: teamconverge.StatePendingApproval},
		{Kind: "skill", Name: "broken", State: teamconverge.StateError, Detail: "boom"},
		{Kind: "skill", Name: "clash", State: teamconverge.StateConflict},
		{Kind: "skill", Name: "fine", State: teamconverge.StateApplied},
	}}

	counts := logConvergenceOutcomes(slog.New(h), "/repo", report)

	var warned []string
	for _, r := range h.records {
		if r.Level >= slog.LevelWarn {
			r.Attrs(func(a slog.Attr) bool {
				if a.Key == "name" {
					warned = append(warned, a.Value.String())
				}
				return true
			})
		}
	}
	assert.ElementsMatch(t, []string{"broken", "clash"}, warned, "only error and conflict warrant a WARN")
	require.Equal(t, 2, counts[teamconverge.StatePending]+counts[teamconverge.StatePendingApproval], "pending artifacts are still counted for the cycle summary")
	assert.Equal(t, 1, counts[teamconverge.StateApplied])
}
