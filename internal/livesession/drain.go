package livesession

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/sessionstream"
)

const (
	// maxRestarts bounds new-layer restarts in one call so a server that keeps
	// refusing a stream cannot spin the daemon.
	maxRestarts = 2
	// maxResyncs bounds resync answers in one drain for the same reason.
	maxResyncs = 16
)

// DrainResult is what one Drain or Close call achieved.
type DrainResult struct {
	// Sent is how many spool bytes the server newly acknowledged.
	Sent int64
	// Acked is the acknowledged spool offset after the call.
	Acked  int64
	Status Status
	// Done: for Drain, nothing is left to send or the stream reached a final
	// status; for Close, the stream reached a final status (closed, deleted or
	// disabled).
	Done bool
	// RetryAfter is the server's requested backoff after a rate limit; zero otherwise.
	RetryAfter time.Duration
}

// verdict is what one append or close answer means for the loop above it.
type verdict int

const (
	vContinue verdict = iota
	vStop
	vNewLayer
)

// session is one locked view of a session dir during a call.
type session struct {
	cfg     Config
	dir     string
	st      State
	c       *sessionstream.Client
	resyncs int
}

func (s *session) spoolPath() string { return filepath.Join(s.dir, spoolFileName) }

func (s *session) log() *slog.Logger {
	return s.cfg.Logger.With("session_id", s.st.SessionID, "conversation_id", s.st.ConversationID, "layer_id", s.st.LayerID)
}

// loadSession reads live.json; it builds no client (see connect).
func loadSession(cfg Config, dir string) (*session, error) {
	st, err := ReadState(dir)
	if err != nil {
		return nil, err
	}
	return &session{cfg: cfg, dir: dir, st: st}, nil
}

// connect builds the signed client for an active stream.
func (s *session) connect(ctx context.Context) error {
	if !s.st.active() {
		return nil
	}
	c, err := s.cfg.newClient(ctx, s.st.Endpoint)
	if err != nil {
		return fmt.Errorf("live client: %w", err)
	}
	s.c = c
	return nil
}

func (s *session) persist() error { return writeState(s.dir, s.st) }

func (s *session) setStatus(ctx context.Context, status Status) {
	s.st.Status = status
	if err := s.persist(); err != nil {
		s.log().WarnContext(ctx, "live_session_state_write_failed", "status", string(status), "error", err)
	}
}

// Drain pushes spool bytes [acked, end) to the server within budget. Retryable
// trouble (offline, 429, 5xx, expired login) returns an error and leaves the
// spool for the next attempt; final outcomes (deleted, closed, disabled) return
// nil with Done set. Concurrent drains of one session are serialized by live.lock.
func Drain(ctx context.Context, cfg Config, dir string, budget time.Duration) (DrainResult, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return DrainResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	var res DrainResult
	err = withLock(ctx, dir, budget, func() error {
		s, err := loadSession(cfg, dir)
		if err != nil {
			return err
		}
		if err := s.connect(ctx); err != nil {
			return err
		}
		if s.st.active() {
			err = s.drain(ctx, &res)
		}
		res.Acked, res.Status = s.st.Acked, s.st.Status
		res.Done = !s.st.active() || s.caughtUp()
		return err
	})
	return res, err
}

func (s *session) caughtUp() bool {
	f, err := os.Open(s.spoolPath())
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }() // read-only or append-only: nothing to flush
	end, err := spoolEnd(f)
	return err == nil && s.st.Acked == end
}

func (s *session) drain(ctx context.Context, res *DrainResult) error {
	for restarts := 0; ; restarts++ {
		v, err := s.pump(ctx, res)
		if err != nil || v != vNewLayer {
			return err
		}
		if restarts >= maxRestarts {
			return errors.New("live stream refused after restarting on a new layer")
		}
		if err := s.restartLayer(ctx); err != nil {
			return err
		}
	}
}

