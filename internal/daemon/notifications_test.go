package daemon

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRecordChanges(t *testing.T) {
	t.Run("basic append", func(t *testing.T) {
		ns := NewNotificationStore(100)
		ns.RecordChanges([]string{"a.md", "b.md"}, "team-1", "Team One")

		if got := ns.EntryCount(); got != 2 {
			t.Fatalf("expected 2 entries, got %d", got)
		}
	})

	t.Run("empty files is no-op", func(t *testing.T) {
		ns := NewNotificationStore(100)
		ns.RecordChanges(nil, "team-1", "Team One")
		ns.RecordChanges([]string{}, "team-1", "Team One")

		if got := ns.EntryCount(); got != 0 {
			t.Fatalf("expected 0 entries, got %d", got)
		}
	})

	t.Run("dedup same file updates timestamp", func(t *testing.T) {
		ns := NewNotificationStore(100)
		ns.RecordChanges([]string{"a.md"}, "team-1", "Team One")

		entries, _ := getEntriesDirectly(t, ns)
		firstTime := entries[0].ChangedAt

		time.Sleep(2 * time.Millisecond)
		ns.RecordChanges([]string{"a.md"}, "team-1", "Team One")

		if got := ns.EntryCount(); got != 1 {
			t.Fatalf("expected 1 entry after dedup, got %d", got)
		}

		entries, _ = getEntriesDirectly(t, ns)
		if !entries[0].ChangedAt.After(firstTime) {
			t.Fatal("expected ChangedAt to advance after dedup update")
		}
	})

	t.Run("same file different teams are separate entries", func(t *testing.T) {
		ns := NewNotificationStore(100)
		ns.RecordChanges([]string{"a.md"}, "team-1", "Team One")
		ns.RecordChanges([]string{"a.md"}, "team-2", "Team Two")

		if got := ns.EntryCount(); got != 2 {
			t.Fatalf("expected 2 entries for different teams, got %d", got)
		}
	})

	t.Run("duplicate files in single call are deduped", func(t *testing.T) {
		ns := NewNotificationStore(100)
		ns.RecordChanges([]string{"a.md", "a.md", "a.md"}, "team-1", "T")

		if got := ns.EntryCount(); got != 1 {
			t.Fatalf("expected 1 entry after dedup of duplicates, got %d", got)
		}
	})
}

func TestRingBufferEviction(t *testing.T) {
	t.Run("evicts oldest entries", func(t *testing.T) {
		ns := NewNotificationStore(3)

		ns.RecordChanges([]string{"a.md"}, "team-1", "T")
		time.Sleep(time.Millisecond)
		ns.RecordChanges([]string{"b.md"}, "team-1", "T")
		time.Sleep(time.Millisecond)
		ns.RecordChanges([]string{"c.md"}, "team-1", "T")
		time.Sleep(time.Millisecond)
		ns.RecordChanges([]string{"d.md"}, "team-1", "T")

		if got := ns.EntryCount(); got != 3 {
			t.Fatalf("expected 3 entries at cap, got %d", got)
		}

		entries, _ := getEntriesDirectly(t, ns)
		for _, e := range entries {
			if e.Path == "a.md" {
				t.Fatal("expected a.md to be evicted")
			}
		}
	})

	t.Run("maxEntries 1", func(t *testing.T) {
		ns := NewNotificationStore(1)

		ns.RecordChanges([]string{"a.md"}, "team-1", "T")
		time.Sleep(time.Millisecond)
		ns.RecordChanges([]string{"b.md"}, "team-1", "T")

		if got := ns.EntryCount(); got != 1 {
			t.Fatalf("expected 1 entry at cap, got %d", got)
		}

		entries, _ := getEntriesDirectly(t, ns)
		if entries[0].Path != "b.md" {
			t.Fatalf("expected b.md to survive, got %s", entries[0].Path)
		}
	})

	t.Run("dedup then eviction ordering", func(t *testing.T) {
		ns := NewNotificationStore(3)

		// fill: a(T=1), b(T=2), c(T=3)
		ns.RecordChanges([]string{"a.md"}, "team-1", "T")
		time.Sleep(time.Millisecond)
		ns.RecordChanges([]string{"b.md"}, "team-1", "T")
		time.Sleep(time.Millisecond)
		ns.RecordChanges([]string{"c.md"}, "team-1", "T")
		time.Sleep(time.Millisecond)

		// dedup a → moves to end: b(T=2), c(T=3), a(T=4)
		ns.RecordChanges([]string{"a.md"}, "team-1", "T")
		time.Sleep(time.Millisecond)

		// add d → evicts oldest (b): c(T=3), a(T=4), d(T=5)
		ns.RecordChanges([]string{"d.md"}, "team-1", "T")

		if got := ns.EntryCount(); got != 3 {
			t.Fatalf("expected 3 entries, got %d", got)
		}

		entries, _ := getEntriesDirectly(t, ns)
		paths := make([]string, len(entries))
		for i, e := range entries {
			paths[i] = e.Path
		}

		// b.md should be evicted (it was oldest after a.md was deduped)
		for _, p := range paths {
			if p == "b.md" {
				t.Fatalf("expected b.md to be evicted, got paths: %v", paths)
			}
		}

		// a.md, c.md, d.md should survive
		found := map[string]bool{}
		for _, p := range paths {
			found[p] = true
		}
		for _, expected := range []string{"a.md", "c.md", "d.md"} {
			if !found[expected] {
				t.Fatalf("expected %s to survive, got paths: %v", expected, paths)
			}
		}
	})
}

