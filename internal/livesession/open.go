package livesession

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	oxsession "github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionstream"
)

// ErrKeyConflict means this login already has a different signing key on the
// server (the local key file was lost). Streaming is disabled for the session;
// the key file is never deleted. Tell the user: run ox login to enroll a new
// signing key.
var ErrKeyConflict = errors.New("login already has a different signing key: run ox login to enroll a new signing key")

// ErrAwaitingPrompt means a prepared stream was not opened because its spool
// holds no human prompt yet: the header a session start spools never opens
// one. Nothing was sent. The capture that spools the first prompt asks for
// the drain that opens it; a stop before that ends it as StatusEmpty.
var ErrAwaitingPrompt = errors.New("live stream not opened: no human prompt captured yet")

// OpenParams names the session being streamed.
type OpenParams struct {
	// SessionID is the ox ses_ id; the conversation id is derived from it.
	SessionID string
	// SessionName is the session folder name.
	SessionName string
	RepoID      string
	// Agent names the coding agent, such as "claude-code".
	Agent     string
	StartedAt time.Time
	// ParentSessionID is the ses_ id of the session whose shell launched this
	// one's agent; ContinuedFromSessionID the earlier recording this one
	// continues. Both optional: empty sends neither.
	ParentSessionID        string
	ContinuedFromSessionID string
}

// newState is the stream state of a session this machine has not streamed yet.
func newState(cfg Config, cnv string, status Status, p OpenParams) State {
	return State{
		ConversationID: cnv, LayerID: "clyr_" + uuid.Must(uuid.NewV7()).String(),
		Endpoint: cfg.Endpoint, Status: status,
		SessionID: p.SessionID, SessionName: p.SessionName, RepoID: p.RepoID, Agent: p.Agent, StartedAt: p.StartedAt,
		ParentSessionID: p.ParentSessionID, ContinuedFromSessionID: p.ContinuedFromSessionID,
	}
}

// Open registers the endpoint's signing key, opens the stream on a new layer and
// writes the stream state (status streaming). Calling it again for an already
// open session re-registers and re-opens on the same layer (both idempotent),
// keeping the acknowledged offset. A key conflict writes status disabled and returns
// ErrKeyConflict; an open answered "deleted" or "closed" records that final
// status; any other failure leaves the state as it was (a fresh session then
// has none and simply does not stream).
func Open(ctx context.Context, cfg Config, dir string, p OpenParams) (State, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return State{}, err
	}
	cnv, err := ConversationID(p.SessionID)
	if err != nil {
		return State{}, err
	}
	var st State
	err = withLock(ctx, dir, openLockWait, func() error {
		var err error
		st, err = openLocked(ctx, cfg, dir, cnv, p)
		return err
	})
	return st, err
}

// Prepare writes a local pending stream state without any network call, so a hook
// can start spooling at once and the daemon (or stop) opens the stream later. It
// is idempotent: an existing state is returned untouched.
func Prepare(ctx context.Context, cfg Config, dir string, p OpenParams) (State, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return State{}, err
	}
	cnv, err := ConversationID(p.SessionID)
	if err != nil {
		return State{}, err
	}
	var st State
	run := func() error {
		var err error
		if st, err = ReadState(dir); err == nil {
			return nil
		} else if !errors.Is(err, ErrNotStreaming) {
			return err
		}
		st = newState(cfg, cnv, StatusPending, p)
		// registered first, bound to this folder: a stream state whose id is
		// not bound to its folder is refused everywhere (see authorize)
		if err := recordMinted(ctx, cfg, cnv, dir); err != nil {
			return err
		}
		return writeState(dir, st)
	}
	err = withLock(ctx, dir, spoolLockWait, run)
	return st, err
}

// openLockWait bounds how long Open waits on a concurrent drain of the same dir.
const openLockWait = 5 * time.Second

