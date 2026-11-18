package livesession

import (
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

// PendingDelete is a server-side delete that has not been confirmed yet.
type PendingDelete struct {
	Endpoint       string    `json:"endpoint"`
	ConversationID string    `json:"conversation_id"`
	EnqueuedAt     time.Time `json:"enqueued_at"`
}

// AbortResult says where an abort's server-side delete ended up.
type AbortResult struct {
	// Deleted: the server confirmed (202 or 410).
	Deleted bool
	// Queued: the server could not be reached in time; the delete is in the
	// pending-deletes queue for RetryPendingDeletes.
	Queued bool
}

// Abort deletes the session's conversation on the server, signed and bounded by
// budget, BEFORE the caller removes local state (the key and ids are needed). On
// failure the delete is queued. It marks the stream state deleted and removes
// the spool; it never touches raw.jsonl. A session that never streamed is a
// no-op.
func Abort(ctx context.Context, cfg Config, dir string, budget time.Duration) (AbortResult, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return AbortResult{}, err
	}
	var res AbortResult
	err = withLock(ctx, dir, budget, func() error {
		s, err := loadSession(cfg, dir)
		if err != nil {
			return err
		}
		if s.st.ConversationID == "" || s.st.Status == StatusDeleted {
			return nil // nothing reached the server
		}
		if s.st.Status != StatusPending { // pending: the server never saw it
			res = s.abortOnServer(ctx, budget)
		}
		s.wipe(ctx)
		return nil
	})
	if errors.Is(err, ErrNotStreaming) {
		return AbortResult{}, nil
	}
	return res, err
}

func (s *session) abortOnServer(ctx context.Context, budget time.Duration) AbortResult {
	dctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	done, err := deleteOnServer(dctx, s.cfg, s.st.Endpoint, s.st.ConversationID)
	if done {
		s.log().InfoContext(ctx, "live_session_abort_deleted")
		return AbortResult{Deleted: true}
	}
	// the budget may be spent; queueing is local and must still happen.
	qctx := context.WithoutCancel(ctx)
	if qerr := enqueueDelete(qctx, s.cfg, PendingDelete{Endpoint: s.st.Endpoint, ConversationID: s.st.ConversationID, EnqueuedAt: time.Now().UTC()}); qerr != nil {
		s.log().ErrorContext(ctx, "live_session_abort_queue_failed", "error", qerr)
		return AbortResult{}
	}
	s.log().WarnContext(ctx, "live_session_abort_queued", "error", err)
	return AbortResult{Queued: true}
}

// deleteOnServer sends the signed DELETE. done is true when the conversation is
// gone (2xx, 410, 404) or can never be deleted from here (no key, permanent
// refusal): retrying those would only loop.
func deleteOnServer(ctx context.Context, cfg Config, ep, cnv string) (done bool, err error) {
	c, err := cfg.newClient(ctx, ep)
	if errors.Is(err, ErrNoKey) {
		return true, nil // cannot sign, so cannot ever succeed
	}
	if err != nil {
		return false, err
	}
	err = c.Delete(ctx, cnv)
	var api *sessionstream.APIError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &api) && (api.Status == http.StatusGone || api.Status == http.StatusNotFound):
		return true, nil
	case errors.As(err, &api) && !retryable(api) && api.Status < 500:
		return true, fmt.Errorf("delete refused permanently: %w", api)
	}
	return false, fmt.Errorf("delete live session: %w", err)
}

// RetryPendingDeletes retries every queued delete once, each bounded by
// AbortBudget, and drops the ones the server confirmed. It returns how many were
// confirmed. The daemon calls it on its tick until the queue empties.
func RetryPendingDeletes(ctx context.Context, cfg Config) (int, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return 0, err
	}
	pending, err := readPending(ctx, cfg)
	if err != nil {
		return 0, err
	}
	var confirmed []PendingDelete
	var errs []error
	for _, p := range pending {
		actx, cancel := context.WithTimeout(ctx, AbortBudget)
		done, err := deleteOnServer(actx, cfg, p.Endpoint, p.ConversationID)
		cancel()
		if done {
			confirmed = append(confirmed, p)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	if len(confirmed) > 0 {
		if err := removePending(ctx, cfg, confirmed); err != nil {
			errs = append(errs, err)
		}
	}
	return len(confirmed), errors.Join(errs...)
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

func enqueueDelete(ctx context.Context, cfg Config, p PendingDelete) error {
	return fileutil.WithFileLockTimeout(ctx, pendingPath(cfg), pendingLockWait, func() error {
		list, err := loadPending(cfg)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(list, func(e PendingDelete) bool {
			return e.Endpoint == p.Endpoint && e.ConversationID == p.ConversationID
		}) {
			return nil
		}
		return savePending(cfg, append(list, p))
	})
}

func removePending(ctx context.Context, cfg Config, gone []PendingDelete) error {
	return fileutil.WithFileLockTimeout(ctx, pendingPath(cfg), pendingLockWait, func() error {
		list, err := loadPending(cfg)
		if err != nil {
			return err
		}
		list = slices.DeleteFunc(list, func(e PendingDelete) bool {
			return slices.ContainsFunc(gone, func(g PendingDelete) bool {
				return g.Endpoint == e.Endpoint && g.ConversationID == e.ConversationID
			})
		})
		return savePending(cfg, list)
	})
}
