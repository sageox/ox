package uxfriction

import (
	"sync"
)

// RingBuffer is a thread-safe circular buffer for FrictionEvents.
// When full, new events overwrite the oldest. Deduplicates by Kind+Input
// within each flush window — if an identical event is already in the buffer,
// the new one is silently dropped.
type RingBuffer struct {
	mu       sync.Mutex
	events   []FrictionEvent
	capacity int
	head     int // next write position
	count    int // current number of stored events
	seen     map[string]bool
}

// NewRingBuffer creates a RingBuffer with the given capacity.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 1
	}
	return &RingBuffer{
		events:   make([]FrictionEvent, capacity),
		capacity: capacity,
		seen:     make(map[string]bool),
	}
}

// dedupeKey returns the deduplication key for an event.
func dedupeKey(e FrictionEvent) string {
	return string(e.Kind) + ":" + e.Input
}

// Add inserts an event into the buffer, overwriting the oldest if full.
// Duplicate events (same Kind+Input) are silently dropped.
func (rb *RingBuffer) Add(event FrictionEvent) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	key := dedupeKey(event)
	if rb.seen[key] {
		return
	}

	// if overwriting an existing event, remove its dedup key
	if rb.count == rb.capacity {
		oldKey := dedupeKey(rb.events[rb.head])
		delete(rb.seen, oldKey)
	}

	rb.events[rb.head] = event
	rb.head = (rb.head + 1) % rb.capacity

	if rb.count < rb.capacity {
		rb.count++
	}
	rb.seen[key] = true
}

// Drain returns all events in chronological order (oldest first) and clears the buffer.
func (rb *RingBuffer) Drain() []FrictionEvent {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if rb.count == 0 {
		return nil
	}

	result := make([]FrictionEvent, rb.count)

	// calculate start position (oldest event)
	start := 0
	if rb.count == rb.capacity {
		// buffer is full, oldest is at head position
		start = rb.head
	}

	// copy events in chronological order
	for i := 0; i < rb.count; i++ {
		idx := (start + i) % rb.capacity
		result[i] = rb.events[idx]
	}

	// clear the buffer
	rb.head = 0
	rb.count = 0
	rb.seen = make(map[string]bool)

	return result
}

// Count returns the current number of stored events.
func (rb *RingBuffer) Count() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.count
}
