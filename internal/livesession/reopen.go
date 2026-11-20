package livesession

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/sageox/ox/internal/fileutil"
	oxsession "github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/sageox/ox/internal/sessionstream"
)

// A person who stops a recording and then prompts the same agent session again
// gets a new recording (new ses_ id, new folder). Streaming it as a new
// conversation split one coding session into two on SageOx, so the new
// recording reuses the stopped one's conversation instead: an open carrying
// reopen, signed by this machine's key like every other request. The server
// grants it only to the owner and only for a conversation its client closed.
//
// The new layer restates the session from byte 0, like a new_layer restart
// does: the stopped stream's spool is copied into the new spool first, then the
// lines of the resumed recording follow. Those bytes are scanned by the same
// publication scan as any append, in the drain.

// ReopenSource is a stopped stream a continuing recording may reopen.
type ReopenSource struct {
	// Dir is the session folder the stream lives in.
	Dir   string
	State State
	// Spool is the stopped stream's whole spool (whole lines).
	Spool []byte
}

// FindReopenSource returns the stream of the recording continuedFrom when it
// may be reopened, or nil. It may be reopened only if this machine's own stop
// ended it and nothing since has withdrawn it:
//   - closed (or reopened and ended before any new prompt), never deleted: a
//     delete, abort, stop_and_wipe or 410 is never reopened;
//   - not held, so a manual-publishing session never goes live again;
//   - minted here for that folder (authorize), so a stream state planted in a
//     Ledger checkout cannot make this machine reopen a conversation.
//
// Any doubt returns nil, and the recording streams as a new conversation.
func FindReopenSource(ctx context.Context, cfg Config, sessionsDir, continuedFrom string) (*ReopenSource, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	if !sessionid.IsValidSessionID(continuedFrom) {
		return nil, nil
	}
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return nil, nil // no sessions folder: nothing to continue
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err // the caller's lookup budget; it then streams a new conversation
		}
		dir := filepath.Join(sessionsDir, e.Name())
		if !e.IsDir() {
			continue
		}
		st, err := ReadState(dir)
		if err != nil || st.SessionID != continuedFrom {
			continue
		}
		return reopenSourceIn(ctx, cfg, dir, st)
	}
	return stashedReopenSource(ctx, cfg, continuedFrom)
}

func reopenSourceIn(ctx context.Context, cfg Config, dir string, st State) (*ReopenSource, error) {
	ok, err := reopenable(ctx, cfg, dir, st)
	if !ok || err != nil {
		return nil, err
	}
	spool, err := readWholeSpool(dir)
	if err != nil {
		cfg.Logger.WarnContext(ctx, "live_session_reopen_skipped", "session_id", st.SessionID, "reason", "spool_unreadable", "error", err)
		return nil, nil
	}
	return &ReopenSource{Dir: dir, State: st, Spool: spool}, nil
}

// reopenable applies the reopen conditions to a stopped stream's state; dir is
// the folder its conversation is bound to.
func reopenable(ctx context.Context, cfg Config, dir string, st State) (bool, error) {
	log := cfg.Logger.With("session_id", st.SessionID, "conversation_id", st.ConversationID, "status", string(st.Status))
	stopped := st.Status == StatusClosed || (st.Status == StatusEmpty && st.Reopen)
	switch {
	case !stopped:
		log.DebugContext(ctx, "live_session_reopen_skipped", "reason", "not_stopped")
		return false, nil
	case st.ManualStop:
		// the person ended it: a manual stop is final
		log.InfoContext(ctx, "live_session_reopen_skipped", "reason", "manual_stop")
		return false, nil
	case st.DeletedOnServer || st.ConversationID == "":
		log.InfoContext(ctx, "live_session_reopen_skipped", "reason", "deleted")
		return false, nil
	case oxsession.IsHeld(dir):
		log.InfoContext(ctx, "live_session_reopen_skipped", "reason", "held")
		return false, nil
	}
	if err := authorize(ctx, cfg, dir, st); err != nil {
		if errors.Is(err, ErrUntrustedState) {
			return false, nil // authorize logged it
		}
		return false, err
	}
	return true, nil
}

