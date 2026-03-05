package daemon

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFlushThrottle_TryFlush_FirstCallSucceeds(t *testing.T) {
	ft := NewFlushThrottle(15 * time.Minute)
	assert.True(t, ft.TryFlush(), "first TryFlush should succeed")
}

func TestFlushThrottle_TryFlush_SecondCallWithinCooldownFails(t *testing.T) {
	ft := NewFlushThrottle(15 * time.Minute)
	assert.True(t, ft.TryFlush())
	assert.False(t, ft.TryFlush(), "second TryFlush within cooldown should fail")
}

func TestFlushThrottle_TryFlush_SucceedsAfterCooldown(t *testing.T) {
	ft := NewFlushThrottle(1 * time.Millisecond)
	assert.True(t, ft.TryFlush())

	time.Sleep(5 * time.Millisecond)
	assert.True(t, ft.TryFlush(), "TryFlush should succeed after cooldown elapses")
}

func TestFlushThrottle_RecordFlush_ResetsCooldown(t *testing.T) {
	ft := NewFlushThrottle(15 * time.Minute)
	ft.RecordFlush()
	assert.False(t, ft.TryFlush(), "TryFlush should fail after RecordFlush resets cooldown")
}

func TestFlushThrottle_ConcurrentTryFlush_OnlyOneWins(t *testing.T) {
	ft := NewFlushThrottle(15 * time.Minute)

	var wins atomic.Int32
	var wg sync.WaitGroup

	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ft.TryFlush() {
				wins.Add(1)
			}
		}()
	}

	wg.Wait()
	assert.Equal(t, int32(1), wins.Load(), "exactly one goroutine should win the flush slot")
}
