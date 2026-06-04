package agenttask

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/gofrs/flock"
	"github.com/google/uuid"
)

// ErrTaskNotFound is returned when an operation targets an unknown task id.
var ErrTaskNotFound = errors.New("task not found")

const (
	sageoxDir = ".sageox"
	subDir    = "agent_tasks"
	fileName  = "agent_tasks.jsonl"

	// maxActive caps the number of non-terminal tasks retained. Producers are
	// deduped, so this is a safety valve against runaway enqueueing, not a
	// normal operating point.
	maxActive = 500

	// terminalRetention is how long completed/canceled tasks stay listable
	// before being pruned. Long enough for an agent to read back the outcome
	// of work it just finished, short enough to keep the ledger small.
	terminalRetention = 1 * time.Hour

	lockTimeout = 5 * time.Second
)

// Store manages task persistence using append-only JSONL with last-write-wins
// semantics, mirroring internal/agentinstance. Unlike instances, tasks are
// shared per repo (not per user): the queue exists so the next available agent
// — whoever that is — can pick up work.
type Store struct {
	projectRoot string
	tasksPath   string
	host        string
}

// NewStore initializes a task store for the given project root, creating the
// .sageox/agent_tasks/ directory if needed.
func NewStore(projectRoot string) (*Store, error) {
	if projectRoot == "" {
		return nil, fmt.Errorf("project root cannot be empty")
	}
	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve project root: %w", err)
	}
	storePath := filepath.Join(absRoot, sageoxDir, subDir)
	if err := os.MkdirAll(storePath, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create task directory: %w", err)
	}
	host, _ := os.Hostname()
	return &Store{
		projectRoot: absRoot,
		tasksPath:   filepath.Join(storePath, fileName),
		host:        host,
	}, nil
}

// Add appends a new task. The id, created-at, and status are filled in when
// empty. If the task carries a DedupKey and an active (non-terminal) task with
// that key already exists, Add is a no-op and reports added=false.
func (s *Store) Add(task *Task) (added bool, err error) {
	if task == nil {
		return false, fmt.Errorf("task cannot be nil")
	}
	if task.Title == "" {
		return false, fmt.Errorf("task title cannot be empty")
	}

	lock, unlock, err := s.acquireLock()
	if err != nil {
		return false, err
	}
	defer unlock()
	_ = lock

	tasks, err := s.reconcileLocked()
	if err != nil {
		return false, err
	}

	if task.DedupKey != "" {
		for _, existing := range tasks {
			if existing.DedupKey == task.DedupKey && !existing.IsTerminal() {
				return false, nil // active duplicate — skip
			}
		}
	}

	if task.ID == "" {
		task.ID = newTaskID()
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now()
	}
	if task.Status == "" {
		task.Status = StatusReady
	}

	if err := s.appendLocked(task); err != nil {
		return false, err
	}
	return true, nil
}

// List returns the current active tasks (after reconciling leases and pruning
// expired/old-terminal rows). When includeTerminal is false, completed and
// canceled tasks are omitted. Results are priority-sorted (lower first), then
// oldest-first within a priority.
func (s *Store) List(includeTerminal bool) ([]*Task, error) {
	lock, unlock, err := s.acquireLock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	_ = lock

	tasks, err := s.reconcileLocked()
	if err != nil {
		return nil, err
	}

	out := make([]*Task, 0, len(tasks))
	for _, t := range tasks {
		if !includeTerminal && t.IsTerminal() {
			continue
		}
		out = append(out, t)
	}
	sortTasks(out)
	return out, nil
}

// Ready returns ready tasks claimable by the given agent type, priority-sorted.
// An empty agentType only matches untargeted tasks.
func (s *Store) Ready(agentType string) ([]*Task, error) {
	all, err := s.List(false)
	if err != nil {
		return nil, err
	}
	out := make([]*Task, 0, len(all))
	for _, t := range all {
		if t.Status == StatusReady && t.matchesAgentType(agentType) {
			out = append(out, t)
		}
	}
	return out, nil
}

// Get returns a single task by id.
func (s *Store) Get(id string) (*Task, error) {
	lock, unlock, err := s.acquireLock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	_ = lock

	tasks, err := s.reconcileLocked()
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		if t.ID == id {
			return t, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrTaskNotFound, id)
}

// ClaimOptions parameterizes Claim.
type ClaimOptions struct {
	AgentID   string        // ox internal agent id of the claimer
	AgentType string        // claimer's agent type (for target matching)
	PID       int           // claimer's process id (host-local liveness)
	Lease     time.Duration // how long to hold the claim; defaults to DefaultLease
}

