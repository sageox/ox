package daemon

import (
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// SyncMetrics tracks observability counters and timing for sync operations.
// Counters and timestamps use lock-free atomics; only pullDurations needs a mutex.
type SyncMetrics struct {
	// lock-free counters
	pullSuccessCount   atomic.Int64
	pullFailureCount   atomic.Int64
	conflictCount      atomic.Int64
	divergenceCount    atomic.Int64
	teamSyncCount      atomic.Int64
	teamSyncErrorCount atomic.Int64

	// lock-free timestamps (UnixNano)
	lastPullSuccess atomic.Int64
	lastPullFailure atomic.Int64
	lastConflict    atomic.Int64

	// timing (rolling window, last 100 samples) — needs mutex
	mu            sync.Mutex
	pullDurations []time.Duration
	maxSamples    int
}

// NewSyncMetrics creates a new SyncMetrics instance.
func NewSyncMetrics() *SyncMetrics {
	return &SyncMetrics{
		maxSamples:    100,
		pullDurations: make([]time.Duration, 0, 100),
	}
}

// RecordPullSuccess records a successful pull operation.
func (m *SyncMetrics) RecordPullSuccess(duration time.Duration) {
	m.pullSuccessCount.Add(1)
	m.lastPullSuccess.Store(time.Now().UnixNano())

	m.mu.Lock()
	m.pullDurations = appendDuration(m.pullDurations, duration, m.maxSamples)
	m.mu.Unlock()
}

// RecordPullFailure records a failed pull operation.
func (m *SyncMetrics) RecordPullFailure() {
	m.pullFailureCount.Add(1)
	m.lastPullFailure.Store(time.Now().UnixNano())
}

// RecordConflict records a merge conflict detection.
func (m *SyncMetrics) RecordConflict() {
	m.conflictCount.Add(1)
	m.lastConflict.Store(time.Now().UnixNano())
}

// RecordDivergence records a branch divergence detection.
func (m *SyncMetrics) RecordDivergence() {
	m.divergenceCount.Add(1)
}

// RecordTeamSync records a successful team context sync.
func (m *SyncMetrics) RecordTeamSync() {
	m.teamSyncCount.Add(1)
}

// RecordTeamSyncError records a failed team context sync.
func (m *SyncMetrics) RecordTeamSyncError() {
	m.teamSyncErrorCount.Add(1)
}

// SyncMetricsSnapshot is a point-in-time copy of sync metrics for reporting.
type SyncMetricsSnapshot struct {
	PullSuccessCount   int64         `json:"pull_success_count"`
	PullFailureCount   int64         `json:"pull_failure_count"`
	ConflictCount      int64         `json:"conflict_count"`
	DivergenceCount    int64         `json:"divergence_count"`
	TeamSyncCount      int64         `json:"team_sync_count"`
	TeamSyncErrorCount int64         `json:"team_sync_error_count"`
	LastPullSuccess    time.Time     `json:"last_pull_success,omitempty"`
	LastPullFailure    time.Time     `json:"last_pull_failure,omitempty"`
	LastConflict       time.Time     `json:"last_conflict,omitempty"`
	AvgPullDuration    time.Duration `json:"avg_pull_duration"`
	P95PullDuration    time.Duration `json:"p95_pull_duration"`
}

// Snapshot returns a point-in-time copy of metrics for reporting.
func (m *SyncMetrics) Snapshot() SyncMetricsSnapshot {
	// lock-free reads for counters and timestamps
	snap := SyncMetricsSnapshot{
		PullSuccessCount:   m.pullSuccessCount.Load(),
		PullFailureCount:   m.pullFailureCount.Load(),
		ConflictCount:      m.conflictCount.Load(),
		DivergenceCount:    m.divergenceCount.Load(),
		TeamSyncCount:      m.teamSyncCount.Load(),
		TeamSyncErrorCount: m.teamSyncErrorCount.Load(),
		LastPullSuccess:    timeFromNano(m.lastPullSuccess.Load()),
		LastPullFailure:    timeFromNano(m.lastPullFailure.Load()),
		LastConflict:       timeFromNano(m.lastConflict.Load()),
	}

	// mutex only for pullDurations slice
	m.mu.Lock()
	snap.AvgPullDuration = avgDuration(m.pullDurations)
	snap.P95PullDuration = p95Duration(m.pullDurations)
	m.mu.Unlock()

	return snap
}

// timeFromNano converts a UnixNano int64 to time.Time. Returns zero time for 0.
func timeFromNano(nano int64) time.Time {
	if nano == 0 {
		return time.Time{}
	}
	return time.Unix(0, nano)
}

// appendDuration appends a duration to a slice, maintaining max size.
func appendDuration(durations []time.Duration, d time.Duration, maxSamples int) []time.Duration {
	durations = append(durations, d)
	if len(durations) > maxSamples {
		durations = durations[len(durations)-maxSamples:]
	}
	return durations
}

// avgDuration calculates the average duration.
func avgDuration(durations []time.Duration) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	var total time.Duration
	for _, d := range durations {
		total += d
	}
	return total / time.Duration(len(durations))
}

// p95Duration calculates the 95th percentile duration.
func p95Duration(durations []time.Duration) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	// copy and sort
	sorted := make([]time.Duration, len(durations))
	copy(sorted, durations)
	slices.SortFunc(sorted, func(a, b time.Duration) int {
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
		return 0
	})
	// p95 index
	idx := int(float64(len(sorted)) * 0.95)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
