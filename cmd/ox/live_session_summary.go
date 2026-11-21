package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/fileutil"
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

// liveConversationEntries is what the live close summarizes: what the stream
// carried. A stream resumed after the agent exited restates the whole
// conversation (earlier recordings first), so its summary must cover all of
// it, while raw holds only this recording's lines. A stream that withheld a
// batch as a secret carries a gap line where raw.jsonl keeps the original, so
// its summary is built from the spool, never raw, or from nothing when the
// spool cannot be read: a summary may quote prompts, and the secret must not
// reach SageOx through it. The spool is also redacted, with paused content
// left out. Any other stream, or a spool that cannot be read, summarizes raw
// as before.
func liveConversationEntries(dir string, raw []map[string]any) []map[string]any {
	var entries []map[string]any
	// Read state and spool under the stream's live.lock: a drain withholds a
	// batch under it (saving the flag, then rewriting the spool), so outside it
	// a read could pair the old spool, which still holds the batch, with
	// either state. The summary runs before the close takes the lock, so this
	// never waits on itself. If the lock cannot be had, nothing is quoted.
	ctx, cancel := context.WithTimeout(context.Background(), liveSummaryLockWait)
	defer cancel()
	err := fileutil.WithFileLockTimeout(ctx, livesession.LockTarget(dir), liveSummaryLockWait, func() error {
		entries = carriedEntries(dir, raw)
		return nil
	})
	if err != nil {
		slog.Warn("live_session_summary_entries_unlocked", "session", filepath.Base(dir), "impact", "summary quotes no prompt", "error", err)
		return nil
	}
	return entries
}

// liveSummaryLockWait bounds the wait for live.lock before the stop
// summarizes: a drain holds it for at most its own budget, and the stop is
// already waiting on the model, so a few seconds cost nothing. A var so a
// test can shorten it.
var liveSummaryLockWait = 5 * time.Second

// carriedEntries applies the rule above; the caller holds live.lock.
func carriedEntries(dir string, raw []map[string]any) []map[string]any {
	st, err := livesession.ReadState(dir)
	if err != nil {
		return raw
	}
	stored, err := session.ReadSessionFromPath(filepath.Join(dir, "spool.jsonl"))
	switch {
	case st.SecretWithheld && err != nil:
		return nil
	case st.SecretWithheld:
		return stored.Entries
	case st.SeedBytes == 0 || err != nil || len(stored.Entries) < len(raw):
		return raw
	}
	return stored.Entries
}
