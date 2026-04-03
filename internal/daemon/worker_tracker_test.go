package daemon

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- A. Registration and retrieval ---
// Verifies that a registered worker can be retrieved by ID.
// Failure prevented: workers lost after registration.

func TestWorkerTracker_RegisterAndGet(t *testing.T) {
	wt := NewWorkerTracker(testLogger(), 10)

	ws := &WorkerState{
		WorkerID:    "w-agent1-ab12",
		AgentID:     "agent-1",
		AdapterType: "claude-code",
		Status:      "starting",
		StartedAt:   time.Now(),
		Task:        "write tests",
		Model:       "claude-sonnet-4-6",
	}

	if err := wt.Register(ws); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	got, ok := wt.Get("w-agent1-ab12")
	if !ok {
		t.Fatal("expected to find registered worker")
	}
	if got.WorkerID != ws.WorkerID {
		t.Errorf("expected worker_id %q, got %q", ws.WorkerID, got.WorkerID)
	}
	if got.AgentID != ws.AgentID {
		t.Errorf("expected agent_id %q, got %q", ws.AgentID, got.AgentID)
	}
	if got.Task != ws.Task {
		t.Errorf("expected task %q, got %q", ws.Task, got.Task)
	}
}

// --- B. Capacity enforcement ---
// Verifies that registration fails when active worker count reaches the max.
// Failure prevented: unbounded worker spawning exhausts resources.

func TestWorkerTracker_RegisterAtCapacity(t *testing.T) {
	wt := NewWorkerTracker(testLogger(), 2)

	for i := 0; i < 2; i++ {
		err := wt.Register(&WorkerState{
			WorkerID:    fmt.Sprintf("w-agent-%d", i),
			AgentID:     "agent-1",
			AdapterType: "claude-code",
			Status:      "running",
			StartedAt:   time.Now(),
		})
		if err != nil {
			t.Fatalf("register worker %d failed: %v", i, err)
		}
	}

	err := wt.Register(&WorkerState{
		WorkerID:    "w-agent-overflow",
		AgentID:     "agent-1",
		AdapterType: "claude-code",
		Status:      "starting",
		StartedAt:   time.Now(),
	})
	if err == nil {
		t.Fatal("expected capacity error, got nil")
	}
	if !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("expected capacity error, got: %v", err)
	}
}

// --- C. Completed workers do not count toward capacity ---
// Verifies that terminal-status workers are excluded from active count.
// Failure prevented: completed workers blocking new spawns.

