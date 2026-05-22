package daemon

import (
	"context"
	"log/slog"
	"sync"
	"time"

	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// TimeBasedSource produces whisper entries on a periodic interval.
type TimeBasedSource interface {
	Name() string
	Interval() time.Duration
	Produce(ctx context.Context) []whisperstore.WhisperEntry
}

// WhisperScheduler runs periodic whisper sources and feeds entries to the registry.
type WhisperScheduler struct {
	registry *WhisperRegistry
	sources  []TimeBasedSource
	logger   *slog.Logger
	mu       sync.Mutex
}

// NewWhisperScheduler creates a scheduler that feeds periodic whisper entries to the registry.
func NewWhisperScheduler(registry *WhisperRegistry, logger *slog.Logger) *WhisperScheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &WhisperScheduler{
		registry: registry,
		logger:   logger,
	}
}

// RegisterSource adds a time-based source. Must be called before Start.
func (s *WhisperScheduler) RegisterSource(source TimeBasedSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sources = append(s.sources, source)
}

// Start runs each source on its own ticker goroutine. Blocks until ctx is canceled.
func (s *WhisperScheduler) Start(ctx context.Context, wg *sync.WaitGroup) {
	s.mu.Lock()
	sources := make([]TimeBasedSource, len(s.sources))
	copy(sources, s.sources)
	s.mu.Unlock()

	for _, src := range sources {
		wg.Add(1)
		go func(source TimeBasedSource) {
			defer wg.Done()
			s.runSource(ctx, source)
		}(src)
	}
}

func (s *WhisperScheduler) runSource(ctx context.Context, source TimeBasedSource) {
	ticker := time.NewTicker(source.Interval())
	defer ticker.Stop()

	s.logger.Debug("whisper source started", "name", source.Name(), "interval", source.Interval())

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			entries := source.Produce(ctx)
			if len(entries) == 0 {
				continue
			}
			if err := s.registry.Add("ledger", entries...); err != nil {
				s.logger.Warn("failed to add whisper entries", "source", source.Name(), "err", err)
			}
		}
	}
}

// RunPrune runs periodic cleanup on the whisper registry.
// Called as a daemon goroutine.
func (s *WhisperScheduler) RunPrune(ctx context.Context, wg *sync.WaitGroup, interval time.Duration) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.registry.Prune(24 * time.Hour)
				s.registry.EnforceMaxSize(10 * 1024 * 1024) // 10MB
			}
		}
	}()
}

// Default tuning for the idle-close janitor.
//
//   - DefaultIdleCloseThreshold: a team store with no read/write traffic
//     for this long is closed. Comfortably longer than the typical team
//     sync interval (~5 min) so a normally-active team never trips it.
//   - DefaultIdleCloseInterval: how often the janitor wakes up to check.
const (
	DefaultIdleCloseThreshold = 15 * time.Minute
	DefaultIdleCloseInterval  = 5 * time.Minute
)

// RunIdleCloseJanitor periodically closes team whisper stores that have
// been idle for longer than `threshold`. Each closed store releases ~3
// SQLite file descriptors (.db + .db-shm + .db-wal). The next sync cycle
// that touches the team will lazily reopen via AddTeamStore.
//
// Pass zero for either argument to use DefaultIdleCloseInterval /
// DefaultIdleCloseThreshold respectively. Set threshold < 0 to disable.
func (s *WhisperScheduler) RunIdleCloseJanitor(ctx context.Context, wg *sync.WaitGroup, interval, threshold time.Duration) {
	if threshold < 0 {
		return
	}
	if interval <= 0 {
		interval = DefaultIdleCloseInterval
	}
	if threshold == 0 {
		threshold = DefaultIdleCloseThreshold
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if n := s.registry.CloseIdleTeamStores(threshold); n > 0 {
					s.logger.Info("whisper idle-close janitor", "closed", n, "threshold", threshold)
				}
			}
		}
	}()
}
