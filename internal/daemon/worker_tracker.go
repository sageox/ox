package daemon

import (
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"
)

var (
	// ErrWorkerCapacity is returned when the worker tracker is at maximum capacity.
	ErrWorkerCapacity = errors.New("worker capacity reached")

	// ErrWorkerExists is returned when registering a worker with a duplicate ID.
	ErrWorkerExists = errors.New("worker already registered")
)

// WorkerState tracks the lifecycle of a single subagent worker.
type WorkerState struct {
	WorkerID    string    `json:"worker_id"`
	AgentID     string    `json:"agent_id"`     // parent session that spawned this worker
	AdapterType string    `json:"adapter_type"` // which adapter runs this worker
	Status      string    `json:"status"`       // starting, running, completed, failed, canceled, timed_out
	StartedAt   time.Time `json:"started_at"`
	Task        string    `json:"task"`
	Model       string    `json:"model"`
}

// WorkerTracker manages the lifecycle of subagent workers.
// It tracks which workers are active, their status, and handles
// the mapping from worker_id to adapter type + agent_id.
type WorkerTracker struct {
	mu      sync.RWMutex
	workers map[string]*WorkerState // key: worker_id
	logger  *slog.Logger

	maxTotalWorkers int
}

// NewWorkerTracker creates a tracker with the given capacity limit.
func NewWorkerTracker(logger *slog.Logger, maxTotal int) *WorkerTracker {
	if logger == nil {
		logger = slog.Default()
	}
	if maxTotal <= 0 {
		maxTotal = 4
	}
	return &WorkerTracker{
		workers:         make(map[string]*WorkerState),
		logger:          logger,
		maxTotalWorkers: maxTotal,
	}
}

// Register adds a worker to the tracker. Returns ErrWorkerCapacity if at limit,
// ErrWorkerExists if the worker_id is already registered.
func (wt *WorkerTracker) Register(ws *WorkerState) error {
	wt.mu.Lock()
	defer wt.mu.Unlock()

	if _, exists := wt.workers[ws.WorkerID]; exists {
		return fmt.Errorf("%w: %s", ErrWorkerExists, ws.WorkerID)
	}

	active := wt.activeCountLocked()
	if active >= wt.maxTotalWorkers {
		return fmt.Errorf("%w: %d/%d active workers", ErrWorkerCapacity, active, wt.maxTotalWorkers)
	}

	wt.workers[ws.WorkerID] = ws
	wt.logger.Info("worker registered", "worker_id", ws.WorkerID, "agent_id", ws.AgentID, "adapter", ws.AdapterType)
	return nil
}

// UpdateStatus changes the status of a tracked worker.
// Terminal workers (completed, failed, canceled, timed_out) are automatically
// removed from the map to prevent unbounded memory growth.
// No-op if the worker is not found.
func (wt *WorkerTracker) UpdateStatus(workerID, status string) {
	wt.mu.Lock()
	defer wt.mu.Unlock()

	ws, exists := wt.workers[workerID]
	if !exists {
		return
	}

	old := ws.Status
	ws.Status = status
	wt.logger.Info("worker status changed", "worker_id", workerID, "from", old, "to", status)

	if isTerminalStatus(status) {
		delete(wt.workers, workerID)
		wt.logger.Info("worker reaped", "worker_id", workerID, "status", status)
	}
}

// Get returns the worker state for a given ID.
func (wt *WorkerTracker) Get(workerID string) (*WorkerState, bool) {
	wt.mu.RLock()
	defer wt.mu.RUnlock()

	ws, exists := wt.workers[workerID]
	if !exists {
		return nil, false
	}
	// return a copy to avoid races on the caller side
	cp := *ws
	return &cp, true
}

// Remove deletes a worker from the tracker.
func (wt *WorkerTracker) Remove(workerID string) {
	wt.mu.Lock()
	defer wt.mu.Unlock()

	if ws, exists := wt.workers[workerID]; exists {
		wt.logger.Info("worker removed", "worker_id", workerID, "agent_id", ws.AgentID, "status", ws.Status)
		delete(wt.workers, workerID)
	}
}

// ListByAgent returns all workers belonging to a given parent agent session.
func (wt *WorkerTracker) ListByAgent(agentID string) []*WorkerState {
	wt.mu.RLock()
	defer wt.mu.RUnlock()

	var result []*WorkerState
	for _, ws := range wt.workers {
		if ws.AgentID == agentID {
			cp := *ws
			result = append(result, &cp)
		}
	}
	return result
}

// ActiveCount returns the number of workers in an active status
// (starting or running).
func (wt *WorkerTracker) ActiveCount() int {
	wt.mu.RLock()
	defer wt.mu.RUnlock()
	return wt.activeCountLocked()
}

// activeCountLocked returns the count of active workers. Caller must hold mu.
func (wt *WorkerTracker) activeCountLocked() int {
	count := 0
	for _, ws := range wt.workers {
		if isActiveStatus(ws.Status) {
			count++
		}
	}
	return count
}

// ActiveCountByType returns the number of active workers for a specific adapter type.
func (wt *WorkerTracker) ActiveCountByType(adapterType string) int {
	wt.mu.RLock()
	defer wt.mu.RUnlock()

	count := 0
	for _, ws := range wt.workers {
		if ws.AdapterType == adapterType && isActiveStatus(ws.Status) {
			count++
		}
	}
	return count
}

// isActiveStatus returns true for statuses that represent a running worker.
func isActiveStatus(status string) bool {
	return status == "starting" || status == "running"
}

// isTerminalStatus returns true for statuses that represent a finished worker.
func isTerminalStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "canceled" || status == "timed_out"
}

// GenerateWorkerID creates a worker ID scoped to the parent session.
// Format: w-<agent_id_prefix>-<6char_random>
func GenerateWorkerID(agentID string) string {
	prefix := agentID
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}

	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	suffix := make([]byte, 6)
	for i := range suffix {
		suffix[i] = chars[rand.IntN(len(chars))]
	}

	return fmt.Sprintf("w-%s-%s", prefix, string(suffix))
}
