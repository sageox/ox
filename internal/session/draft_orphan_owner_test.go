package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A machine reaps only the drafts its own user owns. Another coworker's
// transcript may sit in THEIR local cache awaiting recovery, which this machine
// cannot see, so a stale heartbeat alone proves nothing about it.
func TestFindOrphanedDrafts_OnlyReapsLocalUsersDrafts(t *testing.T) {
	ledger := t.TempDir()
	local := identity.LocalAttributionUsername()

	// Distinct ages keep the oldest-first ordering deterministic.
	cases := []struct {
		name, owner string
		age         time.Duration
	}{
		{"2026-01-01T00-00-" + local + "-OxMine1", local, 4 * OrphanedDraftAge},
		{"2026-01-01T00-00-someoneelse-OxTheirs", "someoneelse", 3 * OrphanedDraftAge},
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
			Version: "1.0", SessionName: c.name, SessionID: "ses_owner_test", AgentID: "OxOwner",
			Username: c.owner, CreatedAt: aged, Draft: true, UpdatedAt: &aged,
			Files: map[string]lfs.FileRef{},
		}))
	}

	orphans, err := FindOrphanedDrafts(ledger, nil)

	require.NoError(t, err)
	assert.Equal(t, []string{
		"2026-01-01T00-00-" + local + "-OxMine1",
		"2026-01-01T00-00-" + local + "-OxLegacy",
	}, orphans,
		"a stale draft owned by another coworker must not be reaped from this machine, "+
			"and a legacy draft must be reaped only when its name carries the local user")
}