func TestWorkerTracker_CompletedWorkersDoNotCountTowardCapacity(t *testing.T) {
	wt := NewWorkerTracker(testLogger(), 2)

	// register 2 workers at capacity
	for i := 0; i < 2; i++ {
		err := wt.Register(&WorkerState{
			WorkerID:    fmt.Sprintf("w-agent-%d", i),
			AgentID:     "agent-1",
			AdapterType: "claude-code",
			Status:      "running",
			StartedAt:   time.Now(),
		})
		if err != nil {
			t.Fatalf("register failed: %v", err)
		}
	}

	// mark one as completed
	wt.UpdateStatus("w-agent-0", "completed")

	// should be able to register a new one
	err := wt.Register(&WorkerState{
		WorkerID:    "w-agent-new",
		AgentID:     "agent-1",
		AdapterType: "claude-code",
		Status:      "starting",
		StartedAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("expected registration to succeed after completion, got: %v", err)
	}
}

// --- D. Duplicate registration ---
// Verifies that registering the same worker_id twice returns an error.
// Failure prevented: accidental state overwrite from duplicate spawn.

func TestWorkerTracker_DuplicateRegistration(t *testing.T) {
	wt := NewWorkerTracker(testLogger(), 10)

	ws := &WorkerState{
		WorkerID:    "w-dup-ab12",
		AgentID:     "agent-1",
		AdapterType: "claude-code",
		Status:      "starting",
		StartedAt:   time.Now(),
	}
	if err := wt.Register(ws); err != nil {
		t.Fatal(err)
	}

	err := wt.Register(ws)
	if err == nil {
		t.Fatal("expected error on duplicate registration")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("expected 'already registered' error, got: %v", err)
	}
}

// --- E. UpdateStatus changes state ---
// Verifies that status transitions are reflected in Get.
// Failure prevented: stale status displayed to users.

func TestWorkerTracker_UpdateStatus(t *testing.T) {
	wt := NewWorkerTracker(testLogger(), 10)

	wt.Register(&WorkerState{
		WorkerID: "w-status-test",
		AgentID:  "agent-1",
		Status:   "starting",
	})

	wt.UpdateStatus("w-status-test", "running")

	got, ok := wt.Get("w-status-test")
	if !ok {
		t.Fatal("worker not found")
	}
	if got.Status != "running" {
		t.Errorf("expected status 'running', got %q", got.Status)
	}

	// update on nonexistent worker is a no-op (no panic)
	wt.UpdateStatus("w-nonexistent", "failed")
}

// --- F. Remove cleans up ---
// Verifies that removed workers are no longer retrievable.
// Failure prevented: stale worker state leaking memory.

func TestWorkerTracker_Remove(t *testing.T) {
	wt := NewWorkerTracker(testLogger(), 10)

	wt.Register(&WorkerState{
		WorkerID: "w-remove-me",
		AgentID:  "agent-1",
		Status:   "running",
	})

	wt.Remove("w-remove-me")

	_, ok := wt.Get("w-remove-me")
	if ok {
		t.Fatal("expected worker to be gone after removal")
	}

	if got := wt.ActiveCount(); got != 0 {
		t.Errorf("expected 0 active after removal, got %d", got)
	}

	// remove on nonexistent is a no-op
	wt.Remove("w-nonexistent")
}

// --- G. ListByAgent filters correctly ---
// Verifies that ListByAgent returns only workers for the specified parent.
// Failure prevented: workers from other sessions leaking into a session's view.

func TestWorkerTracker_ListByAgent(t *testing.T) {
	wt := NewWorkerTracker(testLogger(), 10)

	wt.Register(&WorkerState{WorkerID: "w-a-0001", AgentID: "agent-a", Status: "running"})
	wt.Register(&WorkerState{WorkerID: "w-a-0002", AgentID: "agent-a", Status: "starting"})
	wt.Register(&WorkerState{WorkerID: "w-b-0001", AgentID: "agent-b", Status: "running"})

	agentA := wt.ListByAgent("agent-a")
	if len(agentA) != 2 {
		t.Fatalf("expected 2 workers for agent-a, got %d", len(agentA))
	}

	agentB := wt.ListByAgent("agent-b")
	if len(agentB) != 1 {
		t.Fatalf("expected 1 worker for agent-b, got %d", len(agentB))
	}

	none := wt.ListByAgent("agent-nonexistent")
	if len(none) != 0 {
		t.Fatalf("expected 0 workers for unknown agent, got %d", len(none))
	}
}

// --- H. ActiveCount accuracy ---
// Verifies that ActiveCount only counts starting/running workers.
// Failure prevented: incorrect capacity decisions from wrong count.

func TestWorkerTracker_ActiveCount(t *testing.T) {
	wt := NewWorkerTracker(testLogger(), 10)

	wt.Register(&WorkerState{WorkerID: "w-1", AgentID: "a", Status: "starting"})
	wt.Register(&WorkerState{WorkerID: "w-2", AgentID: "a", Status: "running"})
	wt.Register(&WorkerState{WorkerID: "w-3", AgentID: "a", Status: "completed"})
	wt.Register(&WorkerState{WorkerID: "w-4", AgentID: "a", Status: "failed"})
	wt.Register(&WorkerState{WorkerID: "w-5", AgentID: "a", Status: "canceled"})

	if got := wt.ActiveCount(); got != 2 {
		t.Errorf("expected 2 active (starting+running), got %d", got)
	}
}

// --- I. ActiveCountByType ---
// Verifies per-adapter-type active counting.
// Failure prevented: wrong adapter getting throttled due to miscount.

func TestWorkerTracker_ActiveCountByType(t *testing.T) {
	wt := NewWorkerTracker(testLogger(), 10)

	wt.Register(&WorkerState{WorkerID: "w-cc-1", AgentID: "a", AdapterType: "claude-code", Status: "running"})
	wt.Register(&WorkerState{WorkerID: "w-cc-2", AgentID: "a", AdapterType: "claude-code", Status: "completed"})
	wt.Register(&WorkerState{WorkerID: "w-gm-1", AgentID: "a", AdapterType: "gemini", Status: "running"})

	if got := wt.ActiveCountByType("claude-code"); got != 1 {
		t.Errorf("expected 1 active claude-code, got %d", got)
	}
	if got := wt.ActiveCountByType("gemini"); got != 1 {
		t.Errorf("expected 1 active gemini, got %d", got)
	}
	if got := wt.ActiveCountByType("nonexistent"); got != 0 {
		t.Errorf("expected 0 active nonexistent, got %d", got)
	}
}

// --- J. Concurrent register/remove safety ---
// Verifies that concurrent operations do not cause data races or panics.
// Failure prevented: data race under concurrent daemon operations.

func TestWorkerTracker_ConcurrentSafety(t *testing.T) {
	wt := NewWorkerTracker(testLogger(), 100)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := fmt.Sprintf("w-conc-%04d", n)
			_ = wt.Register(&WorkerState{
				WorkerID:    id,
				AgentID:     fmt.Sprintf("agent-%d", n%5),
				AdapterType: "claude-code",
				Status:      "running",
			})
		}(i)
	}
	wg.Wait()

	// concurrent reads + removes
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			wt.ListByAgent(fmt.Sprintf("agent-%d", n%5))
			wt.ActiveCount()
			wt.Get(fmt.Sprintf("w-conc-%04d", n))
		}(i)
		go func(n int) {
			defer wg.Done()
			wt.UpdateStatus(fmt.Sprintf("w-conc-%04d", n), "completed")
			wt.Remove(fmt.Sprintf("w-conc-%04d", n))
		}(i)
	}
	wg.Wait()

	// all should be removed
	if got := wt.ActiveCount(); got != 0 {
		t.Errorf("expected 0 active after concurrent cleanup, got %d", got)
	}
}

