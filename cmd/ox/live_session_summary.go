package main

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/livesummary"
	"github.com/sageox/ox/internal/session"
)

// liveCloseSummary is the summary `ox session stop` closes the live stream
// with (see livesummary.Bounded: bounded by livesession.SummaryBudget, empty on
// a thin session, a failure or a timeout).
func liveCloseSummary(state *session.RecordingState, raw []map[string]any) string {
	raw = liveConversationEntries(state.SessionPath, raw)
	return livesummary.Bounded(context.Background(), slog.With("session_id", state.SessionID), state.AdapterName, raw)
}

// liveConversationEntries is what the live close summarizes. A stream resumed
// after the agent exited restates the whole conversation (earlier recordings
// first), so its summary must cover all of it, while raw holds only this
// recording's lines. The spool carries exactly what the stream carries:
// redacted, with paused content already left out. Any other stream, or a spool
// that cannot be read, summarizes raw as before.
func liveConversationEntries(dir string, raw []map[string]any) []map[string]any {
	st, err := livesession.ReadState(dir)
	if err != nil || st.SeedBytes == 0 {
		return raw
	}
	stored, err := session.ReadSessionFromPath(filepath.Join(dir, "spool.jsonl"))
	if err != nil || len(stored.Entries) < len(raw) {
		return raw
	}
	return stored.Entries
}
