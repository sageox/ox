package main

import (
	"log/slog"
	"path/filepath"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
)

// recordTurnUses counts, on the status line, the surfaced items the AI
// coworker's latest turn shows it used. It runs from the Stop hook, once per
// turn, after the turn has been drained into raw.jsonl: that reply is where
// the use evidence lands. Without it "N used" moved only when `ox session
// report` ran the same scan, so with the pane closed the line sat stale until
// the coworker opened it.
//
// The scan is withLiveUses, the same incremental scan the report runs, and
// its only effect here is the recordStatuslineUse side effect for fresh
// evidence. The report and this hook can scan at the same time: each writes
// the scan cache atomically from its own read, so the loser's write is a
// slightly older offset and costs one extra incremental scan, and
// recordStatuslineUse dedups under the state file lock, so a use both find
// counts once. Fail-open: nothing here can fail the turn.
func recordTurnUses(projectRoot, agentID string) {
	if projectRoot == "" || agentID == "" || !statuslineEnabled() {
		return
	}
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil || state == nil || state.SessionPath == "" {
		return // not recording: no reply to scan
	}
	if state.AgentSessionID == "" {
		// The status line is keyed by the harness session, and
		// recordStatuslineUse counts nothing for a recording not yet bound to
		// one. Scanning now would still cache what it found as seen, so the
		// first scan after the binding would never count it. Wait.
		return
	}
	events, err := contexttrace.ReadEvents(filepath.Join(state.SessionPath, contexttrace.FileName))
	if err != nil {
		slog.Debug("statusline: read trace for turn uses", "agent_id", agentID, "err", err)
		return
	}
	_ = withLiveUses(projectRoot, state, events)
}
