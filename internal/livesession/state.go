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
	// closeRequestFileName marks that stop asked for the close. It is a file of
	// its own, written without live.lock, because a drain may hold the lock for
	// longer than stop can wait, and a drain persisting stream-state.json would
	// overwrite a request written into it.
	closeRequestFileName = "close-requested"
	// deleteRequestFileName marks that abort let the session go. While it
	// exists the session reads as deleted, so nothing spools, drains, opens or
	// closes it. Like close-requested it is written without live.lock, because a
	// drain or open may hold the lock far longer than abort can wait.
	deleteRequestFileName = "delete-requested"
	// spoolLostFileName logs committed bytes Spool could not write (EACCES,
	// ENOSPC, a read-only volume, a lock or state failure), one line per loss.
	// It is append-only and never removed: recording a loss takes no lock and
	// no context, so it cannot fail for the reason the spool write just did.
	spoolLostFileName = "spool-lost"
	// spoolLostDeclaredFileName holds how many of spool-lost's bytes gap lines
	// already declared. The next spool write, or the close, declares the rest,
	// so the loss shows in the stream instead of vanishing.
	spoolLostDeclaredFileName = "spool-lost-declared"
	// noticeOwedFileName marks a stream whose person may still be owed the
	// notice that it withheld a secret (OweSecretWithheldNotice). Like
	// close-requested it is a file of its own, written without live.lock: a
	// drain persisting stream-state.json would overwrite a flag kept in it.
	noticeOwedFileName = "withheld-notice-owed"
)

// IsLocalFile reports whether name is one of the live-stream files this package
// keeps in a session folder. They are machine-local state (the spool duplicates
// raw.jsonl; the rest are ids, offsets and markers), so anything that copies a
// session folder into the Ledger must skip them.
func IsLocalFile(name string) bool {
	switch name {
	case spoolFileName, stateFileName, lockFileName, closeRequestFileName, deleteRequestFileName,
		spoolLostFileName, spoolLostDeclaredFileName, noticeOwedFileName:
		return true
	}
	return false
}

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

// ErrNotStreaming means the session has no live stream to act on: never opened,
// or ended (closed, deleted or disabled). Hooks ignore it.
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

	// OpenSent is recorded before a pending session's first open request
	// leaves this machine. The server may then hold the conversation even
	// though the reply never arrived, so abort must delete it.
	OpenSent bool `json:"open_sent,omitempty"`

	// CloseRequested is set when stop asked for the close but could not finish
	// it: the server was unreachable, or a drain held the session longer than
	// stop could wait. The daemon opens (if pending), drains and closes it.
	// ReadState also reports it while the close-requested file exists.
	CloseRequested bool `json:"close_requested,omitempty"`

	// DeletedOnServer is set with StatusDeleted when the server answered that the
	// session was deleted (410 / stop_and_wipe). A local abort leaves it false:
	// only a server-side delete must keep the v1 copy off the Ledger.
	DeletedOnServer bool `json:"deleted_on_server,omitempty"`

	// DisabledReason says why the stream was disabled: the server's refusal
	// reason. A key conflict leaves it empty (Open returns ErrKeyConflict
	// instead). A secret never disables a stream (see withhold); streams an
	// older ox disabled for one keep "secret_refused".
	DisabledReason string `json:"disabled_reason,omitempty"`

	// SecretWithheld is set once the publication scan refused a batch and a
	// secret_withheld gap line replaced it. Stop reads it to tell the person,
	// once, that part of the session was left out of the live view.
	SecretWithheld bool `json:"secret_withheld,omitempty"`
}

func (s State) active() bool { return s.Status == StatusStreaming || s.Status == StatusClosing }

// OweSecretWithheldNotice records that the person behind the stream in dir has
// not been told it withheld a secret, and may need to be: their recording
// stopped while the stream could still withhold (its close left to the
// daemon), or a hook stopped it with no one to tell. TakeSecretWithheldNotices
// says it once the stream has withheld.
func OweSecretWithheldNotice(dir string) error {
	if err := os.WriteFile(filepath.Join(dir, noticeOwedFileName), nil, 0o600); err != nil {
		return fmt.Errorf("record owed withheld notice: %w", err)
	}
	return nil
}

// TakeSecretWithheldNotices returns how many streams in the session folders
// under sessionsDir (a moved stream's .live-<id> folder included) owe their
// person the withheld notice and have withheld a secret by now. The caller
// says it, and they owe it no longer. A stream that ended without withholding,
// or was deleted, owes nothing any more; one that may still withhold keeps its
// mark. A mark that cannot be removed is said again next time: twice beats never.
func TakeSecretWithheldNotices(sessionsDir string) int {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(sessionsDir, e.Name())
		mark := filepath.Join(dir, noticeOwedFileName)
		if _, err := os.Lstat(mark); err != nil {
			continue
		}
		st, err := ReadState(dir)
		switch {
		case errors.Is(err, ErrNotStreaming), err == nil && st.Status == StatusDeleted:
			// nothing left to tell: the stream is gone, or so is its server copy
		case err == nil && st.SecretWithheld:
			n++
		case err == nil && (st.Status == StatusClosed || st.Status == StatusDisabled):
			// ended without withholding anything
		default:
			continue // may still withhold, or unreadable for now
		}
		_ = os.Remove(mark)
	}
	return n
}

// reachedServer reports whether the server may hold this session's
// conversation, so that abort has to delete it there.
func (s State) reachedServer() bool {
	switch {
	case s.ConversationID == "" || s.Status == StatusDeleted:
		return false
	case s.Status == StatusPending:
		return s.OpenSent
	}
	return true
}

// WasDeletedOnServer reports whether the session in dir was deleted on the
// server while it streamed. An unreadable or missing state reports false.
// Publication guards check it alongside the hold marker: the stream state is
// written even when the marker could not be.
func WasDeletedOnServer(dir string) bool {
	st, err := ReadState(dir)
	return err == nil && st.DeletedOnServer
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
// While the delete-requested marker exists the status reads as deleted, so no
// path streams, opens or closes a session that abort already let go.
func ReadState(dir string) (State, error) {
	st, err := readState(dir)
	if err == nil && deleteRequested(dir) {
		st.Status = StatusDeleted
	}
	return st, err
}

// readState is ReadState without the delete-requested overlay, which would hide
// what reached the server from abort itself.
func readState(dir string) (State, error) {
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
	if !st.CloseRequested {
		_, err := os.Stat(filepath.Join(dir, closeRequestFileName))
		st.CloseRequested = err == nil
	}
	return st, nil
}

func deleteRequested(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, deleteRequestFileName))
	return err == nil
}

func writeState(dir string, st State) error {
	if err := fileutil.AtomicWriteJSON(filepath.Join(dir, stateFileName), st, 0o600); err != nil {
		return fmt.Errorf("write stream state: %w", err)
	}
	return nil
}
