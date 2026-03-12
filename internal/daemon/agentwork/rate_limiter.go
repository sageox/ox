package agentwork

import (
	"sync"
	"time"
)

// RateLimiter implements a simple token-bucket rate limiter that refills
// at the start of each window. It is safe for concurrent use.
type RateLimiter struct {
	mu        sync.Mutex
	maxTokens int
	remaining int
	window    time.Duration
	windowEnd time.Time
}

// NewRateLimiter creates a rate limiter that allows maxTokens calls per window.
func NewRateLimiter(maxTokens int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		maxTokens: maxTokens,
		remaining: maxTokens,
		window:    window,
		windowEnd: time.Now().Add(window),
	}
}

// Allow consumes one token and returns true, or returns false if the
// bucket is exhausted for the current window.
func (r *RateLimiter) Allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	if now.After(r.windowEnd) {
		// window expired; reset bucket
		r.remaining = r.maxTokens
		r.windowEnd = now.Add(r.window)
	}

	if r.remaining <= 0 {
		return false
	}

	r.remaining--
	return true
}

// Remaining returns how many tokens are left in the current window.
// The window resets automatically if it has expired.
func (r *RateLimiter) Remaining() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	if now.After(r.windowEnd) {
		r.remaining = r.maxTokens
		r.windowEnd = now.Add(r.window)
	}
	return r.remaining
}
