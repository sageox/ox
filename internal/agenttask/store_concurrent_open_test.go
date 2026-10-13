package agenttask

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
)

// TestEnqueue_ConcurrentFirstOpensKeepEveryTask: producers racing to create
// the queue all land their tasks. Failure prevented: an opener that lost the
// race to convert the new database to WAL took its SQLITE_BUSY for corruption
// and deleted the files the others were writing, losing their tasks or
// crashing the process on a WAL index whose file was replaced (#1339).
// Without NewStore's retry, that opener fails its Enqueue with the BUSY instead.
func TestEnqueue_ConcurrentFirstOpensKeepEveryTask(t *testing.T) {
	if testing.Short() {
		t.Skip("short: 20 rounds of racing opens")
	}
	const producers, rounds = 8, 20
	for r := range rounds {
		root := t.TempDir()
		var wg sync.WaitGroup
		errs := make(chan error, producers)
		for i := range producers {
			wg.Go(func() {
				if _, err := Enqueue(root, &Task{Title: fmt.Sprintf("task %d", i)}); err != nil {
					errs <- err
				}
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("round %d: Enqueue: %v", r, err)
		}
		store, err := NewStore(root)
		if err != nil {
			t.Fatalf("round %d: NewStore: %v", r, err)
		}
		tasks, err := store.List(true)
		_ = store.Close()
		if err != nil {
			t.Fatalf("round %d: List: %v", r, err)
		}
		if len(tasks) != producers {
			t.Fatalf("round %d: %d of %d tasks survived", r, len(tasks), producers)
		}
	}
}

// TestNewStore_BusyQueueIsKeptNotRebuilt: a queue another connection holds
// locked past the retry budget is reported, never deleted. Failure prevented:
// NewStore took the lock's SQLITE_BUSY for corruption and wiped a live queue
// (#1339).
func TestNewStore_BusyQueueIsKeptNotRebuilt(t *testing.T) {
	if testing.Short() {
		t.Skip("short: waits out the busy retry budget")
	}
	root := t.TempDir()
	if _, err := Enqueue(root, &Task{Title: "queued before"}); err != nil {
		t.Fatal(err)
	}
	holder, err := sql.Open("sqlite", QueuePath(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	conn, err := holder.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	// A write lock on a rollback-journal DB: switching it to WAL needs a lock
	// SQLite refuses at once, the BUSY racing creators get.
	for _, q := range []string{"PRAGMA journal_mode=DELETE", "BEGIN IMMEDIATE"} {
		if _, err := conn.ExecContext(t.Context(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if store, err := NewStore(root); err == nil {
		_ = store.Close()
		t.Fatal("NewStore succeeded against a held write lock")
	}
	if _, err := conn.ExecContext(t.Context(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore after the lock: %v", err)
	}
	defer store.Close()
	tasks, err := store.List(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("the locked queue was rebuilt: %d tasks, want 1", len(tasks))
	}
}

// TestNewStore_DamagedQueueIsRebuilt: the queue is ephemeral, so one that
// integrity_check reports damaged is rebuilt empty. Failure prevented: a
// damaged queue is returned as an error instead, failing every task command
// until someone deletes the file.
func TestNewStore_DamagedQueueIsRebuilt(t *testing.T) {
	root := t.TempDir()
	if _, err := Enqueue(root, &Task{Title: "queued before"}); err != nil {
		t.Fatal(err)
	}
	// Overwrite the b-tree header of the tasks table's root page: the file
	// still opens, and integrity_check reports the damage.
	db, err := sql.Open("sqlite", QueuePath(root))
	if err != nil {
		t.Fatal(err)
	}
	var page, size int64
	if err := db.QueryRow(`SELECT rootpage FROM sqlite_master WHERE name = 'tasks'`).Scan(&page); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA page_size`).Scan(&size); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	f, err := os.OpenFile(QueuePath(root), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteAt(bytes.Repeat([]byte{0xff}, 8), (page-1)*size)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore on a damaged queue: %v", err)
	}
	defer store.Close()
	tasks, err := store.List(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("%d tasks, want the rebuilt queue empty", len(tasks))
	}
}
