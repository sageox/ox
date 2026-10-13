package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestOpen_ConcurrentFirstOpensKeepEveryWhisper: writers racing to create the
// store all keep their whispers. Failure prevented: an opener that lost the
// race to convert the new database to WAL took its SQLITE_BUSY for corruption
// and deleted the files the others were writing (#1339).
func TestOpen_ConcurrentFirstOpensKeepEveryWhisper(t *testing.T) {
	if testing.Short() {
		t.Skip("short: races dozens of SQLite opens")
	}
	const writers, rounds = 8, 20
	for r := range rounds {
		dbPath := filepath.Join(t.TempDir(), "whisper.db")
		var wg sync.WaitGroup
		errs := make(chan error, writers)
		for i := range writers {
			wg.Go(func() {
				s, err := Open(dbPath)
				if err != nil {
					errs <- err
					return
				}
				defer s.Close()
				if err := s.Add(makeEntry(fmt.Sprintf("w%d", i), "topic", ImportanceNormal, time.Now())); err != nil {
					errs <- err
				}
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("round %d: %v", r, err)
		}
		s, err := Open(dbPath)
		if err != nil {
			t.Fatalf("round %d: reopen: %v", r, err)
		}
		got, err := s.GetWhispers("reader", AttentionAll, nil)
		_ = s.Close()
		if err != nil {
			t.Fatalf("round %d: GetWhispers: %v", r, err)
		}
		if len(got) != writers {
			t.Fatalf("round %d: %d of %d whispers survived", r, len(got), writers)
		}
	}
}

// TestOpen_BusyDatabaseIsKeptNotRebuilt: a store another connection has
// locked is reported, never deleted. Failure prevented: Open took the lock's
// SQLITE_BUSY for corruption and wiped live whispers (#1339).
func TestOpen_BusyDatabaseIsKeptNotRebuilt(t *testing.T) {
	if testing.Short() {
		t.Skip("short: waits out the busy retry budget")
	}
	dbPath := filepath.Join(t.TempDir(), "whisper.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add(makeEntry("kept", "topic", ImportanceNormal, time.Now())); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	holder, err := sql.Open("sqlite", dbPath)
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
	if s, err := Open(dbPath); err == nil {
		_ = s.Close()
		t.Fatal("Open succeeded against a held write lock")
	}
	_, _ = conn.ExecContext(t.Context(), "ROLLBACK")
	_ = conn.Close()
	_ = holder.Close()

	s, err = Open(dbPath)
	if err != nil {
		t.Fatalf("Open after the lock: %v", err)
	}
	defer s.Close()
	got, err := s.GetWhispers("reader", AttentionAll, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("the locked store was rebuilt: %d whispers, want 1", len(got))
	}
}
