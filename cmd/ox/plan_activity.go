package main

// plan_activity.go is the CLIENT half of the caller-driven plan_id -> plan
// index (bead sageox-gqgkg): the server's POST .../activity endpoint
// currently accepts and drops the body (a stub) until it actually builds the
// index. Wiring the client now means the server can start populating the
// index later with zero further client changes.
//
// postPlanActivityBestEffort is called from the two places a lifecycle event
// actually gets appended: runPlanLifecycleVerbOnDir (plan_lifecycle.go) and
// the review server's /approve handler (plan_review.go) — the same two call
// sites of plan.AppendPlanEvent. It is a stub, deliberately minimal: no
// retry, no queue, no persistence of failed attempts. It NEVER returns an
// error and NEVER blocks the command on network latency beyond the
// RepoClient's own bounded timeout (10s, same as every other Notify* call in
// internal/api) — a failure here is purely advisory and must never affect
// the local events.jsonl append that already succeeded.
//
// Off by default: a project not yet linked to a team (no TeamID in
// .sageox/config.json) or a machine with no logged-in auth token — the
// common case for local dev / CI — silently skips before any network call is
// attempted. No separate feature flag on top of that: this mirrors the exact
// gate notifySessionUploaded (session_linkage_finalize.go) already uses for
// its own best-effort server notification.

import (
	"log/slog"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/plan"
)

// planActivityTimeout bounds the advisory activity notification. See the
// client construction below for why it is not the 10s client default.
const planActivityTimeout = 2 * time.Second

// postPlanActivityBestEffort reports the just-appended lifecycle event kind
// to the server's plan-activity index. Resolves team_id (project config),
// plan_id (folded from planDir's events.jsonl), server URL, and auth token
// entirely from existing ox config/provenance — if any piece is
// unavailable, it silently returns without attempting a network call.
func postPlanActivityBestEffort(gitRoot, planDir string, kind plan.EventKind) {
	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil || cfg == nil || cfg.TeamID == "" {
		return // project not linked to a team — nothing to index against
	}
	ep := endpoint.GetForProject(gitRoot)
	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil || token == nil || token.AccessToken == "" {
		return // not authenticated — the common local/CI case
	}

	events, err := plan.LoadEvents(planDir)
	if err != nil || len(events) == 0 {
		return
	}
	folded := plan.Fold(events)
	if folded.PlanID == "" {
		return
	}

	// Short timeout, deliberately far below the client default of 10s. This runs
	// synchronously on the plan-save path, which an AI coworker's turn blocks
	// on — and that path already does a network git push. Stalling a coworker
	// for 10s to deliver an advisory index hint is strictly worse than not
	// delivering it; the server's periodic ledger sweep is the backstop.
	client := api.NewRepoClientWithEndpoint(ep).
		WithAuthToken(token.AccessToken).
		WithTimeout(planActivityTimeout)
	n := api.PlanActivityNotification{Event: string(kind), Status: string(folded.Status)}
	if err := client.NotifyPlanActivity(cfg.TeamID, folded.PlanID, n); err != nil {
		slog.Debug("plan activity notify failed", "error", err, "plan_id", folded.PlanID, "kind", kind)
	}
}
