package daemon

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

func openTestStore(t *testing.T) *whisperstore.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "whisper.db")
	s, err := whisperstore.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestWhisperRegistryAddAndGet(t *testing.T) {
	ledger := openTestStore(t)
	team := openTestStore(t)

	r := NewWhisperRegistry(ledger, nil)
	r.AddTeamStore("team-1", team)
	defer r.Close()

	now := time.Now()

	// add to ledger scope
	err := r.Add("ledger", whisperstore.WhisperEntry{
		ID: "l1", Scope: "ledger", Type: whisperstore.WhisperTrigger,
		Source: "test", Topic: "lint", Content: "ledger lint",
		Importance: whisperstore.ImportanceNormal, CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("add ledger: %v", err)
	}

	// add to team scope
	err = r.Add("team", whisperstore.WhisperEntry{
		ID: "t1", Scope: "team", Type: whisperstore.WhisperTrigger,
		Source: "test", Topic: "architecture", Content: "team arch",
		Importance: whisperstore.ImportanceCritical, CreatedAt: now.Add(time.Millisecond),
	})
	if err != nil {
		t.Fatalf("add team: %v", err)
	}

	// get all whispers — should merge from both stores
	got, err := r.GetWhispers("agent-1", whisperstore.AttentionAll, nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}
}

func TestWhisperRegistryPrune(t *testing.T) {
	ledger := openTestStore(t)
	r := NewWhisperRegistry(ledger, nil)
	defer r.Close()

	old := time.Now().Add(-48 * time.Hour)
	r.Add("ledger", whisperstore.WhisperEntry{
		ID: "old", Scope: "ledger", Type: whisperstore.WhisperTrigger,
		Source: "test", Topic: "lint", Content: "old",
		Importance: whisperstore.ImportanceNormal, CreatedAt: old,
	})

	r.Prune(24 * time.Hour)

	got, _ := r.GetWhispers("agent-1", whisperstore.AttentionAll, nil)
	if len(got) != 0 {
		t.Fatalf("expected 0 after prune, got %d", len(got))
	}
}

func TestWhisperRegistryRemoveCursor(t *testing.T) {
	ledger := openTestStore(t)
	r := NewWhisperRegistry(ledger, nil)
	defer r.Close()

	now := time.Now()
	r.Add("ledger", whisperstore.WhisperEntry{
		ID: "w1", Scope: "ledger", Type: whisperstore.WhisperTrigger,
		Source: "test", Topic: "lint", Content: "test",
		Importance: whisperstore.ImportanceNormal, CreatedAt: now,
	})

	// set cursor
	r.GetWhispers("agent-1", whisperstore.AttentionAll, nil)

	// remove cursor
	r.RemoveCursor("agent-1")

	// should get entries again
	got, _ := r.GetWhispers("agent-1", whisperstore.AttentionAll, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 after cursor reset, got %d", len(got))
	}
}

func TestWhisperRegistryHasTeamStore(t *testing.T) {
	registry := NewWhisperRegistry(nil, nil)

	if registry.HasTeamStore("team-1") {
		t.Error("expected HasTeamStore to return false for unregistered team")
	}

	// nil store is accepted by AddTeamStore — sufficient for map lookup testing
	registry.AddTeamStore("team-1", nil)

	if !registry.HasTeamStore("team-1") {
		t.Error("expected HasTeamStore to return true after AddTeamStore")
	}

	if registry.HasTeamStore("team-2") {
		t.Error("expected HasTeamStore to return false for different team")
	}
}

func TestWhisperRegistryUnknownScope(t *testing.T) {
	ledger := openTestStore(t)
	r := NewWhisperRegistry(ledger, nil)
	defer r.Close()

	err := r.Add("unknown", whisperstore.WhisperEntry{
		ID: "x", Scope: "unknown", Type: whisperstore.WhisperTrigger,
		Source: "test", Topic: "lint", Content: "test",
		Importance: whisperstore.ImportanceNormal, CreatedAt: time.Now(),
	})
	if err == nil {
		t.Fatal("expected error for unknown scope")
	}
}

func TestWhisperRegistryReopenLedgerStore(t *testing.T) {
	ledger := openTestStore(t)
	r := NewWhisperRegistry(ledger, nil)
	defer r.Close()

	// add a whisper to the original store
	err := r.Add("ledger", whisperstore.WhisperEntry{
		ID:         "before-reopen",
		Scope:      "ledger",
		Type:       whisperstore.WhisperTimeBased,
		Source:     "test",
		Topic:      "test",
		Content:    "original",
		Importance: whisperstore.ImportanceNormal,
		CreatedAt:  time.Now(),
	})
	if err != nil {
		t.Fatalf("add before reopen: %v", err)
	}

	// reopen to a new DB path (simulates GC reclone)
	newDBPath := filepath.Join(t.TempDir(), "whisper-new.db")
	if err := r.ReopenLedgerStore(newDBPath); err != nil {
		t.Fatalf("reopen: %v", err)
	}

	// add a whisper to the new store
	err = r.Add("ledger", whisperstore.WhisperEntry{
		ID:         "after-reopen",
		Scope:      "ledger",
		Type:       whisperstore.WhisperTimeBased,
		Source:     "test",
		Topic:      "test",
		Content:    "new",
		Importance: whisperstore.ImportanceNormal,
		CreatedAt:  time.Now(),
	})
	if err != nil {
		t.Fatalf("add after reopen: %v", err)
	}

	// verify new store works — should only have the post-reopen entry
	entries, _, err := r.GetWhispersPage("", time.Time{}, 50)
	if err != nil {
		t.Fatalf("get whispers: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry in new store, got %d", len(entries))
	}
	if entries[0].ID != "after-reopen" {
		t.Errorf("expected after-reopen entry, got %s", entries[0].ID)
	}
}

func TestWhisperRegistryUnknownScopeRelayHandling(t *testing.T) {
	ledger := openTestStore(t)
	r := NewWhisperRegistry(ledger, nil)
	defer r.Close()

	// IsRelayed should return false for an unknown scope (nil store)
	relayed, err := r.IsRelayed("murmur-unknown", "bogus-scope")
	if err != nil {
		t.Fatalf("IsRelayed with unknown scope should not error, got: %v", err)
	}
	if relayed {
		t.Error("IsRelayed should return false for unknown scope")
	}

	// MarkRelayed should return nil for an unknown scope (nil store)
	err = r.MarkRelayed("murmur-unknown", "bogus-scope")
	if err != nil {
		t.Fatalf("MarkRelayed with unknown scope should not error, got: %v", err)
	}
}

// --- Idle-close behavior ---
//
// Failure prevented: team whisper SQLite stores accumulating FDs across long
// daemon uptimes when whisper traffic is sparse (every-few-minutes workload).

func TestCloseIdleTeamStores(t *testing.T) {
	// Each case sets up a registry with optional ledger + team stores,
	// drives an arbitrary scenario via `setup`, then asserts the result of
	// CloseIdleTeamStores(threshold). `wantTeamsOpen` is the set of team
	// IDs that should remain open after the close pass.
	cases := []struct {
		name          string
		teams         []string
		threshold     time.Duration
		setup         func(t *testing.T, r *WhisperRegistry, clock *time.Time)
		wantClosed    int
		wantTeamsOpen []string
		wantLedger    bool
	}{
		{
			name:      "closes only the idle team",
			teams:     []string{"team-a", "team-b"},
			threshold: 15 * time.Minute,
			setup: func(t *testing.T, r *WhisperRegistry, clock *time.Time) {
				t.Helper()
				// Advance 20 min, then force team-a's lastAccess to look
				// stale while team-b stays fresh from its AddTeamStore touch.
				*clock = clock.Add(20 * time.Minute)
				r.mu.Lock()
				r.lastAccess["team-a"] = clock.Add(-30 * time.Minute)
				r.lastAccess["team-b"] = *clock
				r.mu.Unlock()
			},
			wantClosed:    1,
			wantTeamsOpen: []string{"team-b"},
			wantLedger:    true,
		},
		{
			name:      "noop when team is fresh below threshold",
			teams:     []string{"team-a"},
			threshold: 15 * time.Minute,
			setup: func(t *testing.T, r *WhisperRegistry, clock *time.Time) {
				t.Helper()
				*clock = clock.Add(5 * time.Minute) // under threshold
			},
			wantClosed:    0,
			wantTeamsOpen: []string{"team-a"},
			wantLedger:    true,
		},
		{
			name:      "Add('team') bumps lastAccess so team avoids close",
			teams:     []string{"team-a"},
			threshold: 15 * time.Minute,
			setup: func(t *testing.T, r *WhisperRegistry, clock *time.Time) {
				t.Helper()
				*clock = clock.Add(10 * time.Minute)
				if err := r.Add("team", whisperstore.WhisperEntry{
					ID: "t-bump", Scope: "team", Type: whisperstore.WhisperTrigger,
					Source: "test", Topic: "lint", Content: "bump",
					Importance: whisperstore.ImportanceNormal, CreatedAt: *clock,
				}); err != nil {
					t.Fatalf("Add team: %v", err)
				}
				// Without the bump this is 24 min idle; with it, 14.
				*clock = clock.Add(14 * time.Minute)
			},
			wantClosed:    0,
			wantTeamsOpen: []string{"team-a"},
			wantLedger:    true,
		},
		{
			name:      "ledger is never subject to idle-close",
			teams:     nil,
			threshold: 1 * time.Minute,
			setup: func(t *testing.T, r *WhisperRegistry, clock *time.Time) {
				t.Helper()
				*clock = clock.Add(24 * time.Hour)
			},
			wantClosed:    0,
			wantTeamsOpen: nil,
			wantLedger:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ledger := openTestStore(t)
			r := NewWhisperRegistry(ledger, nil)
			defer r.Close()

			clock := time.Now()
			r.SetClock(func() time.Time { return clock })

			for _, id := range tc.teams {
				r.AddTeamStore(id, openTestStore(t))
			}

			tc.setup(t, r, &clock)

			gotClosed := r.CloseIdleTeamStores(tc.threshold)
			if gotClosed != tc.wantClosed {
				t.Fatalf("CloseIdleTeamStores=%d, want %d", gotClosed, tc.wantClosed)
			}

			wantOpen := make(map[string]struct{}, len(tc.wantTeamsOpen))
			for _, id := range tc.wantTeamsOpen {
				wantOpen[id] = struct{}{}
			}
			for _, id := range tc.teams {
				_, shouldBeOpen := wantOpen[id]
				if got := r.HasTeamStore(id); got != shouldBeOpen {
					t.Errorf("HasTeamStore(%q)=%v, want %v", id, got, shouldBeOpen)
				}
			}

			if tc.wantLedger && r.LedgerStore() == nil {
				t.Error("ledger store missing after close pass")
			}
		})
	}
}

