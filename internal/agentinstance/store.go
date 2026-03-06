package agentinstance

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/gofrs/flock"
)

// ErrInstanceNotFound is returned when an operation targets an agent ID that doesn't exist in the store.
var ErrInstanceNotFound = errors.New("instance not found")

// Design decision: JSONL storage format
// Rationale: Append-only enables concurrent writes; scan newest-first for O(recent) lookups;
// simpler than SQLite for this use case. Abstracted behind Store interface for future migration.
// See: docs/plan/drifting-exploring-quill.md for full analysis

// Design decision: 500 instance hard cap
// Rationale: Supports 120+ parallel agents with 4x headroom; prevents unbounded growth from bugs.

// Design decision: Project-local storage
// Rationale: Multi-project isolation; `.sageox/` matches existing config pattern; gitignored by default.

const (
	sageoxDir                = ".sageox"
	subDir                   = "agent_instances"
	fileName                 = "agent_instances.jsonl"
	maxActive                = 500        // supports 120+ parallel agents with 4x headroom
	compactionSizeThreshold  = 200 * 1024 // 200KB - balance disk I/O vs file bloat
	compactionCountThreshold = 500
	compactionExpiredRatio   = 0.5 // compact when >50% entries expired
	compactionPrunedRatio    = 0.1 // rewrite if >10% pruned on read
	lockTimeout              = 5 * time.Second

	// ExcessivePrimeThreshold is the number of prime calls per instance before warning.
	// Agents should typically call prime once at session start. Multiple calls suggest
	// context compaction issues or agent misconfiguration.
	ExcessivePrimeThreshold = 5
)

// Instance represents an agent instance with server-assigned identifiers.
type Instance struct {
	AgentID         string    `json:"agent_id"` // "Ox" + 4-char identifier (e.g., "Oxa7b3")
	ServerSessionID string    `json:"oxsid"`    // Full server-generated session ID (JSON: oxsid for compat)
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	// Coding agent metadata
	AgentType string `json:"agent_type,omitempty"` // claude-code, droid, cursor, windsurf
	AgentVer  string `json:"agent_ver,omitempty"`  // Agent version (e.g., "1.0.42")
	Model     string `json:"model,omitempty"`      // Model used (e.g., "claude-opus-4-5")
	// Process tracking — PPID is the parent agent process (e.g., Claude Code).
	// Captured at prime time via os.Getppid(). Used by "ox agent list" to
	// check liveness with kill(pid, 0) without needing the daemon.
	ParentPID int `json:"parent_pid,omitempty"`
	// Agent hierarchy — detected at prime time by reading SAGEOX_AGENT_ID env var.
	// If already set when a new agent primes, the existing value is the parent
	// (orchestrator inherits env vars to subagents).
	ParentAgentID string `json:"parent_agent_id,omitempty"`
	// Usage tracking
	PrimeCallCount int `json:"prime_call_count,omitempty"` // number of times prime was called
}

// IsExpired checks if the instance has expired
func (i *Instance) IsExpired() bool {
	return time.Now().After(i.ExpiresAt)
}

// IsProcessAlive checks if the parent agent process is still running.
// Uses kill(pid, 0) which checks existence without sending a signal.
// Returns false if no PID was recorded or the process is gone.
func (i *Instance) IsProcessAlive() bool {
	if i.ParentPID <= 0 {
		return false
	}
	proc, err := os.FindProcess(i.ParentPID)
	if err != nil {
		return false
	}
	// signal 0: test if process exists without actually signaling it
	return proc.Signal(syscall.Signal(0)) == nil
}

// IsPrimeExcessive returns true if prime has been called more than the threshold.
// This indicates potential context compaction issues or agent misconfiguration.
func (i *Instance) IsPrimeExcessive() bool {
	return i.PrimeCallCount > ExcessivePrimeThreshold
}

// Store manages instance persistence using JSONL format
//
// Design decision: Per-user instance directories
// Rationale: Multiple git users (or pair programmers) can work on the same repo
// without instance conflicts. Path: .sageox/agent_instances/<user-slug>/agent_instances.jsonl
type Store struct {
	projectRoot   string
	instancesPath string
	userSlug      string
	mu            sync.RWMutex
}

// NewStore initializes an instance store for the given project root.
// Uses "anonymous" as the user slug for backward compatibility.
// Prefer NewStoreForUser when git identity is available.
func NewStore(projectRoot string) (*Store, error) {
	return NewStoreForUser(projectRoot, "anonymous")
}

