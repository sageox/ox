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
// server writes its own. Close never blocks longer than budget.
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
		res.Done = !s.st.active()
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
		summary = "" // too large for the server; it generates one when missing
	}
	for attempt := 0; ; attempt++ {
		if err := s.drain(ctx, res); err != nil || !s.st.active() {
			return err
		}
		size, sum, err := s.spoolSum()
		if err != nil || s.st.Acked != size {
			return err // not fully acked: stay closing, never close across a gap
		}
		err = s.c.Close(ctx, s.st.ConversationID, sessionstream.CloseRequest{
			LayerID: s.st.LayerID, StoredThrough: size, StreamSHA256: sum, Summary: summary,
		})
		if err == nil {
			s.log().InfoContext(ctx, "live_session_closed", "stored_through", size)
			s.setStatus(ctx, StatusClosed)
			return nil
		}
		v, err := s.onCloseRefusal(ctx, err)
		if err != nil || v == vStop {
			return err
		}
		if attempt >= maxRestarts {
			return errors.New("live close refused after restarting")
		}
		if v == vNewLayer {
			if err := s.restartLayer(ctx); err != nil {
				return err
			}
		}
	}
}

// onCloseRefusal: a refusal that says the stream differs sends us back to drain
// (resync) or onto a new layer; anything transient is returned for a later retry.
func (s *session) onCloseRefusal(ctx context.Context, err error) (verdict, error) {
	var api *sessionstream.APIError
	if !errors.As(err, &api) {
		return vStop, fmt.Errorf("close live session: %w", err)
	}
	if s.terminal(ctx, api) {
		return vStop, nil
	}
	if retryable(api) {
		return vStop, fmt.Errorf("close live session: %w", api)
	}
	switch api.Body.Action {
	case sessionstream.ActionResync:
		return vContinue, nil
	case sessionstream.ActionNewLayer:
		return vNewLayer, nil
	default:
		s.setStatus(ctx, StatusDisabled)
		return vStop, fmt.Errorf("close live session refused, streaming disabled: %w", api)
	}
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
