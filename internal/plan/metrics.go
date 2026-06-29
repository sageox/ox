package plan

import (
	"log/slog"
	"strings"
)

// RecordPlanGenerated emits a single-line, key=value structured metric for a
// completed `ox plan` enrichment. It is purely local observability — there is
// no server LLM in this path, so there is nothing to meter server-side. The
// counts let us see, in aggregate, how often plans fire collision / prior-art /
// expert signals and how much context the bundle carried, without recording any
// plan content.
//
// saved reports whether the enriched plan was captured to the ledger. It is a
// separate boolean (not derived from res) because auto-save is gated on config
// and on a configured ledger, independent of the signal summary.
func RecordPlanGenerated(res Result, saved bool) {
	slog.Info("plan_generated",
		"collisions", res.Signals.Collisions,
		"prior_art", res.Signals.PriorArt,
		"expert_routes", res.Signals.ExpertRoutes,
		"material", res.Signals.Material,
		"annotations", len(res.Annotations),
		"context_items", len(res.Context),
		"saved", saved,
	)
}

// RecordPlanCraft emits the design-craft realization metric for a rendered plan:
// how many visual craft expectations ox produced (a diagram suggested, a
// user-facing surface detected) and how many the render actually realized. The
// hints_emitted vs hints_realized ratio, aggregated across the ledger, is the
// visual-enrichment "did the agent act on the hint" rate — the tachometer for
// whether enrichment changes what gets drawn, not just what gets suggested. No
// plan content is recorded, only counts and the unrealized rule names. A render
// with no craft expectation is silent (nothing to measure).
func RecordPlanCraft(rep CraftReport) {
	if rep.Emitted == 0 {
		return
	}
	gaps := make([]string, 0, len(rep.Gaps))
	for _, g := range rep.Gaps {
		gaps = append(gaps, g.Rule)
	}
	slog.Info("plan_craft",
		"hints_emitted", rep.Emitted,
		"hints_realized", rep.Realized,
		"gaps", strings.Join(gaps, ","),
	)
}
