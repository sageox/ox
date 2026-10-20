package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A remote update must reach Team Context on the next check even when a prior
// fetch is fresh and an older server manifest asks for a multi-minute cooldown.
func TestTeamContext_RemoteUpdateBypassesFetchAge(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git fetch and pull operations")
	}

	for _, tc := range []struct {
		name     string
		manifest string
		minutes  int
	}{
		{name: "default"},
		{name: "legacy five minutes", manifest: "sync_interval_minutes 5\n", minutes: 5},
		{name: "one minute", manifest: "sync_interval_minutes 1\n", minutes: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateCredentials(t)
			bare := setupTeamContextBareRepo(t, "version 1\ninclude /**\n"+tc.manifest, nil)
			clone := filepath.Join(t.TempDir(), "team-context")
			gitInDir(t, t.TempDir(), "clone", bare, clone)
			gitConfig(t, clone)
			gitInDir(t, clone, "fetch", "origin")
			pushExtraCommit(t, bare, "discussion.md", "new discussion\n")
			localNotes := filepath.Join(clone, "memory", "notes.md")
			require.NoError(t, os.WriteFile(localNotes, []byte("local edits\n"), 0o644))

			scheduler := newTestScheduler(setupProjectWithConfig(t, ""))
			scheduler.config.TeamContextSyncInterval = DefaultConfig().TeamContextSyncInterval
			registry := scheduler.WorkspaceRegistry()
			registry.workspaces["team_test"] = &WorkspaceState{ID: "team_test", Path: clone}
			registry.SetSyncIntervalMin(clone, tc.minutes)
			require.Equal(t, tc.minutes, registry.GetSyncIntervalMin(clone))

			// Pin the freshness precondition without waiting for a real timer tick.
			now := time.Now()
			require.NoError(t, os.Chtimes(filepath.Join(clone, ".git", "FETCH_HEAD"), now, now))
			age, ok := gitutil.FetchHeadAge(clone)
			require.True(t, ok)
			require.Less(t, age, 15*time.Second)
			require.False(t, scheduler.remoteRefCheck(context.Background(), clone))

			outcome, err := scheduler.pullTeamContext(context.Background(), clone)
			require.NoError(t, err)
			require.True(t, outcome.PullRan, "a known remote update must not wait for FETCH_HEAD to age")
			body, err := os.ReadFile(filepath.Join(clone, "discussion.md"))
			require.NoError(t, err)
			assert.Equal(t, "new discussion\n", string(body))
			body, err = os.ReadFile(localNotes)
			require.NoError(t, err)
			assert.Equal(t, "local edits\n", string(body), "pull must preserve uncommitted edits")

			outcome, err = scheduler.pullTeamContext(context.Background(), clone)
			require.NoError(t, err)
			assert.False(t, outcome.PullRan, "an unchanged remote must still skip the pull")
		})
	}
}

// Removing Team Context's cooldown must not disable age dedup for other repos.
func TestManagedRepo_RecentFetchStillDeduplicates(t *testing.T) {
	clone := behindClone(t)
	gitInDir(t, clone, "fetch", "origin")
	now := time.Now()
	require.NoError(t, os.Chtimes(filepath.Join(clone, ".git", "FETCH_HEAD"), now, now))
	result := pullOnce(context.Background(), clone)
	require.NoError(t, result.Err)
	assert.True(t, result.Skipped)
	assert.Equal(t, skipReasonRecentlyFetched, result.SkipReason)
	assert.False(t, result.PullRan)
	assert.Equal(t, "v1\n", readF(t, clone))
}
