package daemon

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// DBMaintenanceResult captures what happened during a single DB maintenance cycle.
type DBMaintenanceResult struct {
	Name       string
	Pruned     int64 // entries/rows removed
	SizeBefore int64 // file size before (bytes), -1 if unknown
	SizeAfter  int64 // file size after (bytes), -1 if unknown
	Vacuumed   bool
	Healthy    bool // integrity check passed
	Healed     bool // corruption detected and auto-fixed
	Duration   time.Duration
	Error      error
}

// DBMaintainer is implemented by any SQLite database that needs periodic maintenance.
type DBMaintainer interface {
	// Name returns a short identifier (e.g., "whisper-ledger", "codedb").
	Name() string

	// Maintain runs pruning, vacuum, and integrity checks.
	Maintain(ctx context.Context) DBMaintenanceResult
}

// DBMaintenanceScheduler coordinates periodic maintenance across all registered databases.
// Each database's maintenance is independent — one failing doesn't block others.
type DBMaintenanceScheduler struct {
	maintainers []DBMaintainer
	logger      *slog.Logger
	mu          sync.RWMutex
}

// NewDBMaintenanceScheduler creates a new scheduler.
func NewDBMaintenanceScheduler(logger *slog.Logger) *DBMaintenanceScheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &DBMaintenanceScheduler{logger: logger}
}

// Register adds a maintainer. Must be called before Start.
func (s *DBMaintenanceScheduler) Register(m DBMaintainer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maintainers = append(s.maintainers, m)
}

// RunAll runs maintenance on all registered databases sequentially.
// Returns results for each. Errors in one DB don't affect others.
func (s *DBMaintenanceScheduler) RunAll(ctx context.Context) []DBMaintenanceResult {
	s.mu.RLock()
	maintainers := make([]DBMaintainer, len(s.maintainers))
	copy(maintainers, s.maintainers)
	s.mu.RUnlock()

	results := make([]DBMaintenanceResult, 0, len(maintainers))
	for _, m := range maintainers {
		if ctx.Err() != nil {
			break
		}
		result := m.Maintain(ctx)
		results = append(results, result)

		if result.Error != nil {
			s.logger.Warn("db maintenance failed", "db", result.Name, "error", result.Error, "duration", result.Duration)
		} else if result.Pruned > 0 || result.Vacuumed || result.Healed {
			s.logger.Info("db maintenance complete", "db", result.Name,
				"pruned", result.Pruned, "vacuumed", result.Vacuumed,
				"healed", result.Healed, "duration", result.Duration)
		} else {
			s.logger.Debug("db maintenance complete", "db", result.Name, "duration", result.Duration)
		}
	}
	return results
}

// RunOne runs maintenance on a single named database.
// Returns nil if the name is not found.
func (s *DBMaintenanceScheduler) RunOne(ctx context.Context, name string) *DBMaintenanceResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.maintainers {
		if m.Name() == name {
			result := m.Maintain(ctx)
			return &result
		}
	}
	return nil
}

// Names returns the names of all registered maintainers.
func (s *DBMaintenanceScheduler) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, len(s.maintainers))
	for i, m := range s.maintainers {
		names[i] = m.Name()
	}
	return names
}

// Start runs periodic maintenance on the given interval. Blocks until ctx is canceled.
func (s *DBMaintenanceScheduler) Start(ctx context.Context, wg *sync.WaitGroup, interval time.Duration) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		s.logger.Debug("db maintenance scheduler started", "interval", interval, "databases", s.Names())

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.RunAll(ctx)
			}
		}
	}()
}
