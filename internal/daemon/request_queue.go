package daemon

import (
	"sync"
	"sync/atomic"
)

// RequestQueue serializes IPC requests per agent_id.
// When multiple hook processes fire concurrently for the same session
// (e.g., parallel tool calls), the second request blocks until the first
// completes. Different agent_ids are never blocked by each other.
type RequestQueue struct {
	mu     sync.Mutex
	agents map[string]*agentLock
}

// agentLock tracks a per-agent mutex and active waiter count.
// The waiter count enables safe cleanup: when it reaches zero after
// Release, the entry can be removed from the map to prevent unbounded growth.
type agentLock struct {
	mu      sync.Mutex
	waiters int32 // active + waiting goroutines; updated atomically
}

// NewRequestQueue creates a new RequestQueue.
func NewRequestQueue() *RequestQueue {
	return &RequestQueue{
		agents: make(map[string]*agentLock),
	}
}

// Acquire obtains the per-agent lock for the given agentID.
// If another goroutine holds the lock for the same agentID, this blocks
// until it is released. Locks for different agentIDs do not interfere.
func (q *RequestQueue) Acquire(agentID string) {
	q.mu.Lock()
	al, ok := q.agents[agentID]
	if !ok {
		al = &agentLock{}
		q.agents[agentID] = al
	}
	atomic.AddInt32(&al.waiters, 1)
	q.mu.Unlock()

	al.mu.Lock()
}

// Release unlocks the per-agent lock for the given agentID.
// If no other goroutines are waiting, the map entry is removed to
// prevent unbounded memory growth.
func (q *RequestQueue) Release(agentID string) {
	q.mu.Lock()
	al, ok := q.agents[agentID]
	if !ok {
		q.mu.Unlock()
		return
	}

	remaining := atomic.AddInt32(&al.waiters, -1)
	if remaining == 0 {
		delete(q.agents, agentID)
	}
	q.mu.Unlock()

	al.mu.Unlock()
}

// Cleanup removes the map entry for an agentID when its session ends.
// Safe to call even if the agent has no entry.
// Does NOT unlock; only removes a stale entry that has zero waiters.
func (q *RequestQueue) Cleanup(agentID string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	al, ok := q.agents[agentID]
	if !ok {
		return
	}
	// only remove if nobody is actively holding or waiting
	if atomic.LoadInt32(&al.waiters) == 0 {
		delete(q.agents, agentID)
	}
}

// Len returns the number of agent entries currently in the queue.
// Useful for testing and diagnostics.
func (q *RequestQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.agents)
}