func openLocked(ctx context.Context, cfg Config, dir, cnv string, p OpenParams) (State, error) {
	st, err := ReadState(dir)
	onDisk := err == nil
	pending := err == nil && st.Status == StatusPending
	switch {
	case pending && !st.OpenSent && !mayHoldPrompt(dir):
		// local only: an open already sent may have created the conversation,
		// so only a stream the server has never heard of waits
		return st, ErrAwaitingPrompt
	case pending: // promote: same layer, spool kept
	case err == nil && !st.active():
		return st, nil // deleted, closed or disabled stay that way
	case err == nil: // resume on the same layer, keeping acked
	case errors.Is(err, ErrNotStreaming):
		st = newState(cfg, cnv, StatusStreaming, p)
		if err := recordMinted(ctx, cfg, cnv, dir); err != nil {
			return State{}, err
		}
	default:
		return State{}, err
	}
	// A state read from disk may be a forgery pulled with the Ledger: nothing,
	// not even the key registration, is sent for one ox did not mint here.
	if err := authorize(ctx, cfg, dir, st); err != nil {
		return State{}, err
	}
	key, err := LoadOrCreateKey(ctx, cfg, cfg.Endpoint)
	if err != nil {
		return State{}, err
	}
	c, err := cfg.clientWithKey(ctx, cfg.Endpoint, key)
	if err != nil {
		return State{}, err
	}
	if _, err := c.RegisterKey(ctx, keyLabel()); err != nil {
		var eerr error
		if st, eerr = openAnswered(ctx, cfg, dir, st, onDisk, err, "register_key"); eerr != nil {
			return State{}, eerr
		}
		return disableOnKeyConflict(dir, cfg.Endpoint, st, err)
	}
	if pending && !st.OpenSent {
		// the server may create the conversation even if its reply is lost;
		// abort deletes only what it knows may exist (C2)
		st.OpenSent = true
		if err := writeState(dir, st); err != nil {
			return State{}, err
		}
	}
	// Abort writes delete-requested before it reads OpenSent; this checks it
	// after OpenSent is on disk, so one of the two always sees the other.
	if deleteRequested(dir) {
		st.Status = StatusDeleted
		return st, nil
	}
	// st's id, the one authorize checked; cnv (from the caller's ses_ id) is the
	// same for every stream ox prepared
	_, err = c.Open(ctx, st.ConversationID, openRequest(st))
	if deleteRequested(dir) {
		return abortedWhileOpening(ctx, cfg, st)
	}
	if err != nil {
		var eerr error
		if st, eerr = openAnswered(ctx, cfg, dir, st, onDisk, err, "open"); eerr != nil {
			return State{}, eerr
		}
		return openRefused(ctx, cfg, dir, st, err)
	}
	if pending {
		st.Status = StatusStreaming
	}
	s := &session{cfg: cfg, dir: dir, st: st}
	s.edgeCleared(ctx)
	st = s.st
	if err := writeState(dir, st); err != nil {
		return State{}, err
	}
	cfg.Logger.InfoContext(ctx, "live_session_open", "session_id", st.SessionID, "conversation_id", st.ConversationID, "layer_id", st.LayerID, "acked", st.Acked)
	return st, nil
}

// mayHoldPrompt reports whether dir's spool holds a human prompt, or may have:
// any declared gap (an oversized or unspoolable line, a withheld batch, a torn
// tail, the cap) or recorded spool loss could have been the prompt, and an
// unreadable spool proves nothing. Either way the stream opens, so a prompt the
// spool could not carry is declared on the stream, never hidden as an empty
// session.
func mayHoldPrompt(dir string) bool {
	if lost := readLost(dir); lost.pending || lost.recorded > 0 {
		return true
	}
	f, err := os.Open(filepath.Join(dir, spoolFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	defer func() { _ = f.Close() }() // read-only: nothing to flush
	// Bounded per line: this runs before authorize, on a spool in a checkout a
	// Ledger contributor can write to, so a planted newline-free spool must not
	// make the probe allocate it whole. The spool never holds a line longer
	// than one batch (longer ones become an oversized_line gap), so a longer
	// line proves nothing: say "may hold" and let authorize decide.
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), sessionstream.MaxBatchBytes+1)
	for sc.Scan() {
		line := append(append([]byte(nil), sc.Bytes()...), '\n')
		if declaredGapLine.Match(line) || oxsession.IsUserTurnLine(line) {
			return true
		}
	}
	return sc.Err() != nil
}

