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
	oxsession "github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
)

const (
	stateFileName = "stream-state.json"
	spoolFileName = "spool.jsonl"
	// the hold writer takes this same lock (session.WriteHoldMarkerFor), so
	// the name lives in the session package
	lockFileName = oxsession.LiveLockFileName
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
	// manualStopFileName marks that the person ended this session with `ox
	// session stop`. A manual stop is final: the session is never reopened,
	// whatever prompts next in the same agent conversation. Like close-requested
	// it is written without live.lock, before the stop's close, because a drain
	// may hold the lock longer than stop can wait.
	manualStopFileName = "manual-stop"
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
	case spoolFileName, stateFileName, lockFileName, closeRequestFileName, manualStopFileName, deleteRequestFileName,
		spoolLostFileName, spoolLostDeclaredFileName, noticeOwedFileName, mediaAttemptsFile:
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
	// StatusEmpty: the recording ended before its spool held a human prompt,
	// so the stream was never opened and the server never saw it. A prime or
	// a /clear boundary alone is no session a teammate should see.
	StatusEmpty Status = "empty"
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
	// ParentSessionID and ContinuedFromSessionID are the session links every
	// open sends (sessionstream.OpenSessionRequest). Kept here because the
	// open usually runs later, in the daemon, when the parent's recording may
	// already have ended.
	ParentSessionID        string `json:"parent_session_id,omitempty"`
	ContinuedFromSessionID string `json:"continued_from_session_id,omitempty"`

	// Reopen marks a stream that resumes a conversation its person stopped:
	// the first open is sent with reopen set, so the server adds a layer to
	// the stopped conversation instead of minting one. Cleared once the
	// server has taken the open.
	Reopen bool `json:"reopen,omitempty"`
	// SeedBytes is the length of the spool prefix copied from the stopped
	// stream (Reopen only). It is the part of the stream already published
	// once; a prompt in it does not count as one typed since the resume.
	SeedBytes int64 `json:"seed_bytes,omitempty"`
	// OwnSessionName and OwnStartedAt are this recording's own identity, kept
	// while SessionName and StartedAt name the reopened conversation, so a
	// refused reopen can stream the recording as a conversation of its own.
	OwnSessionName string `json:"own_session_name,omitempty"`
	// OwnHeader is this recording's raw.jsonl header line, kept so a refused
	// reopen can finish its fallback after cache pruning removed raw.jsonl.
	OwnHeader    string    `json:"own_header,omitempty"`
	OwnStartedAt time.Time `json:"own_started_at,omitzero"`

	// AwaitReopen is set on a pending stream that may yet reopen the
	// conversation of the recording it continues, whose close had not
	// finished when this one began (see ReopenAwait). Nothing opens the
	// stream while it is set.
	AwaitReopen *ReopenAwait `json:"await_reopen,omitempty"`

	// ManualStop is set (from the manual-stop file) when the person stopped the
	// session with `ox session stop`. Sticky: once persisted it stays.
	ManualStop bool `json:"manual_stop,omitempty"`

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

	// EdgeBlockedSince is when the network edge in front of SageOx (CDN or
	// WAF) began answering this stream with a 403 that is no live-capture
	// refusal; zero while it is not. The drain keeps retrying, so nothing is
	// lost, but nothing streams either; status output reads it to say so.
	EdgeBlockedSince time.Time `json:"edge_blocked_since,omitzero"`

	// Stalls counts consecutive sends at offset StalledAt that timed out or
	// were edge-blocked. BatchLimit, when set, caps the next batch below
	// MaxBatchBytes after stalls split it; a stored append resets all three.
	StalledAt  int64 `json:"stalled_at,omitempty"`
	Stalls     int   `json:"stalls,omitempty"`
	BatchLimit int   `json:"batch_limit,omitempty"`
}

func (s State) active() bool { return s.Status == StatusStreaming || s.Status == StatusClosing }

// OweSecretWithheldNotice records that the person behind the stream in dir may
// be owed the notice that it withheld a secret: a stop sets it whenever the
// stream has withheld or still could, and only a start or stop that has said
// the notice clears it (ClearSecretWithheldNotice).
func OweSecretWithheldNotice(dir string) error {
	if err := os.WriteFile(filepath.Join(dir, noticeOwedFileName), nil, 0o600); err != nil {
		return fmt.Errorf("record owed withheld notice: %w", err)
	}
	return nil
}

// OwesSecretWithheldNotice reports whether dir's stream is marked as owing its
// person the withheld notice.
func OwesSecretWithheldNotice(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, noticeOwedFileName))
	return err == nil
}

// ClearSecretWithheldNotice removes dir's owed mark. Call it only once the
// notice was said: a mark removed before that is a notice never given.
func ClearSecretWithheldNotice(dir string) error {
	if err := removeIfExists(filepath.Join(dir, noticeOwedFileName)); err != nil {
		return fmt.Errorf("clear owed withheld notice: %w", err)
	}
	return nil
}

// OwedSecretWithheldNotices returns the session folders under sessionsDir (a
// moved stream's .live-<id> folder included) whose stream owes its person the
// withheld notice and has withheld a secret by now. A stream of the recording
// sessionID is left out: its own stop says it. Nothing is removed here, so a
// start or stop that fails, a path that prints nothing, or a stream caught
// between moves (no state yet) keeps its notice owed.
func OwedSecretWithheldNotices(sessionsDir, sessionID string) []string {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return nil
	}
	var owed []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(sessionsDir, e.Name())
		if !OwesSecretWithheldNotice(dir) {
			continue
		}
		st, err := ReadState(dir)
		if err == nil && st.SecretWithheld && st.SessionID != sessionID && st.Status != StatusDeleted {
			owed = append(owed, dir)
		}
	}
	return owed
}

// reachedServer reports whether the server may hold this session's
// conversation, so that abort has to delete it there.
func (s State) reachedServer() bool {
	switch {
	case s.ConversationID == "" || s.Status == StatusDeleted || s.Status == StatusEmpty:
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
	if !st.ManualStop {
		_, err := os.Stat(filepath.Join(dir, manualStopFileName))
		st.ManualStop = err == nil
	}
	return st, nil
}

// MarkManualStop records, with no network call and no lock, that the person
// stopped the session in dir themselves, so it is never reopened (see
// manualStopFileName). It is a no-op for a folder with no live stream.
func MarkManualStop(dir string) error {
	if _, err := readState(dir); err != nil {
		if errors.Is(err, ErrNotStreaming) {
			return nil
		}
		return err
	}
	if err := fileutil.AtomicWriteBytes(filepath.Join(dir, manualStopFileName), nil, 0o600); err != nil {
		return fmt.Errorf("record manual stop: %w", err)
	}
	return nil
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
