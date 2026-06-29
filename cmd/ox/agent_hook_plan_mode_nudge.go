package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/config"
)

// Just-in-time plan-rendering steer (Claude Code / Gold tier for the plan-mode
// trigger; agent-agnostic for the HTML-intent trigger).
//
// The plan-exit nudge (agent_hook_plan_nudge.go) fires AFTER the plan is
// presented. This hint fires WHILE the agent is still drafting — or the moment
// the user asks for an HTML plan — so the deterministic `ox plan enrich --json`
// team context (collisions, prior art, expert routing) is folded into the plan
// BEFORE it reaches the human, and the render goes through `ox plan render`
// instead of a hand-rolled orphan. Prime carries the same guidance, but in a
// long context that guidance is diluted; a crisp, just-in-time reminder at the
// planning moment is what actually changes behavior.
//
// Two independent triggers, one throttle:
//   - PLAN MODE: permission_mode == "plan". Claude Code is the only agent that
//     reports a permission mode, so this trigger is implicitly Gold-only.
//   - HTML-PLAN INTENT: the user's prompt asks to render a plan as an HTML /
//     visual page, in ANY permission mode. This is the gap the plan-mode-only
//     gate missed: most HTML-plan requests ("make me an html plan for X") arrive
//     outside plan mode, where the plan-mode trigger never fires and the agent
//     reaches for a generic html-plan skill or hand-rolls a context-blind render.
//
// Delivery channel: UserPromptSubmit stdout is the ONLY channel Claude Code
// injects into model context (see the table in agent_hook.go). Claude Code's
// UserPromptSubmit payload carries the active permission mode — snake_case
// `permission_mode` in the hook stdin, camelCase `permissionMode` in the
// transcript — and the `prompt` text. We decode both defensively.
//
// Throttle: fire exactly once per planning episode. A per-agent stamp file marks
// "already hinted this episode"; it is cleared the moment a prompt arrives that
// triggers neither path, so a fresh plan-mode entry or a new HTML-plan request
// re-hints. This avoids hinting on every prompt during a long session while
// still re-firing on each genuine planning moment.
//
// Everything here is best-effort and fail-open: any decode/IO failure leaves the
// existing hook behavior untouched. The hint is purely additive.

const (
	// planModeValue is the permission-mode value Claude Code reports while the
	// user is in plan mode.
	planModeValue = "plan"

	// planModeHintCacheSubdir holds the per-agent "already hinted this episode"
	// stamp under the ledger cache (.sageox/cache/). Local-only derived data,
	// never committed. (Name retained for cache stability; the stamp now covers
	// both the plan-mode and HTML-intent triggers.)
	planModeHintCacheSubdir = "plan-mode-hint"
)

// htmlPlanIntentPhrases are the curated phrases that signal the user wants a plan
// rendered as an HTML / visual page. Matched case-insensitively as substrings.
// Each phrase embeds both a plan noun and a render/visual cue so it is
// self-disambiguating; a false positive costs at most one throttled reminder.
var htmlPlanIntentPhrases = []string{
	"html plan",
	"html-plan", // covers "/html-plan" (the slash command)
	"plan as html",
	"plan as a page",
	"plan as an html",
	"plan as a visual",
	"render the plan",
	"render this plan",
	"render that plan",
	"render my plan",
	"render the implementation plan",
	"visualize this plan",
	"visualize the plan",
}

// planModePromptInput is the minimal subset of Claude Code's UserPromptSubmit
// stdin needed to detect plan mode. Both spellings are accepted because the hook
// stdin (snake_case) and the transcript (camelCase) differ.
type planModePromptInput struct {
	PermissionModeSnake string `json:"permission_mode"`
	PermissionModeCamel string `json:"permissionMode"`
}

// extractPermissionMode pulls the active permission mode out of a UserPromptSubmit
// payload, accepting either spelling. Returns "" on any parse failure (fail-open).
func extractPermissionMode(rawBytes []byte) string {
	if len(rawBytes) == 0 {
		return ""
	}
	var in planModePromptInput
	if err := json.Unmarshal(rawBytes, &in); err != nil {
		return ""
	}
	if in.PermissionModeSnake != "" {
		return in.PermissionModeSnake
	}
	return in.PermissionModeCamel
}

