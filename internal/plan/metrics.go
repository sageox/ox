package plan

import "log/slog"

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
