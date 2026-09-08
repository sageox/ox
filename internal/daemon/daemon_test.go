package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A follower must observe successful syncs by the shared owner without rewriting
// config or the owner's cache, including after an upgrade with no usable cache.
func TestDaemonStatus_SharedTeamContextSync(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("SAGEOX_ENDPOINT", "https://status.test.invalid")
	older := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	newer := older.Add(30 * time.Minute)
	cases := []struct {
		name       string
		configSync time.Time
		sharedSync time.Time
		corrupt    bool
		want       time.Time
	}{
		{name: "never synced"},
		{name: "missing cache", configSync: older, want: older},
		{name: "corrupt cache", configSync: older, corrupt: true, want: older},
		{name: "shared owner synced", sharedSync: newer, want: newer},
		{name: "shared cache newer", configSync: older, sharedSync: newer, want: newer},
		{name: "config newer", configSync: newer, sharedSync: older, want: newer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.ProjectRoot = t.TempDir()
			d := New(cfg, nil)
			d.scheduler = NewSyncScheduler(cfg, d.logger)
			d.scheduler.SetGlobalSyncLease("", nil)
			teamPath := t.TempDir()
			ws := &WorkspaceState{
				ID: "team_1", Type: WorkspaceTypeTeamContext, Path: teamPath,
				TeamID: "team_1", TeamName: "Test Team", Exists: true,
				ConfigLastSync: tc.configSync, LastErr: "local sync failed",
			}
			d.scheduler.WorkspaceRegistry().workspaces[ws.ID] = ws
			statePath := filepath.Join(teamPath, ".sageox", "cache", "sync-state.json")
			if !tc.sharedSync.IsZero() {
				require.NoError(t, SaveSyncState(teamPath, &SyncState{LastSync: tc.sharedSync}))
			}
			if tc.corrupt {
				require.NoError(t, os.MkdirAll(filepath.Dir(statePath), 0755))
				require.NoError(t, os.WriteFile(statePath, []byte("{incomplete"), 0600))
			}
			before, beforeErr := os.ReadFile(statePath)
			svc := &daemonServiceImpl{d: d}
			status := svc.Status()
			require.False(t, status.GlobalSyncOwner)
			require.Len(t, status.Workspaces["team-context"], 1)
			require.Len(t, status.TeamContexts, 1)
			assert.Equal(t, tc.want, status.Workspaces["team-context"][0].LastSync)
			assert.Equal(t, tc.want, status.TeamContexts[0].LastSync)
			assert.Equal(t, ws.LastErr, status.Workspaces["team-context"][0].LastErr)
			assert.Equal(t, ws.LastErr, status.TeamContexts[0].LastErr)
			assert.Equal(t, tc.configSync, ws.ConfigLastSync, "status must not mutate registry state")
			after, afterErr := os.ReadFile(statePath)
			assert.Equal(t, before, after, "status must not rewrite shared sync state")
			if os.IsNotExist(beforeErr) {
				assert.True(t, os.IsNotExist(afterErr), "status must not create shared sync state")
			} else {
				require.NoError(t, afterErr)
			}

			// The owner updates the shared cache while this follower keeps running.
			advanced := newer.Add(time.Minute)
			require.NoError(t, SaveSyncState(teamPath, &SyncState{LastSync: advanced}))
			status = svc.Status()
			assert.Equal(t, advanced, status.Workspaces["team-context"][0].LastSync)
			assert.Equal(t, advanced, status.TeamContexts[0].LastSync)
			assert.Equal(t, tc.configSync, ws.ConfigLastSync)
		})
	}
}

func TestNew(t *testing.T) {
	t.Run("with nil config uses defaults", func(t *testing.T) {
		d := New(nil, nil)
		assert.NotNil(t, d)
		assert.NotNil(t, d.config)
		assert.Equal(t, 60*time.Second, d.config.SyncIntervalRead)
	})

	t.Run("with custom config", func(t *testing.T) {
		cfg := &Config{
			SyncIntervalRead: 10 * time.Minute,
			LedgerPath:       "/custom/path",
		}
		d := New(cfg, nil)
		assert.Equal(t, 10*time.Minute, d.config.SyncIntervalRead)
		assert.Equal(t, "/custom/path", d.config.LedgerPath)
	})

	t.Run("with custom logger", func(t *testing.T) {
		logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
		d := New(nil, logger)
		assert.Equal(t, logger, d.logger)
	})
}

func TestIsRunning_NoDaemon(t *testing.T) {
	// use temp dir so no daemon socket exists
	tmpDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	assert.False(t, IsRunning())
}

