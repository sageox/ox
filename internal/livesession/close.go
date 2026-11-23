package livesession

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	oxsession "github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionstream"
)

// CloseBudget is the time `session stop` gives the final drain and close (C7).
const CloseBudget = 15 * time.Second

// Close ends the stream: it marks the session closing, drains within budget and,
// only after every spool byte is acknowledged, sends close with the whole-stream
// sha256. summary is the cheap local summary, or empty. If the server cannot be
// reached in time the status stays closing and a later Close (the daemon's tick)
// finishes the job; the summary is not persisted, so a retry sends none and the
// session has no summary (nothing on the server writes one). Close never
// blocks longer than budget. The summary counts as written for the
// conversation the close finds when it starts (see Summary).
func Close(ctx context.Context, cfg Config, dir, summary string, budget time.Duration) (DrainResult, error) {
	return CloseOf(ctx, cfg, dir, "", Summary{Text: summary}, budget)
}

// Summary is the summary a close sends and the conversation the stream was on
// when the summarizer read it. A close that ends up on another conversation
// sends none: a deferred reopen resolved since (the spool now restates the
// earlier recording too), or a refused reopen fell back (the earlier
// recording's lines left the spool, and a summary of them, of a conversation
// someone may have deleted, must not publish them under the replacement).
type Summary struct {
	Text string
	// ConversationID is the conversation the summary was written for; empty
	// means the one the close finds when it takes live.lock.
	ConversationID string
}

// ErrOtherRecording means the folder's stream now belongs to another recording
// than the one closing it (a new recording started in the same minute reused
// the folder): nothing was closed and no summary was sent.
var ErrOtherRecording = errors.New("the folder's live stream belongs to another recording")

// CloseOf is Close for the stream of the recording sessionID. The check runs
// under live.lock, after the possibly long summary was written without it, so a
// recording that took over the folder meanwhile is never closed with another
// recording's summary. An empty sessionID closes whatever stream the folder
// holds (the daemon's tick, which names no recording).
func CloseOf(ctx context.Context, cfg Config, dir, sessionID string, summary Summary, budget time.Duration) (DrainResult, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return DrainResult{}, err
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	// not the request marker of a stream that is no longer this recording's
	if other(dir, sessionID) {
		return DrainResult{}, ErrOtherRecording
	}
	if err := RequestClose(dir); err != nil {
		cfg.Logger.WarnContext(ctx, "live_session_close_request_failed", "session", filepath.Base(dir), "error", err)
	}
	var res DrainResult
	err = withLockWithin(ctx, parent, dir, budget, func() error {
		if other(dir, sessionID) { // again under the lock: the check above can lose a race
			return ErrOtherRecording
		}
		if summary.ConversationID == "" {
			if st, err := ReadState(dir); err == nil {
				summary.ConversationID = st.ConversationID
			}
		}
		if err := openPending(ctx, cfg, dir); err != nil {
			return err
		}
		s, err := loadSession(cfg, dir)
		if err != nil {
			return err
		}
		if err := s.connect(ctx); err != nil {
			return err
		}
		if s.st.Status == StatusStreaming {
			s.setStatus(ctx, StatusClosing)
		}
		if s.st.active() {
			err = s.finish(ctx, s.summaryFor(ctx, summary), &res)
			// best effort and independent of how the close went: a receipt owed
			// must not wait for a sweep that skips closed streams
			s.sendTeammateAcks(ctx)
		}
		res.Acked, res.Status = s.st.Acked, s.st.Status
		res.Done = err == nil && !s.st.active()
		if res.Done {
			if rerr := removeIfExists(filepath.Join(dir, closeRequestFileName)); rerr != nil {
				s.log().WarnContext(ctx, "live_session_close_request_remove_failed", "error", rerr)
			}
			// Empty too: a resumed stream that ended before a new prompt is
			// still the conversation. A manual stop removes its stash (final);
			// any other end refreshes it, so the next prompt still resumes.
			if s.st.Status == StatusClosed || s.st.Status == StatusEmpty {
				// whoever finished the close (stop, hook or daemon), cache
				// cleanup must not take away the resume of this conversation
				if serr := StashReopen(ctx, cfg, dir); serr != nil {
					s.log().WarnContext(ctx, "live_session_reopen_stash_failed", "error", serr)
				}
			}
		}
		return err
	})
	return res, err
}

// summaryFor is sum's text when it was written for the conversation s now
// streams, else "" (see Summary).
func (s *session) summaryFor(ctx context.Context, sum Summary) string {
	if sum.Text == "" || sum.ConversationID == s.st.ConversationID {
		return sum.Text
	}
	s.log().InfoContext(ctx, "live_session_summary_dropped", "reason", "conversation_changed", "summarized_conversation_id", sum.ConversationID)
	return ""
}

