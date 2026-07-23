package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
)

// Plan-exit enrichment nudge (Gold tier — Claude Code).
//
// The closest plan-exit signal Claude Code exposes is the PostToolUse event
// firing after the ExitPlanMode tool. We do NOT install a separate, always-on
// PostToolUse hook for this — that event already has an ox hook installed (see
// claudeLifecycleEvents), and handleAfterTool runs on every tool. We add a
// narrow, strictly-gated branch: only when ToolName == "ExitPlanMode" do we do
// any plan work. Every other tool is untouched, so this is NOT a noisy hook.
//
// Delivery channel: PostToolUse stdout is COMPLETELY DISCARDED by Claude Code
// (empirically confirmed — see the table in agent_hook.go). So we cannot emit
// the nudge from the PostToolUse handler itself. Instead, the PostToolUse
// branch stashes a one-line nudge to a per-agent pending file, and the next
// UserPromptSubmit (handlePrompt — the ONLY proven stdout-injection channel)
// drains it into model context. After a plan is approved, the agent's next
// turn is exactly where the nudge belongs, so this timing is correct.
//
// Everything here is best-effort and fail-open: any error (no plan text, ox
// not on PATH, no signals, write failure) leaves the existing hook behavior
// completely untouched. The nudge is purely additive.
//
// Aggression (ox-mj0s): a MATERIAL plan — real team-context signals, not just
// structural size — earns an automatic background render (`ox plan render`,
// no --open) so the enriched HTML already exists the moment the nudge is
// delivered. The nudge itself is a directive, not a suggestion — but the one
// thing it may NEVER do is tell the agent to open a browser unconditionally.
// Opening always routes through the AskUserQuestion tool and proceeds only on
// an explicit yes (ox-mj0s's CRITICAL correction: devs would be furious if ox
// opened a browser without permission). Rendering is cheap and invisible;
// opening is outward-facing and needs consent. See
// TestFormatPlanNudgeLine_NeverAutoOpensBrowser for the regression guard.

const (
	// exitPlanModeToolName is Claude Code's plan-mode-exit tool. Its tool_input
	// carries the approved plan markdown in the "plan" field.
	exitPlanModeToolName = "ExitPlanMode"

	// planNudgeCacheSubdir holds per-agent pending plan-exit nudges under the
	// ledger cache (.sageox/cache/). Local-only derived data, never committed.
	planNudgeCacheSubdir = "plan-nudge"

	// planNudgeMaxAge bounds how long a stashed nudge stays deliverable. If the
	// user never submits another prompt, a stale nudge should not surface days
	// later in an unrelated context.
	planNudgeMaxAge = 30 * time.Minute

	// nonTrivialMinFilesHook / nonTrivialMinStepsHook mirror internal/plan's
	// exported NonTrivialMinFiles / NonTrivialMinSteps. The hook stays
	// deliberately decoupled from the plan package (it reads the computed signals
	// over JSON, never recomputes), so these stay local copies used solely for
	// wording the NonTrivial-only nudge. TestPlanNudgeThresholds_MatchPlanPackage
	// asserts the copies never silently diverge from the authoritative values.
	nonTrivialMinFilesHook = 2
	nonTrivialMinStepsHook = 5

	// planSubprocessTimeout caps EACH plan-exit subprocess call (enrichment and,
	// on a Material plan, the background render). Both may synchronously commit
	// and push a draft to the ledger (the chosen durability model), so this is
	// sized to absorb a network push, not just local computation. The hard kill
	// is a safety ceiling: if a push wedges, the local commit still stands and
	// the next push / `ox doctor` carries it — the agent is never hung.
	planSubprocessTimeout = 30 * time.Second
)

// exitPlanModeInput is the minimal shape of Claude Code's ExitPlanMode
// tool_input. Only the plan text is needed to enrich.
type exitPlanModeInput struct {
	Plan string `json:"plan"`
}

