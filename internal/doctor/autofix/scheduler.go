package autofix

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// DefaultInterval is the slow-ticker cadence the scheduler uses when
// the daemon doesn't override it. Half-hour matches the bead's "slow
// cadence" guidance — long enough that even a buggy check can't
// generate spam, short enough that drift gets repaired well within a
// typical work day.
const DefaultInterval = 30 * time.Minute

// Scheduler runs the registered auto-fix checks on a slow ticker.
// Designed to live as a goroutine inside the daemon: the daemon
// supplies the workspaces and the scheduler iterates them on each
// tick, applying any check whose throttle has elapsed.
//
// One instance per daemon. Concurrency-safe with itself; checks may
// see overlapping invocations across different workspaces but never
// against the same (check, workspace) pair within a single tick.
type Scheduler struct {
	registry  *Registry
	logger    *slog.Logger
	interval  time.Duration
	emit      func(CheckResult) // optional sink (e.g., issue tracker, slog)
	workspace func() []string   // returns the list of workspace paths to iterate

	mu      sync.Mutex
	running bool
}

// NewScheduler builds a scheduler. workspace is a callback the
// scheduler invokes each tick to learn the current set of repos —
// daemon's workspace registry is the obvious source of truth, and
// passing it as a callback (rather than a snapshot) means the
// scheduler always sees the freshest set without coordinating
// invalidation.
//
// emit is optional. When non-nil the scheduler calls it for every
// non-Clean result so the daemon can route into IssueTracker / slog.
// nil emit means "log only," useful in tests.
func NewScheduler(reg *Registry, logger *slog.Logger, workspace func() []string, emit func(CheckResult)) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		registry:  reg,
		logger:    logger,
		interval:  DefaultInterval,
		emit:      emit,
		workspace: workspace,
	}
}

// SetInterval overrides the default ticker interval. Useful for tests
// that want to fire a tick on demand without waiting 30 minutes.
func (s *Scheduler) SetInterval(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interval = d
}

// Run blocks until ctx is canceled, firing one round of checks every
// `interval`. Returns when ctx is done.
func (s *Scheduler) Run(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	interval := s.interval
	s.mu.Unlock()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	s.logger.Info("autofix scheduler started", "interval", interval)
	defer s.logger.Info("autofix scheduler stopped")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// RunOnce runs a single round of all eligible checks across all
// workspaces and returns the results. Synchronous; intended for tests
// and for `ox doctor --run-autofix-now` if we ever want a CLI hook.
//
// Like tick(), non-Clean results are routed through emit (when set)
// so callers get the same observable side-effects whether the
// scheduler is running on its ticker or invoked imperatively.
func (s *Scheduler) RunOnce(ctx context.Context) []CheckResult {
	results := s.tickCollect(ctx)
	if s.emit != nil {
		for _, r := range results {
			if r.Status == StatusClean {
				continue
			}
			s.emit(r)
		}
	}
	return results
}

func (s *Scheduler) tick(ctx context.Context) {
	results := s.tickCollect(ctx)
	for _, r := range results {
		if r.Status == StatusClean {
			continue
		}
		if s.emit != nil {
			s.emit(r)
		}
		s.logger.Info("autofix",
			"slug", r.Slug,
			"status", r.Status,
			"repo", r.Repo,
			"summary", r.Summary)
	}
}

func (s *Scheduler) tickCollect(ctx context.Context) []CheckResult {
	checks := s.registry.All()
	var paths []string
	if s.workspace != nil {
		paths = s.workspace()
	}
	if len(paths) == 0 {
		// global checks (no per-workspace iteration) — call once with empty path.
		paths = []string{""}
	}

	now := time.Now()
	results := make([]CheckResult, 0, len(checks)*len(paths))
	for _, c := range checks {
		if !c.shouldRun(now) {
			continue
		}
		for _, p := range paths {
			select {
			case <-ctx.Done():
				return results
			default:
			}
			r := c.Run(ctx, p)
			r.Slug = c.Slug // enforce — checks don't have to remember to set it
			results = append(results, r)
		}
	}
	return results
}