// NewStoreForUser initializes an instance store for a specific user.
// The userSlug should be generated from GitIdentity.Slug() for per-user isolation.
// Path: .sageox/agent_instances/<user-slug>/agent_instances.jsonl
func NewStoreForUser(projectRoot string, userSlug string) (*Store, error) {
	if projectRoot == "" {
		return nil, fmt.Errorf("project root cannot be empty")
	}
	if userSlug == "" {
		userSlug = "anonymous"
	}

	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve project root: %w", err)
	}

	// new path: .sageox/agent_instances/<user-slug>/
	storePath := filepath.Join(absRoot, sageoxDir, subDir, userSlug)
	if err := os.MkdirAll(storePath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create instance directory: %w", err)
	}

	store := &Store{
		projectRoot:   absRoot,
		instancesPath: filepath.Join(storePath, fileName),
		userSlug:      userSlug,
	}

	return store, nil
}

// Add appends a new instance to the JSONL file
func (s *Store) Add(inst *Instance) error {
	if inst == nil {
		return fmt.Errorf("instance cannot be nil")
	}
	if inst.AgentID == "" {
		return fmt.Errorf("agent_id cannot be empty")
	}
	if inst.ServerSessionID == "" {
		return fmt.Errorf("server_session_id cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	lock := flock.New(s.instancesPath + ".lock")
	ctx, cancel := getLockContext()
	defer cancel()

	locked, err := lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return fmt.Errorf("failed to acquire lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("could not acquire file lock within timeout")
	}
	defer lock.Unlock()

	f, err := os.OpenFile(s.instancesPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open instances file: %w", err)
	}
	defer f.Close()

	encoder := json.NewEncoder(f)
	if err := encoder.Encode(inst); err != nil {
		return fmt.Errorf("failed to encode instance: %w", err)
	}

	return nil
}

// Update modifies an existing instance in the store.
// Rewrites the file with the updated instance data.
// Returns error if instance not found.
func (s *Store) Update(inst *Instance) error {
	if inst == nil {
		return fmt.Errorf("instance cannot be nil")
	}
	if inst.AgentID == "" {
		return fmt.Errorf("agent_id cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	lock := flock.New(s.instancesPath + ".lock")
	ctx, cancel := getLockContext()
	defer cancel()

	locked, err := lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return fmt.Errorf("failed to acquire lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("could not acquire file lock within timeout")
	}
	defer lock.Unlock()

	instances, _, _, err := s.readInstancesNoLock()
	if err != nil {
		return err
	}

	// find and update the instance
	found := false
	for i, existing := range instances {
		if existing.AgentID == inst.AgentID {
			instances[i] = inst
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("instance not found for agent_id: %s", inst.AgentID)
	}

	return s.rewriteInstances(instances)
}

// IncrementPrimeCallCount increments the prime call count for an instance.
// Returns the updated instance and whether the prime count is now excessive.
func (s *Store) IncrementPrimeCallCount(agentID string) (*Instance, bool, error) {
	if agentID == "" {
		return nil, false, fmt.Errorf("agent_id cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	lock := flock.New(s.instancesPath + ".lock")
	ctx, cancel := getLockContext()
	defer cancel()

	locked, err := lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return nil, false, fmt.Errorf("failed to acquire lock: %w", err)
	}
	if !locked {
		return nil, false, fmt.Errorf("could not acquire file lock within timeout")
	}
	defer lock.Unlock()

	instances, _, _, err := s.readInstancesNoLock()
	if err != nil {
		return nil, false, err
	}

	// find and update the instance
	var updatedInst *Instance
	for i := len(instances) - 1; i >= 0; i-- {
		if instances[i].AgentID == agentID {
			instances[i].PrimeCallCount++
			updatedInst = instances[i]
			break
		}
	}

	if updatedInst == nil {
		return nil, false, fmt.Errorf("%w: %s", ErrInstanceNotFound, agentID)
	}

	if err := s.rewriteInstances(instances); err != nil {
		return nil, false, err
	}

	return updatedInst, updatedInst.IsPrimeExcessive(), nil
}

// Get retrieves an instance by agent ID, pruning expired instances during read.
//
// Design decision: Prune on read
// Rationale: Amortizes cleanup cost; ensures Get() never returns expired instances
// without extra syscall. Triggers async compaction if thresholds exceeded.
func (s *Store) Get(agentID string) (*Instance, error) {
	if agentID == "" {
		return nil, fmt.Errorf("agent_id cannot be empty")
	}

	instances, totalCount, expiredCount, err := s.readInstances()

	if err != nil {
		return nil, err
	}

	// trigger compaction if needed after read
	if s.shouldCompact(totalCount, expiredCount) {
		go s.Prune()
	}

	// scan backwards (newest first) for the matching agent_id
	for i := len(instances) - 1; i >= 0; i-- {
		if instances[i].AgentID == agentID {
			return instances[i], nil
		}
	}

	return nil, fmt.Errorf("instance not found for agent_id: %s", agentID)
}

// List returns all active (non-expired) instances
func (s *Store) List() ([]*Instance, error) {
	instances, totalCount, expiredCount, err := s.readInstances()

	if err != nil {
		return nil, err
	}

	// trigger compaction if needed
	if s.shouldCompact(totalCount, expiredCount) {
		go s.Prune()
	}

	return instances, nil
}

// Count returns the number of active instances
func (s *Store) Count() int {
	instances, _, _, err := s.readInstances()
	if err != nil {
		return 0
	}

	return len(instances)
}

// Prune removes expired instances and compacts the file if needed
func (s *Store) Prune() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	lock := flock.New(s.instancesPath + ".lock")
	ctx, cancel := getLockContext()
	defer cancel()

	locked, err := lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return 0, fmt.Errorf("failed to acquire lock: %w", err)
	}
	if !locked {
		return 0, fmt.Errorf("could not acquire file lock within timeout")
	}
	defer lock.Unlock()

	instances, _, expiredCount, err := s.readInstancesNoLock()
	if err != nil {
		return 0, err
	}

	if expiredCount == 0 && len(instances) <= maxActive {
		return 0, nil
	}

	// enforce hard cap on active instances (evict oldest first)
	if len(instances) > maxActive {
		slices.SortFunc(instances, func(a, b *Instance) int {
			return a.CreatedAt.Compare(b.CreatedAt)
		})
		evicted := len(instances) - maxActive
		instances = instances[evicted:]
		expiredCount += evicted
	}

	if err := s.rewriteInstances(instances); err != nil {
		return 0, fmt.Errorf("failed to rewrite instances: %w", err)
	}

	return expiredCount, nil
}

// readInstances reads all active instances from the JSONL file
func (s *Store) readInstances() ([]*Instance, int, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readInstancesNoLock()
}

// readInstancesNoLock reads instances without acquiring the mutex (caller must hold lock).
// Deduplicates by AgentID, keeping the latest entry (last-write-wins in append-only JSONL).
func (s *Store) readInstancesNoLock() ([]*Instance, int, int, error) {
	f, err := os.Open(s.instancesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []*Instance{}, 0, 0, nil
		}
		return nil, 0, 0, fmt.Errorf("failed to open instances file: %w", err)
	}
	defer f.Close()

	// last-write-wins: later lines in the JSONL override earlier ones for the same AgentID
	seen := make(map[string]*Instance)
	var order []string // preserve insertion order
	var totalCount int
	now := time.Now()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		totalCount++
		var inst Instance
		if err := json.Unmarshal(scanner.Bytes(), &inst); err != nil {
			continue
		}

		if now.After(inst.ExpiresAt) {
			continue
		}

		if _, exists := seen[inst.AgentID]; !exists {
			order = append(order, inst.AgentID)
		}
		seen[inst.AgentID] = &inst
	}

	if err := scanner.Err(); err != nil {
		return nil, 0, 0, fmt.Errorf("failed to read instances: %w", err)
	}

	instances := make([]*Instance, 0, len(order))
	for _, id := range order {
		instances = append(instances, seen[id])
	}

	expiredCount := totalCount - len(instances)
	return instances, totalCount, expiredCount, nil
}

// rewriteInstances atomically rewrites the instances file with the given instances
func (s *Store) rewriteInstances(instances []*Instance) error {
	tempPath := s.instancesPath + ".tmp"

	f, err := os.Create(tempPath)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	encoder := json.NewEncoder(f)
	for _, inst := range instances {
		if err := encoder.Encode(inst); err != nil {
			f.Close()
			os.Remove(tempPath)
			return fmt.Errorf("failed to encode instance: %w", err)
		}
	}

	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tempPath)
		return fmt.Errorf("failed to sync temp file: %w", err)
	}

	f.Close()

	if err := os.Rename(tempPath, s.instancesPath); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("failed to replace instances file: %w", err)
	}

	return nil
}

// shouldCompact determines if compaction should be triggered
func (s *Store) shouldCompact(totalCount, expiredCount int) bool {
	if expiredCount == 0 {
		return false
	}

	// check expired ratio
	if totalCount > 0 && float64(expiredCount)/float64(totalCount) > compactionExpiredRatio {
		return true
	}

	// check entry count threshold
	if totalCount > compactionCountThreshold {
		return true
	}

	// check file size threshold
	info, err := os.Stat(s.instancesPath)
	if err == nil && info.Size() > compactionSizeThreshold {
		return true
	}

	// check pruned ratio for immediate compaction
	if totalCount > 0 && float64(expiredCount)/float64(totalCount) > compactionPrunedRatio {
		return true
	}

	return false
}

// getLockContext returns a context with timeout for file locking
func getLockContext() (context.Context, func()) {
	return context.WithTimeout(context.Background(), lockTimeout)
}
