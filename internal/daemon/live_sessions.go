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
	"github.com/sageox/ox/internal/sessionstream"
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
	// an error: 15 s doubling to 30 s, so a down server is probed at most twice a
	// minute per session, never by a hook kick after every tool call. The cap is
	// what bounds recovery: once the network is back, the next tick-driven attempt
	// comes within the cap (plus one tick when the last failure came from a hook
	// kick between ticks), inside ADR-247 D6's 60 s from write to visible. A cap
	// in minutes left an outage's backlog unsent for minutes after the network
	// returned.
	liveBackoffBase = 15 * time.Second
	liveBackoffCap  = 30 * time.Second
	// liveBackoffSlack lets a tick that lands a hair before a backoff deadline
	// retry. Ticks keep a fixed phase with a few ms of jitter; without slack the
	// tick exactly one delay later could look early and the retry waited a whole
	// extra tick (a 30 s cap acted like 45 s). Far below the 15 s base, so the
	// server never sees a retry meaningfully early.
	liveBackoffSlack = time.Second
	// liveStepStuckAfter: the longest a step can legitimately run is an open
	// plus a drain, each bounded by liveDrainBudget (a close is shorter). An
	// attempt older than that plus a tick is wedged where no budget reaches.
	liveStepStuckAfter = 2*liveDrainBudget + liveTickInterval
	// liveRulesRefreshInterval paces the background fetch of the current
	// gitleaks catalog the live publication scan adds to the bundled one
	// (livesession.RefreshSecretRules). Drains never wait on it. Fifteen minutes
	// keeps a new upstream detector at most that far behind while staying well
	// clear of GitHub's raw-content rate limits and a whole-catalog recompile
	// per fetch.
	liveRulesRefreshInterval = 15 * time.Minute
)

// LiveSessions opens, drains and closes session spools in the background.
type LiveSessions struct {
	logger *slog.Logger
	// sessionsDir is the only parent IPC-named session paths may live under; the
	// tick scans it too.
	sessionsDir string
	// endpoint is the project endpoint. Every signed request goes to it and to
	// nothing else: a stream-state.json sits in the Ledger checkout, so the
	// endpoint one names is never trusted (see livesession.authorize).
	endpoint string
	// config builds the livesession config for an endpoint; tests point it at a fake server.
	config func(endpoint string) livesession.Config
	now    func() time.Time
	// refreshRules fetches the current secret rules and persists them for every
	// publishing process; tests replace it.
	refreshRules func(context.Context) error
	// onTeammateComments receives the notes a drain returned; nil drops them
	// (the server re-sends every undelivered one on the next append).
	onTeammateComments func(ctx context.Context, dir string, st livesession.State, comments []sessionstream.TeammateComment)
	// flushAcks, ackFlushBudget and ackFlushStart drive the closed sessions'
	// receipt retries (flushOwedAcks); tests replace the first two.
	flushAcks        func(ctx context.Context, cfg livesession.Config, dir string, budget time.Duration, now time.Time) (bool, error)
	ackFlushBudget   time.Duration
	ackFlushStart    int
	ackFlushFailures sync.Map
	// rulesFailing: the last rules refresh failed. Owned by the refresh loop.
	rulesFailing bool

	mu      sync.Mutex
	running map[string]*liveAttempt // a drain goroutine owns this session dir
	rerun   map[string]bool         // another request arrived while it ran
	backoff map[string]liveBackoff
	// known is every session the tick last saw streaming, so one whose folder
	// disappears is reported instead of silently dropping out of the scan
	known map[string]liveKnown
	wg    sync.WaitGroup
}

// liveKnown is a streaming session as the tick last saw it.
type liveKnown struct {
	st      livesession.State
	missing bool // its folder is gone and that was reported
}

// liveAttempt is the step a drain goroutine is running for one session.
type liveAttempt struct {
	started time.Time
	// reported: the attempt outlived liveStepStuckAfter and was logged once.
	reported bool
}

type liveBackoff struct {
	// next is our own pacing, measured from the start of the failed attempt;
	// liveBackoffSlack applies to it.
	next  time.Time
	delay time.Duration
	// notBefore is a wait the server asked for (Retry-After), measured from
	// when its refusal arrived. No slack: retrying early only draws more 429s.
	notBefore time.Time
}

