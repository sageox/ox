package agenttask

import (
	"database/sql"
	"os"
	"path/filepath"
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

// TestAdd_RejectsNonReadyStatus verifies a producer cannot insert a task that is
// already in_progress/terminal. Such a row (no lease, empty/foreign host) would
// be neither claimable nor reclaimable — permanently wedged.
func TestAdd_RejectsNonReadyStatus(t *testing.T) {
	store := newTestStore(t)
	for _, st := range []Status{StatusInProgress, StatusCompleted, StatusCanceled} {
		if _, err := store.Add(&Task{Title: "x", Status: st}); err == nil {
			t.Fatalf("Add must reject status %q", st)
		}
	}
	if _, err := store.Add(&Task{Title: "ok", Status: StatusReady}); err != nil {
		t.Fatalf("Add(ready) should succeed: %v", err)
	}
}

// TestExtendLease_LostClaimErrors verifies that once a claim is reclaimed (lease
// lapsed), ExtendLease surfaces an error instead of silently succeeding — the
// caller must not keep working as if it still holds the task.
func TestExtendLease_LostClaimErrors(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Add(&Task{Title: "x"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	claimed, err := store.Claim(ClaimOptions{AgentID: "Oxa", PID: os.Getpid(), Lease: time.Hour})
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	// backdate the lease, then trigger reconcile so the task reverts to ready
	if _, err := store.db.Exec(`UPDATE tasks SET lease_expires_at=? WHERE id=?`,
		tsToDB(time.Now().Add(-time.Minute)), claimed.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	if _, err := store.Ready(""); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if err := store.ExtendLease(claimed.ID, time.Hour); err == nil {
		t.Fatal("ExtendLease on a reclaimed task must error, not silently succeed")
	}
}

// TestSchemaVersionMismatch_Recreates verifies the ephemeral store rebuilds from
// scratch when it opens a DB stamped with a different schema generation (there is
// no migration tool — schema changes bump SchemaVersion and the old DB is nuked).
func TestSchemaVersionMismatch_Recreates(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, sageoxDir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.Add(&Task{Title: "from old schema"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	store.Close()

	// stamp a foreign schema version onto the existing DB
	db, err := sql.Open("sqlite", QueuePath(root))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	db.Close()

	// reopening must detect the mismatch, nuke, and recreate an empty queue
	store2, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore after version bump: %v", err)
	}
	t.Cleanup(func() { _ = store2.Close() })
	tasks, err := store2.List(true)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected schema-mismatch recreate to empty the queue, got %d tasks", len(tasks))
	}
}

// TestAdd_RejectsSensitivePayloadKey verifies a producer cannot stash a
// credential-named field in payload, which is surfaced verbatim to the model on
// claim. The legitimate "session" key must still be accepted.
func TestAdd_RejectsSensitivePayloadKey(t *testing.T) {
	store := newTestStore(t)
	for i, k := range []string{"password", "api_key", "SECRET", "auth_token", "client_secret"} {
		_, err := store.Add(&Task{
			Title:    "x",
			DedupKey: k, // distinct key so each Add is independent
			Payload:  map[string]string{k: "value"},
		})
		if err == nil {
			t.Fatalf("[%d] expected Add to reject sensitive payload key %q", i, k)
		}
	}
	if _, err := store.Add(&Task{Title: "ok", Payload: map[string]string{"session": "2026-01-10T09-00-testuser-OxFIN"}}); err != nil {
		t.Fatalf("legitimate 'session' payload key must be allowed: %v", err)
	}
}