func TestDaemon_WritePidFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	d := New(nil, nil)
	err := d.writePidFile()
	require.NoError(t, err)

	// verify file exists and contains PID
	content, err := os.ReadFile(PidPath())
	require.NoError(t, err)
	assert.NotEmpty(t, content)
}

func TestDaemon_Cleanup(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	d := New(nil, nil)

	err := d.writePidFile()
	require.NoError(t, err)

	// create fake socket file
	socketPath := SocketPath()
	f, _ := os.Create(socketPath)
	f.Close()

	d.cleanup()

	// PID and socket files should be removed
	_, err = os.Stat(PidPath())
	assert.True(t, os.IsNotExist(err))

	_, err = os.Stat(socketPath)
	assert.True(t, os.IsNotExist(err))
}

func TestDaemon_Stop_NotRunning(t *testing.T) {
	d := New(nil, nil)
	err := d.Stop()
	assert.ErrorIs(t, err, ErrNotRunning)
}

func TestDaemon_Start_AlreadyRunning(t *testing.T) {
	d := New(nil, nil)
	d.running = true

	err := d.Start()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already running")
}

func TestDaemon_ActivityTracking(t *testing.T) {
	t.Run("initial activity timestamp is set", func(t *testing.T) {
		d := New(nil, nil)
		assert.False(t, d.lastActivity.IsZero())
		// should be recent
		assert.WithinDuration(t, time.Now(), d.lastActivity, time.Second)
	})

	t.Run("recordActivity updates timestamp", func(t *testing.T) {
		d := New(nil, nil)
		initial := d.lastActivity

		// wait a bit
		time.Sleep(10 * time.Millisecond)
		d.recordActivity()

		assert.True(t, d.lastActivity.After(initial))
	})

	t.Run("timeSinceLastActivity returns correct duration", func(t *testing.T) {
		d := New(nil, nil)
		d.lastActivity = time.Now().Add(-5 * time.Minute)

		since := d.timeSinceLastActivity()
		assert.True(t, since >= 5*time.Minute)
		assert.True(t, since < 6*time.Minute)
	})
}

func TestDefaultConfig_NewFields(t *testing.T) {
	cfg := DefaultConfig()

	t.Run("inactivity timeout is set", func(t *testing.T) {
		assert.Equal(t, 1*time.Hour, cfg.InactivityTimeout)
	})

	t.Run("team context sync interval is set", func(t *testing.T) {
		assert.Equal(t, 15*time.Second, cfg.TeamContextSyncInterval)
	})

	t.Run("socket check interval is set", func(t *testing.T) {
		assert.Equal(t, 30*time.Second, cfg.SocketCheckInterval)
	})
}

// TestDaemon_Stop_SetsRunningFalseBeforeCancel verifies that Stop() sets
// running=false before calling cancel(). This ordering is critical to prevent
// goroutines from seeing running=true after context is canceled, which can
// cause use-after-free type bugs where code continues operating on canceled
// resources.
func TestDaemon_Stop_SetsRunningFalseBeforeCancel(t *testing.T) {
	d := New(nil, nil)

	// simulate daemon in running state
	d.running = true
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel

	// channel to communicate result from observer goroutine
	resultCh := make(chan bool, 1)

	// observer goroutine that checks running state when context is canceled
	go func() {
		<-ctx.Done()
		d.mu.Lock()
		wasRunning := d.running
		d.mu.Unlock()
		resultCh <- wasRunning
	}()

	// call Stop
	err := d.Stop()
	require.NoError(t, err)

	// wait for observer to report
	select {
	case wasRunning := <-resultCh:
		assert.False(t, wasRunning,
			"running should be false when context is canceled to prevent race conditions")
	case <-time.After(time.Second):
		t.Fatal("observer goroutine did not complete")
	}
}

// TestDaemon_ConcurrentStop_NoRace tests that concurrent Stop() calls don't
// cause race conditions. Run with -race flag to detect issues.
func TestDaemon_ConcurrentStop_NoRace(t *testing.T) {
	d := New(nil, nil)
	d.running = true
	_, cancel := context.WithCancel(context.Background())
	d.cancel = cancel

	// call Stop concurrently
	const numGoroutines = 10
	done := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			done <- d.Stop()
		}()
	}

	// collect results - only one should succeed, rest should get ErrNotRunning
	successCount := 0
	notRunningCount := 0
	for i := 0; i < numGoroutines; i++ {
		err := <-done
		switch err {
		case nil:
			successCount++
		case ErrNotRunning:
			notRunningCount++
		}
	}

	// exactly one should succeed
	assert.Equal(t, 1, successCount, "exactly one Stop should succeed")
	assert.Equal(t, numGoroutines-1, notRunningCount, "others should get ErrNotRunning")
}
