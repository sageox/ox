package main

// plan_activity.go is the CLIENT half of the caller-driven plan_id -> plan
// index (bead sageox-gqgkg): the ping tells SageOx which repo and plan folder
// hold the plan, so a just-saved plan's link can open on the first click
// instead of waiting for SageOx to find the plan on its own.
//
// postPlanActivityBestEffort is called wherever a plan event gets appended:
// savePlanArtifacts (plan.go), runPlanLifecycleVerbOnDir (plan_lifecycle.go)
// and the review server's /approve handler (plan_review.go). It is
// deliberately minimal: no retry, no queue, no persistence of failed
// attempts. It NEVER returns an error and NEVER blocks the command on network
// latency beyond planActivityTimeout — a failure here (including a 400 from
// the server) is purely advisory and must never affect the local
// events.jsonl append that already succeeded.
//
// Off by default: a project not yet linked to a team (no TeamID in
// .sageox/config.json) or a machine with no logged-in auth token — the
// common case for local dev / CI — silently skips before any network call is
// attempted. No separate feature flag on top of that: this mirrors the exact
// gate notifySessionUploaded (session_linkage_finalize.go) already uses for
// its own best-effort server notification.

import (
	"log/slog"
	"path/filepath"
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

// postPlanActivityBestEffort reports the just-appended lifecycle event kind,
// and where the plan lives, to the server's plan-activity index. Resolves
// team_id and repo_id (project config), plan_id (folded from planDir's
// events.jsonl), the plan folder name (planDir's base name), server URL, and
// auth token entirely from existing ox config/provenance — if any required
// piece is unavailable, it silently returns without attempting a network call.
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
	// Sent on every ping, even when the Ledger push failed: a location that holds
	// no matching plan is simply not used, so no special case is needed.
	n := api.PlanActivityNotification{
		Event:  string(kind),
		Status: string(folded.Status),
		RepoID: cfg.RepoID,
		Dir:    filepath.Base(planDir),
	}
	if err := client.NotifyPlanActivity(cfg.TeamID, folded.PlanID, n); err != nil {
		slog.Debug("plan activity notify failed", "error", err, "plan_id", folded.PlanID, "kind", kind)
	}
}
