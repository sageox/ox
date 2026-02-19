package uxfriction

import (
	"fmt"
	"sync"
	"testing"
)

func TestNewRingBuffer(t *testing.T) {
	tests := []struct {
		name             string
		capacity         int
		expectedCapacity int
	}{
		{
			name:             "positive capacity",
			capacity:         10,
			expectedCapacity: 10,
		},
		{
			name:             "capacity of one",
			capacity:         1,
			expectedCapacity: 1,
		},
		{
			name:             "zero capacity defaults to one",
			capacity:         0,
			expectedCapacity: 1,
		},
		{
			name:             "negative capacity defaults to one",
			capacity:         -5,
			expectedCapacity: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rb := NewRingBuffer(tc.capacity)
			if rb == nil {
				t.Fatal("NewRingBuffer returned nil")
			}
			if rb.capacity != tc.expectedCapacity {
				t.Errorf("capacity = %d, want %d", rb.capacity, tc.expectedCapacity)
			}
			if len(rb.events) != tc.expectedCapacity {
				t.Errorf("events slice length = %d, want %d", len(rb.events), tc.expectedCapacity)
			}
			if rb.count != 0 {
				t.Errorf("initial count = %d, want 0", rb.count)
			}
			if rb.head != 0 {
				t.Errorf("initial head = %d, want 0", rb.head)
			}
			if rb.seen == nil {
				t.Error("seen map should be initialized")
			}
		})
	}
}

func TestRingBuffer_Add(t *testing.T) {
	tests := []struct {
		name          string
		capacity      int
		eventsToAdd   int
		expectedCount int
	}{
		{
			name:          "add single event",
			capacity:      5,
			eventsToAdd:   1,
			expectedCount: 1,
		},
		{
			name:          "add events up to capacity",
			capacity:      5,
			eventsToAdd:   5,
			expectedCount: 5,
		},
		{
			name:          "add events beyond capacity overwrites oldest",
			capacity:      3,
			eventsToAdd:   7,
			expectedCount: 3,
		},
		{
			name:          "capacity of one with multiple adds",
			capacity:      1,
			eventsToAdd:   5,
			expectedCount: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rb := NewRingBuffer(tc.capacity)

			for i := 0; i < tc.eventsToAdd; i++ {
				rb.Add(FrictionEvent{
					Kind:  FailureInvalidArg,
					Input: fmt.Sprintf("cmd%d", i),
				})
			}

			if rb.Count() != tc.expectedCount {
				t.Errorf("Count() = %d, want %d", rb.Count(), tc.expectedCount)
			}
		})
	}
}

func TestRingBuffer_Dedup(t *testing.T) {
	t.Run("duplicate events are dropped", func(t *testing.T) {
		rb := NewRingBuffer(10)
		rb.Add(FrictionEvent{Kind: FailureUnknownCommand, Input: "ox statu"})
		rb.Add(FrictionEvent{Kind: FailureUnknownCommand, Input: "ox statu"})
		rb.Add(FrictionEvent{Kind: FailureUnknownCommand, Input: "ox statu"})

		if rb.Count() != 1 {
			t.Errorf("Count() = %d, want 1 (duplicates should be dropped)", rb.Count())
		}
	})

	t.Run("different kind same input are distinct", func(t *testing.T) {
		rb := NewRingBuffer(10)
		rb.Add(FrictionEvent{Kind: FailureUnknownCommand, Input: "ox foo"})
		rb.Add(FrictionEvent{Kind: FailureUnknownFlag, Input: "ox foo"})

		if rb.Count() != 2 {
			t.Errorf("Count() = %d, want 2 (different kinds are distinct)", rb.Count())
		}
	})

	t.Run("same kind different input are distinct", func(t *testing.T) {
		rb := NewRingBuffer(10)
		rb.Add(FrictionEvent{Kind: FailureUnknownCommand, Input: "ox foo"})
		rb.Add(FrictionEvent{Kind: FailureUnknownCommand, Input: "ox bar"})

		if rb.Count() != 2 {
			t.Errorf("Count() = %d, want 2 (different inputs are distinct)", rb.Count())
		}
	})

	t.Run("dedup resets after drain", func(t *testing.T) {
		rb := NewRingBuffer(10)
		rb.Add(FrictionEvent{Kind: FailureUnknownCommand, Input: "ox statu"})

		if rb.Count() != 1 {
			t.Fatalf("Count() = %d, want 1", rb.Count())
		}

		rb.Drain()

		// same event should be accepted again after drain
		rb.Add(FrictionEvent{Kind: FailureUnknownCommand, Input: "ox statu"})
		if rb.Count() != 1 {
			t.Errorf("Count() after re-add = %d, want 1 (dedup should reset on drain)", rb.Count())
		}
	})

	t.Run("overwritten event key is freed", func(t *testing.T) {
		rb := NewRingBuffer(2)

		rb.Add(FrictionEvent{Kind: FailureUnknownCommand, Input: "cmd1"})
		rb.Add(FrictionEvent{Kind: FailureUnknownCommand, Input: "cmd2"})
		// buffer full: [cmd1, cmd2]

		// cmd3 overwrites cmd1, freeing "unknown-command:cmd1"
		rb.Add(FrictionEvent{Kind: FailureUnknownCommand, Input: "cmd3"})

		// cmd1 should be accepted again since it was evicted
		rb.Add(FrictionEvent{Kind: FailureUnknownCommand, Input: "cmd1"})

		events := rb.Drain()
		if len(events) != 2 {
			t.Fatalf("Drain() returned %d events, want 2", len(events))
		}
		if events[0].Input != "cmd3" {
			t.Errorf("events[0].Input = %q, want %q", events[0].Input, "cmd3")
		}
		if events[1].Input != "cmd1" {
			t.Errorf("events[1].Input = %q, want %q", events[1].Input, "cmd1")
		}
	})
}

