package daemon

// KB sync validation tests — mirror sync_team_validate_test.go for the
// kb pipeline. A bubble whose API row or on-disk state is malformed
// must be reported (logged + skipped or written safely) rather than
// crashing the daemon. The kb loop is one of three sources merged in
// internal/kb/merge.go and must not take the whole sync pass down.
//
// Already covered elsewhere — NOT here:
//   - empty kb_id → sync_bubbles_clone_test.go
//   - empty repo_url → sync_bubbles_clone_test.go
//   - corrupt .git → sync_bubbles_pull_test.go (CorruptRepoMovedAside)
//
// What's left, and what this file owns:
//   - unknown KBType (forward-compat with future server enums)
//   - meta.json atomicity (no torn writes, no temp leftovers)
//   - meta.json self-heal from a pre-existing corrupt file
//   - list-error containment (existing checkouts untouched)

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSyncBubbles_Validate_UnknownKBType_StillSyncs verifies that a
// kb_type the CLI doesn't recognize (server rolled out a new type
// before the daemon was updated) is treated as KBTypeUnknown and
// still syncs. Forward-compat per ADR-036.
//
// Failure prevented: a server adding a sixth KBType breaking every
// daemon in the field because the loop crashed on an unrecognized
// enum value.
func TestSyncBubbles_Validate_UnknownKBType_StillSyncs(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "unk", "u.md", "u\n")
	unk := api.KB{
		KBID:    "kb_unknown_type",
		KBType:  api.KBTypeUnknown, // forward-compat fallback bucket
		Slug:    "unknown-type",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{unk}}
	})

	s.syncBubbles(context.Background())

	target := paths.KBDir(unk.KBID)
	assert.DirExists(t, filepath.Join(target, ".git"),
		"KBTypeUnknown must still clone — forward-compat per ADR-036")

	// meta.json should record the unknown type verbatim so an upgraded
	// CLI can later promote it to its real type.
	metaPath := filepath.Join(target, ".sageox", "meta.json")
	require.FileExists(t, metaPath)
	raw, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	var generic map[string]any
	require.NoError(t, json.Unmarshal(raw, &generic))
	assert.Equal(t, "unknown", generic["type"],
		"unknown type must round-trip to disk so a future CLI can re-classify it")
}

// TestSyncBubbles_Validate_MetaJSONIsAtomicWrite verifies meta.json is
// never observed in a half-written state. Atomic-rename means a reader
// either sees the previous version or the new version, never a torn
// JSON. We assert no `meta.json.tmp-*` artifacts remain after multiple
// passes.
//
// Failure prevented: `ox status` or `ox doctor` reading meta.json
// mid-write and getting a JSON parse error, then surfacing a scary
// "your bubble metadata is corrupt" warning. The daemon's own write
// pipeline is supposed to use the tmp + rename pattern.
func TestSyncBubbles_Validate_MetaJSONIsAtomicWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "atomic", "a.md", "a\n")
	bubble := api.KB{
		KBID:        "kb_atomic",
		KBType:      api.KBTypePersonal,
		Slug:        "atomic",
		OwnerUserID: "user_atom",
		ViewerRole:  "owner",
		RepoURL:     "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	// run a few passes — every pass writes meta.json.
	for range 3 {
		s.syncBubbles(context.Background())
	}

	target := paths.KBDir(bubble.KBID)
	metaDir := filepath.Join(target, ".sageox")
	entries, err := os.ReadDir(metaDir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), "meta.json.tmp-",
			"no temp meta.json artifacts must remain after a successful pass")
	}

	// final meta.json must parse.
	raw, err := os.ReadFile(filepath.Join(metaDir, "meta.json"))
	require.NoError(t, err)
	var meta kbMeta
	require.NoError(t, json.Unmarshal(raw, &meta), "meta.json must be valid JSON")
	assert.Equal(t, api.KBTypePersonal, meta.Type)
	assert.Equal(t, "user_atom", meta.OwnerUserID)
}

// TestSyncBubbles_Validate_PreExistingCorruptMeta_Recovered verifies
// the loop overwrites a pre-existing corrupt meta.json without
// crashing. Self-healing: if a previous daemon (or a manual edit) left
// invalid JSON behind, the next pass produces a clean file.
//
// Failure prevented: a corrupt meta.json from a long-ago bug
// permanently wedging `ox status` because the daemon refused to
// overwrite "user state". Distinct from .git corruption (covered by
// sync_bubbles_pull_test.go) — this is *metadata* file corruption,
// which has a separate write path (writeKBMeta).
func TestSyncBubbles_Validate_PreExistingCorruptMeta_Recovered(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "corrupt_meta", "c.md", "c\n")
	bubble := api.KB{
		KBID:    "kb_corrupt_meta",
		KBType:  api.KBTypeTeam,
		Slug:    "corrupt-meta",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	// pass 1: clone + write meta.json.
	s.syncBubbles(context.Background())
	target := paths.KBDir(bubble.KBID)
	metaPath := filepath.Join(target, ".sageox", "meta.json")
	require.FileExists(t, metaPath)

	// inject corrupt JSON.
	require.NoError(t, os.WriteFile(metaPath, []byte("{not valid json"), 0o644))

	// pass 2: must overwrite cleanly.
	assert.NotPanics(t, func() {
		s.syncBubbles(context.Background())
	})

	raw, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	var meta kbMeta
	assert.NoError(t, json.Unmarshal(raw, &meta),
		"daemon must self-heal a corrupt meta.json, not refuse to touch it")
	assert.Equal(t, api.KBTypeTeam, meta.Type)
}

// TestSyncBubbles_Validate_ListErrorLeavesDiskUntouched verifies that
// when the lister returns a non-sentinel error mid-sync, the on-disk
// state of any previously-cloned bubble is left exactly as it was —
// every byte of meta.json must still parse, and no half-written
// updates must have leaked.
//
// Failure prevented: a regression that adds optimistic disk writes
// before list-error checks, leaving a bubble in a weird half-state
// when the API momentarily fails.
func TestSyncBubbles_Validate_ListErrorLeavesDiskUntouched(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	// pass 1: real bubble lands on disk.
	bareDir := makeBareRepo(t, "stable", "s.md", "s\n")
	bubble := api.KB{
		KBID:    "kb_stable",
		KBType:  api.KBTypeTeam,
		Slug:    "stable",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})
	s.syncBubbles(context.Background())
	target := paths.KBDir(bubble.KBID)
	require.DirExists(t, filepath.Join(target, ".git"))

	// snapshot meta.json bytes.
	metaPath := filepath.Join(target, ".sageox", "meta.json")
	before, err := os.ReadFile(metaPath)
	require.NoError(t, err)

	// pass 2: list errors.
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{err: errors.New("transient timeout")}
	})
	assert.NotPanics(t, func() {
		s.syncBubbles(context.Background())
	})

	// the existing checkout must be intact and meta.json must be
	// byte-identical (no spurious mutations).
	assert.DirExists(t, filepath.Join(target, ".git"))
	after, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	assert.Equal(t, before, after,
		"a list error must not mutate any existing bubble's meta.json")
}
