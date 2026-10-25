package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Codex plan capture.
//
// Codex has no ExitPlanMode tool, so the Claude Code capture path
// (handleAfterTool → handlePlanExit) never fires for it and a Codex plan-mode
// plan dies with the session. Codex does fire Stop at the end of every turn
// (wired by .codex/hooks.json as `AGENT_ENV=codex ox agent hook Stop`), and its
// Stop payload carries `permission_mode` and `last_assistant_message`. A
// finished Codex plan is emitted inside a <proposed_plan>…</proposed_plan>
// block in that message, so Stop is the plan-exit signal.
//
// Which signal decides:
//   - A <proposed_plan> block is REQUIRED. It is how Codex marks a finished
//     plan; a plan-mode turn without one is a clarifying question, and
//     capturing it would fill the ledger with half-plans.
//   - permission_mode is NOT required. openai/codex#42881 reports that plan
//     mode may be reported as something other than "plan", so the block alone
//     is trusted; permission_mode is only logged, as evidence for that bug.
//   - If the payload has no last_assistant_message (an older Codex, or a turn
//     that ended on a tool call), there is nothing to capture: skipped with a
//     Debug log. Parsing the transcript file instead was rejected — its schema
//     is Codex-internal and unversioned.
//
// Capture reuses capturePlanText, the exact enrich --persist path ExitPlanMode
// uses, so Codex and Claude plans land in the ledger identically. Stop fires
// every turn and a plan is often restated, so a per-agent hash of the last
// captured plan suppresses re-capturing identical text.
//
// Codex expects Stop stdout to be JSON when non-empty; nothing here writes to
// stdout.

const (
	codexProposedPlanOpen  = "<proposed_plan>"
	codexProposedPlanClose = "</proposed_plan>"

	// codexPlanCaptureCacheSubdir holds the per-agent hash of the last
	// captured plan. Local-only derived data under .sageox/cache/.
	codexPlanCaptureCacheSubdir = "codex-plan-captured"
)

// codexStopInput is the subset of Codex's Stop payload capture reads.
type codexStopInput struct {
	PermissionMode       string `json:"permission_mode"`
	LastAssistantMessage string `json:"last_assistant_message"`
}

// extractProposedPlan returns the body of the LAST <proposed_plan> block in
// msg (a turn that revises its plan restates it; the last one is final), or ""
// when there is no complete block.
func extractProposedPlan(msg string) string {
	end := strings.LastIndex(msg, codexProposedPlanClose)
	if end < 0 {
		return ""
	}
	start := strings.LastIndex(msg[:end], codexProposedPlanOpen)
	if start < 0 {
		return ""
	}
	return strings.TrimSpace(msg[start+len(codexProposedPlanOpen) : end])
}

// codexPlanFromStop returns the finished plan in a Codex Stop payload and the
// reported permission mode, or "" with the reason it was skipped.
func codexPlanFromStop(raw []byte) (planText, mode, skipReason string) {
	if len(raw) == 0 {
		return "", "", "empty payload"
	}
	var in codexStopInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", "", "payload not JSON"
	}
	if strings.TrimSpace(in.LastAssistantMessage) == "" {
		return "", in.PermissionMode, "payload carries no last_assistant_message"
	}
	p := extractProposedPlan(in.LastAssistantMessage)
	if p == "" {
		return "", in.PermissionMode, "no <proposed_plan> block in last assistant message"
	}
	return p, in.PermissionMode, ""
}

// codexPlanCapturedPath is the per-agent dedupe marker path.
func codexPlanCapturedPath(projectRoot, agentID string) string {
	if projectRoot == "" || agentID == "" {
		return ""
	}
	return filepath.Join(projectRoot, ".sageox", "cache", codexPlanCaptureCacheSubdir, agentID+".sha256")
}

// maybeCaptureCodexPlan is the Codex Stop branch. Fail-open throughout.
func maybeCaptureCodexPlan(ctx *HookContext, agentID string) {
	if ctx == nil || ctx.Input == nil || agentID == "" || ctx.AgentType != "codex" {
		return
	}
	planText, mode, reason := codexPlanFromStop(ctx.Input.RawBytes)
	if planText == "" {
		slog.Debug("hook: codex stop, no plan captured", "agent_id", agentID, "permission_mode", mode, "reason", reason)
		return
	}

	sum := sha256.Sum256([]byte(planText))
	digest := hex.EncodeToString(sum[:])
	marker := codexPlanCapturedPath(ctx.ProjectRoot, agentID)
	if prev, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(prev)) == digest {
		slog.Debug("hook: codex stop, plan already captured", "agent_id", agentID)
		return
	}

	started := time.Now()
	if !capturePlanText(ctx, agentID, planText) {
		slog.Warn("hook: codex plan capture failed", "agent_id", agentID, "elapsed_ms", time.Since(started).Milliseconds())
		return
	}
	// The plan went through enrich --persist; any "drafted and never saved"
	// stamp (armed from the planning prompt) is now wrong.
	clearUnsavedPlanStamp(ctx.ProjectRoot, agentID)
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err == nil {
		if werr := os.WriteFile(marker, []byte(digest+"\n"), 0o600); werr != nil {
			slog.Debug("hook: codex plan marker write failed", "err", werr)
		}
	}
	slog.Info("hook: codex plan captured",
		"agent_id", agentID,
		"permission_mode", mode,
		"bytes", len(planText),
		"elapsed_ms", time.Since(started).Milliseconds())
}