// TestPrune_RacesIdleClose hammers the snapshot-style methods
// (Prune, EnforceMaxSize, RemoveCursor) against the idle-close janitor in
// parallel goroutines under -race.
//
// Failure prevented: those methods previously snapshotted teamStores under
// RLock and released the lock before calling into each store. With that
// pattern, CloseIdleTeamStores could close a *Store while a snapshot caller
// was still iterating, producing use-after-close on the SQLite handle.
// Under -race this surfaces as a detected data race or a sql.DB panic.
//
// Iteration-bounded (not time-bounded) so it is deterministic and short.
func TestPrune_RacesIdleClose(t *testing.T) {
	t.Parallel()

	const (
		numTeams        = 4
		iterations      = 50
		snapshotCallers = 3
	)

	ledger := openTestStore(t)
	r := NewWhisperRegistry(ledger, nil)
	defer r.Close()

	for i := 0; i < numTeams; i++ {
		r.AddTeamStore(fmt.Sprintf("team-%d", i), openTestStore(t))
	}
	// Make every store look idle so the janitor actually closes them.
	clock := time.Now()
	r.SetClock(func() time.Time { return clock.Add(time.Hour) })

	var snapshots sync.WaitGroup
	for i := 0; i < snapshotCallers; i++ {
		snapshots.Add(1)
		go func() {
			defer snapshots.Done()
			for k := 0; k < iterations; k++ {
				r.Prune(24 * time.Hour)
				r.EnforceMaxSize(10 * 1024 * 1024)
				r.RemoveCursor("agent-x")
				_, _ = r.GetWhispers("agent-x", whisperstore.AttentionAll, nil)
			}
		}()
	}

	var janitorWG sync.WaitGroup
	janitorStop := make(chan struct{})
	janitorWG.Add(1)
	go func() {
		defer janitorWG.Done()
		for {
			select {
			case <-janitorStop:
				return
			default:
				r.CloseIdleTeamStores(1 * time.Minute)
				// Reopen any closed store so the race window stays open
				// for the full run.
				for i := 0; i < numTeams; i++ {
					id := fmt.Sprintf("team-%d", i)
					if !r.HasTeamStore(id) {
						r.AddTeamStore(id, openTestStore(t))
					}
				}
			}
		}
	}()

	snapshots.Wait()
	close(janitorStop)
	janitorWG.Wait()
}