// pump sends batches until the spool is caught up or an answer says otherwise.
func (s *session) pump(ctx context.Context, res *DrainResult) (verdict, error) {
	f, err := os.Open(s.spoolPath())
	if errors.Is(err, fs.ErrNotExist) {
		return vStop, nil // nothing spooled yet
	}
	if err != nil {
		return vStop, fmt.Errorf("open spool: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only or append-only: nothing to flush
	end, err := spoolEnd(f)
	if err != nil {
		return vStop, err
	}
	if s.st.Acked > end {
		return vNewLayer, nil // the spool is shorter than what we claimed acked
	}
	h, err := prefixHash(f, s.st.Acked)
	if err != nil {
		return vStop, err
	}
	for s.st.Acked < end {
		v, err := s.sendBatch(ctx, f, h, end, res)
		if err != nil || v != vContinue {
			return v, err
		}
	}
	return vStop, nil
}

func (s *session) sendBatch(ctx context.Context, f *os.File, h *sessionstream.StreamHash, end int64, res *DrainResult) (verdict, error) {
	batch, err := readBatch(f, s.st.Acked, end)
	if err != nil {
		return vStop, err
	}
	if len(batch) == 0 {
		return vStop, nil
	}
	resp, err := s.c.AppendRaw(ctx, s.st.ConversationID, s.st.LayerID, s.st.Acked, h.Sum(), batch)
	if err != nil {
		return s.onRefusal(ctx, err, f, h, end, res)
	}
	return s.onStored(ctx, f, h, batch, resp, end, res)
}

// onStored frees bytes only when the server's stream hash matches ours.
func (s *session) onStored(ctx context.Context, f *os.File, h *sessionstream.StreamHash, batch []byte, resp sessionstream.AppendResponse, end int64, res *DrainResult) (verdict, error) {
	stored := resp.StoredThrough
	if stored <= s.st.Acked || stored > end {
		return vNewLayer, nil
	}
	if stored == s.st.Acked+int64(len(batch)) {
		h.Write(batch)
	} else { // the server already held more than we sent
		nh, err := prefixHash(f, stored)
		if err != nil {
			return vStop, err
		}
		*h = *nh
	}
	if h.Sum() != resp.PrefixSHA256 {
		return vNewLayer, nil
	}
	res.Sent += stored - s.st.Acked
	s.st.Acked = stored
	if err := s.persist(); err != nil {
		return vStop, err
	}
	return vContinue, nil
}

// onRefusal maps a failed append to what the drain does next (C6).
func (s *session) onRefusal(ctx context.Context, err error, f *os.File, h *sessionstream.StreamHash, end int64, res *DrainResult) (verdict, error) {
	var api *sessionstream.APIError
	if !errors.As(err, &api) {
		return vStop, fmt.Errorf("append at offset %d: %w", s.st.Acked, err)
	}
	if s.terminal(ctx, api) {
		return vStop, nil
	}
	if retryable(api) {
		if ms := api.Body.RetryAfterMS; ms != nil {
			res.RetryAfter = time.Duration(*ms) * time.Millisecond
		}
		return vStop, fmt.Errorf("append at offset %d: %w", s.st.Acked, api)
	}
	switch api.Body.Action {
	case sessionstream.ActionResync:
		return s.resync(ctx, f, h, end, api)
	case sessionstream.ActionNewLayer:
		return vNewLayer, nil
	default: // stop, fix or an action we do not know: do not loop on it
		s.log().WarnContext(ctx, "live_session_disabled", "reason", api.Body.Reason, "action", string(api.Body.Action), "status", api.Status)
		s.setStatus(ctx, StatusDisabled)
		return vStop, nil
	}
}

// retryable: rate limits, server trouble and expired logins heal with time.
func retryable(api *sessionstream.APIError) bool {
	switch api.Body.Action {
	case sessionstream.ActionRetry, sessionstream.ActionReauth:
		return true
	}
	return api.Status == http.StatusTooManyRequests || api.Status >= 500
}

// terminal handles the answers that end a stream for good: session_closed and
// stop_and_wipe (410). A wipe deletes the spool only; raw.jsonl and the v1 path
// are never touched.
func (s *session) terminal(ctx context.Context, api *sessionstream.APIError) bool {
	switch {
	case api.Body.Reason == sessionstream.ReasonSessionClosed:
		s.log().InfoContext(ctx, "live_session_closed_by_server")
		s.setStatus(ctx, StatusClosed)
		return true
	case api.Body.Action == sessionstream.ActionStopAndWipe || api.Status == http.StatusGone:
		s.log().InfoContext(ctx, "live_session_deleted_by_server")
		s.wipe(ctx)
		return true
	}
	return false
}

func (s *session) wipe(ctx context.Context) {
	if err := removeIfExists(s.spoolPath()); err != nil {
		s.log().WarnContext(ctx, "live_session_spool_remove_failed", "error", err)
	}
	s.setStatus(ctx, StatusDeleted)
}

// resync moves acked to the server's stored_through when we hold those bytes;
// the next append's prefix hash proves the two sides agree.
func (s *session) resync(ctx context.Context, f *os.File, h *sessionstream.StreamHash, end int64, api *sessionstream.APIError) (verdict, error) {
	if api.Body.StoredThrough == nil {
		return vStop, fmt.Errorf("resync without stored_through: %w", api)
	}
	if s.resyncs++; s.resyncs > maxResyncs {
		return vStop, fmt.Errorf("resync loop at offset %d: %w", s.st.Acked, api)
	}
	stored := *api.Body.StoredThrough
	if stored < 0 || stored > end || !atLineBoundary(f, stored) {
		return vNewLayer, nil
	}
	nh, err := prefixHash(f, stored)
	if err != nil {
		return vStop, err
	}
	*h = *nh
	s.st.Acked = stored
	return vContinue, s.persist()
}

// restartLayer opens a fresh layer on the same conversation and starts from byte 0.
func (s *session) restartLayer(ctx context.Context) error {
	next := s.st
	next.LayerID = "clyr_" + uuid.Must(uuid.NewV7()).String()
	next.Acked = 0
	if _, err := s.c.Open(ctx, next.ConversationID, openRequest(next)); err != nil {
		var api *sessionstream.APIError
		if errors.As(err, &api) && s.terminal(ctx, api) {
			return nil
		}
		return fmt.Errorf("open replacement live layer: %w", err)
	}
	s.log().InfoContext(ctx, "live_session_new_layer", "new_layer_id", next.LayerID)
	s.st = next
	return s.persist()
}

// readBatch returns whole lines from [off, end) up to MaxBatchBytes, or nil when
// no complete line fits. Spooled lines never exceed the cap (Spool gaps them).
func readBatch(f io.ReaderAt, off, end int64) ([]byte, error) {
	buf := make([]byte, min(int64(sessionstream.MaxBatchBytes), end-off))
	if _, err := f.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read spool: %w", err)
	}
	i := bytes.LastIndexByte(buf, '\n')
	if i < 0 {
		return nil, nil
	}
	return buf[:i+1], nil
}

// spoolEnd is the end of the last whole line; a torn tail is not part of the stream.
func spoolEnd(f *os.File) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat spool: %w", err)
	}
	return lineEnd(f, info.Size())
}

type hashWriter struct{ h *sessionstream.StreamHash }

func (w hashWriter) Write(p []byte) (int, error) { w.h.Write(p); return len(p), nil }

// prefixHash is the stream hash of spool bytes [0, n), recomputed from the file.
func prefixHash(f io.ReaderAt, n int64) (*sessionstream.StreamHash, error) {
	h := sessionstream.NewStreamHash()
	copied, err := io.Copy(hashWriter{h}, io.NewSectionReader(f, 0, n))
	if err != nil {
		return nil, fmt.Errorf("hash spool: %w", err)
	}
	if copied != n {
		return nil, fmt.Errorf("hash spool: wanted %d bytes, spool has %d", n, copied)
	}
	return h, nil
}

func atLineBoundary(f io.ReaderAt, off int64) bool {
	if off == 0 {
		return true
	}
	var b [1]byte
	_, err := f.ReadAt(b[:], off-1)
	return err == nil && b[0] == '\n'
}
