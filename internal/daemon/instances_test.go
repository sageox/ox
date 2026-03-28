package daemon

import (
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testInstanceStore(t *testing.T) *InstanceStore {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewInstanceStore(logger)
}

func TestNewInstanceStore_NilLogger(t *testing.T) {
	t.Parallel()
	store := NewInstanceStore(nil)
	require.NotNil(t, store)
	assert.Equal(t, 0, store.Count())
}

func TestInstanceStore_RegisterAndGet(t *testing.T) {
	t.Parallel()
	store := testInstanceStore(t)

	inst := &Instance{
		ID:            "agent-1",
		AgentType:     "claude-code",
		WorkspacePath: "/tmp/project",
		ParentPID:     os.Getpid(),
	}
	store.Register(inst)

	assert.Equal(t, 1, store.Count())

	got := store.Get("agent-1")
	require.NotNil(t, got)
	assert.Equal(t, "agent-1", got.ID)
	assert.Equal(t, "claude-code", got.AgentType)
	assert.Equal(t, "/tmp/project", got.WorkspacePath)
	assert.Equal(t, StatusActive, got.Status, "freshly registered instance should be active")
}

func TestInstanceStore_RegisterNilAndEmpty(t *testing.T) {
	t.Parallel()
	store := testInstanceStore(t)

	// nil instance should be a no-op
	store.Register(nil)
	assert.Equal(t, 0, store.Count())

	// empty ID should be a no-op
	store.Register(&Instance{ID: ""})
	assert.Equal(t, 0, store.Count())
}

func TestInstanceStore_RegisterUpdatesExisting(t *testing.T) {
	t.Parallel()
	store := testInstanceStore(t)

	store.Register(&Instance{
		ID:            "agent-1",
		AgentType:     "claude-code",
		WorkspacePath: "/old/path",
	})

	// re-register same ID with new workspace
	store.Register(&Instance{
		ID:            "agent-1",
		AgentType:     "cursor",
		WorkspacePath: "/new/path",
		ParentPID:     12345,
	})

	// should still be 1 instance, not 2
	assert.Equal(t, 1, store.Count())

	got := store.Get("agent-1")
	require.NotNil(t, got)
	assert.Equal(t, "/new/path", got.WorkspacePath, "workspace path should be updated")
	assert.Equal(t, "cursor", got.AgentType, "agent type should be updated")
	assert.Equal(t, 12345, got.ParentPID, "parent PID should be updated")
}

func TestInstanceStore_GetReturnsNilForMissing(t *testing.T) {
	t.Parallel()
	store := testInstanceStore(t)

	assert.Nil(t, store.Get("nonexistent"))
	assert.Nil(t, store.Get(""))
}

func TestInstanceStore_GetReturnsCopy(t *testing.T) {
	t.Parallel()
	store := testInstanceStore(t)

	store.Register(&Instance{
		ID:        "agent-1",
		AgentType: "claude-code",
	})

	// mutating the returned copy should not affect the store
	got := store.Get("agent-1")
	got.AgentType = "tampered"

	got2 := store.Get("agent-1")
	assert.Equal(t, "claude-code", got2.AgentType, "Get should return a copy, not a reference")
}

func TestInstanceStore_Deregister(t *testing.T) {
	t.Parallel()
	store := testInstanceStore(t)

	store.Register(&Instance{ID: "agent-1"})
	store.Register(&Instance{ID: "agent-2"})
	assert.Equal(t, 2, store.Count())

	store.Deregister("agent-1")
	assert.Equal(t, 1, store.Count())
	assert.Nil(t, store.Get("agent-1"))
	assert.NotNil(t, store.Get("agent-2"))

	// deregistering nonexistent is a no-op
	store.Deregister("nonexistent")
	store.Deregister("")
	assert.Equal(t, 1, store.Count())
}

func TestInstanceStore_Heartbeat(t *testing.T) {
	t.Parallel()
	store := testInstanceStore(t)

	store.Register(&Instance{ID: "agent-1"})

	// record initial heartbeat time
	initial := store.Get("agent-1").LastHeartbeat

	// small sleep to ensure time advances
	time.Sleep(5 * time.Millisecond)

	store.Heartbeat("agent-1")
	updated := store.Get("agent-1").LastHeartbeat
	assert.True(t, updated.After(initial), "heartbeat should update timestamp")

	// heartbeat for nonexistent instance is a no-op
	store.Heartbeat("nonexistent")
	store.Heartbeat("")
}

func TestInstanceStore_GetActive(t *testing.T) {
	t.Parallel()
	store := testInstanceStore(t)

	now := time.Now()

	// active instance (recent heartbeat)
	store.Register(&Instance{
		ID:            "active-1",
		LastHeartbeat: now,
	})

	// stale instance (old heartbeat)
	store.Register(&Instance{
		ID:            "stale-1",
		LastHeartbeat: now.Add(-20 * time.Minute), // beyond StaleThreshold (15m)
	})

	active := store.GetActive()
	assert.Len(t, active, 1, "should exclude stale instances")
	assert.Equal(t, "active-1", active[0].ID)
}

func TestInstanceStore_GetStale(t *testing.T) {
	t.Parallel()
	store := testInstanceStore(t)

	now := time.Now()
	store.Register(&Instance{ID: "fresh", LastHeartbeat: now})
	store.Register(&Instance{ID: "stale-old", LastHeartbeat: now.Add(-20 * time.Minute)})
	store.Register(&Instance{ID: "stale-new", LastHeartbeat: now.Add(-10 * time.Minute)})

	stale := store.GetStale(6 * time.Minute)
	assert.Len(t, stale, 2)
	// should be sorted oldest first
	assert.Equal(t, "stale-old", stale[0].ID, "oldest stale first")
	assert.Equal(t, "stale-new", stale[1].ID)
}

func TestInstanceStore_GetAll(t *testing.T) {
	t.Parallel()
	store := testInstanceStore(t)

	now := time.Now()
	store.Register(&Instance{ID: "first", StartTime: now.Add(-2 * time.Hour)})
	store.Register(&Instance{ID: "second", StartTime: now.Add(-1 * time.Hour)})
	store.Register(&Instance{ID: "third", StartTime: now})

	all := store.GetAll()
	assert.Len(t, all, 3)
	// should be sorted by start time ascending (oldest first)
	assert.Equal(t, "first", all[0].ID)
	assert.Equal(t, "second", all[1].ID)
	assert.Equal(t, "third", all[2].ID)
}

func TestInstanceStore_ActiveCount(t *testing.T) {
	t.Parallel()
	store := testInstanceStore(t)

	now := time.Now()
	store.Register(&Instance{ID: "active", LastHeartbeat: now})
	store.Register(&Instance{ID: "stale", LastHeartbeat: now.Add(-20 * time.Minute)})

	assert.Equal(t, 2, store.Count(), "Count includes all instances")
	assert.Equal(t, 1, store.ActiveCount(), "ActiveCount excludes stale")
}

func TestInstanceStore_EvictionAtMaxCapacity(t *testing.T) {
	t.Parallel()
	store := testInstanceStore(t)

	now := time.Now()

	// fill to MaxInstances with stale instances
	for i := range MaxInstances {
		store.Register(&Instance{
			ID:            "inst-" + time.Duration(i).String(),
			LastHeartbeat: now.Add(-time.Duration(MaxInstances-i) * time.Minute), // older = higher index
		})
	}
	assert.Equal(t, MaxInstances, store.Count())

	// registering one more should evict the oldest stale
	store.Register(&Instance{
		ID:            "new-instance",
		LastHeartbeat: now,
	})
	assert.Equal(t, MaxInstances, store.Count(), "should not exceed MaxInstances")
	assert.NotNil(t, store.Get("new-instance"), "new instance should be registered")
}

func TestInstanceStore_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	store := testInstanceStore(t)

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := "agent-" + time.Duration(n).String()
			store.Register(&Instance{ID: id, AgentType: "test"})
			store.Heartbeat(id)
			_ = store.Get(id)
			_ = store.GetActive()
			_ = store.GetAll()
			_ = store.Count()
			_ = store.ActiveCount()
			store.Deregister(id)
		}(i)
	}
	wg.Wait()
}

