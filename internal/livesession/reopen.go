package livesession

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	// Spool is the stopped stream's whole spool (whole lines). Nil while
	// Pending: the close may still append to it.
	Spool []byte
	// Pending marks a stream that is otherwise reopenable but whose close was
	// requested and has not finished (the daemon may still be writing its
	// summary, up to SummaryBudget). It cannot be reopened yet: the
	// continuation is prepared as its own conversation and waits for it
	// (AwaitReopen).
	Pending bool
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
// A stream that meets all of that but is still closing is returned Pending.
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
	v, err := reopenCheck(ctx, cfg, dir, st)
	switch {
	case err != nil || v == reopenRefused:
		return nil, err
	case v == reopenInFlight:
		// Debug: a waiting stream looks its source up again on every open
		cfg.Logger.DebugContext(ctx, "live_session_reopen_source_closing", "session_id", st.SessionID, "conversation_id", st.ConversationID, "status", string(st.Status))
		return &ReopenSource{Dir: dir, State: st, Pending: true}, nil
	}
	spool, err := readWholeSpool(dir)
	if err != nil {
		cfg.Logger.WarnContext(ctx, "live_session_reopen_skipped", "session_id", st.SessionID, "reason", "spool_unreadable", "error", err)
		return nil, nil
	}
	return &ReopenSource{Dir: dir, State: st, Spool: spool}, nil
}

// reopenVerdict is what the reopen conditions make of a stream's state.
type reopenVerdict int

const (
	reopenRefused reopenVerdict = iota
	reopenReady
	// reopenInFlight: reopenable once the close already requested finishes
	reopenInFlight
)

// reopenable applies the reopen conditions to a stopped stream's state; dir is
// the folder its conversation is bound to. A stream still closing is not
// reopenable yet.
func reopenable(ctx context.Context, cfg Config, dir string, st State) (bool, error) {
	v, err := reopenCheck(ctx, cfg, dir, st)
	return v == reopenReady, err
}

