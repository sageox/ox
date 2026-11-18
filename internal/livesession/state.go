package livesession

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/sessionid"
)

const (
	stateFileName = "stream-state.json"
	spoolFileName = "spool.jsonl"
	lockFileName  = "live.lock"
)

// Status is where a session's live stream stands.
type Status string

const (
	// StatusPending: prepared locally at session start; the server has not seen
	// the session yet. Spool accepts lines; Open promotes it to streaming.
	StatusPending Status = "pending"
	// StatusStreaming: open; the spool is appended to and drained.
	StatusStreaming Status = "streaming"
	// StatusClosing: stop was requested; the stream still has to drain and close.
	StatusClosing Status = "closing"
	// StatusClosed: the server finalized the session.
	StatusClosed Status = "closed"
	// StatusDeleted: the session was deleted (server said so, or abort).
	StatusDeleted Status = "deleted"
	// StatusDisabled: streaming is off for this session and will not resume.
	StatusDisabled Status = "disabled"
)

// ErrNotStreaming means the session has no live stream to act on (never opened,
// or it is closing, closed, deleted or disabled). Hooks ignore it.
var ErrNotStreaming = errors.New("session is not streaming")

// State is stream-state.json. The open parameters are kept so a restart on a
// new layer needs nothing from the caller.
type State struct {
	ConversationID string `json:"conversation_id"`
	LayerID        string `json:"layer_id"`
	Endpoint       string `json:"endpoint"`
	// Acked is the spool offset the server has stored; bytes before it are proven.
	Acked  int64  `json:"acked"`
	Status Status `json:"status"`

	SessionID   string    `json:"session_id,omitempty"`
	SessionName string    `json:"session_name,omitempty"`
	RepoID      string    `json:"repo_id,omitempty"`
	Agent       string    `json:"agent,omitempty"`
	StartedAt   time.Time `json:"started_at,omitzero"`

	// CloseRequested is set when stop ran while the session was still pending and
	// the server was unreachable; the daemon opens, drains and closes it.
	CloseRequested bool `json:"close_requested,omitempty"`

	// DeletedOnServer is set with StatusDeleted when the server answered that the
	// session was deleted (410 / stop_and_wipe). A local abort leaves it false:
	// only a server-side delete must keep the v1 copy off the Ledger.
	DeletedOnServer bool `json:"deleted_on_server,omitempty"`
}

func (s State) active() bool { return s.Status == StatusStreaming || s.Status == StatusClosing }

// WasDeletedOnServer reports whether the session in dir was deleted on the
// server while it streamed. An unreadable or missing state reports false.
func WasDeletedOnServer(dir string) bool {
	st, err := ReadState(dir)
	return err == nil && st.Status == StatusDeleted && st.DeletedOnServer
}

// ConversationID derives the server's cnv_ id from an ox ses_ id (same UUID).
func ConversationID(sessionID string) (string, error) {
	u, err := sessionid.ParseSessionID(sessionID)
	if err != nil {
		return "", fmt.Errorf("conversation id from session id: %w", err)
	}
	return "cnv_" + u.String(), nil
}

// ReadState loads stream-state.json; it returns ErrNotStreaming when none exists.
func ReadState(dir string) (State, error) {
	raw, err := os.ReadFile(filepath.Join(dir, stateFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, ErrNotStreaming
	}
	if err != nil {
		return State{}, fmt.Errorf("read stream state: %w", err)
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return State{}, fmt.Errorf("parse stream state: %w", err)
	}
	return st, nil
}

func writeState(dir string, st State) error {
	if err := fileutil.AtomicWriteJSON(filepath.Join(dir, stateFileName), st, 0o600); err != nil {
		return fmt.Errorf("write stream state: %w", err)
	}
	return nil
}