// --- K. GenerateWorkerID format ---
// Verifies worker ID format and uniqueness properties.
// Failure prevented: malformed worker IDs breaking downstream parsing.

func TestGenerateWorkerID_Format(t *testing.T) {
	tests := []struct {
		name    string
		agentID string
		prefix  string // expected prefix after "w-"
	}{
		{name: "short agent id", agentID: "abc", prefix: "abc"},
		{name: "long agent id truncated", agentID: "r7f3a2-OxA1b2", prefix: "r7f3a2-O"},
		{name: "exactly 8 chars", agentID: "12345678", prefix: "12345678"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := GenerateWorkerID(tt.agentID)

			expectedPrefix := "w-" + tt.prefix + "-"
			if !strings.HasPrefix(id, expectedPrefix) {
				t.Errorf("expected prefix %q, got %q", expectedPrefix, id)
			}

			// suffix should be 4 chars
			suffix := id[len(expectedPrefix):]
			if len(suffix) != 4 {
				t.Errorf("expected 4-char suffix, got %q (%d chars)", suffix, len(suffix))
			}
		})
	}

	// uniqueness: generate 100 IDs and check for collisions
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := GenerateWorkerID("agent-test")
		if seen[id] {
			t.Errorf("collision detected: %s", id)
		}
		seen[id] = true
	}
}

// --- L. Get returns a copy ---
// Verifies that modifying the returned WorkerState does not affect the tracker.
// Failure prevented: callers accidentally mutating shared state.

func TestWorkerTracker_GetReturnsCopy(t *testing.T) {
	wt := NewWorkerTracker(testLogger(), 10)

	wt.Register(&WorkerState{
		WorkerID: "w-copy-test",
		AgentID:  "agent-1",
		Status:   "running",
		Task:     "original task",
	})

	got, _ := wt.Get("w-copy-test")
	got.Task = "mutated"

	original, _ := wt.Get("w-copy-test")
	if original.Task != "original task" {
		t.Errorf("Get returned a reference, not a copy: task was mutated to %q", original.Task)
	}
}

// --- M. Default capacity ---
// Verifies that zero or negative maxTotal defaults to 4.
// Failure prevented: tracker created with zero capacity rejecting all registrations.

func TestWorkerTracker_DefaultCapacity(t *testing.T) {
	wt := NewWorkerTracker(testLogger(), 0)

	for i := 0; i < 4; i++ {
		err := wt.Register(&WorkerState{
			WorkerID: fmt.Sprintf("w-def-%d", i),
			AgentID:  "agent-1",
			Status:   "running",
		})
		if err != nil {
			t.Fatalf("expected default capacity to allow 4, failed at %d: %v", i, err)
		}
	}

	err := wt.Register(&WorkerState{
		WorkerID: "w-def-overflow",
		AgentID:  "agent-1",
		Status:   "running",
	})
	if err == nil {
		t.Fatal("expected capacity error at default limit")
	}
}