// Claim atomically pops the highest-priority ready task the claimer is eligible
// for, marks it in_progress, and stamps the lease. Returns (nil, nil) when no
// eligible task is available.
func (s *Store) Claim(opts ClaimOptions) (*Task, error) {
	if opts.AgentID == "" {
		return nil, fmt.Errorf("claim requires an agent id")
	}
	lease := opts.Lease
	if lease <= 0 {
		lease = DefaultLease
	}

	lock, unlock, err := s.acquireLock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	_ = lock

	tasks, err := s.reconcileLocked()
	if err != nil {
		return nil, err
	}

	// candidates: ready + eligible, priority-sorted
	candidates := make([]*Task, 0, len(tasks))
	for _, t := range tasks {
		if t.Status == StatusReady && t.matchesAgentType(opts.AgentType) {
			candidates = append(candidates, t)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	sortTasks(candidates)

	now := time.Now()
	claimed := candidates[0]
	claimed.Status = StatusInProgress
	claimed.ClaimedByAgentID = opts.AgentID
	claimed.ClaimedByPID = opts.PID
	claimed.ClaimedHost = s.host
	claimed.ClaimedAt = now
	claimed.LeaseExpiresAt = now.Add(lease)
	claimed.Attempts++

	if err := s.rewriteLocked(tasks); err != nil {
		return nil, err
	}
	return claimed, nil
}

// Complete marks a task completed with an optional result note. It is
// idempotent on already-terminal tasks.
func (s *Store) Complete(id, result string) error {
	return s.terminate(id, StatusCompleted, result)
}

// Cancel marks a task canceled with an optional reason. It is idempotent on
// already-terminal tasks.
func (s *Store) Cancel(id, reason string) error {
	return s.terminate(id, StatusCanceled, reason)
}

func (s *Store) terminate(id string, status Status, note string) error {
	lock, unlock, err := s.acquireLock()
	if err != nil {
		return err
	}
	defer unlock()
	_ = lock

	tasks, err := s.reconcileLocked()
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.ID != id {
			continue
		}
		if t.IsTerminal() {
			return nil // idempotent
		}
		t.Status = status
		t.CompletedAt = time.Now()
		if note != "" {
			t.Result = note
		}
		// clear lease fields — no longer claimed
		t.LeaseExpiresAt = time.Time{}
		return s.rewriteLocked(tasks)
	}
	return fmt.Errorf("%w: %s", ErrTaskNotFound, id)
}

// ExtendLease pushes out the lease deadline of an in_progress task, letting a
// long-running agent keep its claim. The task must still be in_progress.
func (s *Store) ExtendLease(id string, lease time.Duration) error {
	if lease <= 0 {
		lease = DefaultLease
	}
	lock, unlock, err := s.acquireLock()
	if err != nil {
		return err
	}
	defer unlock()
	_ = lock

	tasks, err := s.reconcileLocked()
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.ID != id {
			continue
		}
		if t.Status != StatusInProgress {
			return fmt.Errorf("task %s is not in progress (status=%s)", id, t.Status)
		}
		t.LeaseExpiresAt = time.Now().Add(lease)
		return s.rewriteLocked(tasks)
	}
	return fmt.Errorf("%w: %s", ErrTaskNotFound, id)
}

// reconcileLocked reads the current task set (last-write-wins by id), drops
// expired and old-terminal rows, reclaims in_progress tasks whose lease expired
// or whose claiming process died, and rewrites the file if anything changed.
// Caller must hold the file lock. Returns the live task set.
func (s *Store) reconcileLocked() ([]*Task, error) {
	tasks, err := s.readTasksLocked()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	changed := false
	kept := tasks[:0]
	for _, t := range tasks {
		// drop expired tasks outright
		if t.IsExpired() {
			changed = true
			continue
		}
		// drop terminal tasks past the retention window
		if t.IsTerminal() && !t.CompletedAt.IsZero() && now.Sub(t.CompletedAt) > terminalRetention {
			changed = true
			continue
		}
		// reclaim stuck in_progress tasks back to ready
		if t.Status == StatusInProgress && (t.LeaseExpired() || t.ClaimerDead(s.host)) {
			t.Status = StatusReady
			t.ClaimedByAgentID = ""
			t.ClaimedByPID = 0
			t.ClaimedHost = ""
			t.ClaimedAt = time.Time{}
			t.LeaseExpiresAt = time.Time{}
			changed = true
		}
		kept = append(kept, t)
	}

	if changed {
		if err := s.rewriteLocked(kept); err != nil {
			return nil, err
		}
	}
	return kept, nil
}

