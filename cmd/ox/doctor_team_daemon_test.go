package main

import (
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/stretchr/testify/assert"
)

// TestExtraTeamContextsFromStatus covers ox-baz5.5: checkTeamContextHealth's
// LFS nested-pointer scan used to walk only localCfg.TeamContexts — the
// repo's declared list — and silently never visit a team-context clone the
// daemon syncs for another reason (observed: a secondary "SageOx Internal"
// clone that was diverged and permanently dirty, while doctor's "Team
// Context" section reported clean in ~23ms because it never looked). This is
// the pure filtering logic behind daemonSyncedTeamContexts, tested without a
// live daemon IPC connection.
func TestExtraTeamContextsFromStatus(t *testing.T) {
	t.Run("daemon-synced context missing from configured is surfaced", func(t *testing.T) {
		status := &daemon.StatusData{
			Workspaces: map[string][]daemon.WorkspaceSyncStatus{
				"team-context": {
					{Path: "/configured/path", Exists: true, TeamID: "t1", TeamName: "Configured"},
					{Path: "/other/path", Exists: true, TeamID: "t2", TeamName: "Other Team"},
				},
			},
		}
		configured := []config.TeamContext{{Path: "/configured/path", TeamID: "t1"}}

		extra := extraTeamContextsFromStatus(status, configured)

		assert.Len(t, extra, 1, "must surface exactly the team context missing from configured")
		assert.Equal(t, "/other/path", extra[0].Path)
		assert.Equal(t, "t2", extra[0].TeamID)
		assert.Equal(t, "Other Team", extra[0].TeamName)
	})

	t.Run("fully configured daemon workspaces produce nothing extra", func(t *testing.T) {
		status := &daemon.StatusData{
			Workspaces: map[string][]daemon.WorkspaceSyncStatus{
				"team-context": {
					{Path: "/configured/path", Exists: true, TeamID: "t1"},
				},
			},
		}
		configured := []config.TeamContext{{Path: "/configured/path", TeamID: "t1"}}

		assert.Empty(t, extraTeamContextsFromStatus(status, configured),
			"must not re-surface a context the caller already scans")
	})

	t.Run("non-existent daemon workspace is skipped", func(t *testing.T) {
		// Exists=false means the daemon knows about the workspace but hasn't
		// cloned it yet — nothing on disk to scan for nested LFS pointers.
		status := &daemon.StatusData{
			Workspaces: map[string][]daemon.WorkspaceSyncStatus{
				"team-context": {
					{Path: "/not/cloned/yet", Exists: false, TeamID: "t3"},
				},
			},
		}
		assert.Empty(t, extraTeamContextsFromStatus(status, nil))
	})

	t.Run("no team-context key in workspaces produces nothing", func(t *testing.T) {
		status := &daemon.StatusData{Workspaces: map[string][]daemon.WorkspaceSyncStatus{
			"ledger": {{Path: "/ledger/path", Exists: true}},
		}}
		assert.Empty(t, extraTeamContextsFromStatus(status, nil))
	})

	t.Run("nil configured list still dedupes against itself", func(t *testing.T) {
		status := &daemon.StatusData{
			Workspaces: map[string][]daemon.WorkspaceSyncStatus{
				"team-context": {
					{Path: "/a", Exists: true, TeamID: "a"},
					{Path: "/b", Exists: true, TeamID: "b"},
				},
			},
		}
		extra := extraTeamContextsFromStatus(status, nil)
		assert.Len(t, extra, 2, "with nothing configured, every existing daemon-synced context is extra")
	})
}

// TestDaemonSyncedTeamContexts_NoDaemon proves the fail-safe direction: when
// no daemon is reachable (or ping times out), the function degrades to "no
// extra contexts" rather than erroring or blocking the doctor check it feeds.
// This scan is a best-effort supplement to the configured list, never a
// replacement — losing it must never make checkTeamContextHealth fail.
func TestDaemonSyncedTeamContexts_NoDaemon(t *testing.T) {
	// No daemon is running in the unit test sandbox (no XDG state dir with a
	// live socket for this repo), so Ping() fails and the function returns
	// nil without touching configured.
	got := daemonSyncedTeamContexts([]config.TeamContext{{Path: "/x"}})
	assert.Nil(t, got)
}
