package livesession

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/sageox/ox/internal/config"
	oxsession "github.com/sageox/ox/internal/session"
)

// SpoolCommitted is the one step every capture path takes once it has
// committed a batch to raw.jsonl: the Claude Code hook and the daemon's
// catch-up, the tail watcher, stop's final drain, and Cursor's drains (hook,
// watcher, stop and inactive finalize). batch must be exactly what the append
// wrote (oxsession.AppendedBatch), never a re-read of raw.jsonl. The recording's
// cursor is already past those bytes, so a batch this step neither spools nor
// declares would be missing from the stream for good, with nothing to say so.
//
// The stream's ownership and hold checks live here, once. A batch never feeds
// a stream that belongs to another recording in a reused folder (C4), nor one
// whose session is held on this machine or publishes manually (C1). A paused
// batch is never streamed, and is not a loss: the stream leaves it out on
// purpose. projectRoot is where the session's publishing mode is configured;
// "" checks the recording's own recorded mode only.
//
// spooled reports that bytes were added to the spool, so the caller should ask
// for a drain. A non-nil error means the batch belonged to a stream that could
// not take it. It is declared, so the stream's next write or its close carries
// a spool_write_failed gap of exactly the batch's size.
func SpoolCommitted(ctx context.Context, projectRoot string, rec *oxsession.RecordingState, batch oxsession.AppendedBatch) (spooled bool, err error) {
	if rec == nil || rec.SessionPath == "" || len(batch.Bytes) == 0 || batch.Suspended {
		return false, nil
	}
	dir := rec.SessionPath
	log := slog.With("session", filepath.Base(dir), "session_id", rec.SessionID, "bytes", len(batch.Bytes))
	st, stateErr := ReadState(dir)
	if errors.Is(stateErr, ErrNotStreaming) {
		return false, nil // never prepared: this session does not stream
	}
	if stateErr == nil && st.SessionID != rec.SessionID {
		log.DebugContext(ctx, "live_session_spool_skipped", "reason", "other_recording", "stream_session_id", st.SessionID)
		return false, nil
	}
	if heldOnThisMachine(projectRoot, rec) {
		log.DebugContext(ctx, "live_session_spool_skipped", "reason", "held")
		return false, nil
	}
	if stateErr != nil {
		// The stream exists but its state cannot be read, so whose it is cannot
		// be checked and the batch cannot be spooled. Declared, not dropped: a
		// gap in a stream that turns out to be another recording's overstates a
		// loss, and a silent drop hides one.
		err := fmt.Errorf("spool committed batch: %w", stateErr)
		declareErr := DeclareLost(ctx, dir, int64(len(batch.Bytes)), err) // logs the loss itself
		log.WarnContext(ctx, "live_session_spool_failed", "reason", "stream_state_unreadable", "declared", declareErr == nil, "error", err)
		return false, err
	}
	res, err := Spool(ctx, dir, batch.Bytes, SpoolOptions{})
	switch {
	case errors.Is(err, ErrNotStreaming):
		return false, nil // the stream ended (closed, deleted or disabled): nothing to feed
	case err != nil:
		// Spool has already declared the batch it could not write (and logged
		// whether that record held), so it is not declared again: a second
		// record would double the gap.
		log.WarnContext(ctx, "live_session_spool_failed", "reason", "spool_write", "error", err)
		return false, err
	}
	return res.Appended > 0, nil
}

// heldOnThisMachine reports whether nothing more of rec's session may leave
// this machine: it is held here, or it publishes manually, by the choice
// recorded on it or by its project's configuration now. A session switched to
// manual mid-recording counts (C1).
func heldOnThisMachine(projectRoot string, rec *oxsession.RecordingState) bool {
	return oxsession.IsHeld(rec.SessionPath) || rec.RecordedManualPublishing() ||
		(projectRoot != "" && config.GetSessionPublishing(projectRoot) == config.SessionPublishingManual)
}