// planJSONResult is the minimal subset of `ox plan --json` output the nudge
// needs. The full Result lives in internal/plan; we deliberately decode only
// the material flag + counts so this stays decoupled from that package.
type planJSONResult struct {
	Signals struct {
		Collisions   int  `json:"collisions"`
		PriorArt     int  `json:"prior_art"`
		ExpertRoutes int  `json:"expert_routes"`
		Material     bool `json:"material"`
		Files        int  `json:"files"`
		Steps        int  `json:"steps"`
		NonTrivial   bool `json:"non_trivial"`
	} `json:"signals"`
}

// handlePlanExit is invoked from handleAfterTool ONLY when the PostToolUse
// event reports ToolName == "ExitPlanMode". It enriches the approved plan via
// `ox plan enrich --json --persist` and, if the signals are material OR the
// plan is structurally non-trivial, stashes a one-line nudge for the next
// UserPromptSubmit to deliver. On a MATERIAL plan it also renders the SageOx
// HTML in the background first (no --open — see runPlanRenderNoOpen): the
// earned-aggression half of ox-mj0s, so the artifact is ready the instant a
// human says yes to the nudge's ask. The nudge itself never instructs an
// unconditional browser-open; opening always waits on an explicit yes via
// AskUserQuestion. The whole recommendation is gated by plan.html (off ==>
// never render, never nudge). Fail-open throughout.
func handlePlanExit(ctx *HookContext, agentID string) {
	if ctx == nil || ctx.Input == nil || agentID == "" {
		return
	}

	planText := extractExitPlanText(ctx.Input.RawBytes)
	if strings.TrimSpace(planText) == "" {
		slog.Debug("hook: plan-exit no plan text, skipping nudge")
		return
	}

	res, ok := runPlanEnrichment(planText)
	if !ok {
		return
	}

	// The render nudge fires on either axis: team-context signals (Material) or
	// structural substance (NonTrivial). The HTML render is worth recommending on
	// a large greenfield plan even when team context is silent.
	if !res.Signals.Material && !res.Signals.NonTrivial {
		slog.Debug("hook: plan-exit not material and trivial, skipping nudge",
			"collisions", res.Signals.Collisions,
			"prior_art", res.Signals.PriorArt,
			"expert_routes", res.Signals.ExpertRoutes,
			"files", res.Signals.Files,
			"steps", res.Signals.Steps)
		return
	}

	// plan.html=off means "never render, never nudge" (the config enum's own
	// definition). Suppress the recommendation. Enrichment + --persist already
	// ran above: the draft save is durability (gated separately on plan.save) and
	// is independent of the render recommendation, so it stands either way.
	if config.PlanHTML(ctx.ProjectRoot) == config.PlanHTMLOff {
		slog.Debug("hook: plan-exit plan.html=off, skipping nudge", "agent_id", agentID)
		return
	}

	// Earned aggression (ox-mj0s): a MATERIAL plan — real team-context signals,
	// not just structural size — gets its HTML rendered right now, in the
	// background, with no --open. This is cheap and invisible: the artifact is
	// simply ready the instant the human answers yes to the nudge's ask. A
	// NonTrivial-only plan has not earned this; the render stays a suggested
	// next step rather than an automatic one. Also gated on plan.save: with
	// capture off there is no ledger to render into, so attempting it would
	// only burn a subprocess on a guaranteed no-op (see runPlanRenderFresh).
	rendered := false
	if res.Signals.Material && config.PlanSave(ctx.ProjectRoot) {
		rendered = runPlanRenderNoOpen(planText)
	}

	nudge := formatPlanNudgeLine(res, config.PlanOpen(ctx.ProjectRoot))
	if err := stashPlanNudge(ctx.ProjectRoot, agentID, nudge); err != nil {
		slog.Debug("hook: plan-exit stash failed", "error", err)
		return
	}
	slog.Info("hook: plan-exit nudge stashed",
		"agent_id", agentID,
		"collisions", res.Signals.Collisions,
		"prior_art", res.Signals.PriorArt,
		"expert_routes", res.Signals.ExpertRoutes,
		"files", res.Signals.Files,
		"steps", res.Signals.Steps,
		"rendered", rendered)
}