func TestGetNotifications(t *testing.T) {
	t.Run("auto-registers unknown agent with empty result", func(t *testing.T) {
		ns := NewNotificationStore(100)
		ns.RecordChanges([]string{"a.md"}, "team-1", "T")

		entries, stale := ns.GetNotifications("agent-new")
		if len(entries) != 0 {
			t.Fatalf("expected empty on first call, got %d entries", len(entries))
		}
		if stale {
			t.Fatal("expected stale=false on first call")
		}
		if got := ns.CursorCount(); got != 1 {
			t.Fatalf("expected 1 cursor after auto-register, got %d", got)
		}
	})

	t.Run("returns only new changes", func(t *testing.T) {
		ns := NewNotificationStore(100)

		ns.RecordChanges([]string{"old.md"}, "team-1", "T")
		time.Sleep(time.Millisecond)

		// register agent cursor
		ns.GetNotifications("agent-1")

		time.Sleep(time.Millisecond)
		ns.RecordChanges([]string{"new.md"}, "team-1", "T")

		entries, stale := ns.GetNotifications("agent-1")
		if len(entries) != 1 {
			t.Fatalf("expected 1 new entry, got %d", len(entries))
		}
		if entries[0].Path != "new.md" {
			t.Fatalf("expected path new.md, got %s", entries[0].Path)
		}
		if stale {
			t.Fatal("expected stale=false")
		}
	})

	t.Run("subsequent call with no new changes returns empty", func(t *testing.T) {
		ns := NewNotificationStore(100)

		ns.GetNotifications("agent-1")
		time.Sleep(time.Millisecond)
		ns.RecordChanges([]string{"a.md"}, "team-1", "T")

		// consume
		ns.GetNotifications("agent-1")

		// no new changes
		entries, _ := ns.GetNotifications("agent-1")
		if len(entries) != 0 {
			t.Fatalf("expected 0 entries on re-read, got %d", len(entries))
		}
	})

	t.Run("stale detection requires actual eviction", func(t *testing.T) {
		ns := NewNotificationStore(2)

		// register agent
		ns.GetNotifications("agent-1")
		time.Sleep(time.Millisecond)

		// fill buffer past capacity so entries are evicted
		ns.RecordChanges([]string{"a.md"}, "team-1", "T")
		time.Sleep(time.Millisecond)
		ns.RecordChanges([]string{"b.md"}, "team-1", "T")
		time.Sleep(time.Millisecond)
		ns.RecordChanges([]string{"c.md"}, "team-1", "T")

		// agent's cursor is before oldest remaining entry AND eviction occurred
		entries, stale := ns.GetNotifications("agent-1")
		if !stale {
			t.Fatal("expected stale=true when eviction occurred and cursor is behind")
		}
		if len(entries) != 2 {
			t.Fatalf("expected 2 entries (buffer contents), got %d", len(entries))
		}
	})

	t.Run("no false stale from dedup without eviction", func(t *testing.T) {
		ns := NewNotificationStore(100) // large cap, no eviction

		// register agent
		ns.GetNotifications("agent-1")
		time.Sleep(time.Millisecond)

		// add entries
		ns.RecordChanges([]string{"a.md"}, "team-1", "T")
		time.Sleep(time.Millisecond)

		// dedup-update a.md — moves its timestamp forward
		// without the evicted flag fix, this could cause false stale
		ns.RecordChanges([]string{"a.md"}, "team-1", "T")

		entries, stale := ns.GetNotifications("agent-1")
		if stale {
			t.Fatal("expected stale=false when no eviction occurred")
		}
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
	})

	t.Run("empty buffer returns empty with no stale", func(t *testing.T) {
		ns := NewNotificationStore(100)
		ns.GetNotifications("agent-1") // register
		entries, stale := ns.GetNotifications("agent-1")
		if len(entries) != 0 {
			t.Fatalf("expected 0 entries, got %d", len(entries))
		}
		if stale {
			t.Fatal("expected stale=false for empty buffer")
		}
	})

	t.Run("multiple agents get independent views", func(t *testing.T) {
		ns := NewNotificationStore(100)

		// register both agents
		ns.GetNotifications("agent-a")
		ns.GetNotifications("agent-b")
		time.Sleep(time.Millisecond)

		// record change
		ns.RecordChanges([]string{"file.md"}, "team-1", "T")

		// agent-a reads and consumes
		entriesA, _ := ns.GetNotifications("agent-a")
		if len(entriesA) != 1 {
			t.Fatalf("agent-a expected 1 entry, got %d", len(entriesA))
		}

		// agent-b should still see it (independent cursor)
		entriesB, _ := ns.GetNotifications("agent-b")
		if len(entriesB) != 1 {
			t.Fatalf("agent-b expected 1 entry, got %d", len(entriesB))
		}

		// both should see empty on re-read
		entriesA, _ = ns.GetNotifications("agent-a")
		entriesB, _ = ns.GetNotifications("agent-b")
		if len(entriesA) != 0 || len(entriesB) != 0 {
			t.Fatal("expected empty on re-read for both agents")
		}
	})

	t.Run("accumulates multiple pulls before agent checks", func(t *testing.T) {
		ns := NewNotificationStore(100)

		ns.GetNotifications("agent-1")
		time.Sleep(time.Millisecond)

		// three separate pulls, each adding files
		ns.RecordChanges([]string{"a.md"}, "team-1", "T")
		time.Sleep(time.Millisecond)
		ns.RecordChanges([]string{"b.md"}, "team-1", "T")
		time.Sleep(time.Millisecond)
		ns.RecordChanges([]string{"c.md"}, "team-1", "T")

		// single check should return all three
		entries, _ := ns.GetNotifications("agent-1")
		if len(entries) != 3 {
			t.Fatalf("expected 3 accumulated entries, got %d", len(entries))
		}
	})
}