// other reports whether dir's stream belongs to a recording other than
// sessionID (never, for an empty sessionID or a folder with no stream).
func other(dir, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	st, err := ReadState(dir)
	return err == nil && st.SessionID != sessionID
}

// RequestClose durably records that the stream in dir must close, with no
// network call: Close does it before it waits for the session, and a new
// recording does it for a stream it supersedes. It takes no lock: a daemon
// drain can hold live.lock longer than stop's budget, and a close lost to that
// wait left the stream open on the server for good, since the daemon only
// finishes closes it knows about. The next locked Close, the caller's or the
// daemon's, acts on it.
func RequestClose(dir string) error {
	st, err := ReadState(dir)
	if err != nil || st.CloseRequested || (st.Status != StatusPending && st.Status != StatusStreaming) {
		return nil // loadSession reports a missing state; nothing else needs the marker
	}
	if err := fileutil.AtomicWriteBytes(filepath.Join(dir, closeRequestFileName), nil, 0o600); err != nil {
		return fmt.Errorf("record close request: %w", err)
	}
	return nil
}

// RequestCloseOf is RequestClose for the stream of the recording sessionID
// only, as a new recording asks of one it supersedes. The check and the write
// happen under live.lock, which a recording reusing the folder holds to retire
// that stream and prepare its own, so a stream started since the caller read
// the folder is never closed in its place: that returns ErrNotStreaming. A lock
// still busy after wait (a drain in flight) is returned as is, and the request
// waits for the AI coworker's next start.
func RequestCloseOf(ctx context.Context, dir, sessionID string, wait time.Duration) error {
	return withLock(ctx, dir, wait, func() error {
		st, err := ReadState(dir)
		if err != nil {
			return err
		}
		if st.SessionID != sessionID {
			return ErrNotStreaming
		}
		return RequestClose(dir)
	})
}

// RequestCloseOfEnded is RequestClose for the stream of the recording
// sessionID, which ended with no stop to close it: an AI coworker that crashed,
// recovered from its cache or finalized by the daemon. Left open, that stream
// read as live on SageOx until the server's 72 h auto-finish. The daemon's
// sweep finishes the close, summary included, as for a hook-ended recording.
//
// It requests nothing for a held session (the sweep deletes its live copy with
// stopHeld and never closes it with a summary), for another recording's
// stream (the folder was reused), or for a stream already closing or ended.
// Like RequestClose it takes no lock, so a drain in flight cannot lose it; the
// marker names sessionID instead, so a recording restarted in the same minute
// that takes the folder between the read below and the write is never closed
// by it (closeRequestedFor). It reports whether a close was recorded.
func RequestCloseOfEnded(dir, sessionID string) (bool, error) {
	if sessionID == "" || oxsession.IsHeld(dir) {
		return false, nil
	}
	st, err := ReadState(dir)
	if errors.Is(err, ErrNotStreaming) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if st.SessionID != sessionID || st.CloseRequested || (st.Status != StatusPending && st.Status != StatusStreaming) {
		return false, nil
	}
	if err := fileutil.AtomicWriteBytes(filepath.Join(dir, closeRequestFileName), []byte(sessionID), 0o600); err != nil {
		return false, fmt.Errorf("record close request: %w", err)
	}
	return true, nil
}

// openPending opens a session that never reached the server so Close can drain
// and finish it. The close request is persisted first: if the server is down the
// daemon still knows stop already happened. A session whose spool never held a
// human prompt ends here as StatusEmpty instead, with nothing sent: the server
// never hears of it.
func openPending(ctx context.Context, cfg Config, dir string) error {
	st, err := ReadState(dir)
	if err != nil || st.Status != StatusPending {
		return nil // loadSession reports a missing state; other statuses need no open
	}
	// before the write below: a forged state is refused untouched
	if err := authorize(ctx, cfg, dir, st); err != nil {
		return err
	}
	if st.AwaitReopen != nil {
		// a stop while the awaited stream still closes resolves once, now: a
		// stop never waits on another stream's summary
		if st, err = resolveAwait(ctx, cfg, dir, st, true); err != nil {
			return err
		}
	}
	if !st.OpenSent {
		empty, err := endIfPromptless(ctx, dir, st)
		if err != nil {
			return err
		}
		if empty {
			cfg.Logger.InfoContext(ctx, "live_session_ended_before_first_prompt", "session_id", st.SessionID, "conversation_id", st.ConversationID, "layer_id", st.LayerID)
			return nil
		}
	}
	if !st.CloseRequested {
		st.CloseRequested = true
		if err := writeState(dir, st); err != nil {
			return err
		}
	}
	_, err = openLocked(ctx, cfg, dir, st.ConversationID, OpenParams{
		SessionID: st.SessionID, SessionName: st.SessionName, RepoID: st.RepoID, Agent: st.Agent, StartedAt: st.StartedAt,
	})
	return err
}

