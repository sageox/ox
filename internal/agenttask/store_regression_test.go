package agenttask

import (
	"os"
	"sync"
	"testing"
	"time"
)

// deadPID is an implausibly high PID that proc.IsAlive reports as not running.
const deadPID = 2000000000

// insertClaimed stages an in_progress row with explicit lease/claim fields,
// bypassing Claim so a test can pin host/PID/lease independently.
func insertClaimed(t *testing.T, s *Store, id, host string, pid int, lease time.Time) {
	t.Helper()
	if _, err := s.db.Exec(
		`INSERT INTO tasks (id, title, status, priority, created_at,
			claimed_by_agent_id, claimed_by_pid, claimed_host, claimed_at, lease_expires_at, attempts)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		id, id, string(StatusInProgress), 5, tsToDB(time.Now()),
		"Oxghost", pid, host, tsToDB(time.Now()), tsToDB(lease), 1); err != nil {
		t.Fatalf("insertClaimed %s: %v", id, err)
	}
}

// TestReclaim_EmptyHostNotPIDChecked verifies a claim whose claimed_host is
// empty (the claimer's os.Hostname failed) is NOT reclaimed via a PID-liveness
// check — its PID is meaningless cross-host, so only lease expiry may reclaim it.
// Failure prevented: a dead PID number is checked against an unrelated local
// process on a different host, wrongly keeping (or freeing) the task.
func TestReclaim_EmptyHostNotPIDChecked(t *testing.T) {
	store := newTestStore(t)
	future := time.Now().Add(time.Hour)

	// empty host + dead PID + future lease: must survive reconcile as in_progress
	insertClaimed(t, store, "no-host", "", deadPID, future)
	// foreign host + dead PID + future lease: also must survive (cross-host)
	insertClaimed(t, store, "other-host", "some-other-machine", deadPID, future)

	if _, err := store.List(true); err != nil { // triggers reconcile
		t.Fatalf("List: %v", err)
	}
	for _, id := range []string{"no-host", "other-host"} {
		got, err := store.Get(id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if got.Status != StatusInProgress {
			t.Fatalf("%s was reclaimed via cross-host PID check; want in_progress, got %s", id, got.Status)
		}
	}
}

// TestReclaim_SameHostDeadPID verifies the same-host dead-claimer fast path still
// works: a claim on THIS host with a dead PID is reclaimed even though its lease
// is far in the future.
func TestReclaim_SameHostDeadPID(t *testing.T) {
	store := newTestStore(t)
	if store.host == "" {
		t.Skip("hostname unavailable; same-host reclaim is intentionally disabled")
	}
	insertClaimed(t, store, "mine", store.host, deadPID, time.Now().Add(time.Hour))

	ready, err := store.Ready("")
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if len(ready) != 1 || ready[0].ID != "mine" || ready[0].Status != StatusReady {
		t.Fatalf("expected same-host dead-claimer reclaimed to ready, got %+v", ready)
	}
}

// TestClaim_ConcurrentSingleWinner verifies two agents racing for one task yield
// it to exactly one — the guarded UPDATE makes a double-claim impossible.
func TestClaim_ConcurrentSingleWinner(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Add(&Task{Title: "contested", Priority: 1}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for _, agent := range []string{"OxaaaA", "OxbbbB"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			// live PID so reconcile never reclaims the winner's claim mid-race
			claimed, err := store.Claim(ClaimOptions{AgentID: id, PID: os.Getpid()})
			if err != nil {
				t.Errorf("Claim(%s): %v", id, err)
				return
			}
			if claimed != nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(agent)
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("expected exactly one claimer to win, got %d", wins)
	}
}

// TestAdd_ConcurrentDedupSingleRow verifies concurrent producers racing the same
// dedup key end with exactly one active row — the partial unique index is the
// race-proof backstop to the in-transaction pre-check.
func TestAdd_ConcurrentDedupSingleRow(t *testing.T) {
	store := newTestStore(t)

	var wg sync.WaitGroup
	var mu sync.Mutex
	added := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.Add(&Task{Title: "dup", DedupKey: "same-key"})
			if err != nil {
				t.Errorf("Add: %v", err)
				return
			}
			if ok {
				mu.Lock()
				added++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if added != 1 {
		t.Fatalf("expected exactly one producer to win the dedup key, got %d", added)
	}
	tasks, _ := store.List(false)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 active row for the dedup key, got %d", len(tasks))
	}
}
