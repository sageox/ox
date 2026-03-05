package daemon

import (
	"sync"
	"time"
)

// FlushThrottle prevents thundering herd flushes by enforcing a minimum cooldown
// between flush operations. Without throttling, every Record() call above a batch
// threshold can spawn a new flush goroutine, creating unbounded HTTP POSTs.
//
// Usage: call TryFlush() before spawning a flush goroutine. It atomically claims
// a flush slot if the cooldown has elapsed. Call RecordFlush() from the flush
// function itself (e.g., ticker-triggered flushes that bypass TryFlush).
type FlushThrottle struct {
	mu        sync.Mutex
	lastFlush time.Time
	cooldown  time.Duration
}

// NewFlushThrottle creates a throttle with the given minimum interval between flushes.
func NewFlushThrottle(cooldown time.Duration) *FlushThrottle {
	return &FlushThrottle{cooldown: cooldown}
}

// TryFlush atomically checks if the cooldown has elapsed and claims the flush slot.
// Returns true if the caller should proceed with flushing.
func (ft *FlushThrottle) TryFlush() bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()

	if time.Since(ft.lastFlush) < ft.cooldown {
		return false
	}
	ft.lastFlush = time.Now()
	return true
}

// RecordFlush updates the last flush timestamp. Use this for flushes triggered
// by the background ticker (which bypass TryFlush).
func (ft *FlushThrottle) RecordFlush() {
	ft.mu.Lock()
	ft.lastFlush = time.Now()
	ft.mu.Unlock()
}