// reopenCheck is reopenable, telling a stream whose close is requested but
// not finished (reopenInFlight) from one that is not stopped at all. A manual
// stop marked before the close is seen while it is still in flight, so a
// person's stop is never waited for.
func reopenCheck(ctx context.Context, cfg Config, dir string, st State) (reopenVerdict, error) {
	log := cfg.Logger.With("session_id", st.SessionID, "conversation_id", st.ConversationID, "status", string(st.Status))
	stopped := st.Status == StatusClosed || (st.Status == StatusEmpty && st.Reopen)
	closing := st.Status == StatusClosing || (st.CloseRequested && (st.Status == StatusPending || st.Status == StatusStreaming))
	switch {
	case !stopped && !closing:
		log.DebugContext(ctx, "live_session_reopen_skipped", "reason", "not_stopped")
		return reopenRefused, nil
	case st.ManualStop:
		// the person ended it: a manual stop is final
		log.InfoContext(ctx, "live_session_reopen_skipped", "reason", "manual_stop")
		return reopenRefused, nil
	case st.DeletedOnServer || st.ConversationID == "":
		log.InfoContext(ctx, "live_session_reopen_skipped", "reason", "deleted")
		return reopenRefused, nil
	case oxsession.IsHeld(dir):
		log.InfoContext(ctx, "live_session_reopen_skipped", "reason", "held")
		return reopenRefused, nil
	}
	if err := authorize(ctx, cfg, dir, st); err != nil {
		if errors.Is(err, ErrUntrustedState) {
			return reopenRefused, nil // authorize logged it
		}
		return reopenRefused, err
	}
	if closing {
		return reopenInFlight, nil
	}
	return reopenReady, nil
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
	if src.Pending {
		// its spool is not final and its close still needs its binding
		return State{}, errors.New("prepare reopen: the stream has not finished closing (AwaitReopen waits for it)")
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

// A person who exits the agent and resumes it within seconds starts the next
// recording while the daemon still writes the stopped one's summary (up to
// SummaryBudget) before its close. That stream is not closed yet, so it cannot
// be reopened, and streaming the continuation as a conversation of its own
// split one session in two for good. So the continuation waits instead: it is
// prepared as its own pending conversation, as Prepare does, with a ReopenAwait
// naming the closing stream. Nothing opens it while it waits, and nothing
// blocks a hook. The first open after the awaited close finishes reopens that
// conversation (resolveAwait); a stream that can no longer be reopened, the
// deadline, or a stop of the continuation ends the wait, and it streams as the
// conversation it already is.

// reopenAwaitWindow bounds the wait. The awaited close takes the summary
// (SummaryBudget, 60 s) plus CloseBudget, and a failed attempt backs off up to
// 30 s in the daemon: five minutes covers a couple of failed rounds, while a
// continuation that waits longer would keep a live session off SageOx for
// longer than a teammate would put up with. A var so tests can expire it.
var reopenAwaitWindow = 5 * time.Minute

// ReopenAwait is a pending stream's wait for the stream of the recording it
// continues (State.AwaitReopen).
type ReopenAwait struct {
	// Dir is the awaited stream's session folder, resolved. A sibling of the
	// waiting stream's folder: a moved stream (.live-<ses id>) included.
	Dir string `json:"dir"`
	// ConversationID is the awaited stream's conversation when the wait began,
	// for diagnosis: the stream is found again by SessionID, and what it ends
	// as (a reopen of its own, or a fallback) is what gets continued.
	ConversationID string `json:"conversation_id"`
	// SessionID is the awaited recording's ses_ id. The stream is looked up by
	// it, so a folder that moved or was pruned (the stash) is still found.
	SessionID string    `json:"session_id"`
	Deadline  time.Time `json:"deadline"`
	// SeedBytes is recorded just before the awaited stream's spool is copied in
	// front of this one. A resolution cut short after the copy and before the
	// reopened state was written leaves it set, and the next one first undoes
	// the copy, so the seed is never copied twice and a fallback never keeps it.
	SeedBytes int64 `json:"seed_bytes,omitempty"`
}

// AwaitReopen makes the pending stream that Prepare wrote in dir for the
// recording sessionID wait for src, a stream FindReopenSource returned Pending.
// It sends nothing and moves no binding: the awaited stream's own close still
// needs its binding where it is. The header line Prepare's caller spooled is
// kept (State.OwnHeader) so a reopen can put the awaited stream's spool, which
// starts with the session's own header, in its place. An error leaves the
// stream as the new conversation Prepare made.
func AwaitReopen(ctx context.Context, cfg Config, dir, sessionID string, src *ReopenSource) (State, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return State{}, err
	}
	if src == nil || !src.Pending {
		return State{}, errors.New("await reopen: the stream is not closing")
	}
	from, to := canonicalDir(src.Dir), canonicalDir(dir)
	if from == to || filepath.Dir(from) != filepath.Dir(to) {
		return State{}, fmt.Errorf("await reopen: the closing stream is in %s, not in a folder beside %s", from, to)
	}
	var st State
	err = withLock(ctx, dir, spoolLockWait, func() error {
		var err error
		if st, err = ReadState(dir); err != nil {
			return err
		}
		switch {
		case st.SessionID != sessionID:
			return ErrOtherRecording
		case st.AwaitReopen != nil:
			return nil // already waiting
		case st.Status != StatusPending || st.OpenSent || st.Reopen:
			// an open may already have created the conversation: it stays it
			return fmt.Errorf("await reopen: the stream is %s (open sent %t, reopen %t)", st.Status, st.OpenSent, st.Reopen)
		}
		header := ownHeaderLine(dir)
		if header == nil || !spoolStartsWith(dir, header) {
			return errors.New("await reopen: the spool does not start with the recording's header line")
		}
		st.OwnHeader = string(header)
		st.AwaitReopen = &ReopenAwait{
			Dir: from, ConversationID: src.State.ConversationID, SessionID: src.State.SessionID,
			Deadline: time.Now().UTC().Add(reopenAwaitWindow),
		}
		return writeState(dir, st)
	})
	if err != nil {
		return State{}, err
	}
	cfg.Logger.InfoContext(ctx, "live_session_reopen_awaiting", "session_id", st.SessionID, "conversation_id", st.ConversationID,
		"awaited_session_id", st.AwaitReopen.SessionID, "awaited_conversation_id", st.AwaitReopen.ConversationID, "deadline", st.AwaitReopen.Deadline)
	return st, nil
}

// ResolveReopen ends, for a stop, the wait of the stream in dir (AwaitReopen)
// before the stop writes its summary: the stream reopens the awaited
// conversation if its close has finished by now, and otherwise streams as its
// own. The stop never waits on another stream's summary, and its own summary
// is then written for the conversation its close carries. A stream that is
// not waiting is returned as it is.
func ResolveReopen(ctx context.Context, cfg Config, dir string, wait time.Duration) (State, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return State{}, err
	}
	var st State
	err = withLock(ctx, dir, wait, func() error {
		var err error
		if st, err = ReadState(dir); err != nil || st.AwaitReopen == nil || st.Status != StatusPending {
			return err
		}
		st, err = settleAwait(ctx, cfg, dir, st, true)
		return err
	})
	return st, err
}