// NewLiveSessions creates the manager for sessions under sessionsDir.
func NewLiveSessions(logger *slog.Logger, sessionsDir, endpoint string) *LiveSessions {
	l := &LiveSessions{
		logger: logger, sessionsDir: sessionsDir, endpoint: endpoint,
		config:  func(ep string) livesession.Config { return livesession.Config{Endpoint: ep, Logger: logger} },
		now:     time.Now,
		running: map[string]*liveAttempt{}, rerun: map[string]bool{}, backoff: map[string]liveBackoff{},
		known: map[string]liveKnown{},
	}
	// persisted in the data dir the drains read, so the CLI's stop-time close
	// scans with the same rules as this daemon
	l.refreshRules = func(ctx context.Context) error {
		return livesession.RefreshSecretRules(ctx, l.config(l.endpoint).DataDir)
	}
	return l
}

// Run sweeps until ctx ends. The secret rules refresh runs beside the sweep,
// never in front of it: a fetch that hangs must not hold a single drain.
func (l *LiveSessions) Run(ctx context.Context) {
	rulesDone := make(chan struct{})
	go func() {
		defer close(rulesDone)
		l.refreshRulesEvery(ctx, liveRulesRefreshInterval)
	}()
	t := time.NewTicker(liveTickInterval)
	defer t.Stop()
	for {
		l.Tick(ctx)
		select {
		case <-ctx.Done():
			l.wg.Wait()
			<-rulesDone
			return
		case <-t.C:
		}
	}
}

// refreshRulesEvery refreshes the secret rules now and then every interval
// until ctx ends.
func (l *LiveSessions) refreshRulesEvery(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		l.refreshRulesOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// refreshRulesOnce runs one refresh. A failure keeps the last good catalog
// (drains scan with it, or with the bundled catalog alone before any refresh
// succeeded on this machine), so it is logged once per failure streak rather
// than every interval, and the recovery once.
func (l *LiveSessions) refreshRulesOnce(ctx context.Context) {
	err := l.refreshRules(ctx)
	switch {
	case ctx.Err() != nil:
		// shutting down: a canceled fetch is no failure
	case err != nil && !l.rulesFailing:
		l.rulesFailing = true
		l.logger.WarnContext(ctx, "live_secret_rules_refresh_failed",
			"impact", "live streams scan with the last good rules, or the bundled catalog alone", "error", err)
	case err == nil && l.rulesFailing:
		l.rulesFailing = false
		l.logger.InfoContext(ctx, "live_secret_rules_refresh_recovered")
	case err == nil:
		l.logger.DebugContext(ctx, "live_secret_rules_refreshed")
	}
}

// Tick handles every session dir with an active stream state, then retries queued
// abort deletes.
func (l *LiveSessions) Tick(ctx context.Context) {
	seen := map[string]livesession.State{}
	var closed []string
	entries, err := os.ReadDir(l.sessionsDir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := canonicalDir(filepath.Join(l.sessionsDir, e.Name()))
			st, err := livesession.ReadState(dir)
			switch {
			case err != nil:
			case liveActive(st):
				seen[dir] = st
				l.request(ctx, dir)
			case st.Status == livesession.StatusClosed:
				closed = append(closed, dir)
			}
		}
	}
	l.trackSessionDirs(ctx, seen)
	if l.endpoint == "" {
		return
	}
	if n, err := livesession.RetryPendingDeletes(ctx, l.config(l.endpoint)); err != nil {
		l.logger.WarnContext(ctx, "live_pending_deletes_retry_failed", "confirmed", n, "error", err)
	} else if n > 0 {
		l.logger.InfoContext(ctx, "live_pending_deletes_confirmed", "confirmed", n)
	}
	// last, and bounded: a receipt for a finished session never delays live work
	l.flushOwedAcks(ctx, closed)
}

const (
	// liveAckFlushBudget is the whole tick's allowance for closed sessions'
	// receipt retries, however many sessions owe one.
	liveAckFlushBudget = 20 * time.Second
	// liveAckFlushMax is how many sessions with receipts owed one tick works.
	liveAckFlushMax = 4
)