// TestWhisperRegistry_NoFDLeak_OnIdleClose is the production-grade FD
// regression test for the whisper subsystem, mirroring
// TestProjectWatcher_NoFDLeak_RealWatcher_Darwin.
//
// Failure prevented: the whisper FD bloat that shipped this fix —
// 9 SQLite databases × 3 files each (.db + .db-shm + .db-wal) pinned
// indefinitely because team stores were never closed. The bug-fix unit
// tests (TestCloseIdleTeamStores subtests) verify that CloseIdleTeamStores
// returns the right *count*, but only this test would have caught a
// regression where Close() failed to actually release the kernel FDs.
//
// Pattern: open N team stores → exercise each once → mark them all idle
// via the injected clock → trigger CloseIdleTeamStores → assert the
// process-wide FD count returns to baseline (within tolerance for SQLite
// driver internals and Go runtime noise).
//
// macOS + Linux only; Windows has no /dev/fd or /proc/self/fd.
func TestWhisperRegistry_NoFDLeak_OnIdleClose(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("FD enumeration unavailable on Windows")
	}
	if testing.Short() {
		t.Skip("short: opens N real SQLite WAL stores")
	}

	const numTeams = 12 // enough that a leak of even 1 FD/team is unmissable

	ledger := openTestStore(t)
	r := NewWhisperRegistry(ledger, nil)
	t.Cleanup(func() { _ = r.Close() })

	// Warm up: open + close one extra store to settle any one-time SQLite
	// driver allocation before measuring baseline.
	warm := openTestStore(t)
	_ = warm.Close()

	baseline := countOpenFDs(t)

	// Open N team stores and exercise each one once so SQLite's WAL
	// machinery has materialized .db-shm and .db-wal alongside .db.
	for i := 0; i < numTeams; i++ {
		id := fmt.Sprintf("team-%d", i)
		r.AddTeamStore(id, openTestStore(t))
	}
	if err := r.Add("team", whisperstore.WhisperEntry{
		ID: "warm-1", Scope: "team", Type: whisperstore.WhisperTrigger,
		Source: "test", Topic: "fd", Content: "warm",
		Importance: whisperstore.ImportanceNormal, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("Add team: %v", err)
	}
	if _, err := r.GetWhispers("agent-fd", whisperstore.AttentionAll, nil); err != nil {
		t.Fatalf("GetWhispers: %v", err)
	}

	// Mark every store idle and trigger the janitor.
	clock := time.Now().Add(time.Hour)
	r.SetClock(func() time.Time { return clock })
	closed := r.CloseIdleTeamStores(1 * time.Minute)
	if closed != numTeams {
		t.Fatalf("expected %d teams closed, got %d", numTeams, closed)
	}

	final := countOpenFDs(t)
	delta := final - baseline

	// Per team store: 1 (.db) + 1 (.db-shm) + 1 (.db-wal) = 3 FDs pre-fix.
	// Pre-fix expected delta: numTeams * 3 = 36. Post-fix should be ~0.
	// Tolerance leaves room for SQLite driver bookkeeping and runtime noise
	// but is far below the pre-fix delta so a regression of even ~1 FD/team
	// trips the assertion.
	const tolerance = 6
	if delta >= tolerance {
		t.Errorf("FD count grew by %d (baseline=%d, final=%d). "+
			"Pre-fix this would be ~%d (3 per team). Tolerance: <%d.",
			delta, baseline, final, numTeams*3, tolerance)
	}
}

func TestTeamIDs(t *testing.T) {
	teamA := openTestStore(t)
	teamB := openTestStore(t)
	r := NewWhisperRegistry(nil, nil)
	r.AddTeamStore("team-a", teamA)
	r.AddTeamStore("team-b", teamB)

	got := r.TeamIDs()
	if len(got) != 2 {
		t.Fatalf("expected 2 team IDs, got %d: %v", len(got), got)
	}

	r.CloseTeamStore("team-a")
	got = r.TeamIDs()
	if len(got) != 1 || got[0] != "team-b" {
		t.Errorf("expected only team-b after close, got %v", got)
	}
}