// readTasksLocked reads all task rows, collapsing duplicate ids to the last
// write. Caller must hold the file lock.
func (s *Store) readTasksLocked() ([]*Task, error) {
	f, err := os.Open(s.tasksPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []*Task{}, nil
		}
		return nil, fmt.Errorf("failed to open tasks file: %w", err)
	}
	defer f.Close()

	seen := make(map[string]*Task)
	var order []string

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var t Task
		if err := json.Unmarshal(line, &t); err != nil {
			continue // skip malformed rows rather than failing the whole read
		}
		if t.ID == "" {
			continue
		}
		if _, exists := seen[t.ID]; !exists {
			order = append(order, t.ID)
		}
		copied := t
		seen[t.ID] = &copied
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read tasks: %w", err)
	}

	out := make([]*Task, 0, len(order))
	for _, id := range order {
		out = append(out, seen[id])
	}
	return out, nil
}

// appendLocked appends a single task row. Caller must hold the file lock.
func (s *Store) appendLocked(task *Task) error {
	f, err := os.OpenFile(s.tasksPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open tasks file: %w", err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(task); err != nil {
		return fmt.Errorf("failed to encode task: %w", err)
	}
	return nil
}

// rewriteLocked atomically rewrites the tasks file. Caller must hold the file
// lock. Enforces the active-task cap by evicting the oldest non-terminal rows.
func (s *Store) rewriteLocked(tasks []*Task) error {
	tasks = enforceActiveCap(tasks)

	tempPath := s.tasksPath + ".tmp"
	f, err := os.Create(tempPath)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	enc := json.NewEncoder(f)
	for _, t := range tasks {
		if err := enc.Encode(t); err != nil {
			f.Close()
			os.Remove(tempPath)
			return fmt.Errorf("failed to encode task: %w", err)
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tempPath)
		return fmt.Errorf("failed to sync temp file: %w", err)
	}
	f.Close()
	if err := os.Rename(tempPath, s.tasksPath); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("failed to replace tasks file: %w", err)
	}
	return nil
}

// enforceActiveCap evicts the oldest non-terminal tasks when the active count
// exceeds maxActive. Terminal tasks are retained (they age out via retention).
func enforceActiveCap(tasks []*Task) []*Task {
	active := 0
	for _, t := range tasks {
		if !t.IsTerminal() {
			active++
		}
	}
	if active <= maxActive {
		return tasks
	}

	// evict oldest active tasks first
	byAge := make([]*Task, len(tasks))
	copy(byAge, tasks)
	sort.SliceStable(byAge, func(i, j int) bool {
		return byAge[i].CreatedAt.Before(byAge[j].CreatedAt)
	})
	toEvict := active - maxActive
	evict := make(map[string]bool)
	for _, t := range byAge {
		if toEvict == 0 {
			break
		}
		if !t.IsTerminal() {
			evict[t.ID] = true
			toEvict--
		}
	}

	kept := tasks[:0]
	for _, t := range tasks {
		if evict[t.ID] {
			continue
		}
		kept = append(kept, t)
	}
	return kept
}

// sortTasks orders tasks by priority (lower first), then oldest-first.
func sortTasks(tasks []*Task) {
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].Priority != tasks[j].Priority {
			return tasks[i].Priority < tasks[j].Priority
		}
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})
}

// acquireLock takes the file lock and returns an unlock function. The returned
// lock value is unused by callers but kept so the flock stays referenced for
// the critical section's lifetime.
func (s *Store) acquireLock() (*flock.Flock, func(), error) {
	lock := flock.New(s.tasksPath + ".lock")
	ctx, cancel := context.WithTimeout(context.Background(), lockTimeout)
	locked, err := lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("failed to acquire lock: %w", err)
	}
	if !locked {
		cancel()
		return nil, nil, fmt.Errorf("could not acquire file lock within timeout")
	}
	unlock := func() {
		_ = lock.Unlock()
		cancel()
	}
	return lock, unlock, nil
}

// newTaskID returns a time-sortable UUIDv7 string, falling back to v4.
func newTaskID() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.New().String()
}

// Enqueue is a convenience wrapper that opens a store for projectRoot and adds
// a single task. Intended for producers (the daemon, doctor) that schedule
// work without holding a long-lived store handle.
func Enqueue(projectRoot string, task *Task) (bool, error) {
	store, err := NewStore(projectRoot)
	if err != nil {
		return false, err
	}
	return store.Add(task)
}
