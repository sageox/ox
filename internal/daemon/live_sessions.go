package daemon

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
)

// Real-time session streaming, daemon side (ADR-247, shadow phase). Hooks only
// append to a local spool and send live_session_drain; this is the part that
// talks to the server. It never touches raw.jsonl or the v1 Ledger push.
const (
	// liveTickInterval is the sweep that finishes sessions whose drain request
	// was lost (no IPC, server was down) and retries queued abort deletes.
	liveTickInterval = 15 * time.Second
	// liveDrainBudget bounds one drain call; a stuck server must not pin a
	// goroutine for longer than a tick or two.
	liveDrainBudget = 30 * time.Second
	// liveBackoffBase and liveBackoffCap shape the per-session retry delay after
	// an error: 15 s doubling to 5 min, so a down server is probed, not hammered
	// by a hook kick after every tool call.
	liveBackoffBase = 15 * time.Second
	liveBackoffCap  = 5 * time.Minute
)

// LiveSessions opens, drains and closes session spools in the background.
type LiveSessions struct {
	logger *slog.Logger
	// sessionsDir is the only parent IPC-named session paths may live under; the
	// tick scans it too.
	sessionsDir string
	// endpoint is the project endpoint, used for the pending-deletes queue.
	endpoint string
	// config builds the livesession config for an endpoint; tests point it at a fake server.
	config func(endpoint string) livesession.Config
	now    func() time.Time

	mu      sync.Mutex
	running map[string]bool // a drain goroutine owns this session dir
	rerun   map[string]bool // another request arrived while it ran
	backoff map[string]liveBackoff
	wg      sync.WaitGroup
}

type liveBackoff struct {
	next  time.Time
	delay time.Duration
}

// NewLiveSessions creates the manager for sessions under sessionsDir.
func NewLiveSessions(logger *slog.Logger, sessionsDir, endpoint string) *LiveSessions {
	return &LiveSessions{
		logger: logger, sessionsDir: sessionsDir, endpoint: endpoint,
		config:  func(ep string) livesession.Config { return livesession.Config{Endpoint: ep, Logger: logger} },
		now:     time.Now,
		running: map[string]bool{}, rerun: map[string]bool{}, backoff: map[string]liveBackoff{},
	}
}

// Run sweeps until ctx ends.
func (l *LiveSessions) Run(ctx context.Context) {
	t := time.NewTicker(liveTickInterval)
	defer t.Stop()
	for {
		l.Tick(ctx)
		select {
		case <-ctx.Done():
			l.wg.Wait()
			return
		case <-t.C:
		}
	}
}

// Tick handles every session dir with an active live state, then retries queued
// abort deletes.
func (l *LiveSessions) Tick(ctx context.Context) {
	entries, err := os.ReadDir(l.sessionsDir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(l.sessionsDir, e.Name())
			if st, err := livesession.ReadState(dir); err == nil && liveActive(st) {
				l.request(ctx, dir)
			}
		}
	}
	if l.endpoint == "" {
		return
	}
	if n, err := livesession.RetryPendingDeletes(ctx, l.config(l.endpoint)); err != nil {
		l.logger.WarnContext(ctx, "live_pending_deletes_retry_failed", "confirmed", n, "error", err)
	} else if n > 0 {
		l.logger.InfoContext(ctx, "live_pending_deletes_confirmed", "confirmed", n)
	}
}

func liveActive(st livesession.State) bool {
	switch st.Status {
	case livesession.StatusPending, livesession.StatusStreaming, livesession.StatusClosing:
		return true
	}
	return false
}

// Drain handles a live_session_drain request. The path comes from an IPC peer,
// so it must be a direct child of the daemon's own sessions dir.
func (l *LiveSessions) Drain(ctx context.Context, sessionPath string) {
	dir, ok := l.trusted(sessionPath)
	if !ok {
		l.logger.WarnContext(ctx, "live_drain_rejected_path", "session", filepath.Base(sessionPath))
		return
	}
	l.request(ctx, dir)
}