// promptRequestsHTMLPlan reports whether the user's prompt is asking to render a
// plan as an HTML / visual page. Reuses extractPromptText (the same envelope the
// recall preamble parses) so the two paths can't drift on field names.
// Conservative substring match on a curated phrase set; returns false on any
// extraction failure (fail-open).
func promptRequestsHTMLPlan(rawBytes []byte) bool {
	text := extractPromptText(rawBytes)
	if text == "" {
		return false
	}
	text = strings.ToLower(text)
	for _, phrase := range htmlPlanIntentPhrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

// emitPlanHint writes the just-in-time plan-rendering steer to w when the agent
// is in plan mode OR the prompt asks to render an HTML plan, and it has not yet
// been hinted for the current planning episode.
//
// Called from handlePrompt (the proven UserPromptSubmit stdout-injection channel)
// on every prompt. State machine, keyed on a per-agent stamp:
//   - triggered, no stamp  -> emit hint, write stamp (hinted this episode)
//   - triggered, stamp set -> suppress (already hinted this episode)
//   - not triggered        -> clear stamp (next trigger re-hints)
func emitPlanHint(w io.Writer, projectRoot, agentID string, rawBytes []byte) {
	if projectRoot == "" || agentID == "" {
		return
	}
	stamp := planModeHintPath(projectRoot, agentID)
	if stamp == "" {
		return
	}

	inPlanMode := extractPermissionMode(rawBytes) == planModeValue
	htmlIntent := promptRequestsHTMLPlan(rawBytes)
	// Honor the user's opt-out: plan.html=off (env SAGEOX_PLAN_HTML / config)
	// silences the HTML-intent steer for users who don't want render nudges. The
	// plan-mode steer leads with `ox plan enrich` (render-independent team
	// context) and keeps its own permission-mode gate, so it's left untouched.
	if htmlIntent && config.PlanHTML(projectRoot) == config.PlanHTMLOff {
		htmlIntent = false
	}
	if !inPlanMode && !htmlIntent {
		// neither trigger — reset so the next planning moment re-hints. Best-effort.
		_ = os.Remove(stamp)
		return
	}

	// triggered: hint once per episode.
	if _, err := os.Stat(stamp); err == nil {
		return // already hinted for this episode
	}
	if err := os.MkdirAll(filepath.Dir(stamp), 0o755); err != nil {
		slog.Debug("hook: plan hint mkdir failed", "error", err)
		return
	}

	// Plan-mode lead wins when both triggers fire — it's the in-draft superset
	// message ("enrich WHILE you draft, then render"). The HTML-intent lead is
	// for the no-plan-mode case where the user is already asking to render.
	// <system-reminder> is the only tag Claude Code treats as trusted system
	// context. Single line — grepability invariant.
	line := htmlPlanHintLine()
	trigger := "html-intent"
	if inPlanMode {
		line = planModeHintLine()
		trigger = "plan-mode"
	}
	fmt.Fprintf(w, "<system-reminder>[ox] %s</system-reminder>\n", line)

	// Write the stamp only after a successful emit so that a failed write
	// doesn't permanently suppress the hint for this episode.
	if err := os.WriteFile(stamp, []byte("1"), 0o600); err != nil {
		slog.Debug("hook: plan hint stamp write failed", "error", err)
		return
	}
	slog.Info("hook: plan hint emitted", "agent_id", agentID, "trigger", trigger)
}

// planModeHintLine is the in-draft steer for plan mode: enrich (JSON) WHILE
// drafting, render (the SageOx team-context-optimized plan) when presenting.
func planModeHintLine() string {
	return strings.Join([]string{
		"Plan mode — run `ox plan enrich --json` WHILE you draft so the plan reflects team context",
		"(collisions, prior art, expert routing) BEFORE you present it; then offer it as a",
		"SageOx team-context-optimized plan via `ox plan render --open`. The render owns the SageOx",
		"footer credit and OX-icon markers — don't hand-author your own.",
	}, " ")
}

// htmlPlanHintLine is the steer for when the user asks to render an HTML plan
// outside plan mode: lead with the renderer, push rich cognition-first visuals
// via the viz catalog, warn off the orphan render.
func htmlPlanHintLine() string {
	return strings.Join([]string{
		"Rendering an HTML plan? Use `ox plan render --open` (run `ox plan enrich --json` first) — only it folds in",
		"SageOx team context (collisions, prior art, expert routing) and saves to the ledger. Lean into the",
		"`ox plan viz` catalog: rich, interactive HTML+JS visuals — dependency explorers, charts, swimlane timelines,",
		"Tufte tables — that surface the critical decisions and tradeoffs and cut the reviewer's cognitive load,",
		"rather than burying them in prose. A hand-rolled render is a context-blind orphan; don't author the SageOx credit — the render owns it.",
	}, " ")
}

// planModeHintPath returns the per-agent stamp path under the ledger cache.
// Empty projectRoot/agentID yields "" (caller no-ops).
func planModeHintPath(projectRoot, agentID string) string {
	if projectRoot == "" || agentID == "" {
		return ""
	}
	// agentID is an ox-generated token (no path separators), safe as a filename.
	return filepath.Join(projectRoot, ".sageox", "cache", planModeHintCacheSubdir, agentID+".txt")
}
