package daemon

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func newTestWorkspaceRegistry(t *testing.T) *WorkspaceRegistry {
	t.Helper()
	return NewWorkspaceRegistry(t.TempDir(), "test-repo")
}

func TestWorkspaceRegistry_GetLedger(t *testing.T) {
	t.Parallel()

	t.Run("nil when no ledger", func(t *testing.T) {
		t.Parallel()
		reg := newTestWorkspaceRegistry(t)
		assert.Nil(t, reg.GetLedger())
	})

	t.Run("returns copy", func(t *testing.T) {
		t.Parallel()
		reg := newTestWorkspaceRegistry(t)
		reg.ledger = &WorkspaceState{ID: "ledger", Path: "/original"}
		reg.workspaces["ledger"] = reg.ledger

		got := reg.GetLedger()
		assert.Equal(t, "/original", got.Path)

		// mutating the copy should not affect the store
		got.Path = "/tampered"
		assert.Equal(t, "/original", reg.GetLedger().Path)
	})
}

func TestWorkspaceRegistry_GetWorkspace(t *testing.T) {
	t.Parallel()

	reg := newTestWorkspaceRegistry(t)
	reg.workspaces["team_abc"] = &WorkspaceState{
		ID:       "team_abc",
		Type:     WorkspaceTypeTeamContext,
		TeamName: "alpha",
	}

	got := reg.GetWorkspace("team_abc")
	assert.NotNil(t, got)
	assert.Equal(t, "alpha", got.TeamName)

	assert.Nil(t, reg.GetWorkspace("nonexistent"))
}

func TestWorkspaceRegistry_GetTeamContexts(t *testing.T) {
	t.Parallel()

	reg := newTestWorkspaceRegistry(t)
	reg.workspaces["ledger"] = &WorkspaceState{ID: "ledger", Type: WorkspaceTypeLedger}
	reg.workspaces["team_1"] = &WorkspaceState{ID: "team_1", Type: WorkspaceTypeTeamContext, TeamName: "alpha"}
	reg.workspaces["team_2"] = &WorkspaceState{ID: "team_2", Type: WorkspaceTypeTeamContext, TeamName: "beta"}

	tcs := reg.GetTeamContexts()
	assert.Len(t, tcs, 2, "should only return team contexts, not ledger")

	names := map[string]bool{}
	for _, tc := range tcs {
		names[tc.TeamName] = true
	}
	assert.True(t, names["alpha"])
	assert.True(t, names["beta"])
}

func TestWorkspaceRegistry_GetAllWorkspaces(t *testing.T) {
	t.Parallel()

	reg := newTestWorkspaceRegistry(t)
	reg.workspaces["ledger"] = &WorkspaceState{ID: "ledger", Type: WorkspaceTypeLedger}
	reg.workspaces["team_1"] = &WorkspaceState{ID: "team_1", Type: WorkspaceTypeTeamContext}

	all := reg.GetAllWorkspaces()
	assert.Len(t, all, 2, "should return all workspaces")
}

func TestWorkspaceRegistry_SetAndClearWorkspaceError(t *testing.T) {
	t.Parallel()

	reg := newTestWorkspaceRegistry(t)
	reg.workspaces["ws1"] = &WorkspaceState{ID: "ws1"}

	reg.SetWorkspaceError("ws1", "sync failed: network error")
	assert.Equal(t, "sync failed: network error", reg.GetWorkspace("ws1").LastErr)

	reg.ClearWorkspaceError("ws1")
	assert.Empty(t, reg.GetWorkspace("ws1").LastErr)

	// no-op for nonexistent workspace (should not panic)
	reg.SetWorkspaceError("nonexistent", "error")
	reg.ClearWorkspaceError("nonexistent")
}

func TestWorkspaceRegistry_SetSyncInProgress(t *testing.T) {
	t.Parallel()

	reg := newTestWorkspaceRegistry(t)
	reg.workspaces["ws1"] = &WorkspaceState{ID: "ws1"}

	reg.SetSyncInProgress("ws1", true)
	ws := reg.GetWorkspace("ws1")
	assert.True(t, ws.SyncInProgress)
	assert.False(t, ws.LastSyncAttempt.IsZero(), "should set LastSyncAttempt when starting sync")

	reg.SetSyncInProgress("ws1", false)
	ws = reg.GetWorkspace("ws1")
	assert.False(t, ws.SyncInProgress)

	// no-op for nonexistent
	reg.SetSyncInProgress("nonexistent", true)
}

