package main

import (
	"log/slog"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/oxactivity"
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

// statuslineTeamCtxRef is the item `ox agent <id> team-ctx` reads: the
// distilled Team Context as a whole, so it counts as one doc per session.
const statuslineTeamCtxRef = "agent team-ctx"

// recordStatuslineRetrieval counts an already-classified retrieval on the
// status line of the harness session it ran in. Calls with no countable item
// return before the config read.
func recordStatuslineRetrieval(projectRoot, agentID, harnessSessionID string, act oxactivity.Activity, refType string) {
	if projectRoot == "" || act.Kind != oxactivity.KindRetrieval || !statuslineCountedRefTypes[refType] {
		return
	}
	ref := act.Ref
	if refType == "team-context" {
		ref = statuslineTeamCtxRef
	}
	if ref == "" || !statuslineEnabled() {
		return
	}
	if err := oxactivity.RecordInfluence(projectRoot, agentID, harnessSessionID, refType, ref); err != nil {
		slog.Debug("statusline: record influence failed", "agent_id", agentID, "ref_type", refType, "err", err)
	}
}
