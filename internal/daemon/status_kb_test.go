package daemon

// Tests for kbWorkspaceStatus — the on-disk scanner that feeds the Knowledge
// Bubbles section of `ox daemon status`. The scanner must work in ANY daemon
// (owner or follower), so these tests exercise it without a global-sync lease
// and assert only on what's physically on disk.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/require"
)

// writeBubbleOnDisk materializes a bubble dir under the canonical kb root with
// an optional .git marker and meta.json, matching what reconcileBubble writes.
func writeBubbleOnDisk(t *testing.T, kbID string, hasGit bool, meta *kbMeta) {
	t.Helper()
	dir := paths.KBDir(endpoint.Get(), kbID)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".sageox"), 0o755))
	if hasGit {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	}
	if meta != nil {
		data, err := json.MarshalIndent(meta, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".sageox", "meta.json"), data, 0o644))
	}
}

// TestKBWorkspaceStatus_ReportsClonedBubbles verifies synced bubbles surface
// with their slug/type/last-sync read from meta.json.
// Failure prevented: `ox daemon status` shows no knowledge bubbles even though
// the daemon is actively syncing them to disk.
func TestKBWorkspaceStatus_ReportsClonedBubbles(t *testing.T) {
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	synced := time.Now().UTC().Truncate(time.Second)
	writeBubbleOnDisk(t, "kb_personal_1", true, &kbMeta{
		Type:     api.KBTypePersonal,
		Slug:     "my-notes",
		LastSync: synced,
	})
	writeBubbleOnDisk(t, "kb_team_2", true, &kbMeta{
		Type:     api.KBTypeTeam,
		Slug:     "frontend",
		LastSync: synced,
	})

	rows := s.kbWorkspaceStatus()
	require.Len(t, rows, 2)

	byID := map[string]WorkspaceSyncStatus{}
	for _, r := range rows {
		require.Equal(t, "kb", r.Type)
		byID[r.ID] = r
	}

	personal := byID["kb_personal_1"]
	require.True(t, personal.Exists)
	require.Equal(t, "my-notes", personal.Slug)
	require.Equal(t, string(api.KBTypePersonal), personal.KBType)
	require.Equal(t, synced, personal.LastSync.UTC())

	team := byID["kb_team_2"]
	require.Equal(t, "frontend", team.Slug)
	require.Equal(t, string(api.KBTypeTeam), team.KBType)
}

// TestKBWorkspaceStatus_MissingMetaStillListed verifies a clone whose meta.json
// hasn't been written yet (initial-clone pending) still appears, with a zero
// LastSync rather than being dropped.
// Failure prevented: a freshly-cloned bubble silently vanishes from status
// during the window before the daemon writes its first meta.json.
func TestKBWorkspaceStatus_MissingMetaStillListed(t *testing.T) {
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	writeBubbleOnDisk(t, "kb_pending_1", true, nil) // .git but no meta.json

	rows := s.kbWorkspaceStatus()
	require.Len(t, rows, 1)
	require.Equal(t, "kb_pending_1", rows[0].ID)
	require.True(t, rows[0].Exists)
	require.True(t, rows[0].LastSync.IsZero())
	require.Empty(t, rows[0].Slug)
}

// TestKBWorkspaceStatus_SkipsDotDirsAndNonClones verifies the scanner ignores
// the .trash/ sibling and reports a partial clone (no .git) as Exists=false.
// Failure prevented: GC's .trash/ holding area is mistaken for a real bubble,
// or a half-cloned dir is reported as a healthy checkout.
func TestKBWorkspaceStatus_SkipsDotDirsAndNonClones(t *testing.T) {
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	// .trash/ must be skipped
	require.NoError(t, os.MkdirAll(filepath.Join(paths.KBDir(endpoint.Get(), ""), ".trash", "kb_old"), 0o755))
	// a dir with no .git is a partial clone — listed but Exists=false
	writeBubbleOnDisk(t, "kb_partial_1", false, &kbMeta{Type: api.KBTypeRepo, Slug: "half"})

	rows := s.kbWorkspaceStatus()
	require.Len(t, rows, 1)
	require.Equal(t, "kb_partial_1", rows[0].ID)
	require.False(t, rows[0].Exists)
}

// TestKBWorkspaceStatus_EmptyStoreReturnsNil verifies a daemon with no kb store
// yet (fresh install) returns no rows rather than erroring.
// Failure prevented: status crashes or logs noise on machines that never
// synced a bubble.
func TestKBWorkspaceStatus_EmptyStoreReturnsNil(t *testing.T) {
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	require.Empty(t, s.kbWorkspaceStatus())
}

// TestFormatKBGroup_OwnerVsFollowerBadge verifies the section header tells the
// user whether THIS daemon syncs bubbles or a sibling does.
// Failure prevented: a follower daemon's status implies nothing is syncing the
// bubbles, when in fact the global-sync owner is keeping them fresh.
func TestFormatKBGroup_OwnerVsFollowerBadge(t *testing.T) {
	base := &StatusData{
		Workspaces: map[string][]WorkspaceSyncStatus{
			"kb": {{ID: "kb_1", Type: "kb", Slug: "notes", KBType: "personal", Exists: true, LastSync: time.Now()}},
		},
		GlobalSyncEndpoint: "staging.sageox.ai",
	}

	owner := *base
	owner.GlobalSyncOwner = true
	require.Contains(t, stripANSI(formatKBGroup(&owner, false)), "syncing here")

	follower := *base
	follower.GlobalSyncOwner = false
	out := stripANSI(formatKBGroup(&follower, false))
	require.Contains(t, out, "synced by another daemon")
	require.Contains(t, out, "staging.sageox.ai")
	require.Contains(t, out, "notes")

	// no bubbles -> empty section
	require.Empty(t, formatKBGroup(&StatusData{}, false))
}
