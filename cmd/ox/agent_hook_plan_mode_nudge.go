package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// In-plan-mode enrichment hint (Gold tier — Claude Code).
//
// The plan-exit nudge (agent_hook_plan_nudge.go) fires AFTER the plan is
// presented. This hint fires WHILE the agent is still drafting, so the
// deterministic `ox plan enrich --json` team context (collisions, prior art,
// expert routing) is folded into the plan BEFORE it reaches the human — not
// bolted on after. Prime carries the same guidance, but in a long context that
// guidance is diluted; a crisp, just-in-time reminder at the planning moment is
// what actually changes behavior.
//
// Delivery channel: UserPromptSubmit stdout is the ONLY channel Claude Code
// injects into model context (see the table in agent_hook.go). Claude Code's
// UserPromptSubmit payload carries the active permission mode — snake_case
// `permission_mode` in the hook stdin, camelCase `permissionMode` in the
// transcript. We decode both defensively. Value "plan" == plan mode.
//
// Gold-tier gating is implicit: only Claude Code sends a permission mode, so for
// every other agent extractPermissionMode returns "" and this is a clean no-op.
//
// Throttle: fire exactly once per plan-mode entry. A per-agent stamp file marks
// "already hinted this entry"; it is cleared the moment a non-plan prompt
// arrives, so re-entering plan mode re-hints. This avoids hinting on every
// prompt during a long plan-mode session while still re-firing on each entry.
//
// Everything here is best-effort and fail-open: any decode/IO failure leaves the
// existing hook behavior untouched. The hint is purely additive.

const (
	// planModeValue is the permission-mode value Claude Code reports while the
	// user is in plan mode.
	planModeValue = "plan"

	// planModeHintCacheSubdir holds the per-agent "already hinted this entry"
	// stamp under the ledger cache (.sageox/cache/). Local-only derived data,
	// never committed.
	planModeHintCacheSubdir = "plan-mode-hint"
)

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

// emitPlanModeHint writes the in-plan-mode enrichment hint to w when the agent is
// in plan mode and has not yet been hinted for the current plan-mode entry.
//
// Called from handlePrompt (the proven UserPromptSubmit stdout-injection channel)
// on every prompt. State machine, keyed on a per-agent stamp:
//   - mode == "plan", no stamp  -> emit hint, write stamp (hinted this entry)
//   - mode == "plan", stamp set -> suppress (already hinted this entry)
//   - mode != "plan"            -> clear stamp (next plan-mode entry re-hints)
func emitPlanModeHint(w io.Writer, projectRoot, agentID string, rawBytes []byte) {
	if projectRoot == "" || agentID == "" {
		return
	}
	stamp := planModeHintPath(projectRoot, agentID)
	if stamp == "" {
		return
	}

	if extractPermissionMode(rawBytes) != planModeValue {
		// not in plan mode — reset so the next entry re-hints. Best-effort.
		_ = os.Remove(stamp)
		return
	}

	// in plan mode: hint once per entry.
	if _, err := os.Stat(stamp); err == nil {
		return // already hinted for this plan-mode entry
	}
	if err := os.MkdirAll(filepath.Dir(stamp), 0o755); err != nil {
		slog.Debug("hook: plan-mode hint mkdir failed", "error", err)
		return
	}
	// <system-reminder> is the only tag Claude Code treats as trusted system
	// context. Single line — grepability invariant.
	fmt.Fprintf(w, "<system-reminder>[ox] %s</system-reminder>\n", planModeHintLine())

	// Write the stamp only after a successful emit so that a failed write
	// doesn't permanently suppress the hint for this plan-mode entry.
	if err := os.WriteFile(stamp, []byte("1"), 0o600); err != nil {
		slog.Debug("hook: plan-mode hint stamp write failed", "error", err)
		return
	}
	slog.Info("hook: plan-mode hint emitted", "agent_id", agentID)
}

// planModeHintLine is the crisp two-beat steer: enrich (JSON) WHILE drafting,
// render (the SageOx team-context-optimized plan) when presenting.
func planModeHintLine() string {
	return strings.Join([]string{
		"Plan mode — run `ox plan enrich --json` WHILE you draft so the plan reflects team context",
		"(collisions, prior art, expert routing) BEFORE you present it; then offer it as a",
		"SageOx team-context-optimized plan via `ox plan render --open`.",
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