// extractExitPlanText pulls the plan markdown out of ExitPlanMode tool_input.
// Claude Code shapes the hook stdin as {"tool_name":"ExitPlanMode",
// "tool_input":{"plan":"..."}}. Returns "" on any parse failure (fail-open).
func extractExitPlanText(rawBytes []byte) string {
	if len(rawBytes) == 0 {
		return ""
	}
	var envelope struct {
		ToolInput json.RawMessage `json:"tool_input"`
	}
	if err := json.Unmarshal(rawBytes, &envelope); err != nil || len(envelope.ToolInput) == 0 {
		return ""
	}
	var ti exitPlanModeInput
	if err := json.Unmarshal(envelope.ToolInput, &ti); err != nil {
		return ""
	}
	return ti.Plan
}

// planEnrichArgs / planRenderArgs name the exact `ox` subcommand args for each
// plan-exit subprocess step, as their own small tested functions rather than
// inline slice literals at each call site. That makes "the render step never
// passes --open" a structural, independently-checkable guarantee — see
// TestPlanRenderArgs_NeverOpensBrowser — not just a claim in a comment.
func planEnrichArgs() []string { return []string{"plan", "enrich", "--json", "--persist"} }
func planRenderArgs() []string { return []string{"plan", "render"} }

// runPlanSubprocess execs `ox <args...>` with planText piped on stdin — the
// shared plumbing for every plan-exit hook step (enrichment, rendering).
// Bounded by planSubprocessTimeout and hard-killed on expiry so a wedged
// subprocess never stalls the agent's turn. Returns captured stdout and
// ok=false on any failure; every caller treats a failure as fail-open (skip
// this step, never block the nudge).
func runPlanSubprocess(planText string, args ...string) ([]byte, bool) {
	oxPath, err := os.Executable()
	if err != nil {
		slog.Debug("hook: plan-exit cannot find ox executable", "error", err)
		return nil, false
	}

	// hard timeout so a wedged subprocess never stalls the agent's turn.
	ctx, cancel := context.WithTimeout(context.Background(), planSubprocessTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, oxPath, args...)
	cmd.Stdin = strings.NewReader(planText)
	cmd.Env = os.Environ()

	out, err := cmd.Output()
	if err != nil {
		slog.Debug("hook: plan-exit subprocess failed", "args", args, "error", err)
		return nil, false
	}
	return out, true
}

// runPlanEnrichment shells out to `ox plan enrich --json --persist`, feeding
// the plan markdown on stdin. This is the deterministic, 0-token, no-network
// plumbing path. We invoke the managed CLI rather than calling internal/plan
// directly so the nudge stays decoupled from the enrichment internals (another
// agent owns that package). --persist durably saves + commits a draft now, so
// a plan exists on the ledger the moment the agent leaves plan mode (not
// contingent on a later `ox plan` / skill save). Returns ok=false on any
// failure (fail-open).
//
// Exposed as a variable so tests can stub it: os.Executable() inside `go
// test` resolves to the compiled TEST binary, not the real ox CLI, so
// exercising the real subprocess here would hang (see
// coverage_boost_test.go's note on runPrimeForHook for the same constraint
// elsewhere in this package).
var runPlanEnrichment = func(planText string) (planJSONResult, bool) {
	var res planJSONResult
	out, ok := runPlanSubprocess(planText, planEnrichArgs()...)
	if !ok {
		return res, false
	}
	if err := json.Unmarshal(out, &res); err != nil {
		slog.Debug("hook: plan-exit enrichment output not parseable", "error", err)
		return res, false
	}
	return res, true
}

// runPlanRenderNoOpen shells out to `ox plan render` (no --open) to
// materialize the plan's SageOx-enriched HTML into the ledger — the
// earned-aggression half of ox-mj0s: on a Material plan the artifact is ready
// in the background, invisibly, the instant a human says yes to the nudge's
// ask. planRenderArgs() never includes --open or -o/--output, so this call
// cannot open a browser by construction, not merely by convention. It reuses
// the exact `ox plan render` command a human would run by hand, feeding the
// same plan text already extracted for enrichment — the same idempotent,
// read-merge-by-slug write path already exercised by manual use
// (internal/plan/store.go's Save + MutatePlanMeta), so this second render
// after enrich --persist safely converges on the same dated-slug ledger
// directory instead of duplicating it, and a failed push there is a logged
// warning, not a hook error.
//
// Exposed as a variable so tests can stub it — see runPlanEnrichment.
var runPlanRenderNoOpen = func(planText string) bool {
	_, ok := runPlanSubprocess(planText, planRenderArgs()...)
	return ok
}