// readWholeSpool reads dir's spool up to its last whole line. A spool past the
// unacknowledged cap is not copied: restating it would just open on a stream
// the cap gaps, so the recording starts a conversation of its own instead.
func readWholeSpool(dir string) ([]byte, error) {
	f, err := os.Open(filepath.Join(dir, spoolFileName))
	if err != nil {
		return nil, fmt.Errorf("open spool: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only: nothing to flush
	end, err := spoolEnd(f)
	if err != nil {
		return nil, err
	}
	if end > DefaultSpoolCap {
		return nil, fmt.Errorf("spool is %d bytes, over the %d cap", end, DefaultSpoolCap)
	}
	buf := make([]byte, end)
	if _, err := f.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read spool: %w", err)
	}
	return buf, nil
}

// prepareReopenStateHook, nil in production, stands in for the stream state
// write of PrepareReopen so a test can fail it.
var prepareReopenStateHook func() error

// PrepareReopen is Prepare for a recording that continues src's conversation:
// a pending state on src's conversation id with Reopen set, the conversation's
// binding moved to dir, and src's spool copied in as the start of the new one.
// It sends nothing; the open follows with the daemon's drain or the stop. It is
// idempotent: an existing state is returned untouched.
//
// The conversation keeps its first recording's name and start time, which is
// what the web addresses it by.
func PrepareReopen(ctx context.Context, cfg Config, dir string, p OpenParams, src *ReopenSource) (State, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return State{}, err
	}
	if src == nil || src.State.ConversationID == "" {
		return State{}, errors.New("prepare reopen: no stopped stream")
	}
	var st State
	run := func() error {
		var err error
		if st, err = ReadState(dir); err == nil {
			return nil
		} else if !errors.Is(err, ErrNotStreaming) {
			return err
		}
		ownName, ownStart := p.SessionName, p.StartedAt
		if src.State.SessionName != "" {
			p.SessionName = src.State.SessionName
		}
		if !src.State.StartedAt.IsZero() {
			p.StartedAt = src.State.StartedAt
		}
		st = newState(cfg, src.State.ConversationID, StatusPending, p)
		st.Reopen = true
		st.OwnSessionName, st.OwnStartedAt = ownName, ownStart
		st.OwnHeader = string(ownHeaderLine(dir))
		st.SeedBytes = int64(len(src.Spool))
		// no stream state exists yet, so any spool here is a leftover of an
		// interrupted prepare: replaced, never appended to, or the restated
		// lines would appear twice. Written before the binding moves, so a
		// failure here leaves the stopped stream exactly as it was.
		if err := fileutil.AtomicWriteBytes(filepath.Join(dir, spoolFileName), src.Spool, 0o600); err != nil {
			return fmt.Errorf("seed spool from the stopped stream: %w", err)
		}
		// bound before any state names the id: a stream whose id is not bound
		// to its folder is refused everywhere (see authorize)
		if err := rebindMinted(ctx, cfg, st.ConversationID, src.Dir, dir); err != nil {
			return err
		}
		stateErr := writeState(dir, st)
		if prepareReopenStateHook != nil {
			stateErr = prepareReopenStateHook()
		}
		if err := stateErr; err != nil {
			// the binding goes back, so the stopped stream stays authorized and
			// the next start can retry the same reopen
			if rerr := bindMinted(ctx, cfg, st.ConversationID, canonicalDir(dir), canonicalDir(src.Dir)); rerr != nil {
				cfg.Logger.ErrorContext(ctx, "live_session_reopen_rebind_restore_failed", "conversation_id", st.ConversationID, "error", rerr)
			}
			return err
		}
		return nil
	}
	err = withLock(ctx, dir, spoolLockWait, run)
	if err == nil {
		retargetStash(ctx, cfg, st.ConversationID, st.SessionID, dir)
	}
	return st, err
}

// rebindMinted moves cnv's registry binding from the stopped stream's folder to
// the folder that resumes it. It moves only a binding that names from: an id
// minted for no folder, or for another one, is never taken over (fail closed).
func rebindMinted(ctx context.Context, cfg Config, cnv, from, to string) error {
	info, err := os.Stat(to)
	if err == nil && !info.IsDir() {
		err = errors.New("not a directory")
	}
	if err != nil {
		return fmt.Errorf("rebind live conversation: session folder %s: %w", to, err)
	}
	return bindMinted(ctx, cfg, cnv, canonicalDir(from), canonicalDir(to))
}

// bindMinted moves cnv's binding from src to dst (both already resolved), only
// when it names src or already names dst.
func bindMinted(ctx context.Context, cfg Config, cnv, src, dst string) error {
	err := withMinted(ctx, cfg, func(list []mintedConversation) ([]mintedConversation, bool, error) {
		i := mintedIndex(list, cfg.Endpoint, cnv)
		switch {
		case i < 0:
			return nil, false, fmt.Errorf("conversation %s was not minted on this machine", cnv)
		case list[i].Dir == dst:
			return list, false, nil // an interrupted earlier prepare already moved it
		case list[i].Dir != src:
			return nil, false, fmt.Errorf("conversation %s is bound to session folder %s, not %s", cnv, list[i].Dir, src)
		}
		list[i].Dir = dst
		return list, true, nil
	})
	if err != nil {
		return fmt.Errorf("rebind live conversation: %w", err)
	}
	return nil
}

// sessionTwin is the ses_ id that shares conversation id cnv's UUID.
func sessionTwin(cnv string) (string, bool) {
	const prefix = "cnv_"
	if len(cnv) <= len(prefix) || cnv[:len(prefix)] != prefix {
		return "", false
	}
	id := sessionid.Prefix() + cnv[len(prefix):]
	return id, sessionid.IsValidSessionID(id)
}