func TestRingBuffer_Drain(t *testing.T) {
	tests := []struct {
		name           string
		capacity       int
		eventsToAdd    []string // use Input field to track order
		expectedInputs []string // expected order after drain
	}{
		{
			name:           "drain empty buffer",
			capacity:       5,
			eventsToAdd:    []string{},
			expectedInputs: nil,
		},
		{
			name:           "drain single event",
			capacity:       5,
			eventsToAdd:    []string{"cmd1"},
			expectedInputs: []string{"cmd1"},
		},
		{
			name:           "drain multiple events in order",
			capacity:       5,
			eventsToAdd:    []string{"cmd1", "cmd2", "cmd3"},
			expectedInputs: []string{"cmd1", "cmd2", "cmd3"},
		},
		{
			name:           "drain at capacity",
			capacity:       3,
			eventsToAdd:    []string{"cmd1", "cmd2", "cmd3"},
			expectedInputs: []string{"cmd1", "cmd2", "cmd3"},
		},
		{
			name:           "drain after overwrite preserves chronological order",
			capacity:       3,
			eventsToAdd:    []string{"cmd1", "cmd2", "cmd3", "cmd4", "cmd5"},
			expectedInputs: []string{"cmd3", "cmd4", "cmd5"}, // oldest (cmd1, cmd2) overwritten
		},
		{
			name:           "drain with capacity one after multiple adds",
			capacity:       1,
			eventsToAdd:    []string{"cmd1", "cmd2", "cmd3"},
			expectedInputs: []string{"cmd3"}, // only last remains
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rb := NewRingBuffer(tc.capacity)

			for _, input := range tc.eventsToAdd {
				rb.Add(FrictionEvent{Input: input})
			}

			result := rb.Drain()

			// check nil vs empty slice
			if tc.expectedInputs == nil {
				if result != nil {
					t.Errorf("Drain() = %v, want nil", result)
				}
				return
			}

			if len(result) != len(tc.expectedInputs) {
				t.Fatalf("Drain() returned %d events, want %d", len(result), len(tc.expectedInputs))
			}

			for i, expected := range tc.expectedInputs {
				if result[i].Input != expected {
					t.Errorf("result[%d].Input = %q, want %q", i, result[i].Input, expected)
				}
			}

			// verify buffer is cleared after drain
			if rb.Count() != 0 {
				t.Errorf("Count() after Drain() = %d, want 0", rb.Count())
			}
			if rb.head != 0 {
				t.Errorf("head after Drain() = %d, want 0", rb.head)
			}
		})
	}
}

func TestRingBuffer_Drain_ClearsBuffer(t *testing.T) {
	rb := NewRingBuffer(5)

	rb.Add(FrictionEvent{Input: "cmd1"})
	rb.Add(FrictionEvent{Input: "cmd2"})

	// first drain should return events
	first := rb.Drain()
	if len(first) != 2 {
		t.Fatalf("first Drain() returned %d events, want 2", len(first))
	}

	// second drain should return nil (empty)
	second := rb.Drain()
	if second != nil {
		t.Errorf("second Drain() = %v, want nil", second)
	}
}

