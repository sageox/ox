package daemon

// KB sync state tests — verify the persistent on-disk state for kb
// sync (meta.json) survives a daemon restart and is the source of
// truth for "when did this bubble last sync". Mirrors the
// SyncState load/save tests for ledgers (sync_state_test.go),
// applied to the kb meta.json surface.
//
// Why meta.json (not SyncState): The ledger has a dedicated
// .sageox/cache/sync-state.json for derived sync timing. Bubbles
// piggyback their sync timing on the canonical .sageox/meta.json
// inside the bubble checkout (last_sync field). That's the file an
// external `ox doctor` consults to detect stale clones, so its
// round-trip across restarts is what matters here.

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSyncBubbles_State_PersistsAcrossSchedulerRestart verifies the
// daemon's kb sync state (meta.json) survives a scheduler restart:
// stop the scheduler, build a new one, and the second scheduler must
// see the bubble as already-cloned (no re-clone) and read back the
// last_sync stamp.
//
// Failure prevented: the daemon re-cloning every bubble on every
// restart because state lived only in memory. Repeat-clones are
// expensive and trip flaky-network sites unnecessarily.
func TestSyncBubbles_State_PersistsAcrossSchedulerRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	bareDir := makeBareRepo(t, "persist", "p.md", "v1\n")
	bubble := api.KB{
		KBID:        "kb_persist",
		KBType:      api.KBTypeTeam,
		Slug:        "persist",
		OwnerUserID: "u1",
		ViewerRole:  "member",
		RepoURL:     "file://" + bareDir,
	}

	// First scheduler: clone + write meta.
	s1, _ := kbTestScheduler(t)
	s1.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})
	s1.syncBubbles(context.Background())

	target := paths.KBDir(endpoint.Get(), bubble.KBID)
	metaPath := filepath.Join(target, ".sageox", "meta.json")
	require.FileExists(t, metaPath)
	beforeRaw, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	var before kbMeta
	require.NoError(t, json.Unmarshal(beforeRaw, &before))
	require.False(t, before.LastSync.IsZero())

	// "Restart" — build a fresh scheduler. The kb dir is already on
	// disk under the same XDG layout (test env is unchanged).
	s2, _ := kbTestScheduler(t)
	s2.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	// nudge FETCH_HEAD past the dedup threshold so the new scheduler
	// will actually pull (and update meta.json's last_sync).
	fetchHead := filepath.Join(target, ".git", "FETCH_HEAD")
	if info, err := os.Stat(fetchHead); err == nil {
		past := info.ModTime().Add(-10 * time.Minute)
		_ = os.Chtimes(fetchHead, past, past)
	}

	s2.syncBubbles(context.Background())

	// the .git dir must be the same (no re-clone — the original git
	// dir's timestamps would change wholesale on a re-clone). We don't
	// have a clean way to fingerprint, but we *can* assert no .bak.*
	// sibling appeared (which is what the corrupt-repo move-aside
	// branch would produce on a forced re-clone).
	parent := filepath.Dir(target)
	entries, err := os.ReadDir(parent)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".bak.",
			"restart must not move-aside a healthy bubble")
	}

	// meta.json's last_sync must have advanced (or at least be set).
	afterRaw, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	var after kbMeta
	require.NoError(t, json.Unmarshal(afterRaw, &after))
	assert.False(t, after.LastSync.IsZero())
	assert.True(t, !after.LastSync.Before(before.LastSync),
		"second scheduler's last_sync must not regress (got %v before %v)", after.LastSync, before.LastSync)

	// the immutable identity fields must round-trip exactly.
	assert.Equal(t, before.Type, after.Type)
	assert.Equal(t, before.Slug, after.Slug)
	assert.Equal(t, before.OwnerUserID, after.OwnerUserID)
	assert.Equal(t, before.ViewerRole, after.ViewerRole)
}

