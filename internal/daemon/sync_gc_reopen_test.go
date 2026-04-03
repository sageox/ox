package daemon

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	whisperstore "github.com/sageox/ox/internal/whisper/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReopenWhisperStoreAfterGC_BlueGreenSwap(t *testing.T) {
	// real-world failure prevented: after blue-green GC, the old sql.DB handle
	// points to a deleted inode (renamed-away directory). Without reopening,
	// writes silently fail and whisper data is lost.
	if testing.Short() {
		t.Skip("short: SQLite file operations")
	}

	// phase 1: create store, write entry
	ledgerRoot := filepath.Join(t.TempDir(), "ledger")
	dbDir := filepath.Join(ledgerRoot, ".sageox", "cache", "whisper")
	require.NoError(t, os.MkdirAll(dbDir, 0o755))
	dbPath := filepath.Join(dbDir, "whisper.db")

	store, err := whisperstore.Open(dbPath)
	require.NoError(t, err)
	registry := NewWhisperRegistry(store, nil)
	defer registry.Close()

	require.NoError(t, registry.Add("ledger", whisperstore.WhisperEntry{
		ID: "pre-gc", Scope: "ledger", Type: whisperstore.WhisperTrigger,
		Source: "test", Topic: "gc", Content: "before",
		Importance: whisperstore.ImportanceNormal, CreatedAt: time.Now(),
	}))

	// phase 2: simulate blue-green swap (rename away, recreate fresh dir)
	require.NoError(t, os.Rename(ledgerRoot, ledgerRoot+".old"))
	require.NoError(t, os.MkdirAll(dbDir, 0o755))

	// phase 3: reopen at same path (now fresh, empty)
	require.NoError(t, registry.ReopenLedgerStore(dbPath))

	// phase 4: verify writes work on new store
	require.NoError(t, registry.Add("ledger", whisperstore.WhisperEntry{
		ID: "post-gc", Scope: "ledger", Type: whisperstore.WhisperTrigger,
		Source: "test", Topic: "gc", Content: "after",
		Importance: whisperstore.ImportanceNormal, CreatedAt: time.Now(),
	}))

	// phase 5: only post-GC entry should be in the new store
	entries, _, err := registry.GetWhispersPage("", time.Time{}, 50)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	assert.Equal(t, "post-gc", entries[0].ID)
}

func TestReopenWhisperStoreAfterGC_ConcurrentReads(t *testing.T) {
	// real-world failure prevented: agent polling reads whispers concurrently
	// while GC triggers a reopen — must not deadlock or panic
	if testing.Short() {
		t.Skip("short: SQLite concurrent operations")
	}

	dbPath := filepath.Join(t.TempDir(), "whisper.db")
	store, err := whisperstore.Open(dbPath)
	require.NoError(t, err)
	registry := NewWhisperRegistry(store, nil)
	defer registry.Close()

	// seed data for readers to scan
	for i := 0; i < 10; i++ {
		require.NoError(t, registry.Add("ledger", whisperstore.WhisperEntry{
			ID: "w-" + time.Now().Add(time.Duration(i)*time.Millisecond).Format("150405.000000"),
			Scope: "ledger", Type: whisperstore.WhisperTimeBased,
			Source: "test", Topic: "concurrent", Content: "data",
			Importance: whisperstore.ImportanceNormal,
			CreatedAt:  time.Now().Add(time.Duration(i) * time.Millisecond),
		}))
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// spawn concurrent readers
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = registry.GetWhispers("agent-concurrent", whisperstore.AttentionAll, nil)
				}
			}
		}()
	}

	// brief pause to ensure readers are actively polling before reopen
	time.Sleep(5 * time.Millisecond)

	// reopen while readers are active
	newDBPath := filepath.Join(t.TempDir(), "whisper-new.db")
	require.NoError(t, registry.ReopenLedgerStore(newDBPath))

	// verify store is healthy after concurrent reopen
	require.NoError(t, registry.Add("ledger", whisperstore.WhisperEntry{
		ID: "post-concurrent", Scope: "ledger", Type: whisperstore.WhisperTrigger,
		Source: "test", Topic: "gc", Content: "survived",
		Importance: whisperstore.ImportanceNormal, CreatedAt: time.Now(),
	}))

	close(stop)
	wg.Wait()

	entries, _, err := registry.GetWhispersPage("", time.Time{}, 50)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	assert.Equal(t, "post-concurrent", entries[0].ID)
}

func TestReopenWhisperStoreAfterGC_NilRegistry(t *testing.T) {
	// real-world failure prevented: if murmur feature is disabled, whisperRegistry
	// is nil — reopenWhisperStoreAfterGC must be a safe no-op
	t.Parallel()

	projectDir := setupProjectWithConfig(t, "# empty\n")
	s := newTestScheduler(projectDir)

	assert.NotPanics(t, func() {
		s.reopenWhisperStoreAfterGC()
	})
}
