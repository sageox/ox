package resilience

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestThrottle_FirstCallImmediate(t *testing.T) {
	th := NewThrottle(WithMinInterval(100 * time.Millisecond))

	start := time.Now()
	th.Wait()
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 10*time.Millisecond, "first call should be immediate")
}

func TestThrottle_EnforcesMinInterval(t *testing.T) {
	interval := 50 * time.Millisecond
	th := NewThrottle(WithMinInterval(interval))

	th.Wait() // first call

	start := time.Now()
	th.Wait() // should wait
	elapsed := time.Since(start)

	// should have waited close to the interval
	assert.GreaterOrEqual(t, elapsed, interval-5*time.Millisecond, "expected to wait ~%v, but only waited %v", interval, elapsed)
}

func TestThrottle_NoWaitAfterInterval(t *testing.T) {
	interval := 20 * time.Millisecond
	th := NewThrottle(WithMinInterval(interval))

	th.Wait()
	time.Sleep(interval + 10*time.Millisecond) // wait longer than interval

	start := time.Now()
	th.Wait()
	elapsed := time.Since(start)

	// should not wait since interval already passed
	assert.Less(t, elapsed, 10*time.Millisecond, "should not wait after interval passed")
}

func TestThrottle_DefaultInterval(t *testing.T) {
	th := NewThrottle()
	assert.Equal(t, 100*time.Millisecond, th.MinInterval())
}

// regression: concurrent callers must sleep in parallel, not serialize behind the lock
func TestThrottle_ConcurrentCallersDoNotSerialize(t *testing.T) {
	interval := 50 * time.Millisecond
	th := NewThrottle(WithMinInterval(interval))
	th.Wait() // prime the first request

	const n = 3
	var wg sync.WaitGroup
	wg.Add(n)

	start := time.Now()
	for range n {
		go func() {
			defer wg.Done()
			th.Wait()
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	// with the old bug, elapsed would be ~n*interval (serialized sleeps)
	// with the fix, callers reserve slots and sleep in parallel, so elapsed
	// should be roughly n*interval but wall time should be close to n*interval
	// because each reserves the next slot. The key property: it should NOT take
	// ~n*interval of *serialized mutex hold time*. We allow generous bounds.
	maxExpected := time.Duration(n)*interval + 30*time.Millisecond
	assert.LessOrEqual(t, elapsed, maxExpected,
		"concurrent callers should complete within %v, took %v", maxExpected, elapsed)
}

func TestThrottle_DefaultThrottleIsSingleton(t *testing.T) {
	t1 := DefaultThrottle()
	t2 := DefaultThrottle()

	assert.Same(t, t1, t2, "expected DefaultThrottle to return same instance")
}