func TestRemoveCursor(t *testing.T) {
	ns := NewNotificationStore(100)

	ns.GetNotifications("agent-1")
	ns.GetNotifications("agent-2")

	if got := ns.CursorCount(); got != 2 {
		t.Fatalf("expected 2 cursors, got %d", got)
	}

	ns.RemoveCursor("agent-1")

	if got := ns.CursorCount(); got != 1 {
		t.Fatalf("expected 1 cursor after removal, got %d", got)
	}

	// removing non-existent cursor is a no-op
	ns.RemoveCursor("agent-nonexistent")
	if got := ns.CursorCount(); got != 1 {
		t.Fatalf("expected 1 cursor after no-op removal, got %d", got)
	}
}

func TestDefaultMaxEntries(t *testing.T) {
	ns := NewNotificationStore(0)
	if ns.maxEntries != 1000 {
		t.Fatalf("expected default maxEntries=1000, got %d", ns.maxEntries)
	}

	ns = NewNotificationStore(-5)
	if ns.maxEntries != 1000 {
		t.Fatalf("expected default maxEntries=1000 for negative input, got %d", ns.maxEntries)
	}
}

func TestDaemonRestartLosesState(t *testing.T) {
	// simulate daemon restart: create store, record changes, create new store
	ns1 := NewNotificationStore(100)
	ns1.GetNotifications("agent-1") // register
	time.Sleep(time.Millisecond)
	ns1.RecordChanges([]string{"important.md"}, "team-1", "T")

	// "restart" — new store, all state lost
	ns2 := NewNotificationStore(100)

	// agent-1 auto-registers fresh, gets empty (not stale)
	entries, stale := ns2.GetNotifications("agent-1")
	if len(entries) != 0 {
		t.Fatal("expected empty after restart")
	}
	if stale {
		t.Fatal("expected stale=false after restart (no eviction in new store)")
	}
}

func TestConcurrentAccess(t *testing.T) {
	ns := NewNotificationStore(50)
	var wg sync.WaitGroup

	// concurrent writers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				ns.RecordChanges(
					[]string{fmt.Sprintf("file-%d-%d.md", i, j)},
					fmt.Sprintf("team-%d", i),
					fmt.Sprintf("Team %d", i),
				)
			}
		}(i)
	}

	// concurrent readers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			agentID := fmt.Sprintf("agent-%d", i)
			for j := 0; j < 20; j++ {
				ns.GetNotifications(agentID)
			}
		}(i)
	}

	// concurrent dedup writers (same file, same team)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				ns.RecordChanges([]string{"shared.md"}, "team-shared", "Shared")
			}
		}()
	}

	// concurrent diagnostics
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				ns.EntryCount()
				ns.CursorCount()
			}
		}()
	}

	wg.Wait()

	// buffer should not exceed cap
	if got := ns.EntryCount(); got > 50 {
		t.Fatalf("expected entries <= 50, got %d", got)
	}
}

// getEntriesDirectly reads entries under lock for test assertions.
func getEntriesDirectly(t *testing.T, ns *NotificationStore) ([]ChangeEntry, map[string]time.Time) {
	t.Helper()
	ns.mu.RLock()
	defer ns.mu.RUnlock()

	entries := make([]ChangeEntry, len(ns.entries))
	copy(entries, ns.entries)

	cursors := make(map[string]time.Time, len(ns.cursors))
	for k, v := range ns.cursors {
		cursors[k] = v
	}
	return entries, cursors
}