// fallBackFromRefusedReopen handles the server refusing an open that carried
// reopen (403, 409 stop or new_layer, 410, any other 4xx): the stream drops
// the reopen and becomes a new conversation of this recording, as it would
// have been before reopening existed, instead of staying pending forever.
// Transport errors, 5xx, 429 and an edge block are not refusals: they return
// (st, false, nil) and the open is retried as for any stream. The conversation
// the reopen asked for is left exactly as it was; nothing is deleted.
func fallBackFromRefusedReopen(ctx context.Context, cfg Config, dir string, st State, openErr error) (State, bool, error) {
	var api *sessionstream.APIError
	// 401 (reauth), 408 and 429 say the credential or the moment was wrong,
	// not that the server refused the reopen: the open is retried as it stands,
	// and the seed and the conversation are never touched.
	if !errors.As(openErr, &api) || api.Status < 400 || api.Status >= 500 ||
		api.Status == http.StatusUnauthorized || api.Status == http.StatusRequestTimeout || api.Status == http.StatusTooManyRequests ||
		api.Body.Action == sessionstream.ActionReauth {
		return st, false, nil
	}
	if _, edge := edgeShape(api); edge {
		return st, false, nil
	}
	cfg.Logger.WarnContext(ctx, "live_session_reopen_refused",
		"session_id", st.SessionID, "conversation_id", st.ConversationID, "status", api.Status,
		"reason", api.Body.Reason, "action", string(api.Body.Action))
	cnv, err := ConversationID(st.SessionID)
	if err != nil {
		return st, false, err
	}
	if err := recordMinted(ctx, cfg, cnv, dir); err != nil {
		return st, false, err
	}
	if err := dropSeed(ctx, dir, st.SeedBytes, []byte(st.OwnHeader)); err != nil {
		return st, false, err
	}
	// the refused conversation can no longer be resumed from here, and its
	// stash (tagged with this recording's id) must not shadow the new
	// conversation's own stash when this recording closes
	if err := removeStash(cfg, st.ConversationID); err != nil {
		cfg.Logger.WarnContext(ctx, "live_session_reopen_stash_remove_failed", "conversation_id", st.ConversationID, "error", err)
	}
	st.ConversationID, st.LayerID = cnv, "clyr_"+uuid.Must(uuid.NewV7()).String()
	st.Reopen, st.SeedBytes, st.Acked = false, 0, 0
	if st.OwnSessionName != "" {
		st.SessionName = st.OwnSessionName
	} else {
		st.SessionName = filepath.Base(dir)
	}
	if !st.OwnStartedAt.IsZero() {
		st.StartedAt = st.OwnStartedAt
	}
	st.OwnSessionName, st.OwnStartedAt, st.OwnHeader = "", time.Time{}, ""
	if err := writeState(dir, st); err != nil {
		return st, false, err
	}
	return st, true, nil
}

// dropSeed rewrites dir's spool without its first seed bytes (the stopped
// session's lines) and with this recording's own header line in their place,
// since the seed's header named the other recording. header comes from the
// stream state (State.OwnHeader), not raw.jsonl, which cache pruning may have
// removed. It holds the swap lock, which every append made without live.lock
// also holds.
func dropSeed(ctx context.Context, dir string, seed int64, header []byte) error {
	if len(header) == 0 {
		return errors.New("drop seeded spool: the recording's header line was not kept")
	}
	return withSwapLock(ctx, dir, func() error {
		spool, err := os.ReadFile(filepath.Join(dir, spoolFileName))
		if err != nil {
			return fmt.Errorf("read spool: %w", err)
		}
		// A fallback interrupted after this write but before its state was
		// saved comes back here with the old seed length. The spool then
		// already starts with this recording's own header, which the seed's
		// first line (another recording's header) never equals: cutting again
		// would remove this recording's own lines.
		if bytes.HasPrefix(spool, header) {
			return nil
		}
		if seed < 0 || seed > int64(len(spool)) {
			return fmt.Errorf("seed of %d bytes is past the %d-byte spool", seed, len(spool))
		}
		out := append(append([]byte(nil), header...), spool[seed:]...)
		if err := fileutil.AtomicWriteBytes(filepath.Join(dir, spoolFileName), out, 0o600); err != nil {
			return fmt.Errorf("drop seeded spool: %w", err)
		}
		return nil
	})
}

// ownHeaderLine is raw.jsonl's first line (the recording's header) with its
// newline, or nil when it cannot be read as one bounded JSON line.
func ownHeaderLine(dir string) []byte {
	f, err := os.Open(filepath.Join(dir, "raw.jsonl"))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }() // read-only: nothing to flush
	line, err := bufio.NewReaderSize(f, 64<<10).ReadBytes('\n')
	line = bytes.TrimSpace(line)
	if (err != nil && !errors.Is(err, io.EOF)) || len(line) == 0 || !validLine(line) || len(line) >= sessionstream.MaxBatchBytes {
		return nil
	}
	return append(line, '\n')
}