// endIfPromptless marks the pending stream st in dir StatusEmpty when its
// spool holds no human prompt. The caller holds live.lock, and the swap lock
// held here is what an append that found live.lock busy takes to read the
// status and write, so a prompt is either seen here or refused afterwards
// (ErrNotStreaming; raw.jsonl keeps it): never spooled into an ended stream.
func endIfPromptless(ctx context.Context, dir string, st State) (bool, error) {
	empty := false
	err := withSwapLock(ctx, dir, func() error {
		if mayHoldPromptFrom(dir, st.SeedBytes) {
			return nil
		}
		st.Status, st.CloseRequested = StatusEmpty, false
		if err := writeState(dir, st); err != nil {
			return err
		}
		empty = true
		return nil
	})
	return empty, err
}

// heldAtClose reports whether the session is held, logging that the close is
// left to the daemon's stopHeld. Checked under live.lock: the hold writer takes
// the same lock (session.WriteHoldMarkerFor), so a hold placed first is seen
// here and one placed later waits for this close. A held session is never
// uploaded or summarized by an automatic path, so a close that finds one sends
// nothing more (no tail, no summary, no close) and the stream stays closing
// for stopHeld, which deletes the live copy.
func (s *session) heldAtClose(ctx context.Context) bool {
	if !oxsession.IsHeld(s.dir) {
		return false
	}
	s.log().InfoContext(ctx, "live_session_close_held", "acked", s.st.Acked, "impact", "close_left_to_held_stop")
	return true
}

func (s *session) finish(ctx context.Context, summary string, res *DrainResult) error {
	if len(summary) > sessionstream.MaxSummaryBytes {
		summary = "" // too large for the server; the session closes without one
	}
	if err := s.declareLost(); err != nil {
		return err // stay closing: without the gap line the close would claim a complete stream
	}
	checked := false
	for restarts := 0; ; {
		if s.heldAtClose(ctx) {
			return nil
		}
		if err := s.drain(ctx, res); err != nil || !s.st.active() {
			return err
		}
		size, sum, err := s.spoolSum()
		if err != nil || s.st.Acked != size {
			return err // not fully acked: stay closing, never close across a gap
		}
		if !checked {
			summary, checked = s.publishableSummary(ctx, summary), true
		}
		// again immediately before the send: the drain above can take a while,
		// and a hold placed during it must still stop the summary
		if s.heldAtClose(ctx) {
			return nil
		}
		err = s.c.Close(ctx, s.st.ConversationID, sessionstream.CloseRequest{
			LayerID: s.st.LayerID, StoredThrough: size, StreamSHA256: sum, Summary: summary,
		})
		if err == nil {
			s.log().InfoContext(ctx, "live_session_closed", "stored_through", size)
			s.edgeCleared(ctx) // setStatus persists it
			s.setStatus(ctx, StatusClosed)
			s.reportUnsentTail(ctx, size)
			return nil
		}
		var api *sessionstream.APIError
		if errors.As(err, &api) && api.Body.RetryAfterMS != nil {
			// api-go defers a close whose end it cannot prove yet (recovery
			// budget spent, storage blip); the daemon backs off this long (C9)
			res.RetryAfter = time.Duration(*api.Body.RetryAfterMS) * time.Millisecond
		}
		v, err := s.onCloseRefusal(ctx, err, size)
		if err != nil || v == vStop {
			return err
		}
		if v != vNewLayer {
			continue // resync: the next drain resends from the server's stored_through
		}
		if restarts++; restarts > maxRestarts {
			return errors.New("live close refused after restarting on a new layer")
		}
		if err := s.restartLayer(ctx); err != nil {
			return err
		}
	}
}

// reportUnsentTail re-measures the spool after the server accepted a close of
// closedThrough bytes. A hook that found live.lock busy spools without it (see
// Spool), so a batch can land while the close request is on the wire. The
// server answers session_closed to anything sent after a close, so those bytes
// can never reach the stream; raw.jsonl keeps them. They are reported at Error
// rather than left to vanish from the stream unremarked. Only an append in the
// instant between this measure and the status write above can still slip past.
func (s *session) reportUnsentTail(ctx context.Context, closedThrough int64) {
	f, err := os.Open(s.spoolPath())
	if err != nil {
		return // no spool: nothing was appended after the close measured it
	}
	defer func() { _ = f.Close() }() // read-only: nothing to flush
	end, err := spoolEnd(f)
	if err != nil {
		s.log().WarnContext(ctx, "live_session_close_remeasure_failed", "closed_through", closedThrough, "error", err)
		return
	}
	if end > closedThrough {
		s.log().ErrorContext(ctx, "live_session_closed_without_tail",
			"closed_through", closedThrough, "spool_end", end, "unsent_bytes", end-closedThrough)
	}
}