// settleAwait resolves a waiting stream st in dir; the caller holds live.lock.
// The stream itself is authorized first: a state planted in a Ledger checkout
// names any folder it likes, and must never move a real conversation's
// binding into its own.
func settleAwait(ctx context.Context, cfg Config, dir string, st State, final bool) (State, error) {
	if err := authorize(ctx, cfg, dir, st); err != nil {
		return st, err
	}
	return resolveAwait(ctx, cfg, dir, st, final)
}

// resolveAwait decides a waiting stream: it reopens the awaited conversation
// once that stream has closed and may still be reopened, keeps waiting while
// it is closing, and otherwise ends the wait, leaving the stream the new
// conversation it already is. final (a stop or close of this stream) never
// keeps waiting. The caller holds live.lock and authorized st.
func resolveAwait(ctx context.Context, cfg Config, dir string, st State, final bool) (State, error) {
	if st.AwaitReopen == nil {
		return st, nil
	}
	aw := *st.AwaitReopen
	st.AwaitReopen = &aw // never write through the caller's copy
	log := cfg.Logger.With("session_id", st.SessionID, "conversation_id", st.ConversationID,
		"awaited_session_id", aw.SessionID, "awaited_conversation_id", aw.ConversationID)
	if aw.SeedBytes > 0 {
		// an earlier resolution was cut short after it copied the seed in
		if err := dropSeed(ctx, dir, aw.SeedBytes, []byte(st.OwnHeader)); err != nil {
			return st, fmt.Errorf("undo an interrupted reopen: %w", err)
		}
		log.WarnContext(ctx, "live_session_reopen_await_undone", "seed_bytes", aw.SeedBytes)
		aw.SeedBytes = 0
		if err := writeState(dir, st); err != nil {
			return st, err
		}
	}
	reason := ""
	var src *ReopenSource
	switch from, to := canonicalDir(aw.Dir), canonicalDir(dir); {
	case st.Status != StatusPending || st.OpenSent || st.Reopen:
		reason = "already_opened" // nothing opens a waiting stream; never seed one that opened
	case from == to || filepath.Dir(from) != filepath.Dir(to):
		reason = "awaited_folder_not_a_sibling"
	default:
		var err error
		if src, err = FindReopenSource(ctx, cfg, filepath.Dir(from), aw.SessionID); err != nil {
			return st, err
		}
		switch {
		case src == nil:
			// manual stop, deleted, held, gone or untrusted: the lookup logged which
			reason = "not_reopenable"
		case src.Pending && final:
			reason = "stopped_while_awaiting"
		case src.Pending && time.Now().After(aw.Deadline):
			reason = "deadline"
		case src.Pending:
			log.DebugContext(ctx, "live_session_reopen_still_awaiting", "deadline", aw.Deadline)
			return st, nil
		}
	}
	if reason == "" {
		next, err := reopenAwaited(ctx, cfg, dir, st, src, log)
		if err == nil {
			return next, nil
		}
		if !final && time.Now().Before(aw.Deadline) {
			return st, err // retried by the next open
		}
		log.WarnContext(ctx, "live_session_reopen_await_failed", "error", err)
		reason = "reopen_failed"
		if st, err = undoAwaitSeed(ctx, dir, st); err != nil {
			return st, err
		}
	}
	return endAwait(ctx, dir, st, reason, log)
}

// endAwait ends st's wait without a reopen: the stream stays the conversation
// Prepare made for this recording, with its own header first in the spool.
func endAwait(ctx context.Context, dir string, st State, reason string, log *slog.Logger) (State, error) {
	st.AwaitReopen, st.OwnHeader = nil, ""
	if err := writeState(dir, st); err != nil {
		return st, err
	}
	log.InfoContext(ctx, "live_session_reopen_await_ended", "outcome", "new_conversation", "reason", reason)
	return st, nil
}

