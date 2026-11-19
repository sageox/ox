package livesession

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/sessionstream"
)

// AbortBudget is how long `session abort` may wait on the network (C8).
const AbortBudget = 10 * time.Second

const pendingDeletesFileName = "live-pending-deletes.json"

// pendingLockWait bounds waiting on the queue lock; the queue is tiny.
const pendingLockWait = 5 * time.Second

// noKeyReason is the refusal reason when this machine has no signing key for
// the endpoint: no request it sends can be accepted, so retrying cannot help.
const noKeyReason = "no_signing_key"

// PendingDelete is a server-side delete that has not been confirmed yet.
type PendingDelete struct {
	Endpoint       string    `json:"endpoint"`
	ConversationID string    `json:"conversation_id"`
	EnqueuedAt     time.Time `json:"enqueued_at"`
	// Refused: the delete can never succeed from this machine (the server
	// refused it for good, or nothing here can sign it). The entry stays, so
	// the request is never taken for done, but it is not retried: the person
	// has to delete the session on SageOx. Reason says why.
	Refused bool   `json:"refused,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// AbortResult says where an abort's server-side delete ended up. At most one
// field of Deleted, Queued and Refused is set; none means nothing reached the
// server, so there was nothing to delete.
type AbortResult struct {
	// Deleted: the server confirmed the conversation is gone (2xx, 404 or 410).
	Deleted bool
	// Queued: the delete is in the pending-deletes queue; RetryPendingDeletes
	// sends it until the server confirms.
	Queued bool
	// Refused: the delete cannot succeed from here (Reason says why). It is
	// kept in the queue as refused and not retried; tell the person to delete
	// the session on SageOx.
	Refused bool
	Reason  string
}

type deleteStatus int

const (
	deleteRetry deleteStatus = iota
	deleteDone
	deleteRefused
)

// Abort deletes the session's conversation on the server, signed and bounded
// by budget, and marks the session deleted here; it never touches raw.jsonl.
//
// The delete request is recorded in the pending-deletes queue, outside the
// session folder, before anything can lose it (C2): before the request is
// sent, before Abort waits for live.lock (a drain may hold it far longer than
// budget), and before the caller removes the folder. RetryPendingDeletes then
// sends it until the server confirms. The DELETE itself needs no lock.
//
// An open racing the abort is covered too: see the marker comment below and
// openLocked. A session that never reached the server is only marked deleted.
// A non-nil error means the delete could not be recorded and was not
// confirmed: the caller must keep the session folder, because its stream state
// is then the only record of what to delete, and the session keeps streaming
// (see takeBackDeleteRequest).
func Abort(ctx context.Context, cfg Config, dir string, budget time.Duration) (AbortResult, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return AbortResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if _, err := readState(dir); errors.Is(err, ErrNotStreaming) {
		return AbortResult{}, nil
	} else if err != nil {
		return AbortResult{}, err // what reached the server is unknown
	}
	// The marker goes before the state is read, and an open records OpenSent
	// before it checks the marker, so of an abort and an open racing each other
	// at least one sees the other: the open is not sent, or abort deletes it.
	marker := filepath.Join(dir, deleteRequestFileName)
	ours := !deleteRequested(dir)
	if err := fileutil.AtomicWriteBytes(marker, nil, 0o600); err != nil {
		cfg.Logger.WarnContext(ctx, "live_session_delete_request_failed", "session", filepath.Base(dir), "error", err)
	}
	st, err := readState(dir)
	if err != nil {
		return AbortResult{}, takeBackDeleteRequest(ctx, cfg, dir, ours, err)
	}
	log := cfg.Logger.With("session_id", st.SessionID, "conversation_id", st.ConversationID)
	var res AbortResult
	if st.reachedServer() {
		if res, err = abortOnServer(ctx, cfg, st); err != nil {
			return res, takeBackDeleteRequest(ctx, cfg, dir, ours, err)
		}
	}
	// Local cleanup only, so it gets the rest of the budget and no more. A
	// drain still holding the lock stops at its next call; the caller removes
	// the folder anyway.
	err = withLock(context.WithoutCancel(ctx), dir, remaining(ctx, spoolLockWait), func() error {
		s, err := loadSession(cfg, dir)
		if err != nil {
			return err
		}
		s.wipe(ctx)
		return nil
	})
	if err != nil && !errors.Is(err, ErrNotStreaming) {
		log.WarnContext(ctx, "live_session_abort_wipe_deferred", "error", err)
	}
	return res, nil
}

// takeBackDeleteRequest removes the delete-requested marker of an abort that
// failed (cause): its delete was neither confirmed nor recorded, so the caller
// keeps the session and restores its recording, and that recording must go on
// streaming. Left in place the marker made the session read as deleted for
// good. ours is false when an earlier abort wrote the marker; that abort's
// delete may already be queued, so its marker stays. The stream state still
// names what to delete, so running the abort again finds it.
func takeBackDeleteRequest(ctx context.Context, cfg Config, dir string, ours bool, cause error) error {
	if !ours {
		return cause
	}
	if err := removeIfExists(filepath.Join(dir, deleteRequestFileName)); err != nil {
		cfg.Logger.ErrorContext(ctx, "live_session_delete_request_not_taken_back", "session", filepath.Base(dir), "error", err, "abort_error", cause)
		return errors.Join(cause, fmt.Errorf("take back the delete request (the session reads as deleted until it goes): %w", err))
	}
	return cause
}

// remaining is the time left before ctx's deadline, never less than floor.
func remaining(ctx context.Context, floor time.Duration) time.Duration {
	if dl, ok := ctx.Deadline(); ok {
		return max(time.Until(dl), floor)
	}
	return floor
}

// abortOnServer records the delete, then sends it once within ctx.
func abortOnServer(ctx context.Context, cfg Config, st State) (AbortResult, error) {
	log := cfg.Logger.With("session_id", st.SessionID, "conversation_id", st.ConversationID)
	p := PendingDelete{Endpoint: st.Endpoint, ConversationID: st.ConversationID, EnqueuedAt: time.Now().UTC()}
	queueErr := enqueueDelete(ctx, cfg, p)
	if queueErr != nil {
		log.ErrorContext(ctx, "live_session_abort_queue_failed", "error", queueErr)
	}
	status, reason, sendErr := deleteOnServer(ctx, cfg, p.Endpoint, p.ConversationID)
	// the budget may be spent; settling the queue is local and must still happen
	qctx := context.WithoutCancel(ctx)
	switch status {
	case deleteDone:
		log.InfoContext(ctx, "live_session_abort_deleted")
		if queueErr == nil {
			if err := settlePending(qctx, cfg, []PendingDelete{p}, nil); err != nil {
				// harmless: the retry gets 404/410 and drops it
				log.WarnContext(ctx, "live_session_abort_dequeue_failed", "error", err)
			}
		}
		return AbortResult{Deleted: true}, nil
	case deleteRefused:
		log.WarnContext(ctx, "live_session_abort_refused", "reason", reason, "error", sendErr)
		if queueErr == nil {
			p.Refused, p.Reason = true, reason
			if err := settlePending(qctx, cfg, nil, []PendingDelete{p}); err != nil {
				log.WarnContext(ctx, "live_session_abort_refusal_unrecorded", "error", err)
			}
		}
		return AbortResult{Refused: true, Reason: reason}, nil
	}
	if queueErr != nil {
		return AbortResult{}, fmt.Errorf("live delete neither confirmed nor recorded: %w", errors.Join(queueErr, sendErr))
	}
	log.WarnContext(ctx, "live_session_abort_queued", "error", sendErr)
	return AbortResult{Queued: true}, nil
}

// deleteOnServer sends the signed DELETE. Only a confirmed deletion or a
// confirmed absence (2xx, 404, 410) is deleteDone. A delete that can never
// succeed from here (403, stop or fix, no signing key) is deleteRefused with
// its reason; anything else, unknown answers included, is deleteRetry.
func deleteOnServer(ctx context.Context, cfg Config, ep, cnv string) (deleteStatus, string, error) {
	c, err := cfg.newClient(ctx, ep)
	if errors.Is(err, ErrNoKey) {
		return deleteRefused, noKeyReason, err
	}
	if err != nil {
		return deleteRetry, "", fmt.Errorf("delete live session: %w", err)
	}
	err = c.Delete(ctx, cnv)
	var api *sessionstream.APIError
	switch {
	case err == nil:
		return deleteDone, "", nil
	case !errors.As(err, &api):
		return deleteRetry, "", fmt.Errorf("delete live session: %w", err)
	case api.Status == http.StatusNotFound || classify(api) == refusalDeleted:
		return deleteDone, "", nil
	case api.Status == http.StatusForbidden || classify(api) == refusalStop:
		return deleteRefused, cmp.Or(api.Body.Reason, fmt.Sprintf("http_%d", api.Status)), fmt.Errorf("delete refused: %w", api)
	}
	return deleteRetry, "", fmt.Errorf("delete live session: %w", api)
}

// RetryPendingDeletes retries every queued delete not already refused, each
// bounded by AbortBudget. It drops the ones the server confirmed, marks the
// ones it refused for good (they stay visible to RefusedDeletes and are not
// retried), and returns how many were confirmed. The daemon calls it on its
// tick until the queue holds no retryable entry.
func RetryPendingDeletes(ctx context.Context, cfg Config) (int, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return 0, err
	}
	pending, err := readPending(ctx, cfg)
	if err != nil {
		return 0, err
	}
	var confirmed, refused []PendingDelete
	var errs []error
	for _, p := range pending {
		if p.Refused {
			continue
		}
		actx, cancel := context.WithTimeout(ctx, AbortBudget)
		status, reason, err := deleteOnServer(actx, cfg, p.Endpoint, p.ConversationID)
		cancel()
		switch status {
		case deleteDone:
			confirmed = append(confirmed, p)
		case deleteRefused:
			cfg.Logger.WarnContext(ctx, "live_pending_delete_refused", "conversation_id", p.ConversationID, "reason", reason, "error", err)
			p.Refused, p.Reason = true, reason
			refused = append(refused, p)
		default:
			errs = append(errs, err)
		}
	}
	if len(confirmed)+len(refused) > 0 {
		if err := settlePending(ctx, cfg, confirmed, refused); err != nil {
			errs = append(errs, err)
		}
	}
	return len(confirmed), errors.Join(errs...)
}

// RefusedDeletes lists the deletes that cannot succeed from this machine, so a
// command can tell the person which sessions to delete on SageOx themselves.
func RefusedDeletes(ctx context.Context, cfg Config) ([]PendingDelete, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	pending, err := readPending(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(pending, func(p PendingDelete) bool { return !p.Refused }), nil
}

func pendingPath(cfg Config) string { return filepath.Join(cfg.DataDir, pendingDeletesFileName) }

func readPending(ctx context.Context, cfg Config) ([]PendingDelete, error) {
	var list []PendingDelete
	err := fileutil.WithFileLockTimeout(ctx, pendingPath(cfg), pendingLockWait, func() error {
		var err error
		list, err = loadPending(cfg)
		return err
	})
	return list, err
}

func loadPending(cfg Config) ([]PendingDelete, error) {
	raw, err := os.ReadFile(pendingPath(cfg))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read pending live deletes: %w", err)
	}
	var list []PendingDelete
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse pending live deletes: %w", err)
	}
	return list, nil
}

func savePending(cfg Config, list []PendingDelete) error {
	if err := fileutil.EnsurePrivateDir(cfg.DataDir); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	if err := fileutil.AtomicWriteJSON(pendingPath(cfg), list, 0o600); err != nil {
		return fmt.Errorf("write pending live deletes: %w", err)
	}
	return nil
}

func sameDelete(a, b PendingDelete) bool {
	return a.Endpoint == b.Endpoint && a.ConversationID == b.ConversationID
}

func enqueueDelete(ctx context.Context, cfg Config, p PendingDelete) error {
	return fileutil.WithFileLockTimeout(ctx, pendingPath(cfg), pendingLockWait, func() error {
		list, err := loadPending(cfg)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(list, func(e PendingDelete) bool { return sameDelete(e, p) }) {
			return nil
		}
		return savePending(cfg, append(list, p))
	})
}

// settlePending drops the confirmed deletes and records the refused ones.
func settlePending(ctx context.Context, cfg Config, gone, refused []PendingDelete) error {
	return fileutil.WithFileLockTimeout(ctx, pendingPath(cfg), pendingLockWait, func() error {
		list, err := loadPending(cfg)
		if err != nil {
			return err
		}
		list = slices.DeleteFunc(list, func(e PendingDelete) bool {
			return slices.ContainsFunc(gone, func(g PendingDelete) bool { return sameDelete(e, g) })
		})
		for i, e := range list {
			if j := slices.IndexFunc(refused, func(r PendingDelete) bool { return sameDelete(e, r) }); j >= 0 {
				list[i].Refused, list[i].Reason = true, refused[j].Reason
			}
		}
		return savePending(cfg, list)
	})
}
