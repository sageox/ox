package agentwork

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRateLimiter(t *testing.T) {
	tests := []struct {
		name      string
		maxTokens int
		window    time.Duration
		calls     int
		wantAllow int // how many should return true
	}{
		{
			name:      "all within limit",
			maxTokens: 5,
			window:    time.Hour,
			calls:     3,
			wantAllow: 3,
		},
		{
			name:      "exact limit",
			maxTokens: 3,
			window:    time.Hour,
			calls:     3,
			wantAllow: 3,
		},
		{
			name:      "exceeds limit",
			maxTokens: 2,
			window:    time.Hour,
			calls:     5,
			wantAllow: 2,
		},
		{
			name:      "single token",
			maxTokens: 1,
			window:    time.Hour,
			calls:     3,
			wantAllow: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rl := NewRateLimiter(tc.maxTokens, tc.window)

			allowed := 0
			for range tc.calls {
				if rl.Allow() {
					allowed++
				}
			}
			assert.Equal(t, tc.wantAllow, allowed)
		})
	}
}

func TestRateLimiter_ResetAfterWindow(t *testing.T) {
	// use a 10ms window so we can exhaust then wait for reset
	rl := NewRateLimiter(2, 10*time.Millisecond)

	// exhaust the bucket within the window
	assert.True(t, rl.Allow())
	assert.True(t, rl.Allow())
	assert.False(t, rl.Allow(), "should be exhausted")

	// wait for window to expire
	time.Sleep(20 * time.Millisecond)

	assert.True(t, rl.Allow(), "should allow after window reset")
	assert.Equal(t, 1, rl.Remaining())
}

func TestRateLimiter_Remaining(t *testing.T) {
	rl := NewRateLimiter(3, time.Hour)

	assert.Equal(t, 3, rl.Remaining())
	rl.Allow()
	assert.Equal(t, 2, rl.Remaining())
	rl.Allow()
	rl.Allow()
	assert.Equal(t, 0, rl.Remaining())
}
