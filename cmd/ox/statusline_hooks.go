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
	// Bound to the live recording so the bar counts what the session report
	// counts: a resumed harness session does not carry an old recording's
	// counts into a new one.
	recordingID := ""
	if state, err := session.LoadRecordingStateForAgent(projectRoot, agentID); err == nil && state != nil {
		recordingID = state.SessionID
	}
	if err := oxactivity.RecordInfluenceIn(projectRoot, agentID, harnessSessionID, recordingID, refType, ref); err != nil {
		slog.Debug("statusline: record influence failed", "agent_id", agentID, "ref_type", refType, "err", err)
	}
}

// recordStatuslineUse counts one surfaced item the AI coworker used — reported
// with `ox agent <id> context used` or cited in a commit message — on the
// status line of the harness session the AI coworker runs in.
//
// The harness session comes from the live recording when there is one, and
// the counts are bound to that recording. A recording not yet bound to a
// harness session (no AgentSessionID) records nothing: recordTurnUses relies
// on that to defer its scan. With no live recording (state == nil) the
// harness session comes from agentID's primed session marker, and the write
// names no recording: counts that have never belonged to a recording are
// adopted by the first one that starts, while counts left by an earlier
// recording, and anything added to them since, start over with the next one
// (State.BindRecording). No marker, or an agent ID primed in more than one
// harness session, records nothing. An empty harness session never records,
// so nothing is written agent-wide.
//
// aliases are the item's other names (contextuse.SurfacedRef.Aliases, such as
// a plan's slug beside its pln_ id), so a use and a fetch under different
// names count as one item.
func recordStatuslineUse(projectRoot, agentID string, state *session.RecordingState, refType, ref string, aliases ...string) {
	if projectRoot == "" || ref == "" {
		return
	}
	if state != nil {
		if state.AgentID == "" || state.AgentSessionID == "" || !statuslineEnabled() {
			return
		}
		if err := oxactivity.RecordUseIn(projectRoot, state.AgentID, state.AgentSessionID, state.SessionID, refType, ref, aliases...); err != nil {
			slog.Debug("statusline: record use failed", "agent_id", state.AgentID, "ref_type", refType, "err", err)
		}
		return
	}
	if agentID == "" {
		return
	}
	marker := FindUnambiguousSessionMarkerByAgentID(agentID)
	if marker == nil || marker.AgentSessionID == "" || !statuslineEnabled() {
		return
	}
	if err := oxactivity.RecordUse(projectRoot, agentID, marker.AgentSessionID, refType, ref, aliases...); err != nil {
		slog.Debug("statusline: record use failed", "agent_id", agentID, "ref_type", refType, "err", err)
	}
}