// TestSyncBubbles_State_UpdatesAfterPull verifies that meta.json's
// last_sync stamp advances after each successful pull, so external
// tools (`ox status`, `ox doctor`) can detect a stale bubble.
//
// Failure prevented: meta.json being written once at clone time and
// never updated again, leading `ox doctor` to perpetually report
// "stale sync" even when the daemon is faithfully pulling.
func TestSyncBubbles_State_UpdatesAfterPull(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	s, _ := kbTestScheduler(t)
	bareDir := makeBareRepo(t, "stamp", "x.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_stamp",
		KBType:  api.KBTypeTeam,
		Slug:    "stamp",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	// pass 1: clone + write meta (last_sync = T1).
	s.syncBubbles(context.Background())
	target := paths.KBDir(endpoint.Get(), bubble.KBID)
	metaPath := filepath.Join(target, ".sageox", "meta.json")
	raw, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	var m1 kbMeta
	require.NoError(t, json.Unmarshal(raw, &m1))
	t1 := m1.LastSync
	require.False(t, t1.IsZero())

	// push a remote commit and nudge FETCH_HEAD into the past.
	pushExtraCommit(t, bareDir, "x.md", "v2\n")
	fetchHead := filepath.Join(target, ".git", "FETCH_HEAD")
	if info, err := os.Stat(fetchHead); err == nil {
		past := info.ModTime().Add(-10 * time.Minute)
		_ = os.Chtimes(fetchHead, past, past)
	}

	// give the wall clock a beat so T2 > T1 even on fast machines.
	time.Sleep(2 * time.Millisecond)

	// pass 2: pull lands the new commit + writes new last_sync.
	s.syncBubbles(context.Background())

	raw, err = os.ReadFile(metaPath)
	require.NoError(t, err)
	var m2 kbMeta
	require.NoError(t, json.Unmarshal(raw, &m2))
	assert.True(t, m2.LastSync.After(t1),
		"last_sync must advance after a successful pull: t1=%v t2=%v", t1, m2.LastSync)
}

// TestSyncBubbles_State_ManualMetaDeletion_Recovered verifies that an
// external process deleting meta.json (e.g., user ran a cleanup script,
// or another daemon clobbered it) is healed on the next pass — the
// daemon writes a fresh meta.json from the API row.
//
// Failure prevented: a missing meta.json wedging the daemon into "I
// have no record of this bubble" and re-cloning unnecessarily, or
// worse, refusing to touch the dir at all.
func TestSyncBubbles_State_ManualMetaDeletion_Recovered(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	s, _ := kbTestScheduler(t)
	bareDir := makeBareRepo(t, "metadel", "z.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_metadel",
		KBType:  api.KBTypePersonal,
		Slug:    "metadel",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	// pass 1: clone + meta.
	s.syncBubbles(context.Background())
	target := paths.KBDir(endpoint.Get(), bubble.KBID)
	metaPath := filepath.Join(target, ".sageox", "meta.json")
	require.FileExists(t, metaPath)

	// delete meta.json out from under the daemon.
	require.NoError(t, os.Remove(metaPath))

	// pass 2: must regenerate.
	s.syncBubbles(context.Background())
	assert.FileExists(t, metaPath, "deleted meta.json must be regenerated on next pass")

	raw, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	var m kbMeta
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.Equal(t, api.KBTypePersonal, m.Type)
	assert.Equal(t, "metadel", m.Slug)
}

// TestSyncBubbles_State_MetaSurvivesScheduler_Without_Project verifies
// that meta.json on disk is independent of any specific scheduler's
// in-memory state. Specifically: a scheduler with no project root (a
// no-op kb sync), then a real scheduler — the second one should still
// honor the on-disk meta.json from any prior pass.
//
// Failure prevented: a regression where the meta.json schema gets
// coupled to per-scheduler state, breaking restarts when two daemons
// (e.g., dev container + host) share the XDG store.
func TestSyncBubbles_State_MetaSurvivesScheduler_Without_Project(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	bareDir := makeBareRepo(t, "share", "s.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_share",
		KBType:  api.KBTypeTeam,
		Slug:    "share",
		RepoURL: "file://" + bareDir,
	}

	// scheduler 1: real, clones the bubble + writes meta.
	s1, _ := kbTestScheduler(t)
	s1.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})
	s1.syncBubbles(context.Background())

	target := paths.KBDir(endpoint.Get(), bubble.KBID)
	metaPath := filepath.Join(target, ".sageox", "meta.json")
	require.FileExists(t, metaPath)
	beforeRaw, err := os.ReadFile(metaPath)
	require.NoError(t, err)

	// scheduler 2: no project root — kb sync is a silent no-op.
	cfg := DefaultConfig()
	cfg.ProjectRoot = ""
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	s2 := NewSyncScheduler(cfg, logger)
	s2.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})
	s2.syncBubbles(context.Background())

	// meta.json must still be there, byte-identical to the snapshot.
	afterRaw, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	assert.Equal(t, beforeRaw, afterRaw,
		"a scheduler with no project root must not touch existing meta.json")
}