func TestInstanceStore_StartCleanupAndStop(t *testing.T) {
	t.Parallel()
	store := testInstanceStore(t)

	store.StartCleanup()
	// stop should not hang or panic
	store.Stop()
}

func TestInstanceStore_StopWithoutStart(t *testing.T) {
	t.Parallel()
	store := testInstanceStore(t)
	// should not panic when stopCleanup is nil
	store.Stop()
}

func TestInstance_ComputeStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		elapsed  time.Duration
		expected string
	}{
		{"fresh heartbeat is active", 0, StatusActive},
		{"1m ago is active", 1 * time.Minute, StatusActive},
		{"4m59s ago is active", 4*time.Minute + 59*time.Second, StatusActive},
		{"5m ago is idle", 5 * time.Minute, StatusIdle},
		{"10m ago is idle", 10 * time.Minute, StatusIdle},
		{"14m59s ago is idle", 14*time.Minute + 59*time.Second, StatusIdle},
		{"15m ago is stale", 15 * time.Minute, StatusStale},
		{"1h ago is stale", 1 * time.Hour, StatusStale},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			now := time.Now()
			inst := &Instance{LastHeartbeat: now.Add(-tc.elapsed)}
			assert.Equal(t, tc.expected, inst.computeStatus(now))
		})
	}
}

func TestInstance_IsProcessAlive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		pid      int
		expected bool
	}{
		{"zero PID returns false", 0, false},
		{"negative PID returns false", -1, false},
		{"current process is alive", os.Getpid(), true},
		// PID 1 (init) should be alive on unix, but we don't own it
		// so signal(0) might fail depending on permissions
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inst := &Instance{ParentPID: tc.pid}
			assert.Equal(t, tc.expected, inst.IsProcessAlive())
		})
	}
}