// flushOwedAcks retries the delivery receipts closed streams still owe (the
// sweep skips closed streams, so without this a receipt that failed around the
// close would never reach the server). It runs after everything live in the
// tick, under one budget, for at most liveAckFlushMax sessions per tick; the
// starting point rotates, so a dead server cannot pin the same sessions first.
// Each receipt is retried for a day (livesession).
func (l *LiveSessions) flushOwedAcks(ctx context.Context, dirs []string) {
	if len(dirs) == 0 {
		return
	}
	budget := l.ackFlushBudget
	if budget <= 0 {
		budget = liveAckFlushBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	flush := l.flushAcks
	if flush == nil {
		flush = livesession.FlushTeammateAcks
	}
	start := l.ackFlushStart % len(dirs)
	l.ackFlushStart++
	attempts := 0
	for i := range dirs {
		if attempts >= liveAckFlushMax || ctx.Err() != nil {
			return
		}
		dir := dirs[(start+i)%len(dirs)]
		attempted, err := flush(ctx, l.config(l.endpoint), dir, liveDrainBudget, l.now())
		if attempted {
			attempts++
		}
		if err != nil {
			l.reportAckFlushFailure(ctx, dir, err)
		}
	}
}

// reportAckFlushFailure logs a flush failure once per session and error: a
// folder whose receipt log is oversized or unreadable fails the same way every
// tick, and one line is the signal.
func (l *LiveSessions) reportAckFlushFailure(ctx context.Context, dir string, err error) {
	key := dir + "\x00" + err.Error()
	if _, logged := l.ackFlushFailures.LoadOrStore(key, true); logged {
		return
	}
	l.logger.WarnContext(ctx, "live_teammate_acks_flush_failed", "session", filepath.Base(dir), "error", err)
}

// trackSessionDirs reports, once, a streaming session whose folder vanished,
// and logs when it comes back (the next tick then streams it again). Without
// it a session simply dropped out of the scan: the Ledger clone self-heal once
// moved the whole half-made Ledger aside, sessions and all, and the daemon
// went quiet with turns still unsent. A whole sessions folder disappearing is
// never legitimate, so that is an ERROR; a single folder can be an abort or a
// prune racing the tick, so that is a WARN.
func (l *LiveSessions) trackSessionDirs(ctx context.Context, seen map[string]livesession.State) {
	_, dirErr := os.Stat(l.sessionsDir)
	var gone, back []string
	l.mu.Lock()
	for dir, st := range seen {
		if k, ok := l.known[dir]; ok && k.missing {
			back = append(back, dir)
		}
		l.known[dir] = liveKnown{st: st}
	}
	for dir, k := range l.known {
		if _, ok := seen[dir]; ok {
			continue
		}
		if _, err := os.Lstat(dir); err == nil {
			delete(l.known, dir) // still there and no longer active: closed, deleted or disabled
			continue
		}
		if !k.missing {
			k.missing = true
			l.known[dir] = k
			gone = append(gone, dir)
		}
	}
	reports := map[string]liveKnown{}
	for _, dir := range append(gone, back...) {
		reports[dir] = l.known[dir]
	}
	l.mu.Unlock()

	for _, dir := range gone {
		st := reports[dir].st
		args := []any{"path", dir, "sessions_dir", l.sessionsDir, "sessions_dir_missing", dirErr != nil,
			"session_id", st.SessionID, "conversation_id", st.ConversationID, "layer_id", st.LayerID,
			"status", string(st.Status), "acked", st.Acked}
		if dirErr != nil {
			l.logger.ErrorContext(ctx, "live_session_dir_missing", args...)
		} else {
			l.logger.WarnContext(ctx, "live_session_dir_missing", args...)
		}
	}
	for _, dir := range back {
		st := reports[dir].st
		l.logger.InfoContext(ctx, "live_session_dir_back", "path", dir, "session_id", st.SessionID, "conversation_id", st.ConversationID)
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

// Spooled requests a drain of a session whose spool one of the daemon's
// capture paths just added to (livesession.SpoolCommitted).
func (l *LiveSessions) Spooled(ctx context.Context, sessionDir string) {
	l.request(ctx, canonicalDir(sessionDir))
}

// Wait blocks until in-flight drains finish (shutdown and tests).
func (l *LiveSessions) Wait() { l.wg.Wait() }

func (l *LiveSessions) trusted(path string) (string, bool) {
	dir := canonicalDir(path)
	if l.sessionsDir == "" || filepath.Dir(dir) != canonicalDir(l.sessionsDir) {
		return "", false
	}
	return dir, true
}

// canonicalDir is the one key a session folder has in the running, rerun,
// backoff and known maps, however a caller spelled its path. The tick scans
// sessionsDir as configured while a hook kick is resolved through symlinks;
// keyed by spelling, a symlink anywhere in the cache path made them two
// sessions, so kicks skipped the tick's backoff and both could drain at once.
// A path that cannot be resolved (a folder already gone) falls back to Clean.
func canonicalDir(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// request runs a drain for dir unless one is running (it then reruns once) or
// the session is backing off after an error. A running attempt that outlived
// every budget is reported once (reportStuck).
func (l *LiveSessions) request(ctx context.Context, dir string) {
	l.mu.Lock()
	now := l.now()
	if a := l.running[dir]; a != nil {
		l.rerun[dir] = true
		ran := now.Sub(a.started)
		stuck := !a.reported && ran >= liveStepStuckAfter
		a.reported = a.reported || stuck
		l.mu.Unlock()
		if stuck {
			l.reportStuck(ctx, dir, ran)
		}
		return
	}
	if b, ok := l.backoff[dir]; ok && (now.Add(liveBackoffSlack).Before(b.next) || now.Before(b.notBefore)) {
		l.mu.Unlock()
		return
	}
	a := &liveAttempt{started: now}
	l.running[dir] = a
	l.mu.Unlock()

	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		for {
			l.step(ctx, dir, a.started)
			l.mu.Lock()
			again := l.rerun[dir]
			delete(l.rerun, dir)
			_, backingOff := l.backoff[dir]
			if !again || backingOff || ctx.Err() != nil {
				delete(l.running, dir)
				l.mu.Unlock()
				return
			}
			a = &liveAttempt{started: l.now()}
			l.running[dir] = a
			l.mu.Unlock()
		}
	}()
}

// reportStuck names a session whose drain attempt outlived every budget, so a
// session the daemon has stopped trying is never silent. The attempt is not
// abandoned: it most likely holds live.lock, so a second one would only queue
// behind it. This line plus a goroutine dump (SIGQUIT) is what finds the wedge.
func (l *LiveSessions) reportStuck(ctx context.Context, dir string, ran time.Duration) {
	args := []any{"session", filepath.Base(dir), "running_ms", ran.Milliseconds(), "stuck_after_ms", liveStepStuckAfter.Milliseconds()}
	if st, err := livesession.ReadState(dir); err == nil {
		args = append(args, "session_id", st.SessionID, "conversation_id", st.ConversationID, "layer_id", st.LayerID, "status", string(st.Status))
	}
	l.logger.ErrorContext(ctx, "live_session_step_stuck", args...)
}

// step does whatever the session's status calls for and records the outcome.
// Every way it ends is logged: a session the daemon quietly stops trying is
// the one failure nobody can see. started is when the attempt began; backoff
// is measured from it, not from when a slow failure finally returned.
func (l *LiveSessions) step(ctx context.Context, dir string, started time.Time) {
	st, err := livesession.ReadState(dir)
	switch {
	case errors.Is(err, livesession.ErrNotStreaming):
		l.logger.InfoContext(ctx, "live_session_step_skipped", "session", filepath.Base(dir), "reason", "no_stream_state")
		return
	case err != nil:
		l.logger.WarnContext(ctx, "live_session_step_skipped", "session", filepath.Base(dir), "reason", "state_unreadable", "error", err)
		return
	case !liveActive(st):
		l.logger.InfoContext(ctx, "live_session_step_skipped", "session", filepath.Base(dir), "reason", "not_active", "status", string(st.Status))
		return
	}
	log := l.logger.With("session_id", st.SessionID, "conversation_id", st.ConversationID, "layer_id", st.LayerID)
	if session.IsHeld(dir) {
		l.stopHeld(ctx, dir, st, started, log)
		return
	}
	res, err := l.attempt(ctx, dir, st)
	if res.TeammateCommentsListed && l.onTeammateComments != nil {
		l.onTeammateComments(ctx, dir, st, res.TeammateComments)
	}
	switch {
	case errors.Is(err, livesession.ErrKeyConflict):
		log.WarnContext(ctx, "live_session_disabled", "reason", "key_conflict", "action", "run ox login to enroll a new signing key")
		l.clearBackoff(dir)
	case errors.Is(err, livesession.ErrAwaitingPrompt):
		// expected until the first prompt: its spool kicks a drain that opens
		// the stream, so no backoff, and nothing above Debug per tick
		l.clearBackoff(dir)
		log.DebugContext(ctx, "live_session_step_skipped", "reason", "awaiting_first_prompt")
	case errors.Is(err, livesession.ErrUntrustedState):
		// livesession logged it once at Error; nothing was sent, and a retry
		// cannot change the answer, so no backoff and no line per tick
		l.clearBackoff(dir)
		log.DebugContext(ctx, "live_session_step_skipped", "reason", "untrusted_state", "error", err)
	case errors.Is(err, livesession.ErrEdgeBlocked):
		// same backoff as any retryable failure; livesession logged the streak
		// once at Error and logs when it clears, so a line per retry is noise
		delay := l.fail(dir, res.RetryAfter, started)
		log.DebugContext(ctx, "live_session_step_failed", "status", string(st.Status), "retry_in_ms", delay.Milliseconds(), "error", err)
	case err != nil:
		delay := l.fail(dir, res.RetryAfter, started)
		log.WarnContext(ctx, "live_session_step_failed", "status", string(st.Status), "retry_in_ms", delay.Milliseconds(), "error", err)
	case res.Done:
		l.clearBackoff(dir)
		log.InfoContext(ctx, "live_session_step_done", "status_before", string(st.Status))
	default:
		// no error and bytes left: more was spooled while it drained (a kick
		// reruns it), or the spool holds something no batch can carry
		l.clearBackoff(dir)
		log.InfoContext(ctx, "live_session_step_partial", "status_before", string(st.Status), "status", string(res.Status), "acked", res.Acked)
	}
}

// stopHeld ends the live stream of a session that was held after it started
// streaming (stopped with session_publishing: manual, or handed off by /clear).
// Held means "not shared until I upload": nothing more is appended, no close
// publishes it, and the copy already on SageOx is deleted with the signed abort
// (queued if SageOx is unreachable). raw.jsonl stays on this machine.
func (l *LiveSessions) stopHeld(ctx context.Context, dir string, st livesession.State, started time.Time, log *slog.Logger) {
	res, err := livesession.Abort(ctx, l.config(l.endpoint), dir, livesession.AbortBudget)
	if err != nil {
		delay := l.fail(dir, 0, started)
		log.WarnContext(ctx, "live_session_held_stop_failed", "status", string(st.Status), "retry_in_ms", delay.Milliseconds(), "error", err)
		return
	}
	l.clearBackoff(dir)
	log.InfoContext(ctx, "live_session_held_stopped", "status_before", string(st.Status),
		"deleted", res.Deleted, "queued", res.Queued, "refused", res.Refused, "reason", res.Reason)
}

// attempt runs one open-and-drain, or a close once stop asked for one.
func (l *LiveSessions) attempt(ctx context.Context, dir string, st livesession.State) (livesession.DrainResult, error) {
	cfg := l.config(l.endpoint)
	if st.Status == livesession.StatusClosing || st.CloseRequested {
		return livesession.Close(ctx, cfg, dir, "", livesession.CloseBudget)
	}
	if st.Status == livesession.StatusPending {
		// bounded like a drain: a token refresh inside open is otherwise held only
		// by its per-request timeouts, and no step may outlive its budget
		octx, cancel := context.WithTimeout(ctx, liveDrainBudget)
		_, err := livesession.Open(octx, cfg, dir, livesession.OpenParams{
			SessionID: st.SessionID, SessionName: st.SessionName, RepoID: st.RepoID, Agent: st.Agent, StartedAt: st.StartedAt,
		})
		cancel()
		if err != nil {
			return livesession.DrainResult{}, err
		}
	}
	return livesession.Drain(ctx, cfg, dir, liveDrainBudget)
}

// fail records a failed attempt and returns how long until the next one may
// run. Our own backoff counts from when the attempt started; a server's
// Retry-After counts from its refusal, which arrived no later than now.
func (l *LiveSessions) fail(dir string, retryAfter time.Duration, started time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.backoff[dir]
	if b.delay == 0 {
		b.delay = liveBackoffBase
	} else {
		b.delay = min(b.delay*2, liveBackoffCap)
	}
	b.next = started.Add(b.delay)
	b.notBefore = time.Time{}
	if retryAfter > 0 {
		b.notBefore = now.Add(retryAfter)
	}
	l.backoff[dir] = b
	return max(b.next.Sub(now), b.notBefore.Sub(now), 0)
}

func (l *LiveSessions) clearBackoff(dir string) {
	l.mu.Lock()
	delete(l.backoff, dir)
	l.mu.Unlock()
}
