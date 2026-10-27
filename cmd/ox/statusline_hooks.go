package main

import (
	"log/slog"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/oxactivity"
	"github.com/sageox/ox/internal/session"
)

// Hook-side writers for the ox status line. Hooks count the team context an
// AI coworker pulled into the per-session state; `ox statusline` only reads
// it. Writers are best-effort: a failure is logged and never reaches the
// hook's output.

// statuslineEnabled reads the coworker's statusline setting. Callers load it
// only after they know there is something to record, so a hook that records
// nothing pays no config read.
func statuslineEnabled() bool {
	cfg, err := config.LoadUserConfig()
	if err != nil || cfg == nil {
		return true // the default; a broken config file must not hide the line
	}
	return cfg.IsStatuslineEnabled()
}

// statuslineCountedRefTypes are the retrieval ref_types the PostToolUse hook
// counts on the status line: reads that name one item of team context.
// Searches (query, code), listings (murmur list, session list), decisions and
// lifecycle calls pull no single item and are never counted here; murmurs and
// sessions are counted only by use-evidence writers (RecordInfluence).
var statuslineCountedRefTypes = map[string]bool{
	"discussion":   true,
	"plan":         true,
	"team-context": true,
}

// recordStatuslineRetrieval counts an already-classified retrieval on the
// status line of the harness session it ran in. Calls with no countable item
// return before the config read.
func recordStatuslineRetrieval(projectRoot, agentID, harnessSessionID string, act oxactivity.Activity, refType string) {
	if projectRoot == "" || act.Kind != oxactivity.KindRetrieval || !statuslineCountedRefTypes[refType] {
		return
	}
	ref := act.Ref
	if refType == "team-context" {
		// the distilled Team Context as a whole: one doc per session, the same
		// item a reported use of any team-ctx/<slug> ref counts as
		ref = oxactivity.TeamContextRef
	}
	if ref == "" || !statuslineEnabled() {
		return
	}
	if err := oxactivity.RecordInfluence(projectRoot, agentID, harnessSessionID, refType, ref); err != nil {
		slog.Debug("statusline: record influence failed", "agent_id", agentID, "ref_type", refType, "err", err)
	}
}

// recordStatuslineUse counts one surfaced item the AI coworker used — reported
// with `ox agent <id> context used` or cited in a commit message — on the
// status line of the recording's harness session. Only a live recording names
// that session, so a nil state or one without AgentSessionID records nothing.
func recordStatuslineUse(projectRoot string, state *session.RecordingState, refType, ref string) {
	if projectRoot == "" || state == nil || state.AgentID == "" || state.AgentSessionID == "" || ref == "" {
		return
	}
	if !statuslineEnabled() {
		return
	}
	if err := oxactivity.RecordUse(projectRoot, state.AgentID, state.AgentSessionID, refType, ref); err != nil {
		slog.Debug("statusline: record use failed", "agent_id", state.AgentID, "ref_type", refType, "err", err)
	}
}
