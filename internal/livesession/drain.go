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
	"github.com/sageox/ox/internal/secretguard"
	// aliased: this package's own session type would shadow it
	oxsession "github.com/sageox/ox/internal/session"
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
	// disabled). Never set together with an error.
	Done bool
	// RetryAfter is the server's requested backoff after a refusal that asked
	// for a retry (rate limit, unproven stored end); zero otherwise.
	RetryAfter time.Duration
}

// verdict is what one append or close answer means for the loop above it.
type verdict int

const (
	vContinue verdict = iota
	vStop
	vNewLayer
	// vWithhold: the publication scan refused the batch at acked; the drain
	// replaces it in the spool with one gap line (withhold).
	vWithhold
)

// session is one locked view of a session dir during a call.
type session struct {
	cfg     Config
	dir     string
	st      State
	c       *sessionstream.Client
	resyncs int
	// withheld is the refused batch at acked and its replacement (vWithhold).
	withheld withheldBatch
	// rules is the persisted secret-rules delta, loaded on the first scan.
	rules       *secretguard.RuleSet
	rulesLoaded bool
}

func (s *session) spoolPath() string { return filepath.Join(s.dir, spoolFileName) }

func (s *session) log() *slog.Logger {
	return s.cfg.Logger.With("session_id", s.st.SessionID, "conversation_id", s.st.ConversationID, "layer_id", s.st.LayerID)
}

// loadSession reads the stream state; it builds no client (see connect).
func loadSession(cfg Config, dir string) (*session, error) {
	st, err := ReadState(dir)
	if err != nil {
		return nil, err
	}
	return &session{cfg: cfg, dir: dir, st: st}, nil
}

