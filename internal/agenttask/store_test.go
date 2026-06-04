package agenttask

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	// emulate an initialized repo
	if err := os.MkdirAll(filepath.Join(root, sageoxDir), 0o755); err != nil {
		t.Fatalf("mkdir .sageox: %v", err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func TestAddAndList(t *testing.T) {
	store := newTestStore(t)

	added, err := store.Add(&Task{Title: "first", Priority: 10})
	if err != nil || !added {
		t.Fatalf("Add: added=%v err=%v", added, err)
	}
	added, err = store.Add(&Task{Title: "second", Priority: 5})
	if err != nil || !added {
		t.Fatalf("Add: added=%v err=%v", added, err)
	}

	tasks, err := store.List(false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	// priority-sorted: lower first
	if tasks[0].Title != "second" {
		t.Fatalf("expected priority-sorted, got %q first", tasks[0].Title)
	}
	for _, task := range tasks {
		if task.ID == "" {
			t.Fatalf("task id not assigned")
		}
		if task.Status != StatusReady {
			t.Fatalf("expected ready status, got %s", task.Status)
		}
	}
}

func TestAddRequiresTitle(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Add(&Task{}); err == nil {
		t.Fatalf("expected error for empty title")
	}
	if _, err := store.Add(nil); err == nil {
		t.Fatalf("expected error for nil task")
	}
}

func TestDedupKey(t *testing.T) {
	store := newTestStore(t)

	added, err := store.Add(&Task{Title: "doctor", DedupKey: "doctor-agent"})
	if err != nil || !added {
		t.Fatalf("first add: added=%v err=%v", added, err)
	}
	added, err = store.Add(&Task{Title: "doctor again", DedupKey: "doctor-agent"})
	if err != nil {
		t.Fatalf("second add err: %v", err)
	}
	if added {
		t.Fatalf("expected dedup to skip active duplicate")
	}

	tasks, _ := store.List(false)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task after dedup, got %d", len(tasks))
	}

	// once the first is terminal, the dedup key frees up again
	if err := store.Complete(tasks[0].ID, "done"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	added, err = store.Add(&Task{Title: "doctor third", DedupKey: "doctor-agent"})
	if err != nil || !added {
		t.Fatalf("expected re-add after terminal: added=%v err=%v", added, err)
	}
}

func TestClaimPriorityOrder(t *testing.T) {
	store := newTestStore(t)
	_, _ = store.Add(&Task{Title: "low", Priority: 30})
	_, _ = store.Add(&Task{Title: "high", Priority: 1})
	_, _ = store.Add(&Task{Title: "mid", Priority: 10})

	claimed, err := store.Claim(ClaimOptions{AgentID: "Oxaaaa", PID: os.Getpid()})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if claimed == nil || claimed.Title != "high" {
		t.Fatalf("expected to claim highest priority, got %+v", claimed)
	}
	if claimed.Status != StatusInProgress {
		t.Fatalf("claimed task not in_progress: %s", claimed.Status)
	}
	if claimed.ClaimedByAgentID != "Oxaaaa" || claimed.Attempts != 1 {
		t.Fatalf("claim fields not set: %+v", claimed)
	}

	// a claimed task is no longer in the ready set
	ready, _ := store.Ready("")
	if len(ready) != 2 {
		t.Fatalf("expected 2 ready after one claim, got %d", len(ready))
	}
}

func TestClaimEmptyQueue(t *testing.T) {
	store := newTestStore(t)
	claimed, err := store.Claim(ClaimOptions{AgentID: "Oxaaaa"})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if claimed != nil {
		t.Fatalf("expected nil claim on empty queue, got %+v", claimed)
	}
}

func TestClaimTargetAgentFiltering(t *testing.T) {
	store := newTestStore(t)
	_, _ = store.Add(&Task{Title: "claude-only", TargetAgent: "claude", Priority: 1})
	_, _ = store.Add(&Task{Title: "any", Priority: 5})

	// a codex agent cannot claim the claude-targeted task; it gets "any"
	claimed, err := store.Claim(ClaimOptions{AgentID: "Oxcodx", AgentType: "codex"})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if claimed == nil || claimed.Title != "any" {
		t.Fatalf("expected codex to claim 'any', got %+v", claimed)
	}

	// a claude agent (alias claude-code) gets the targeted one
	claimed, err = store.Claim(ClaimOptions{AgentID: "Oxcld1", AgentType: "claude-code"})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if claimed == nil || claimed.Title != "claude-only" {
		t.Fatalf("expected claude to claim 'claude-only', got %+v", claimed)
	}
}

func TestCompleteAndCancel(t *testing.T) {
	store := newTestStore(t)
	added, _ := store.Add(&Task{Title: "a"})
	_ = added
	tasks, _ := store.List(false)
	id := tasks[0].ID

	if err := store.Complete(id, "finished"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusCompleted || got.Result != "finished" {
		t.Fatalf("unexpected terminal state: %+v", got)
	}
	if got.CompletedAt.IsZero() {
		t.Fatalf("CompletedAt not set")
	}
	// idempotent
	if err := store.Complete(id, "again"); err != nil {
		t.Fatalf("Complete idempotent: %v", err)
	}

	// not in active (non-terminal) listing
	active, _ := store.List(false)
	if len(active) != 0 {
		t.Fatalf("expected 0 active, got %d", len(active))
	}
	withTerminal, _ := store.List(true)
	if len(withTerminal) != 1 {
		t.Fatalf("expected 1 with terminal, got %d", len(withTerminal))
	}
}

func TestCancel(t *testing.T) {
	store := newTestStore(t)
	_, _ = store.Add(&Task{Title: "a"})
	tasks, _ := store.List(false)
	if err := store.Cancel(tasks[0].ID, "obsolete"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	got, _ := store.Get(tasks[0].ID)
	if got.Status != StatusCanceled || got.Result != "obsolete" {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestReclaimExpiredLease(t *testing.T) {
	store := newTestStore(t)
	_, _ = store.Add(&Task{Title: "leased"})

	// claim with an already-expired lease (negative duration -> default; so
	// claim then mutate the row directly via a tiny lease)
	claimed, err := store.Claim(ClaimOptions{AgentID: "Oxdead", PID: os.Getpid(), Lease: time.Millisecond})
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	time.Sleep(5 * time.Millisecond)

	// reconcile-on-read should flip it back to ready and bump attempts
	ready, err := store.Ready("")
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if len(ready) != 1 {
		t.Fatalf("expected reclaimed task to be ready, got %d", len(ready))
	}
	if ready[0].Status != StatusReady {
		t.Fatalf("expected ready, got %s", ready[0].Status)
	}
	if ready[0].ClaimedByAgentID != "" {
		t.Fatalf("claim fields not cleared on reclaim: %+v", ready[0])
	}
	if ready[0].Attempts != 1 {
		t.Fatalf("expected attempts=1 after one claim, got %d", ready[0].Attempts)
	}
}

func TestReclaimDeadClaimer(t *testing.T) {
	store := newTestStore(t)
	_, _ = store.Add(&Task{Title: "orphan"})

	// claim with a long lease but a PID that is not alive
	deadPID := 2000000000 // implausible PID; proc.IsAlive returns false
	claimed, err := store.Claim(ClaimOptions{AgentID: "Oxorph", PID: deadPID, Lease: time.Hour})
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	if claimed.ClaimedHost == "" {
		t.Fatalf("expected claimed host to be set")
	}

	// even though the lease is far in the future, the dead PID triggers reclaim
	ready, err := store.Ready("")
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if len(ready) != 1 || ready[0].Status != StatusReady {
		t.Fatalf("expected dead-claimer task reclaimed to ready, got %+v", ready)
	}
}

func TestExpiry(t *testing.T) {
	store := newTestStore(t)
	_, _ = store.Add(&Task{Title: "ephemeral", ExpiresAt: time.Now().Add(-time.Minute)})
	_, _ = store.Add(&Task{Title: "durable"})

	tasks, err := store.List(true)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Title != "durable" {
		t.Fatalf("expected expired task dropped, got %+v", tasks)
	}
}

func TestExtendLease(t *testing.T) {
	store := newTestStore(t)
	_, _ = store.Add(&Task{Title: "long"})
	claimed, _ := store.Claim(ClaimOptions{AgentID: "Oxlong", PID: os.Getpid(), Lease: time.Minute})

	before := claimed.LeaseExpiresAt
	time.Sleep(2 * time.Millisecond)
	if err := store.ExtendLease(claimed.ID, time.Hour); err != nil {
		t.Fatalf("ExtendLease: %v", err)
	}
	got, _ := store.Get(claimed.ID)
	if !got.LeaseExpiresAt.After(before) {
		t.Fatalf("lease not extended: before=%v after=%v", before, got.LeaseExpiresAt)
	}

	// extending a non-in_progress task errors
	_ = store.Complete(claimed.ID, "")
	if err := store.ExtendLease(claimed.ID, time.Hour); err == nil {
		t.Fatalf("expected error extending terminal task")
	}
}

func TestEnqueueConvenience(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, sageoxDir), 0o755)
	added, err := Enqueue(root, &Task{Title: "via convenience", Source: "test"})
	if err != nil || !added {
		t.Fatalf("Enqueue: added=%v err=%v", added, err)
	}
	store, _ := NewStore(root)
	tasks, _ := store.List(false)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
}

func TestGetNotFound(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Get("nope"); err == nil {
		t.Fatalf("expected ErrTaskNotFound")
	}
	if err := store.Complete("nope", ""); err == nil {
		t.Fatalf("expected ErrTaskNotFound on Complete")
	}
}

func TestTerminalRetentionPrune(t *testing.T) {
	store := newTestStore(t)
	_, _ = store.Add(&Task{Title: "old"})
	tasks, _ := store.List(false)
	id := tasks[0].ID
	_ = store.Complete(id, "done")

	// backdate CompletedAt beyond retention by rewriting the row directly
	all, _ := store.readTasksLocked()
	for _, tk := range all {
		if tk.ID == id {
			tk.CompletedAt = time.Now().Add(-2 * terminalRetention)
		}
	}
	if err := store.rewriteLocked(all); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	withTerminal, _ := store.List(true)
	if len(withTerminal) != 0 {
		t.Fatalf("expected old terminal task pruned, got %d", len(withTerminal))
	}
}
