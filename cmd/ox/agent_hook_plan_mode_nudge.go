package main

import (
	"encoding/json"
	"errors"
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
// "already hinted this episode" and records WHICH trigger hinted; it is cleared
// the moment a prompt arrives that triggers neither path, so a fresh plan-mode
// entry or a new HTML-plan request re-hints. Plan mode is the higher-value
// trigger (its lead steers enrich WHILE drafting), so it upgrades over an
// earlier HTML-intent stamp in the same episode — the html-intent → plan-mode
// transition with no neutral prompt between must not swallow the draft-first
// steer. This avoids hinting on every prompt during a long session while still
// re-firing on each genuine planning moment.
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

	// Stamp payloads recording which trigger last hinted this episode. Plan mode
	// outranks HTML intent, so a plan-mode hint may overwrite an html-intent
	// stamp (an upgrade), but not vice-versa.
	triggerPlanMode   = "plan-mode"
	triggerHTMLIntent = "html-intent"
)

// htmlPlanIntentPhrases are the curated phrases that signal the user wants a plan
// rendered as an HTML / visual page. Matched case-insensitively on WORD
// boundaries (see containsWord) so "render the plan" does not fire on "render the
// planned route" / "visualize the planning timeline". Each phrase embeds both a
// plan noun and a render/visual cue so it is self-disambiguating.
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
// Word-boundary match on a curated phrase set; returns false on any extraction
// failure (fail-open).
func promptRequestsHTMLPlan(rawBytes []byte) bool {
	text := strings.ToLower(extractPromptText(rawBytes))
	if text == "" {
		return false
	}
	for _, phrase := range htmlPlanIntentPhrases {
		if containsWord(text, phrase) {
			return true
		}
	}
	return false
}

// containsWord reports whether phrase occurs in text bounded by a
// non-alphanumeric character (or the string edge) on both sides — a
// word-boundary-aware strings.Contains. Both args are assumed already
// lowercased. ASCII-only: a multibyte rune adjacent to a match counts as a
// boundary, which is fine for these ASCII phrases.
func containsWord(text, phrase string) bool {
	for from := 0; ; {
		i := strings.Index(text[from:], phrase)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(phrase)
		leftOK := start == 0 || !isASCIIAlphaNum(text[start-1])
		rightOK := end == len(text) || !isASCIIAlphaNum(text[end])
		if leftOK && rightOK {
			return true
		}
		from = start + 1
	}
}

func isASCIIAlphaNum(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}

// emitPlanHint writes the just-in-time plan-rendering steer to w when the agent
// is in plan mode OR the prompt asks to render an HTML plan, and it has not yet
// been hinted for the current planning episode.
//
// Called from handlePrompt (the proven UserPromptSubmit stdout-injection channel)
// on every prompt. State machine, keyed on a per-agent stamp that records the
// triggering source:
//   - triggered, no stamp          -> emit, stamp the trigger
//   - plan-mode, stamp=html-intent -> emit (upgrade), restamp plan-mode
//   - triggered, stamp >= trigger  -> suppress (already hinted this episode)
//   - not triggered                -> clear stamp (next trigger re-hints)
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

	// Pick the trigger + message. Plan mode is the higher-value lead (it steers
	// enrich WHILE drafting); the HTML-intent lead is render-first, for the
	// no-plan-mode case where the user is already asking to render.
	trigger := triggerHTMLIntent
	line := htmlPlanHintLine()
	if inPlanMode {
		trigger = triggerPlanMode
		line = planModeHintLine()
	}

	// Throttle: once per episode, but a plan-mode hint may UPGRADE over an
	// earlier html-intent stamp (the html-intent → plan-mode transition with no
	// neutral prompt between, where the draft-first steer would otherwise be
	// swallowed). Reading the stamp also distinguishes "absent" (proceed) from a
	// real read error (leave state untouched rather than risk a double-hint).
	if prev, err := os.ReadFile(stamp); err == nil {
		if trigger != triggerPlanMode || strings.TrimSpace(string(prev)) == triggerPlanMode {
			return // already hinted this episode for an equal-or-higher trigger
		}
		// stamp is html-intent and we're now in plan mode — fall through to upgrade.
	} else if !errors.Is(err, os.ErrNotExist) {
		slog.Debug("hook: plan hint stamp read failed", "error", err)
		return
	}

	if err := os.MkdirAll(filepath.Dir(stamp), 0o755); err != nil {
		slog.Debug("hook: plan hint mkdir failed", "error", err)
		return
	}

	// <system-reminder> is the only tag Claude Code treats as trusted system
	// context. Single line — grepability invariant. Persist the stamp ONLY after
	// a successful emit, so a broken stdout doesn't suppress a hint the model
	// never saw.
	if _, err := fmt.Fprintf(w, "<system-reminder>[ox] %s</system-reminder>\n", line); err != nil {
		slog.Debug("hook: plan hint emit failed", "error", err)
		return
	}
	if err := os.WriteFile(stamp, []byte(trigger), 0o600); err != nil {
		slog.Debug("hook: plan hint stamp write failed", "error", err)
		return
	}
	slog.Info("hook: plan hint emitted", "agent_id", agentID, "trigger", trigger)
}

// planModeHintLine is the in-draft steer: enrich while drafting, then keep the
// authored HTML as the single source when presenting a material plan.
func planModeHintLine() string {
	return strings.Join([]string{
		"A plan follows a creed: don't waste human attention, delight them, educate them visually and crisply.",
		"Plan mode — run `ox plan enrich --json` WHILE you draft so the plan reflects team context",
		"(collisions, prior art, expert routing) BEFORE you present it. For material work author a visual",
		"`plan.html`, save it canonically with `ox plan save --file plan.html`, then offer",
		"a SageOx team-context-optimized plan via `ox plan render --open --file plan.html`; ox preserves the page and owns SageOx chrome.",
	}, " ")
}

// htmlPlanHintLine is the steer for when the user asks to render an HTML plan
// outside plan mode: lead with canonical authored HTML and cognition-first
// visuals, then pass that page through ox for context and review chrome.
func htmlPlanHintLine() string {
	return strings.Join([]string{
		"A plan follows a creed: don't waste human attention, delight them, educate them visually and crisply.",
		"Rendering an HTML plan? Run `ox plan enrich --json`, author the visual `plan.html`, save it as the single",
		"source with `ox plan save --file plan.html`, then use `ox plan render --open --file plan.html` to inject",
		"SageOx team context without replacing the page. Lean into the",
		"`ox viz` catalog: rich, interactive HTML+JS visuals — dependency explorers, charts, swimlane timelines,",
		"Tufte tables — that surface the critical decisions and tradeoffs and cut the reviewer's cognitive load,",
		"with one closed Implementation notes appendix for file-level depth. Never use legacy `--plan + --html`.",
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
