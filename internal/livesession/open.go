package livesession

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/sessionstream"
)

// ErrKeyConflict means this login already has a different signing key on the
// server (the local key file was lost). Streaming is disabled for the session;
// the key file is never deleted. Tell the user: run ox login to enroll a new
// signing key.
var ErrKeyConflict = errors.New("login already has a different signing key: run ox login to enroll a new signing key")

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
}

// Open registers the endpoint's signing key, opens the stream on a new layer and
// writes the stream state (status streaming). Calling it again for an already
// open session re-registers and re-opens on the same layer (both idempotent),
// keeping the acknowledged offset. A key conflict writes status disabled and returns
// ErrKeyConflict; any other failure writes nothing, so the session simply does
// not stream.
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
		st = State{
			ConversationID: cnv, LayerID: "clyr_" + uuid.Must(uuid.NewV7()).String(),
			Endpoint: cfg.Endpoint, Status: StatusPending,
			SessionID: p.SessionID, SessionName: p.SessionName, RepoID: p.RepoID, Agent: p.Agent, StartedAt: p.StartedAt,
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
	switch {
	case err == nil && st.Status == StatusPending: // promote: same layer, spool kept
		st.Status = StatusStreaming
	case err == nil && !st.active():
		return st, nil // deleted, closed or disabled stay that way
	case err == nil: // resume on the same layer, keeping acked
	case errors.Is(err, ErrNotStreaming):
		st = State{
			ConversationID: cnv, LayerID: "clyr_" + uuid.Must(uuid.NewV7()).String(),
			Endpoint: cfg.Endpoint, Status: StatusStreaming,
			SessionID: p.SessionID, SessionName: p.SessionName, RepoID: p.RepoID, Agent: p.Agent, StartedAt: p.StartedAt,
		}
	default:
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
		return disableOnKeyConflict(dir, cfg.Endpoint, st, err)
	}
	if _, err := c.Open(ctx, cnv, openRequest(st)); err != nil {
		return State{}, fmt.Errorf("open live session: %w", err)
	}
	if err := writeState(dir, st); err != nil {
		return State{}, err
	}
	cfg.Logger.InfoContext(ctx, "live_session_open", "session_id", st.SessionID, "conversation_id", st.ConversationID, "layer_id", st.LayerID, "acked", st.Acked)
	return st, nil
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
	}
}

func keyLabel() string {
	if host, err := os.Hostname(); err == nil && host != "" {
		return "ox CLI · " + host
	}
	return "ox CLI"
}
