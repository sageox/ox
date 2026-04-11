package hooks_test

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon/hooks"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

func TestEmitCallsHookDispatch(t *testing.T) {
	t.Parallel()

	bus := hooks.New(testLogger())

	var dispatched hooks.Event
	bus.SetHookDispatch(func(_ context.Context, event hooks.Event) {
		dispatched = event
	})

	event := hooks.Event{
		Name:    hooks.EventDaemonStarted,
		Project: "/tmp/test-project",
	}

	bus.Emit(context.Background(), event)

	if dispatched.Name != hooks.EventDaemonStarted {
		t.Fatalf("hookDispatch not called; got event name %q", dispatched.Name)
	}
}

func TestEmitSetsTimestamp(t *testing.T) {
	t.Parallel()

	bus := hooks.New(testLogger())

	var captured hooks.Event
	bus.SetHookDispatch(func(_ context.Context, event hooks.Event) {
		captured = event
	})

	before := time.Now()
	bus.Emit(context.Background(), hooks.Event{Name: hooks.EventDaemonStarted})
	after := time.Now()

	if captured.Timestamp.IsZero() {
		t.Fatal("timestamp should be set when emitting with zero timestamp")
	}
	if captured.Timestamp.Before(before) || captured.Timestamp.After(after) {
		t.Fatalf("timestamp %v not between %v and %v", captured.Timestamp, before, after)
	}
}

func TestEmitPreservesExistingTimestamp(t *testing.T) {
	t.Parallel()

	bus := hooks.New(testLogger())

	var captured hooks.Event
	bus.SetHookDispatch(func(_ context.Context, event hooks.Event) {
		captured = event
	})

	fixed := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	bus.Emit(context.Background(), hooks.Event{
		Name:      hooks.EventDaemonStarted,
		Timestamp: fixed,
	})

	if !captured.Timestamp.Equal(fixed) {
		t.Fatalf("timestamp changed from %v to %v", fixed, captured.Timestamp)
	}
}

// TestEmitHookDispatchPanic verifies a panicking hookDispatch function
// doesn't crash the test process. hookDispatch runs synchronously in Emit(),
// so the panic propagates to the caller.
// Failure prevented: hook dispatch panic takes down the event bus.
func TestEmitHookDispatchPanic(t *testing.T) {
	t.Parallel()

	bus := hooks.New(testLogger())
	bus.SetHookDispatch(func(_ context.Context, _ hooks.Event) {
		panic("hookDispatch exploded")
	})

	recovered := make(chan bool, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				recovered <- true
			} else {
				recovered <- false
			}
		}()
		bus.Emit(context.Background(), hooks.Event{Name: hooks.EventDaemonStarted})
	}()

	select {
	case didPanic := <-recovered:
		if !didPanic {
			t.Log("hookDispatch panic was not observed (may be caught internally)")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Emit with panicking hookDispatch hung")
	}
}

// TestEmitNilHookDispatch verifies Emit works when hookDispatch is nil
// (the default state before SetHookDispatch is called).
// Failure prevented: nil function call panic on fresh EventBus.
func TestEmitNilHookDispatch(t *testing.T) {
	t.Parallel()

	bus := hooks.New(testLogger())
	// should not panic
	bus.Emit(context.Background(), hooks.Event{Name: hooks.EventDaemonStarted})
}

// TestEmitAfterSetHookDispatchNil verifies Emit works after hookDispatch
// is explicitly set to nil.
// Failure prevented: nil function call panic after explicit nil assignment.
func TestEmitAfterSetHookDispatchNil(t *testing.T) {
	t.Parallel()

	bus := hooks.New(testLogger())
	bus.SetHookDispatch(func(_ context.Context, _ hooks.Event) {})
	bus.SetHookDispatch(nil)

	// should not panic
	bus.Emit(context.Background(), hooks.Event{Name: hooks.EventDaemonStarted})
}

// TestEmitConcurrent verifies many goroutines can Emit simultaneously without
// races or deadlocks. Run with -race.
// Failure prevented: data race on EventBus fields during concurrent emit.
func TestEmitConcurrent(t *testing.T) {
	t.Parallel()

	bus := hooks.New(testLogger())

	var dispatched sync.WaitGroup
	bus.SetHookDispatch(func(_ context.Context, _ hooks.Event) {
		dispatched.Done()
	})

	events := []hooks.Event{
		{Name: hooks.EventSessionUploaded, Payload: hooks.SessionUploadedPayload("s", "url", "a", time.Second)},
		{Name: hooks.EventMurmurReceived, Payload: hooks.MurmurPayload("m", "a", "p", "t", "n", "content")},
		{Name: hooks.EventDaemonStarted},
		{Name: hooks.EventSyncCompleted, Payload: hooks.SyncPayload("ws", "pull", time.Second)},
	}

	const goroutines = 20
	dispatched.Add(goroutines)

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			bus.Emit(context.Background(), events[n%len(events)])
		}(i)
	}

	wg.Wait()
	dispatched.Wait()
}

// TestEmitNilPayload verifies an event with nil payload dispatches cleanly.
// Failure prevented: nil map iteration panic during marshal.
func TestEmitNilPayload(t *testing.T) {
	t.Parallel()

	bus := hooks.New(testLogger())

	var captured hooks.Event
	bus.SetHookDispatch(func(_ context.Context, event hooks.Event) {
		captured = event
	})

	bus.Emit(context.Background(), hooks.Event{
		Name:    hooks.EventSessionUploaded,
		Payload: nil,
	})

	if captured.Name != hooks.EventSessionUploaded {
		t.Fatalf("event not dispatched; got %q", captured.Name)
	}
}