func TestRingBuffer_Count(t *testing.T) {
	rb := NewRingBuffer(5)

	// empty buffer
	if rb.Count() != 0 {
		t.Errorf("Count() on empty buffer = %d, want 0", rb.Count())
	}

	// add events
	rb.Add(FrictionEvent{Input: "cmd1"})
	if rb.Count() != 1 {
		t.Errorf("Count() after 1 add = %d, want 1", rb.Count())
	}

	rb.Add(FrictionEvent{Input: "cmd2"})
	rb.Add(FrictionEvent{Input: "cmd3"})
	if rb.Count() != 3 {
		t.Errorf("Count() after 3 adds = %d, want 3", rb.Count())
	}

	// drain and verify count resets
	rb.Drain()
	if rb.Count() != 0 {
		t.Errorf("Count() after Drain() = %d, want 0", rb.Count())
	}
}

func TestRingBuffer_ConcurrentAdd(t *testing.T) {
	rb := NewRingBuffer(100)

	var wg sync.WaitGroup
	numGoroutines := 10
	eventsPerGoroutine := 50

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for j := 0; j < eventsPerGoroutine; j++ {
				rb.Add(FrictionEvent{
					Kind:  FailureInvalidArg,
					Input: fmt.Sprintf("g%d-e%d", goroutineID, j),
				})
			}
		}(i)
	}

	wg.Wait()

	totalUnique := numGoroutines * eventsPerGoroutine // 500 unique events
	expectedCount := rb.capacity                       // capped at 100
	if totalUnique < rb.capacity {
		expectedCount = totalUnique
	}

	if rb.Count() != expectedCount {
		t.Errorf("Count() after concurrent adds = %d, want %d", rb.Count(), expectedCount)
	}

	// verify drain works after concurrent adds
	events := rb.Drain()
	if len(events) != expectedCount {
		t.Errorf("Drain() returned %d events, want %d", len(events), expectedCount)
	}
}

func TestRingBuffer_ConcurrentAddAndDrain(t *testing.T) {
	rb := NewRingBuffer(50)

	var wg sync.WaitGroup
	numWriters := 5
	eventsPerWriter := 20

	// start writers with unique inputs per goroutine+iteration
	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()
			for j := 0; j < eventsPerWriter; j++ {
				rb.Add(FrictionEvent{Input: fmt.Sprintf("w%d-e%d", writerID, j)})
			}
		}(i)
	}

	// start readers that drain periodically
	drainResults := make(chan int, 10)
	wg.Add(1)
	go func() {
		defer wg.Done()
		totalDrained := 0
		for i := 0; i < 5; i++ {
			events := rb.Drain()
			totalDrained += len(events)
		}
		drainResults <- totalDrained
	}()

	wg.Wait()
	close(drainResults)

	// final drain to get any remaining events
	remaining := rb.Drain()

	// verify no panics occurred and buffer is in valid state
	if rb.Count() != 0 {
		t.Errorf("Count() after all operations = %d, want 0", rb.Count())
	}

	// verify we can still use the buffer after concurrent operations
	rb.Add(FrictionEvent{Input: "after"})
	if rb.Count() != 1 {
		t.Errorf("Count() after post-concurrent add = %d, want 1", rb.Count())
	}

	_ = remaining // used to verify final drain completed without error
}

func TestRingBuffer_HeadWrapAround(t *testing.T) {
	// test that head correctly wraps around the buffer
	rb := NewRingBuffer(3)

	// add 7 events: head should wrap around twice
	for i := 0; i < 7; i++ {
		rb.Add(FrictionEvent{Input: string(rune('a' + i))})
	}

	// after 7 adds with capacity 3:
	// positions: [e, f, g] (indices 0, 1, 2)
	// head should be at 7 % 3 = 1
	if rb.head != 1 {
		t.Errorf("head = %d, want 1 after 7 adds in capacity 3 buffer", rb.head)
	}

	// drain should return in chronological order: e, f, g
	events := rb.Drain()
	expected := []string{"e", "f", "g"}
	for i, exp := range expected {
		if events[i].Input != exp {
			t.Errorf("events[%d].Input = %q, want %q", i, events[i].Input, exp)
		}
	}
}
