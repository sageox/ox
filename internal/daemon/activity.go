package daemon

import (
	"sync"
	"time"
)

// ActivityTracker stores recent timestamps for any named key.
// Thread-safe. Capped to N entries per key for memory efficiency.
// Also limits total number of keys to prevent unbounded memory growth.
// Useful for tracking heartbeats, syncs, and other events for sparkline display.
type ActivityTracker struct {
	mu       sync.RWMutex
	rings    map[string]*RingBuffer
	capacity int // entries per key
	maxKeys  int // max number of keys (0 = unlimited)
}

// RingBuffer stores N most recent timestamps in a circular buffer.
type RingBuffer struct {
	timestamps []time.Time
	head       int // next write position
	count      int // number of entries (up to capacity)
}

// defaultMaxKeys limits memory growth from unique keys.
// 1000 keys * 100 entries * 24 bytes = ~2.4MB max memory.
const defaultMaxKeys = 1000

// NewActivityTracker creates a new activity tracker with the given capacity per key.
func NewActivityTracker(capacity int) *ActivityTracker {
	return NewActivityTrackerWithMaxKeys(capacity, defaultMaxKeys)
}

// NewActivityTrackerWithMaxKeys creates a tracker with custom capacity and key limit.
func NewActivityTrackerWithMaxKeys(capacity, maxKeys int) *ActivityTracker {
	if capacity < 1 {
		capacity = 50 // default for sparkline display
	}
	if maxKeys < 0 {
		maxKeys = 0 // 0 means unlimited
	}
	return &ActivityTracker{
		rings:    make(map[string]*RingBuffer),
		capacity: capacity,
		maxKeys:  maxKeys,
	}
}

// newRingBuffer creates a new ring buffer with the given capacity.
func newRingBuffer(capacity int) *RingBuffer {
	return &RingBuffer{
		timestamps: make([]time.Time, capacity),
		head:       0,
		count:      0,
	}
}

// Add adds a timestamp to the ring buffer.
func (r *RingBuffer) Add(t time.Time) {
	r.timestamps[r.head] = t
	r.head = (r.head + 1) % len(r.timestamps)
	if r.count < len(r.timestamps) {
		r.count++
	}
}

// Slice returns all timestamps in chronological order (oldest first).
func (r *RingBuffer) Slice() []time.Time {
	if r.count == 0 {
		return nil
	}

	result := make([]time.Time, r.count)
	if r.count < len(r.timestamps) {
		// buffer not full yet, entries are 0..count-1
		copy(result, r.timestamps[:r.count])
	} else {
		// buffer is full, oldest entry is at head
		// copy from head to end, then from start to head
		n := copy(result, r.timestamps[r.head:])
		copy(result[n:], r.timestamps[:r.head])
	}
	return result
}

// Count returns the number of entries in the buffer.
func (r *RingBuffer) Count() int {
	return r.count
}

// Record adds a timestamp for the given key.
func (t *ActivityTracker) Record(key string) {
	t.RecordAt(key, time.Now())
}

// RecordAt adds a specific timestamp for the given key.
func (t *ActivityTracker) RecordAt(key string, ts time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	ring, ok := t.rings[key]
	if !ok {
		// enforce max keys limit by evicting oldest before adding new
		if t.maxKeys > 0 && len(t.rings) >= t.maxKeys {
			t.evictOldest()
		}
		ring = newRingBuffer(t.capacity)
		t.rings[key] = ring
	}
	ring.Add(ts)
}

// evictOldest removes the key with the oldest last activity.
// Must be called with mu held.
func (t *ActivityTracker) evictOldest() {
	var oldestKey string
	var oldestTime time.Time

	for key, ring := range t.rings {
		if ring.count == 0 {
			// empty ring - evict immediately
			delete(t.rings, key)
			return
		}
		// get last timestamp for this key
		lastIdx := (ring.head - 1 + len(ring.timestamps)) % len(ring.timestamps)
		last := ring.timestamps[lastIdx]

		if oldestKey == "" || last.Before(oldestTime) {
			oldestKey = key
			oldestTime = last
		}
	}

	if oldestKey != "" {
		delete(t.rings, oldestKey)
	}
}

// Get returns recent timestamps for the key (oldest first).
// Returns nil if key not found.
func (t *ActivityTracker) Get(key string) []time.Time {
	t.mu.RLock()
	defer t.mu.RUnlock()

	ring, ok := t.rings[key]
	if !ok {
		return nil
	}
	return ring.Slice()
}

// Has returns true if the key has been recorded at least once.
func (t *ActivityTracker) Has(key string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, ok := t.rings[key]
	return ok
}

// Count returns the number of recorded events for the key.
func (t *ActivityTracker) Count(key string) int {
	t.mu.RLock()
	defer t.mu.RUnlock()

	ring, ok := t.rings[key]
	if !ok {
		return 0
	}
	return ring.Count()
}

// Keys returns all tracked keys.
func (t *ActivityTracker) Keys() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	keys := make([]string, 0, len(t.rings))
	for k := range t.rings {
		keys = append(keys, k)
	}
	return keys
}

// Last returns the most recent timestamp for the key.
// Returns zero time if key not found or no entries.
func (t *ActivityTracker) Last(key string) time.Time {
	t.mu.RLock()
	defer t.mu.RUnlock()

	ring, ok := t.rings[key]
	if !ok || ring.count == 0 {
		return time.Time{}
	}

	// head points to next write position, so last entry is at head-1
	lastIdx := (ring.head - 1 + len(ring.timestamps)) % len(ring.timestamps)
	return ring.timestamps[lastIdx]
}

// Clear removes all entries for the given key.
func (t *ActivityTracker) Clear(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.rings, key)
}

// Reset removes all tracked data.
func (t *ActivityTracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rings = make(map[string]*RingBuffer)
}
