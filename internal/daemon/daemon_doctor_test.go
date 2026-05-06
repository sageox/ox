package daemon

import (
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestDoctor_AsyncSingleFlight pins the contract that Doctor() returns
// immediately (BackgroundStarted=true) on the first call and returns
// AlreadyRunning=true on a concurrent second call without starting a
// second pass. Without this, two concurrent Doctor() invocations would
// race two ForceDetect walks across the same ledger and double-process
// the same sessions.
//
// Failure prevented: regression that re-introduces synchronous Doctor()
// or drops the single-flight guard, causing IPC timeouts on large
// ledgers and racing finalize work.
func TestDoctor_AsyncSingleFlight(t *testing.T) {
	// Use a zero-value Daemon: scheduler nil-guarded, autofixSched/
	// agentWorker/sessionWatcher already nil-guarded in runDoctorPass.
	// We just need the doctorRunning atomic and the wg, both zero-value safe.
	d := &Daemon{logger: slog.New(slog.NewTextHandler(testWriter{t}, nil))}
	svc := &daemonServiceImpl{d: d}

	// First call: claims the slot, kicks off a goroutine.
	resp1 := svc.Doctor()
	assert.True(t, resp1.BackgroundStarted, "first call must report BackgroundStarted")
	assert.False(t, resp1.AlreadyRunning, "first call must NOT be AlreadyRunning")

	// Race: while the first goroutine is running, fire many concurrent
	// follow-up calls. Each must return AlreadyRunning=true and NOT
	// start its own goroutine.
	const concurrent = 20
	var wg sync.WaitGroup
	results := make([]*DoctorResponse, concurrent)
	for i := 0; i < concurrent; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = svc.Doctor()
		}()
	}
	wg.Wait()

	// At least some of the concurrent calls must have hit the in-flight
	// guard. (We can't assert ALL of them because the first goroutine
	// may have completed before a particular follow-up landed — that's
	// fine: those return BackgroundStarted=true for a fresh pass.)
	var alreadyRunning, started int
	for _, r := range results {
		switch {
		case r.AlreadyRunning:
			alreadyRunning++
		case r.BackgroundStarted:
			started++
		default:
			t.Errorf("call returned neither AlreadyRunning nor BackgroundStarted: %+v", r)
		}
	}
	if alreadyRunning == 0 {
		t.Errorf("expected at least one of %d concurrent calls to see AlreadyRunning, got 0", concurrent)
	}

	// Wait for all spawned goroutines to drain before returning so the
	// test doesn't leak. wg.Wait() on Daemon's wg covers any goroutines
	// the daemon started internally.
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("doctor goroutines did not drain within 2s")
	}

	// After draining, doctorRunning must be cleared so a future call
	// can start a fresh pass — otherwise we'd permanently lock out
	// the user after one Doctor() call.
	assert.False(t, d.doctorRunning.Load(),
		"doctorRunning must be cleared after the goroutine exits")
}

// testWriter routes slog output to t.Log so test failures include the
// daemon logs without polluting normal stderr.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}