func TestWorkspaceRegistry_SyncIntervalMin(t *testing.T) {
	t.Parallel()

	reg := newTestWorkspaceRegistry(t)
	reg.workspaces["team_1"] = &WorkspaceState{ID: "team_1", Path: "/path/to/team"}

	// default is 0
	assert.Equal(t, 0, reg.GetSyncIntervalMin("/path/to/team"))

	reg.SetSyncIntervalMin("/path/to/team", 5)
	assert.Equal(t, 5, reg.GetSyncIntervalMin("/path/to/team"))

	// unknown path returns 0
	assert.Equal(t, 0, reg.GetSyncIntervalMin("/unknown"))
}

func TestWorkspaceRegistry_GCInterval(t *testing.T) {
	t.Parallel()

	reg := newTestWorkspaceRegistry(t)
	reg.workspaces["team_1"] = &WorkspaceState{ID: "team_1", Path: "/path/to/team"}

	// default is 7 when not set
	assert.Equal(t, 7, reg.GetGCInterval("/path/to/team"))

	reg.SetGCInterval("/path/to/team", 14)
	assert.Equal(t, 14, reg.GetGCInterval("/path/to/team"))

	// unknown path returns default 7
	assert.Equal(t, 7, reg.GetGCInterval("/unknown"))
}

func TestWorkspaceRegistry_LastGCTime(t *testing.T) {
	t.Parallel()

	reg := newTestWorkspaceRegistry(t)
	reg.workspaces["team_1"] = &WorkspaceState{ID: "team_1"}

	// initially zero
	assert.True(t, reg.GetLastGCTime("team_1").IsZero())

	reg.UpdateLastGC("team_1")
	assert.WithinDuration(t, time.Now(), reg.GetLastGCTime("team_1"), 2*time.Second)

	// unknown returns zero time
	assert.True(t, reg.GetLastGCTime("nonexistent").IsZero())
}

func TestWorkspaceRegistry_RecordSyncAttempt(t *testing.T) {
	t.Parallel()

	reg := newTestWorkspaceRegistry(t)
	reg.workspaces["ws1"] = &WorkspaceState{ID: "ws1"}

	reg.RecordSyncAttempt("ws1")
	ws := reg.GetWorkspace("ws1")
	assert.WithinDuration(t, time.Now(), ws.LastSyncAttempt, 2*time.Second)

	// no-op for nonexistent
	reg.RecordSyncAttempt("nonexistent")
}

func TestWorkspaceRegistry_GetRepoIDAndEndpoint(t *testing.T) {
	t.Parallel()

	reg := newTestWorkspaceRegistry(t)
	reg.repoID = "repo_abc123"
	reg.endpoint = "sageox.ai"

	assert.Equal(t, "repo_abc123", reg.GetRepoID())
	assert.Equal(t, "sageox.ai", reg.GetEndpoint())
}

func TestWorkspaceRegistry_GetLedgerPath(t *testing.T) {
	t.Parallel()

	t.Run("empty when no ledger", func(t *testing.T) {
		t.Parallel()
		reg := newTestWorkspaceRegistry(t)
		assert.Empty(t, reg.GetLedgerPath())
	})

	t.Run("returns path when ledger exists", func(t *testing.T) {
		t.Parallel()
		reg := newTestWorkspaceRegistry(t)
		reg.ledger = &WorkspaceState{ID: "ledger", Path: "/my/ledger"}
		assert.Equal(t, "/my/ledger", reg.GetLedgerPath())
	})
}

func TestWorkspaceRegistry_ProjectTeamID(t *testing.T) {
	t.Parallel()

	reg := newTestWorkspaceRegistry(t)
	reg.projectTeamID = "team_main"
	assert.Equal(t, "team_main", reg.ProjectTeamID())
}

func TestWorkspaceRegistry_SetLedgerCloneURL(t *testing.T) {
	t.Parallel()

	t.Run("returns false when no ledger", func(t *testing.T) {
		t.Parallel()
		reg := newTestWorkspaceRegistry(t)
		assert.False(t, reg.SetLedgerCloneURL("https://git.example.com/ledger.git"))
	})

	t.Run("sets clone URL", func(t *testing.T) {
		t.Parallel()
		reg := newTestWorkspaceRegistry(t)
		reg.ledger = &WorkspaceState{ID: "ledger", Type: WorkspaceTypeLedger}
		reg.workspaces["ledger"] = reg.ledger

		assert.True(t, reg.SetLedgerCloneURL("https://git.example.com/ledger.git"))
		assert.Equal(t, "https://git.example.com/ledger.git", reg.ledger.CloneURL)
	})
}

