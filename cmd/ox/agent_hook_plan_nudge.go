package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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

	// planSubprocessTimeout caps the `ox plan --json --persist` call. Enrichment
	// itself is pure-local, but --persist also saves + synchronously commits and
	// pushes a draft to the ledger (the chosen durability model), so this is
	// sized to absorb a network push, not just local enrichment. The hard kill
	// is a safety ceiling: if the push wedges, the local commit still stands and
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
	} `json:"signals"`
}

// handlePlanExit is invoked from handleAfterTool ONLY when the PostToolUse
// event reports ToolName == "ExitPlanMode". It enriches the approved plan via
// `ox plan --json` and, if the signals are material, stashes a one-line nudge
// for the next UserPromptSubmit to deliver. Fail-open throughout.
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
	if !res.Signals.Material {
		slog.Debug("hook: plan-exit no material signals",
			"collisions", res.Signals.Collisions,
			"prior_art", res.Signals.PriorArt,
			"expert_routes", res.Signals.ExpertRoutes)
		return
	}

	nudge := formatPlanNudgeLine(res)
	if err := stashPlanNudge(ctx.ProjectRoot, agentID, nudge); err != nil {
		slog.Debug("hook: plan-exit stash failed", "error", err)
		return
	}
	slog.Info("hook: plan-exit nudge stashed",
		"agent_id", agentID,
		"collisions", res.Signals.Collisions,
		"prior_art", res.Signals.PriorArt,
		"expert_routes", res.Signals.ExpertRoutes)
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

// runPlanEnrichment shells out to `ox plan --json`, feeding the plan markdown
// on stdin. This is the deterministic, 0-token, no-network plumbing path. We
// invoke the managed CLI rather than calling internal/plan directly so the
// nudge stays decoupled from the enrichment internals (another agent owns that
// package). Returns ok=false on any failure (fail-open).
func runPlanEnrichment(planText string) (planJSONResult, bool) {
	var res planJSONResult

	oxPath, err := os.Executable()
	if err != nil {
		slog.Debug("hook: plan-exit cannot find ox executable", "error", err)
		return res, false
	}

	// --persist: durably save + commit the draft now, so a plan exists on the
	// ledger the moment the agent leaves plan mode (not contingent on a later
	// `ox plan` / skill save). Enrichment output (stdout JSON) is unchanged.
	cmd := exec.Command(oxPath, "plan", "--json", "--persist")
	cmd.Stdin = strings.NewReader(planText)
	cmd.Env = os.Environ()

	// hard timeout so a wedged subprocess never stalls the agent's turn.
	timer := time.AfterFunc(planSubprocessTimeout, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	defer timer.Stop()

	out, err := cmd.Output()
	if err != nil {
		slog.Debug("hook: plan-exit enrichment subprocess failed", "error", err)
		return res, false
	}
	if err := json.Unmarshal(out, &res); err != nil {
		slog.Debug("hook: plan-exit enrichment output not parseable", "error", err)
		return res, false
	}
	return res, true
}

// formatPlanNudgeLine builds the concise one-line nudge from material signals.
// Single line, no multi-line noise. Only mentions signal classes that fired.
func formatPlanNudgeLine(res planJSONResult) string {
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
	detail := strings.Join(parts, " + ")
	if detail == "" {
		detail = "team-context signals"
	}
	return fmt.Sprintf("Your plan touches %s. Render an enriched plan for faster human review: run `ox plan`.", detail)
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