// abortedWhileOpening queues the delete of a conversation this open may have
// created after abort began. Abort's own delete can reach the server before the
// open does, see nothing to delete, and take that as done.
func abortedWhileOpening(ctx context.Context, cfg Config, st State) (State, error) {
	st.Status = StatusDeleted
	p := PendingDelete{Endpoint: cfg.Endpoint, ConversationID: st.ConversationID, EnqueuedAt: time.Now().UTC()}
	if err := enqueueDelete(context.WithoutCancel(ctx), cfg, p); err != nil {
		return st, fmt.Errorf("queue delete of a session aborted while it opened: %w", err)
	}
	cfg.Logger.InfoContext(ctx, "live_session_open_aborted", "session_id", st.SessionID, "conversation_id", st.ConversationID)
	return st, nil
}

// openAnswered tracks the edge streak across a failed key registration or
// open, as a drain does. An edge 403 (see edgeShape) is logged once per streak
// without the body and returned as ErrEdgeBlocked; any other answer came from
// SageOx itself and ends a streak (a 401 or 503 is not the edge's doing). A
// stream already on disk (a pending one included) records either change, so
// status tells it right while the stream waits to open; a fresh stream has no
// state to record it in and leaves none. A transport failure heard from
// neither, so it changes nothing. It returns the state as recorded.
func openAnswered(ctx context.Context, cfg Config, dir string, st State, onDisk bool, err error, during string) (State, error) {
	var api *sessionstream.APIError
	if !errors.As(err, &api) {
		return st, nil
	}
	s := &session{cfg: cfg, dir: dir, st: st}
	shape, edge := edgeShape(api)
	var eerr error
	if edge {
		eerr = fmt.Errorf("%s live session: %w (status %d, %s body)", during, s.edgeBlocked(ctx, api, shape), api.Status, shape)
	} else if !s.edgeCleared(ctx) {
		return st, nil
	}
	if onDisk {
		if werr := writeState(dir, s.st); werr != nil {
			cfg.Logger.WarnContext(ctx, "live_session_state_write_failed", "during", "edge_streak", "session_id", st.SessionID, "error", werr)
		}
	}
	return s.st, eerr
}

// openRefused records an open answered "deleted" or "closed" exactly as the
// same answer to an append is recorded, so a session someone deleted while its
// open reply was lost is held, not published. Any other failure leaves the
// state as it was for a later attempt.
func openRefused(ctx context.Context, cfg Config, dir string, st State, err error) (State, error) {
	var api *sessionstream.APIError
	if errors.As(err, &api) {
		s := &session{cfg: cfg, dir: dir, st: st}
		done, terr := s.terminal(ctx, api)
		if terr != nil {
			return State{}, terr
		}
		if done {
			return s.st, nil
		}
		if api.Body.Reason == sessionstream.ReasonSessionNameTaken {
			// another person owns this session name in this repo; no retry can
			// change that, so the stream ends here instead of looping every tick
			s.disable(ctx, api.Body.Reason, "action", string(api.Body.Action), "status", api.Status)
			return s.st, nil
		}
	}
	return State{}, fmt.Errorf("open live session: %w", err)
}

// disableOnKeyConflict records a disabled session for a 409 on key registration.
// No conversation id is stored: the server never saw this session, so abort has
// nothing to delete.
func disableOnKeyConflict(dir, ep string, st State, err error) (State, error) {
	var api *sessionstream.APIError
	if !errors.As(err, &api) || api.Status != http.StatusConflict {
		return State{}, fmt.Errorf("register live signing key: %w", err)
	}
	st = State{Endpoint: ep, Status: StatusDisabled, SessionID: st.SessionID}
	if werr := writeState(dir, st); werr != nil {
		return State{}, werr
	}
	return st, ErrKeyConflict
}

func openRequest(st State) sessionstream.OpenSessionRequest {
	return sessionstream.OpenSessionRequest{
		LayerID: st.LayerID, RepoID: st.RepoID, SessionID: st.SessionID,
		SessionName: st.SessionName, Agent: st.Agent, StartedAt: st.StartedAt,
		ParentSessionID: st.ParentSessionID, ContinuedFromSessionID: st.ContinuedFromSessionID,
	}
}

func keyLabel() string {
	if host, err := os.Hostname(); err == nil && host != "" {
		return "ox CLI · " + host
	}
	return "ox CLI"
}