func TestWorkspaceRegistry_CloneRetry(t *testing.T) {
	t.Parallel()

	reg := newTestWorkspaceRegistry(t)
	reg.workspaces["team_1"] = &WorkspaceState{ID: "team_1"}

	// initially should allow clone
	assert.True(t, reg.ShouldRetryClone("team_1"))

	// set retry with future backoff
	future := time.Now().Add(1 * time.Hour)
	reg.SetCloneRetry("team_1", 2, future)

	attempts, next := reg.GetCloneRetryInfo("team_1")
	assert.Equal(t, 2, attempts)
	assert.Equal(t, future.Unix(), next.Unix())
	assert.False(t, reg.ShouldRetryClone("team_1"), "should not retry during backoff")

	// clear retry
	reg.ClearCloneRetry("team_1")
	attempts, next = reg.GetCloneRetryInfo("team_1")
	assert.Equal(t, 0, attempts)
	assert.True(t, next.IsZero())
	assert.True(t, reg.ShouldRetryClone("team_1"))
}

func TestWorkspaceRegistry_ShouldRetryClone_UnknownWorkspace(t *testing.T) {
	t.Parallel()
	reg := newTestWorkspaceRegistry(t)
	assert.True(t, reg.ShouldRetryClone("nonexistent"), "unknown workspace should allow clone")
}

func TestWorkspaceRegistry_GetTeamContextStatus(t *testing.T) {
	t.Parallel()

	reg := newTestWorkspaceRegistry(t)
	now := time.Now()
	reg.workspaces["ledger"] = &WorkspaceState{ID: "ledger", Type: WorkspaceTypeLedger}
	reg.workspaces["team_1"] = &WorkspaceState{
		ID:             "team_1",
		Type:           WorkspaceTypeTeamContext,
		TeamID:         "team_1",
		TeamName:       "alpha",
		Path:           "/path/team1",
		CloneURL:       "https://git.example.com/alpha.git",
		ConfigLastSync: now,
		LastErr:        "",
		Exists:         true,
	}

	statuses := reg.GetTeamContextStatus()
	assert.Len(t, statuses, 1)
	assert.Equal(t, "team_1", statuses[0].TeamID)
	assert.Equal(t, "alpha", statuses[0].TeamName)
	assert.True(t, statuses[0].Exists)
}

func TestWorkspaceRegistry_InvalidateConfigCache(t *testing.T) {
	t.Parallel()

	reg := newTestWorkspaceRegistry(t)
	reg.localConfigLoadedAt = time.Now()

	reg.InvalidateConfigCache()
	assert.True(t, reg.localConfigLoadedAt.IsZero(), "should reset loaded timestamp")
}

func TestWorkspaceRegistry_HasFetchHead(t *testing.T) {
	t.Parallel()

	t.Run("returns false for nonexistent workspace", func(t *testing.T) {
		t.Parallel()
		reg := newTestWorkspaceRegistry(t)
		exists, _ := reg.HasFetchHead("nonexistent")
		assert.False(t, exists)
	})

	t.Run("returns false for empty path", func(t *testing.T) {
		t.Parallel()
		reg := newTestWorkspaceRegistry(t)
		reg.workspaces["ws1"] = &WorkspaceState{ID: "ws1", Path: ""}
		exists, _ := reg.HasFetchHead("ws1")
		assert.False(t, exists)
	})
}

func TestWorkspaceRegistry_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	reg := newTestWorkspaceRegistry(t)
	reg.workspaces["ws1"] = &WorkspaceState{ID: "ws1", Path: "/test"}

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = reg.GetLedger()
			_ = reg.GetWorkspace("ws1")
			_ = reg.GetTeamContexts()
			_ = reg.GetAllWorkspaces()
			_ = reg.GetLedgerPath()
			_ = reg.GetRepoID()
			_ = reg.GetEndpoint()
			_ = reg.ProjectTeamID()
			_ = reg.GetSyncIntervalMin("/test")
			_ = reg.GetGCInterval("/test")
			_ = reg.GetLastGCTime("ws1")
			_ = reg.GetTeamContextStatus()
			reg.SetWorkspaceError("ws1", "err")
			reg.ClearWorkspaceError("ws1")
			reg.SetSyncInProgress("ws1", true)
			reg.SetSyncInProgress("ws1", false)
			reg.RecordSyncAttempt("ws1")
			reg.InvalidateConfigCache()
		}()
	}
	wg.Wait()
}