// Capture spools a batch the tail watcher just committed and requests a drain.
func (l *LiveSessions) Capture(ctx context.Context, sessionDir string, batch session.AppendedBatch) {
	res, err := livesession.Spool(ctx, sessionDir, batch.Bytes, livesession.SpoolOptions{Paused: batch.Suspended})
	if err != nil {
		if !errors.Is(err, livesession.ErrNotStreaming) {
			l.logger.WarnContext(ctx, "live_spool_failed", "session", filepath.Base(sessionDir), "error", err)
		}
		return
	}
	if res.Appended > 0 {
		l.request(ctx, sessionDir)
	}
}

// Wait blocks until in-flight drains finish (shutdown and tests).
func (l *LiveSessions) Wait() { l.wg.Wait() }

func (l *LiveSessions) trusted(path string) (string, bool) {
	clean := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	dir := clean(path)
	if l.sessionsDir == "" || filepath.Dir(dir) != clean(l.sessionsDir) {
		return "", false
	}
	return dir, true
}

// request runs a drain for dir unless one is running (it then reruns once) or
// the session is backing off after an error.
func (l *LiveSessions) request(ctx context.Context, dir string) {
	l.mu.Lock()
	if l.running[dir] {
		l.rerun[dir] = true
		l.mu.Unlock()
		return
	}
	if b, ok := l.backoff[dir]; ok && l.now().Before(b.next) {
		l.mu.Unlock()
		return
	}
	l.running[dir] = true
	l.mu.Unlock()

	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		for {
			l.step(ctx, dir)
			l.mu.Lock()
			again := l.rerun[dir]
			delete(l.rerun, dir)
			_, backingOff := l.backoff[dir]
			if !again || backingOff || ctx.Err() != nil {
				delete(l.running, dir)
				l.mu.Unlock()
				return
			}
			l.mu.Unlock()
		}
	}()
}

// step does whatever the session's status calls for and records the outcome.
func (l *LiveSessions) step(ctx context.Context, dir string) {
	st, err := livesession.ReadState(dir)
	if err != nil || !liveActive(st) {
		return
	}
	log := l.logger.With("session_id", st.SessionID, "conversation_id", st.ConversationID, "layer_id", st.LayerID)
	cfg := l.config(st.Endpoint)
	var (
		retryAfter time.Duration
		done       bool
	)
	switch {
	case st.Status == livesession.StatusClosing, st.Status == livesession.StatusPending && st.CloseRequested:
		var res livesession.DrainResult
		res, err = livesession.Close(ctx, cfg, dir, "", livesession.CloseBudget)
		done, retryAfter = res.Done, res.RetryAfter
	default:
		if st.Status == livesession.StatusPending {
			if _, err = livesession.Open(ctx, cfg, dir, livesession.OpenParams{
				SessionID: st.SessionID, SessionName: st.SessionName, RepoID: st.RepoID, Agent: st.Agent, StartedAt: st.StartedAt,
			}); err != nil {
				if errors.Is(err, livesession.ErrKeyConflict) {
					log.WarnContext(ctx, "live_session_disabled", "reason", "key_conflict", "action", "run ox login to enroll a new signing key")
					l.clearBackoff(dir)
					return
				}
				break
			}
		}
		var res livesession.DrainResult
		res, err = livesession.Drain(ctx, cfg, dir, liveDrainBudget)
		done, retryAfter = res.Done, res.RetryAfter
	}
	if err != nil {
		delay := l.fail(dir, retryAfter)
		log.WarnContext(ctx, "live_session_step_failed", "status", string(st.Status), "retry_in_ms", delay.Milliseconds(), "error", err)
		return
	}
	l.clearBackoff(dir)
	if done {
		log.InfoContext(ctx, "live_session_step_done", "status_before", string(st.Status))
	}
}

func (l *LiveSessions) fail(dir string, retryAfter time.Duration) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.backoff[dir]
	if b.delay == 0 {
		b.delay = liveBackoffBase
	} else {
		b.delay = min(b.delay*2, liveBackoffCap)
	}
	delay := max(b.delay, retryAfter)
	b.next = l.now().Add(delay)
	l.backoff[dir] = b
	return delay
}

func (l *LiveSessions) clearBackoff(dir string) {
	l.mu.Lock()
	delete(l.backoff, dir)
	l.mu.Unlock()
}
