package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A machine reaps only the drafts its own user owns. Another coworker's
// transcript may sit in THEIR local cache awaiting recovery, which this machine
// cannot see, so a stale heartbeat alone proves nothing about it.
func TestFindOrphanedDrafts_OnlyReapsLocalUsersDrafts(t *testing.T) {
	ledger := t.TempDir()
	const local = "ann"

	// Distinct ages keep the oldest-first ordering deterministic.
	cases := []struct {
		name, owner string
		age         time.Duration
	}{
		{"2026-01-01T00-00-" + local + "-OxMine1", local, 4 * OrphanedDraftAge},
		// The display name is deliberately identical to the local owner. It must
		// not authorize reaping another user's draft.
		{"2026-01-01T00-00-someoneelse-OxTheirs", local, 3 * OrphanedDraftAge},
		{"2026-01-01T00-00-ann-smith-OxPrefix", local, 3*OrphanedDraftAge + time.Hour},
		// Pre-attribution draft (no meta.username) owned by the local user: reaped via name slug.
		{"2026-01-01T00-00-" + local + "-OxLegacy", "", 2 * OrphanedDraftAge},
		// Pre-attribution draft named for another coworker: never reaped from here.
		{"2026-01-01T00-00-someoneelse-OxLegacyTheirs", "", OrphanedDraftAge + time.Hour},
	}
	for _, c := range cases {
		aged := time.Now().Add(-c.age).UTC()
		dir := filepath.Join(ledger, "sessions", c.name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
			Version: "1.0", SessionName: c.name, SessionID: "ses_owner_test", AgentID: c.name[strings.LastIndex(c.name, "-")+1:],
			Username: c.owner, CreatedAt: aged, Draft: true, UpdatedAt: &aged,
			Files: map[string]lfs.FileRef{},
		}))
	}

	orphans, err := FindOrphanedDrafts(ledger, nil, local)

	require.NoError(t, err)
	assert.Equal(t, []string{
		"2026-01-01T00-00-" + local + "-OxMine1",
		"2026-01-01T00-00-" + local + "-OxLegacy",
	}, orphans,
		"a stale draft owned by another coworker must not be reaped from this machine, "+
			"and a legacy draft must be reaped only when its name carries the local user")
}

func TestFindOrphanedDrafts_UnknownOwnerFailsClosed(t *testing.T) {
	ledger := t.TempDir()
	aged := time.Now().Add(-2 * OrphanedDraftAge).UTC()
	name := "2026-01-01T00-00-ann-OxOwner"
	dir := filepath.Join(ledger, "sessions", name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
		Version: "1.0", SessionName: name, AgentID: "OxOwner", Username: "ann",
		CreatedAt: aged, Draft: true, UpdatedAt: &aged, Files: map[string]lfs.FileRef{},
	}))

	for _, owner := range []string{"", "anonymous"} {
		orphans, err := FindOrphanedDrafts(ledger, nil, owner)
		require.NoError(t, err)
		assert.Empty(t, orphans, "unknown owner %q must reap nothing", owner)
	}
}