// planReviewAskClause is the shared ask-before-open tail for both nudge
// paths below. It is the ONLY place either path names a browser-opening
// command (`ox plan render --open`, the `ox plan review` loop — which itself
// launches a browser, see runPlanReview), and it always names AskUserQuestion
// FIRST. That makes every nudge this file can produce structurally gated: ask,
// then act only on an explicit yes — never an unconditional open. See
// TestFormatPlanNudgeLine_NeverAutoOpensBrowser, the regression guard for
// ox-mj0s's CRITICAL correction (devs would be furious if ox opened a browser
// without permission).
const planReviewAskClause = ` Ask the user via AskUserQuestion ("Review this plan in your browser?") before opening anything — offer "Yes, open" / "No" / "Always open from now on" / "Never ask again", and if they pick one of the last two run ` + "`ox config set plan.open always`" + ` (or ` + "`never`" + `) so the choice sticks. Only on an explicit yes, render it as a SageOx team-context-optimized plan (HTML) with ` + "`ox plan render --open`" + `, or start the live review loop with ` + "`ox plan review <slug>`" + ` so they mark it up in-browser and you receive their feedback in-turn via ` + "`ox plan review await <slug>`" + ` — it BLOCKS for feedback, so confirm with the user before entering it.`

// planReviewAlwaysClause is the tail used when the user has explicitly set
// plan.open=always. This is the ONLY sanctioned path where the nudge instructs
// an open WITHOUT a preceding AskUserQuestion — the consent lives in the
// persisted config choice the user made earlier, not in a per-plan prompt.
const planReviewAlwaysClause = ` You've set plan.open=always, so open it directly (no need to ask): render it as a SageOx team-context-optimized plan (HTML) with ` + "`ox plan render --open <slug>`" + `, or start the live review loop with ` + "`ox plan review <slug>`" + ` — it BLOCKS for feedback, so let the user know before entering it. To stop auto-opening, run ` + "`ox config set plan.open ask`" + `.`

// planReviewNeverClause is the tail used when the user has set plan.open=never.
// It never prompts to open and never instructs an open — it only records that
// the plan is available, framing any open as a user-initiated choice.
const planReviewNeverClause = ` You've set plan.open=never, so do NOT prompt to open or open a browser. The enriched plan is saved to the ledger; the user can open it themselves any time with ` + "`ox plan render --open <slug>`" + ` (` + "`ox plan list`" + ` shows the slug), or re-enable prompting with ` + "`ox config set plan.open ask`" + `.`

// planOpenClause returns the open-policy tail for the nudge. Default (unknown or
// "ask") keeps the ask-before-open behavior — the safe default that never
// seizes the screen without an explicit yes.
func planOpenClause(policy string) string {
	switch policy {
	case config.PlanOpenAlways:
		return planReviewAlwaysClause
	case config.PlanOpenNever:
		return planReviewNeverClause
	default:
		return planReviewAskClause
	}
}

