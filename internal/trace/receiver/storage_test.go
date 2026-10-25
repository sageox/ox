package receiver

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/require"
)

// Retention must protect unfinished recordings and uncertain metadata, rather
// than treating an unreadable index as evidence that the payload is expendable.
func TestRetentionProtectsReferencedAndUncertainSessions(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	old := now.Add(-15 * 24 * time.Hour)
	for _, tt := range []struct {
		name                               string
		age                                time.Time
		protected, corrupt, missing, fresh bool
		deleted, wantErr                   bool
	}{
		{name: "old", age: old, deleted: true},
		{name: "referenced", age: old, protected: true},
		{name: "recent", age: now},
		{name: "boundary", age: now.Add(-14 * 24 * time.Hour)},
		{name: "corrupt", age: old, corrupt: true, wantErr: true},
		{name: "missing", age: old, missing: true, wantErr: true},
		{name: "stale index after failed append", age: old, fresh: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spool := filepath.Join(t.TempDir(), "spool")
			require.NoError(t, store(spool, "traces", "request", map[string][]byte{sessionA: payload("traces", sessionA)}, tt.age))
			sessionDir := filepath.Join(spool, sessionA)
			entries, err := os.ReadDir(sessionDir)
			require.NoError(t, err)
			for _, entry := range entries {
				require.NoError(t, os.Chtimes(filepath.Join(sessionDir, entry.Name()), tt.age, tt.age))
			}
			if tt.corrupt {
				require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "index.json"), []byte("corrupt"), 0600))
			}
			if tt.missing {
				require.NoError(t, os.Remove(filepath.Join(sessionDir, "index.json")))
			}
			if tt.fresh {
				require.NoError(t, os.Chtimes(filepath.Join(sessionDir, "traces.jsonl"), now, now))
			}
			err = Prune(spool, now, map[string]bool{sessionA: tt.protected})
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			_, err = os.Stat(sessionDir)
			if tt.deleted {
				require.ErrorIs(t, err, os.ErrNotExist)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestStorageDefaultsAndMalformedPaths(t *testing.T) {
	t.Parallel()
	absent := filepath.Join(t.TempDir(), "absent")
	stats, err := Stats(absent)
	require.NoError(t, err)
	require.Equal(t, SpoolStats{}, stats)
	require.NoError(t, Prune(absent, time.Now(), nil))
	_, err = Stats("")
	require.Error(t, err)
	require.Error(t, Prune("", time.Now(), nil))
	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, nil, 0600))
	_, err = Stats(file)
	require.Error(t, err)
	require.Error(t, Prune(file, time.Now(), nil))
	require.Error(t, store(file, "traces", "request", nil, time.Now()))
	require.Error(t, store(t.TempDir(), "traces", "request", map[string][]byte{"../bad": []byte(`{}`)}, time.Now()))
}

func TestInvalidIndexAndSymlinkRetention(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"invalid index", "linked session", "linked payload", "unknown directory"} {
		t.Run(name, func(t *testing.T) {
			spool := t.TempDir()
			outside := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(outside, "sentinel"), []byte("keep"), 0600))
			sessionDir := filepath.Join(spool, sessionA)
			switch name {
			case "linked session":
				if err := os.Symlink(outside, sessionDir); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				_, err := Stats(spool)
				require.Error(t, err)
				require.Error(t, Prune(spool, time.Now(), nil))
			case "unknown directory":
				require.NoError(t, os.Mkdir(filepath.Join(spool, "not-a-session"), 0700))
				require.NoError(t, Prune(spool, time.Now(), nil))
				stats, err := Stats(spool)
				require.NoError(t, err)
				require.Zero(t, stats.Sessions)
			default:
				old := time.Now().Add(-15 * 24 * time.Hour)
				require.NoError(t, store(spool, "traces", "req", map[string][]byte{sessionA: []byte(`{}`)}, old))
				if name == "invalid index" {
					b, err := json.Marshal(SessionIndex{SessionID: sessionB, FirstSeen: old, LastSeen: old, Requests: 1, Bytes: 3})
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "index.json"), b, 0600))
					require.Error(t, Prune(spool, time.Now(), nil))
				} else {
					require.NoError(t, os.Remove(filepath.Join(sessionDir, "traces.jsonl")))
					if err := os.Symlink(outside, filepath.Join(sessionDir, "traces.jsonl")); err != nil {
						t.Skipf("symlinks unavailable: %v", err)
					}
					require.NoError(t, Prune(spool, time.Now(), nil))
				}
			}
			b, err := os.ReadFile(filepath.Join(outside, "sentinel"))
			require.NoError(t, err)
			require.Equal(t, "keep", string(b))
		})
	}
}

// A status subprocess must wait for the receiver's filesystem lock, rather than
// pruning a session whose trace file is currently being appended.
func TestPruneWaitsForOtherProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("short: subprocess lock coordination")
	}
	if os.Getenv("OX_TRACE_PRUNE_LOCK_HELPER") == "1" {
		err := Prune(os.Getenv("OX_TRACE_PRUNE_LOCK_SPOOL"), time.Now(), nil)
		if err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	spool := t.TempDir()
	old := time.Now().Add(-15 * 24 * time.Hour)
	require.NoError(t, store(spool, "traces", "req", map[string][]byte{sessionA: []byte(`{}`)}, old))
	sessionDir := filepath.Join(spool, sessionA)
	entries, err := os.ReadDir(sessionDir)
	require.NoError(t, err)
	for _, entry := range entries {
		require.NoError(t, os.Chtimes(filepath.Join(sessionDir, entry.Name()), old, old))
	}
	lock := flock.New(filepath.Join(spool, ".lock"))
	require.NoError(t, lock.Lock())
	defer lock.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPruneWaitsForOtherProcess$")
	cmd.Env = append(os.Environ(), "OX_TRACE_PRUNE_LOCK_HELPER=1", "OX_TRACE_PRUNE_LOCK_SPOOL="+spool) // safe: helper only prunes the explicit t.TempDir spool, never invokes ox or reads user configuration.
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		t.Fatalf("prune returned while locked: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	_, err = os.Stat(sessionDir)
	require.NoError(t, err)
	require.NoError(t, lock.Unlock())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("prune did not finish after unlock")
	}
	_, err = os.Stat(sessionDir)
	require.ErrorIs(t, err, os.ErrNotExist)
}
