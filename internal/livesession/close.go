package livesession

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/fileutil"
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
// blocks longer than budget.
func Close(ctx context.Context, cfg Config, dir, summary string, budget time.Duration) (DrainResult, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return DrainResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if err := requestClose(dir); err != nil {
		cfg.Logger.WarnContext(ctx, "live_session_close_request_failed", "session", filepath.Base(dir), "error", err)
	}
	var res DrainResult
	err = withLock(ctx, dir, budget, func() error {
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
			err = s.finish(ctx, summary, &res)
		}
		res.Acked, res.Status = s.st.Acked, s.st.Status
		res.Done = err == nil && !s.st.active()
		if res.Done {
			if rerr := removeIfExists(filepath.Join(dir, closeRequestFileName)); rerr != nil {
				s.log().WarnContext(ctx, "live_session_close_request_remove_failed", "error", rerr)
			}
		}
		return err
	})
	return res, err
}

// requestClose durably records that stop asked for the close before Close
// waits for the session. It takes no lock: a daemon drain can hold live.lock
// longer than stop's budget, and a close lost to that wait left the stream
// open on the server for good, since the daemon only finishes closes it knows
// about. The next locked Close, this one or the daemon's, acts on it.
func requestClose(dir string) error {
	st, err := ReadState(dir)
	if err != nil || st.CloseRequested || (st.Status != StatusPending && st.Status != StatusStreaming) {
		return nil // loadSession reports a missing state; nothing else needs the marker
	}
	if err := fileutil.AtomicWriteBytes(filepath.Join(dir, closeRequestFileName), nil, 0o600); err != nil {
		return fmt.Errorf("record close request: %w", err)
	}
	return nil
}

// openPending opens a session that never reached the server so Close can drain
// and finish it. The close request is persisted first: if the server is down the
// daemon still knows stop already happened.
func openPending(ctx context.Context, cfg Config, dir string) error {
	st, err := ReadState(dir)
	if err != nil || st.Status != StatusPending {
		return nil // loadSession reports a missing state; other statuses need no open
	}
	// before the write below: a forged state is refused untouched
	if err := authorize(ctx, cfg, dir, st); err != nil {
		return err
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

func (s *session) finish(ctx context.Context, summary string, res *DrainResult) error {
	if len(summary) > sessionstream.MaxSummaryBytes {
		summary = "" // too large for the server; the session closes without one
	}
	if err := s.declareLost(); err != nil {
		return err // stay closing: without the gap line the close would claim a complete stream
	}
	checked := false
	for restarts := 0; ; {
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
		err = s.c.Close(ctx, s.st.ConversationID, sessionstream.CloseRequest{
			LayerID: s.st.LayerID, StoredThrough: size, StreamSHA256: sum, Summary: summary,
		})
		if err == nil {
			s.log().InfoContext(ctx, "live_session_closed", "stored_through", size)
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