// formatPlanNudgeLine builds the concise one-line nudge. Single line, no
// multi-line noise (grepability invariant). When team-context signals fired it
// leads with the cost of skipping the render — the specific signals a
// hand-authored plan drops; when the plan fired only on structural
// non-triviality it leads with the plan's scope instead. Both paths end on the
// same ask-gated tail (planReviewAskClause) — see its doc for why that sharing
// matters.
func formatPlanNudgeLine(res planJSONResult, openPolicy string) string {
	tail := planOpenClause(openPolicy)

	var parts []string
	if res.Signals.Collisions > 0 {
		parts = append(parts, fmt.Sprintf("%s in open PRs/active files", pluralize(res.Signals.Collisions, "collision", "collisions")))
	}
	if res.Signals.PriorArt > 0 {
		parts = append(parts, pluralize(res.Signals.PriorArt, "prior-art match", "prior-art matches"))
	}
	if res.Signals.ExpertRoutes > 0 {
		parts = append(parts, pluralize(res.Signals.ExpertRoutes, "expert route", "expert routes"))
	}
	if detail := strings.Join(parts, " + "); detail != "" {
		// Material path: lead with what a hand-authored plan would drop.
		return fmt.Sprintf("Your plan touches %s. A hand-authored plan drops all of that and the team never sees it.%s", detail, tail)
	}

	// NonTrivial-only path: no team-context signals fired, but the plan is
	// structurally substantial enough to warrant a real review.
	return fmt.Sprintf("Your plan spans %s — substantial enough to warrant a real review before it ships.%s", planScopePhrase(res.Signals.Files, res.Signals.Steps), tail)
}

// planScopePhrase describes plan scale from the structural counts, naming only
// the dimension(s) that crossed the non-trivial threshold, with correct
// pluralization. At least one dimension is non-zero when this is reached; the
// fallback keeps it safe if the thresholds are ever loosened relative to the
// firing gate.
func planScopePhrase(files, steps int) string {
	var parts []string
	if files >= nonTrivialMinFilesHook {
		parts = append(parts, pluralize(files, "file", "files"))
	}
	if steps >= nonTrivialMinStepsHook {
		parts = append(parts, pluralize(steps, "step", "steps"))
	}
	if len(parts) == 0 {
		if files > 0 {
			parts = append(parts, pluralize(files, "file", "files"))
		}
		if steps > 0 {
			parts = append(parts, pluralize(steps, "step", "steps"))
		}
	}
	if len(parts) == 0 {
		return "multiple files"
	}
	return strings.Join(parts, " / ")
}

// pluralize renders "<n> <singular|plural>" picking the form by count.
func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// planNudgePath returns the per-agent pending-nudge file path under the ledger
// cache. Empty projectRoot/agentID yields "" (caller no-ops).
func planNudgePath(projectRoot, agentID string) string {
	if projectRoot == "" || agentID == "" {
		return ""
	}
	// agentID is an ox-generated token (no path separators), safe as a filename.
	return filepath.Join(projectRoot, ".sageox", "cache", planNudgeCacheSubdir, agentID+".txt")
}

// stashPlanNudge writes a single pending nudge for the agent. Overwrites any
// existing pending nudge (the latest plan exit wins). Best-effort directory
// creation; errors bubble up for the caller's debug log.
func stashPlanNudge(projectRoot, agentID, line string) error {
	path := planNudgePath(projectRoot, agentID)
	if path == "" {
		return fmt.Errorf("plan-nudge: empty project root or agent id")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("plan-nudge mkdir: %w", err)
	}
	return os.WriteFile(path, []byte(line), 0o600)
}

// emitPlanNudge drains and delivers a pending plan-exit nudge to w, then
// removes the file (deliver-once). Called from handlePrompt — the proven
// UserPromptSubmit stdout-injection channel. No-op when there is no pending
// nudge, or when the nudge is older than planNudgeMaxAge (stale → discard).
func emitPlanNudge(w io.Writer, projectRoot, agentID string) {
	path := planNudgePath(projectRoot, agentID)
	if path == "" {
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		return // no pending nudge
	}

	// always remove after observing it — even if stale — so a stale nudge does
	// not linger and resurface on a later unrelated prompt.
	defer func() { _ = os.Remove(path) }()

	if time.Since(info.ModTime()) > planNudgeMaxAge {
		slog.Debug("hook: plan-exit nudge stale, discarding", "age", time.Since(info.ModTime()))
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	line := strings.TrimSpace(string(data))
	if line == "" {
		return
	}

	// <system-reminder> is the only tag Claude Code treats as trusted system
	// context (see formatWhispers — <new-context> is rejected as injection).
	fmt.Fprintf(w, "<system-reminder>[ox] %s</system-reminder>\n", line)
}