// undoAwaitSeed undoes a seed copy reopenAwaited recorded but did not finish.
func undoAwaitSeed(ctx context.Context, dir string, st State) (State, error) {
	if st.AwaitReopen == nil || st.AwaitReopen.SeedBytes == 0 {
		return st, nil
	}
	if err := dropSeed(ctx, dir, st.AwaitReopen.SeedBytes, []byte(st.OwnHeader)); err != nil {
		return st, fmt.Errorf("undo an unfinished reopen: %w", err)
	}
	st.AwaitReopen.SeedBytes = 0
	return st, nil
}

// reopenAwaitedStateHook, nil in production, runs in place of the state write
// that finishes a deferred reopen, so a test can cut the reopen short there.
var reopenAwaitedStateHook func() error

// reopenAwaited turns the waiting stream st into a reopen of src's
// conversation, exactly as PrepareReopen would have made it: src's spool first
// (in place of this recording's own header, since it starts with the
// session's), the binding moved here, the conversation's name and start kept.
// The seed's length is on disk before the spool changes, so a crash between
// the two is undone by the next resolution, never repeated.
func reopenAwaited(ctx context.Context, cfg Config, dir string, st State, src *ReopenSource, log *slog.Logger) (State, error) {
	own := []byte(st.OwnHeader)
	st.AwaitReopen.SeedBytes = int64(len(src.Spool))
	if err := writeState(dir, st); err != nil {
		return st, err
	}
	if err := prependSeed(ctx, dir, src.Spool, own); err != nil {
		return st, err
	}
	next := st
	next.ConversationID, next.LayerID = src.State.ConversationID, "clyr_"+uuid.Must(uuid.NewV7()).String()
	next.Reopen, next.SeedBytes, next.Acked = true, int64(len(src.Spool)), 0
	next.OwnSessionName, next.OwnStartedAt = st.SessionName, st.StartedAt
	if src.State.SessionName != "" {
		next.SessionName = src.State.SessionName
	}
	if !src.State.StartedAt.IsZero() {
		next.StartedAt = src.State.StartedAt
	}
	next.AwaitReopen = nil
	if err := rebindMinted(ctx, cfg, next.ConversationID, src.Dir, dir); err != nil {
		return st, err
	}
	var stateErr error
	if reopenAwaitedStateHook != nil {
		stateErr = reopenAwaitedStateHook()
	} else {
		stateErr = writeState(dir, next)
	}
	if stateErr != nil {
		// the binding goes back, so the closed stream stays authorized where
		// it is; the seed is undone from st's record
		if rerr := bindMinted(ctx, cfg, next.ConversationID, canonicalDir(dir), canonicalDir(src.Dir)); rerr != nil {
			log.ErrorContext(ctx, "live_session_reopen_rebind_restore_failed", "error", rerr)
		}
		return st, stateErr
	}
	log.InfoContext(ctx, "live_session_reopen_await_ended", "outcome", "reopened", "reopened_conversation_id", next.ConversationID,
		"layer_id", next.LayerID, "seed_bytes", next.SeedBytes)
	retargetStash(ctx, cfg, next.ConversationID, next.SessionID, dir)
	return next, nil
}

// prependSeed puts seed (the awaited stream's whole spool) in front of dir's
// spool, in place of its first line: this recording's own header, which the
// seed's own first line already states for the session. It holds the swap
// lock, which every append made without live.lock also holds, so no capture
// lands between the read and the rewrite.
func prependSeed(ctx context.Context, dir string, seed, header []byte) error {
	if len(header) == 0 {
		return errors.New("seed the spool: the recording's header line was not kept")
	}
	return withSwapLock(ctx, dir, func() error {
		path := filepath.Join(dir, spoolFileName)
		spool, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read spool: %w", err)
		}
		if !bytes.HasPrefix(spool, header) {
			return errors.New("seed the spool: it does not start with the recording's header line")
		}
		out := make([]byte, 0, len(seed)+len(spool)-len(header))
		out = append(append(out, seed...), spool[len(header):]...)
		if err := fileutil.AtomicWriteBytes(path, out, 0o600); err != nil {
			return fmt.Errorf("seed the spool: %w", err)
		}
		return nil
	})
}

// spoolStartsWith reports whether dir's spool begins with prefix.
func spoolStartsWith(dir string, prefix []byte) bool {
	f, err := os.Open(filepath.Join(dir, spoolFileName))
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }() // read-only: nothing to flush
	got := make([]byte, len(prefix))
	_, err = io.ReadFull(f, got)
	return err == nil && bytes.Equal(got, prefix)
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