// declareLost writes a pending spool-lost count into the spool as its gap
// line before the close measures the stream (see Spool).
func (s *session) declareLost() error {
	if !readLost(s.dir).pending {
		return nil
	}
	if _, err := appendToSpool(s.dir, s.st.Acked, nil, SpoolOptions{}); err != nil {
		return fmt.Errorf("declare lost spool bytes before close: %w", err)
	}
	return nil
}

// onCloseRefusal: a refusal that says the stream differs sends us back to drain
// from the server's stored_through (resync) or onto a new layer; anything
// transient or unknown is returned for a later retry (C7). size is the stream
// length the refused close claimed.
func (s *session) onCloseRefusal(ctx context.Context, err error, size int64) (verdict, error) {
	var api *sessionstream.APIError
	if !errors.As(err, &api) {
		return vStop, fmt.Errorf("close live session: %w", err)
	}
	if shape, ok := edgeShape(api); ok {
		eerr := s.edgeBlocked(ctx, api, shape)
		if perr := s.persist(); perr != nil {
			s.log().WarnContext(ctx, "live_session_state_write_failed", "during", "edge_blocked", "error", perr)
		}
		return vStop, fmt.Errorf("close live session: %w (status %d, %s body)", eerr, api.Status, shape)
	}
	// SageOx itself answered: a fully acked stream sends no append, so only
	// a close reply can end an edge streak that began on a close
	if s.edgeCleared(ctx) {
		if perr := s.persist(); perr != nil {
			s.log().WarnContext(ctx, "live_session_state_write_failed", "during", "edge_unblocked", "error", perr)
		}
	}
	if done, err := s.terminal(ctx, api); done || err != nil {
		return vStop, err
	}
	switch classify(api) {
	case refusalResync:
		return s.closeResync(ctx, api, size)
	case refusalNewLayer:
		return vNewLayer, nil
	case refusalStop:
		s.disable(ctx, api.Body.Reason, "action", string(api.Body.Action), "status", api.Status, "during", "close")
		return vStop, fmt.Errorf("close live session refused, streaming disabled: %w", api)
	default:
		return vStop, fmt.Errorf("close live session: %w", api)
	}
}

// closeResync handles a close refused with resync: the server's row stands at
// stored_through and does not match the close. Retrying the close alone can
// never change that (each daemon tick would be refused again), so acked moves
// back to stored_through and the next drain resends from there, under the
// drain's resync cap. ox never frees spool bytes, so any whole-line offset
// short of size can be resent, however far behind its acks the server fell.
// At or past size, or inside a line, the server holds a stream this spool did
// not write (equal lengths refused means the hashes differ): a new layer.
func (s *session) closeResync(ctx context.Context, api *sessionstream.APIError, size int64) (verdict, error) {
	if api.Body.StoredThrough == nil {
		return vStop, fmt.Errorf("close resync without stored_through: %w", api)
	}
	if s.resyncs++; s.resyncs > maxResyncs {
		return vStop, fmt.Errorf("close resync loop at offset %d: %w", s.st.Acked, api)
	}
	stored := *api.Body.StoredThrough
	if stored >= 0 && stored < s.st.Acked {
		// the server once answered 200 through acked and now holds less: it
		// lost acknowledged bytes. The resend below still repairs the stream
		// (ox never frees spool bytes), but a server that loses acks must show.
		s.log().ErrorContext(ctx, "live_session_server_behind_ack", "acked", s.st.Acked, "stored_through", stored, "closed_through", size)
	}
	if stored < 0 || stored >= size || !s.spoolLineBoundary(stored) {
		s.log().WarnContext(ctx, "live_session_close_diverged", "stored_through", stored, "closed_through", size)
		return vNewLayer, nil
	}
	s.log().WarnContext(ctx, "live_session_close_resync", "stored_through", stored, "acked", s.st.Acked, "closed_through", size)
	s.st.Acked = stored
	return vContinue, s.persist()
}

func (s *session) spoolLineBoundary(off int64) bool {
	f, err := os.Open(s.spoolPath())
	if err != nil {
		return off == 0
	}
	defer func() { _ = f.Close() }() // read-only: nothing to flush
	return atLineBoundary(f, off)
}

// spoolSum is the length and stream hash of the whole spool (whole lines).
func (s *session) spoolSum() (int64, string, error) {
	f, err := os.Open(s.spoolPath())
	if errors.Is(err, os.ErrNotExist) {
		return 0, sessionstream.EmptyStreamSHA256, nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("open spool: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only or append-only: nothing to flush
	end, err := spoolEnd(f)
	if err != nil {
		return 0, "", err
	}
	h, err := prefixHash(f, end)
	if err != nil {
		return 0, "", err
	}
	return end, h.Sum(), nil
}
