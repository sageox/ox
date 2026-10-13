package agenttask

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestEnqueue_ConcurrentFirstOpensKeepEveryTask: producers racing to create
// the queue all land their tasks. Failure prevented: an opener that lost the
// race to convert the new database to WAL took its SQLITE_BUSY for corruption
// and deleted the files the others were writing, losing their tasks or
// crashing the process on a WAL index whose file was replaced (#1339).
func TestEnqueue_ConcurrentFirstOpensKeepEveryTask(t *testing.T) {
	if testing.Short() {
		t.Skip("short: races dozens of SQLite opens")
	}
	const producers, rounds = 8, 20
	for r := range rounds {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, sageoxDir), 0o755); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make(chan error, producers)
		for i := range producers {
			wg.Go(func() {
				if _, err := Enqueue(root, &Task{Title: fmt.Sprintf("task %d", i), DedupKey: fmt.Sprintf("k%d", i)}); err != nil {
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

// TestNewStore_RebuildsOnlyDamage: the queue is ephemeral, so a damaged one is
// rebuilt, but a queue another connection has locked is reported and kept.
// Failure prevented: NewStore took the lock's SQLITE_BUSY for corruption and
// wiped a live queue (#1339), or no longer recovered from real damage.
func TestNewStore_RebuildsOnlyDamage(t *testing.T) {
	if testing.Short() {
		t.Skip("short: waits out the busy retry budget")
	}
	tests := []struct {
		name string
		// break leaves the queue unusable and returns how to undo that, if it can be undone
		breakQueue func(t *testing.T, root string) (undo func())
		wantErr    bool
		wantTasks  int // after undo, or after the rebuild
	}{
		{"held write lock is reported and kept", holdWriteLock, true, 1},
		{"damaged table is rebuilt", damageTasksTable, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, sageoxDir), 0o755); err != nil {
				t.Fatal(err)
			}
			if _, err := Enqueue(root, &Task{Title: "queued before"}); err != nil {
				t.Fatal(err)
			}
			undo := tt.breakQueue(t, root)
			store, err := NewStore(root)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewStore err = %v, want error %v", err, tt.wantErr)
			}
			if err != nil {
				undo()
				if store, err = NewStore(root); err != nil {
					t.Fatalf("NewStore after the lock: %v", err)
				}
			}
			defer store.Close()
			tasks, err := store.List(true)
			if err != nil {
				t.Fatal(err)
			}
			if len(tasks) != tt.wantTasks {
				t.Fatalf("%d tasks, want %d", len(tasks), tt.wantTasks)
			}
		})
	}
}

// holdWriteLock holds a write lock on the queue in rollback-journal mode:
// switching it to WAL then needs a lock SQLite refuses at once, the BUSY
// racing creators get.
func holdWriteLock(t *testing.T, root string) func() {
	t.Helper()
	holder, err := sql.Open("sqlite", QueuePath(root))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := holder.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"PRAGMA journal_mode=DELETE", "BEGIN IMMEDIATE"} {
		if _, err := conn.ExecContext(t.Context(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return func() {
		_, _ = conn.ExecContext(t.Context(), "ROLLBACK")
		_ = conn.Close()
		_ = holder.Close()
	}
}

// damageTasksTable overwrites the b-tree header of the tasks table's root
// page: the file still opens, and integrity_check reports the damage.
func damageTasksTable(t *testing.T, root string) func() {
	t.Helper()
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
	defer f.Close()
	if _, err := f.WriteAt([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, (page-1)*size); err != nil {
		t.Fatal(err)
	}
	return func() {}
}
