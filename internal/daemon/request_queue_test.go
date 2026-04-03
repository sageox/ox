package daemon

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- A. Serialization guarantees ---

// TestRequestQueue_SameAgentSerialized verifies that concurrent requests for
// the same agent_id are serialized: the second caller blocks until the first
// releases.
// Failure prevented: duplicate reads / offset corruption when parallel hook
// processes hit the daemon for the same session.
func TestRequestQueue_SameAgentSerialized(t *testing.T) {
	q := NewRequestQueue()
	agentID := "agent-1"

	// first goroutine acquires and holds the lock
	q.Acquire(agentID)

	started := make(chan struct{})
	finished := make(chan struct{})

	go func() {
		close(started)
		q.Acquire(agentID)
		close(finished)
		q.Release(agentID)
	}()

	<-started
	// give the second goroutine time to block on Acquire
	time.Sleep(20 * time.Millisecond)

	select {
	case <-finished:
		t.Fatal("second Acquire returned while first still held the lock")
	default:
		// expected: second goroutine is blocked
	}

	q.Release(agentID)

	select {
	case <-finished:
		// expected: second goroutine proceeds after release
	case <-time.After(2 * time.Second):
		t.Fatal("second Acquire did not return after first Release")
	}
}

// --- B. Independence across agents ---

// TestRequestQueue_DifferentAgentsParallel verifies that locks for different
// agent_ids do not block each other.
// Failure prevented: global serialization that would defeat the purpose of
// per-agent queuing and create unnecessary latency.
func TestRequestQueue_DifferentAgentsParallel(t *testing.T) {
	q := NewRequestQueue()

	q.Acquire("agent-a")
	defer q.Release("agent-a")

	done := make(chan struct{})
	go func() {
		q.Acquire("agent-b")
		q.Release("agent-b")
		close(done)
	}()

	select {
	case <-done:
		// expected: agent-b is independent of agent-a
	case <-time.After(2 * time.Second):
		t.Fatal("agent-b blocked by agent-a lock")
	}
}

// --- C. Cleanup lifecycle ---

// TestRequestQueue_CleanupRemovesIdleEntry verifies that Cleanup removes the
// map entry when no goroutines hold or wait on the lock.
// Failure prevented: unbounded memory growth from accumulated agent entries.
func TestRequestQueue_CleanupRemovesIdleEntry(t *testing.T) {
	q := NewRequestQueue()

	q.Acquire("agent-1")
	q.Release("agent-1")

	// after release with zero waiters, entry is auto-removed
	if got := q.Len(); got != 0 {
		t.Fatalf("expected 0 entries after release, got %d", got)
	}

	// explicit cleanup on nonexistent entry should be safe
	q.Cleanup("agent-1")
	if got := q.Len(); got != 0 {
		t.Fatalf("expected 0 entries after cleanup, got %d", got)
	}
}

// TestRequestQueue_CleanupPreservesActiveEntry verifies that Cleanup does not
// remove an entry that has active waiters.
// Failure prevented: removing a lock while a goroutine is blocked on it,
// which would cause the blocked goroutine to acquire a dangling mutex.
func TestRequestQueue_CleanupPreservesActiveEntry(t *testing.T) {
	q := NewRequestQueue()

	q.Acquire("agent-1")

	// cleanup while lock is held should be a no-op
	q.Cleanup("agent-1")
	if got := q.Len(); got != 1 {
		t.Fatalf("expected 1 entry (active lock), got %d", got)
	}

	q.Release("agent-1")
}

// TestRequestQueue_ReleaseUnknownAgent verifies that releasing an agent_id
// that was never acquired does not panic or corrupt state.
// Failure prevented: panic on Release for an agent whose entry was already
// cleaned up or never created.
func TestRequestQueue_ReleaseUnknownAgent(t *testing.T) {
	q := NewRequestQueue()
	// should be a no-op, not a panic
	q.Release("nonexistent")
}

// --- D. Auto-eviction on last release ---

// TestRequestQueue_AutoEvictOnLastRelease verifies that the map entry is
// removed when the last waiter releases, without needing explicit Cleanup.
// Failure prevented: callers forgetting to call Cleanup still get eviction.
func TestRequestQueue_AutoEvictOnLastRelease(t *testing.T) {
	q := NewRequestQueue()

	q.Acquire("agent-1")
	if got := q.Len(); got != 1 {
		t.Fatalf("expected 1 entry, got %d", got)
	}
	q.Release("agent-1")
	if got := q.Len(); got != 0 {
		t.Fatalf("expected 0 entries after last release, got %d", got)
	}
}

// --- E. Concurrency stress ---

// TestRequestQueue_StressConcurrent exercises the queue under heavy concurrent
// load: 100 goroutines across 10 agent_ids, verifying no data races, no
// deadlocks, and correct serialization (via per-agent counter check).
// Failure prevented: race conditions, deadlocks, and map corruption under
// concurrent access patterns that mirror real hook process behavior.
func TestRequestQueue_StressConcurrent(t *testing.T) {
	q := NewRequestQueue()

	const numAgents = 10
	const numWorkers = 100
	const opsPerWorker = 50

	// per-agent counter to verify serialization: if two goroutines for the
	// same agent overlap inside the critical section, the counter check fails
	var counters [numAgents]int64

	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < opsPerWorker; i++ {
				agentIdx := (workerID + i) % numAgents
				agentID := agentIDs[agentIdx]

				q.Acquire(agentID)

				// inside critical section: increment, yield, check
				cur := atomic.AddInt64(&counters[agentIdx], 1)
				if cur != 1 {
					t.Errorf("agent %s: concurrent access detected (counter=%d)", agentID, cur)
				}

				// simulate brief work
				atomic.AddInt64(&counters[agentIdx], -1)

				q.Release(agentID)
			}
		}(w)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(30 * time.Second):
		t.Fatal("stress test timed out (likely deadlock)")
	}

	// all entries should be evicted
	if got := q.Len(); got != 0 {
		t.Errorf("expected 0 entries after stress test, got %d", got)
	}
}

// agentIDs is a fixed set of agent identifiers for stress tests.
var agentIDs = [10]string{
	"agent-0", "agent-1", "agent-2", "agent-3", "agent-4",
	"agent-5", "agent-6", "agent-7", "agent-8", "agent-9",
}

// --- F. Sequential reuse ---

// TestRequestQueue_SequentialReuse verifies that the same agent_id can be
// acquired and released multiple times sequentially without issues.
// Failure prevented: stale state from a previous acquire/release cycle
// interfering with subsequent ones.
func TestRequestQueue_SequentialReuse(t *testing.T) {
	q := NewRequestQueue()

	for i := 0; i < 100; i++ {
		q.Acquire("agent-reuse")
		q.Release("agent-reuse")
	}

	if got := q.Len(); got != 0 {
		t.Fatalf("expected 0 entries after sequential reuse, got %d", got)
	}
}