// connect builds the signed client for an active stream, on the configured
// endpoint and only for a conversation minted here (authorize). Every append
// and close goes through it.
func (s *session) connect(ctx context.Context) error {
	if !s.st.active() {
		return nil
	}
	if err := authorize(ctx, s.cfg, s.dir, s.st); err != nil {
		return err
	}
	c, err := s.cfg.newClient(ctx, s.cfg.Endpoint)
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
		res.Done = err == nil && (!s.st.active() || s.caughtUp())
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
	for restarts := 0; ; {
		v, err := s.pump(ctx, res)
		if err != nil {
			return err
		}
		switch v {
		case vWithhold:
			// pump has closed the spool; the next pump reads the rewritten one
			if err := s.withhold(ctx); err != nil {
				return err
			}
		case vNewLayer:
			if restarts >= maxRestarts {
				return errors.New("live stream refused after restarting on a new layer")
			}
			restarts++
			if err := s.restartLayer(ctx); err != nil {
				return err
			}
		default:
			return nil
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

// sendBatch scans the window a full-size batch would carry, then sends as much
// of it as the batch cap allows. The scan never shrinks with the cap: a secret
// whose parts span lines (a Helm extraEnv name and its value) is refused only
// when the parts are scanned together, so sending a stalled window in smaller
// pieces must not split what one scan sees. Each piece rescans its whole
// window; that costs CPU only while a link is stalling.
func (s *session) sendBatch(ctx context.Context, f *os.File, h *sessionstream.StreamHash, end int64, res *DrainResult) (verdict, error) {
	window, err := readBatch(f, s.st.Acked, end)
	if err != nil {
		return vStop, err
	}
	if len(window) == 0 {
		return vStop, nil
	}
	if err := s.checkPublication(ctx, window); err != nil {
		var refused *secretguard.RefusedError
		if !errors.As(err, &refused) {
			return vStop, err
		}
		w, err := withholdRefused(window)
		if err != nil {
			return vStop, fmt.Errorf("withhold refused batch at offset %d: %w", s.st.Acked, err)
		}
		w.by = "local_scan"
		s.withheld = w
		return vWithhold, nil
	}
	batch := capLines(window, s.batchLimit())
	resp, err := s.c.AppendRaw(ctx, s.st.ConversationID, s.st.LayerID, s.st.Acked, h.Sum(), batch)
	if err != nil {
		return s.onRefusal(ctx, err, f, h, window, len(batch), end, res)
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
	s.unstalled()
	s.edgeCleared(ctx)
	if err := s.persist(); err != nil {
		return vStop, err
	}
	return vContinue, nil
}

// onRefusal maps a failed append of the first sent bytes of window to what the
// drain does next (C6).
func (s *session) onRefusal(ctx context.Context, err error, f *os.File, h *sessionstream.StreamHash, window []byte, sent int, end int64, res *DrainResult) (verdict, error) {
	var api *sessionstream.APIError
	if !errors.As(err, &api) {
		if isTimeout(ctx, err) {
			s.stalled(ctx, sent, "timeout")
		}
		return vStop, fmt.Errorf("append at offset %d: %w", s.st.Acked, err)
	}
	if shape, ok := edgeShape(api); ok {
		eerr := s.edgeBlocked(ctx, api, shape)
		s.stalled(ctx, sent, "edge_blocked") // persists EdgeBlockedSince too
		return vStop, fmt.Errorf("append at offset %d: %w (status %d, %s body)", s.st.Acked, eerr, api.Status, shape)
	}
	if s.edgeCleared(ctx) {
		if err := s.persist(); err != nil {
			s.log().WarnContext(ctx, "live_session_state_write_failed", "during", "edge_unblocked", "error", err)
		}
	}
	if done, err := s.terminal(ctx, api); done || err != nil {
		return vStop, err
	}
	if api.Body.Reason == reasonSecretRefused {
		// the server's scan (F11) found what the local one missed: the same
		// withhold as a local refusal (F1, R1), and the stream goes on. The
		// whole scan window goes, not just the piece sent: a match's other
		// parts may lie in the rest of it
		w, werr := withholdRefused(window)
		if werr == nil {
			w.by, w.line = "server", api.Body.Line
			s.withheld = w
			return vWithhold, nil
		}
		// a batch of gap lines only: withholding again could loop forever, so
		// today's stop applies below
		s.log().WarnContext(ctx, "live_session_secret_refused_gaps_only", "offset", s.st.Acked, "bytes", len(window))
	}
	switch classify(api) {
	case refusalResync:
		return s.resync(ctx, f, h, end, api)
	case refusalNewLayer:
		return vNewLayer, nil
	case refusalStop:
		s.disable(ctx, api.Body.Reason, "action", string(api.Body.Action), "status", api.Status)
		return vStop, nil
	default: // retry, reauth, 429, 5xx, or an answer this client does not know yet
		if ms := api.Body.RetryAfterMS; ms != nil {
			res.RetryAfter = time.Duration(*ms) * time.Millisecond
		}
		return vStop, fmt.Errorf("append at offset %d: %w", s.st.Acked, api)
	}
}

// refusal is what a refused request means for the stream.
type refusal int

const (
	// refusalRetry: transient, or not an answer this client knows. The bytes
	// stay spooled and the caller backs off (C7): a new server answer must
	// never switch off capture on clients that predate it.
	refusalRetry refusal = iota
	refusalResync
	refusalNewLayer
	// refusalStop: the server refused for good (action stop or fix).
	refusalStop
	// refusalDeleted: 410 / stop_and_wipe.
	refusalDeleted
	// refusalClosed: the server already finalized the session.
	refusalClosed
)

// classify lists the answers known to settle a request; everything else is
// refusalRetry.
func classify(api *sessionstream.APIError) refusal {
	switch {
	case api.Body.Action == sessionstream.ActionStopAndWipe || api.Status == http.StatusGone:
		return refusalDeleted
	case api.Body.Reason == sessionstream.ReasonSessionClosed:
		return refusalClosed
	case api.Body.Action == sessionstream.ActionRetry, api.Body.Action == sessionstream.ActionReauth,
		api.Status == http.StatusTooManyRequests, api.Status >= 500:
		return refusalRetry
	}
	switch api.Body.Action {
	case sessionstream.ActionResync:
		return refusalResync
	case sessionstream.ActionNewLayer:
		return refusalNewLayer
	case sessionstream.ActionStop, sessionstream.ActionFix:
		return refusalStop
	}
	return refusalRetry
}

func (s *session) disable(ctx context.Context, reason string, args ...any) {
	s.log().WarnContext(ctx, "live_session_disabled", append([]any{"reason", reason}, args...)...)
	s.st.DisabledReason = reason
	s.setStatus(ctx, StatusDisabled)
}

// terminal handles the answers that end a stream for good: session_closed and
// stop_and_wipe (410). It reports whether the stream ended; the error is set
// only when a server-side delete could not be recorded anywhere (see
// deletedByServer), and then the caller must retry.
func (s *session) terminal(ctx context.Context, api *sessionstream.APIError) (bool, error) {
	switch classify(api) {
	case refusalClosed:
		s.log().InfoContext(ctx, "live_session_closed_by_server")
		s.setStatus(ctx, StatusClosed)
		return true, nil
	case refusalDeleted:
		return true, s.deletedByServer(ctx)
	}
	return false, nil
}

// deletedByServer holds a session someone deleted on SageOx, then wipes the
// spool: raw.jsonl stays (it is the person's own copy), but no automatic path
// may publish it to the Ledger. The session is held in memory at once, so this
// call sends nothing more. Two records outlive it, and every publisher checks
// both: the hold marker and DeletedOnServer in the stream state (C3). One is
// enough. With neither written nothing durable knows of the delete, so the
// spool and the on-disk status are left as they were and an error is returned:
// the next attempt hears the 410 again and records it then.
//
// A 410 that answers this machine's own abort (delete-requested) is no
// server-side delete, so it only wipes.
func (s *session) deletedByServer(ctx context.Context) error {
	if deleteRequested(s.dir) {
		s.wipe(ctx)
		return nil
	}
	s.log().InfoContext(ctx, "live_session_deleted_by_server")
	s.st.Status, s.st.DeletedOnServer = StatusDeleted, true
	// the hold names this stream's recording: the folder may pass to a new
	// recording restarted within the minute, and it must not be held by this one
	holdErr := oxsession.WriteHoldMarkerFor(s.dir, oxsession.HoldDeletedOnServer, "live_stream", s.st.SessionID)
	stateErr := s.persist()
	if holdErr != nil && stateErr != nil {
		err := errors.Join(holdErr, stateErr)
		s.log().ErrorContext(ctx, "live_session_delete_unrecorded", "error", err)
		return fmt.Errorf("record server-side delete: %w", err)
	}
	if holdErr != nil {
		s.log().WarnContext(ctx, "live_session_hold_failed", "deleted_on_server_recorded", true, "error", holdErr)
	}
	if stateErr != nil {
		s.log().WarnContext(ctx, "live_session_state_write_failed", "status", string(StatusDeleted), "hold_recorded", true, "error", stateErr)
	}
	if err := removeIfExists(s.spoolPath()); err != nil {
		s.log().WarnContext(ctx, "live_session_spool_remove_failed", "error", err)
	}
	return nil
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
		if errors.As(err, &api) {
			if done, terr := s.terminal(ctx, api); done || terr != nil {
				return terr
			}
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

// capLines is the longest prefix of whole lines in b (which ends in a newline)
// within limit bytes, or b's first line when that alone is longer: a line is
// never split.
func capLines(b []byte, limit int) []byte {
	if len(b) <= limit {
		return b
	}
	i := bytes.LastIndexByte(b[:limit], '\n')
	if i < 0 {
		i = bytes.IndexByte(b, '\n')
	}
	return b[:i+1]
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
