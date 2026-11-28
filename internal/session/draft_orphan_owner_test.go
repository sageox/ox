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
	const localUserID = "user-local"

	// Distinct ages keep the oldest-first ordering deterministic.
	cases := []struct {
		name, userID, displayName string
		age                       time.Duration
	}{
		{"2026-01-01T00-00-ann-OxMine1", localUserID, "Private User", 4 * OrphanedDraftAge},
		// Display names and colliding username slugs cannot identify an owner.
		{"2026-01-01T00-00-ann-OxOther", "user-foreign", "Private User", 3 * OrphanedDraftAge},
		{"2026-01-01T00-00-ann-smith-OxPrefix", "user-foreign", "", 3*OrphanedDraftAge + time.Hour},
		// Old drafts without a stable user ID fail closed, even when their slug matches.
		{"2026-01-01T00-00-ann-OxLegacy", "", "", 2 * OrphanedDraftAge},
		{"2026-01-01T00-00-someoneelse-OxLegacyTheirs", "user-foreign", "", OrphanedDraftAge + time.Hour},
	}
	for _, c := range cases {
		aged := time.Now().Add(-c.age).UTC()
		dir := filepath.Join(ledger, "sessions", c.name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
			Version: "1.0", SessionName: c.name, SessionID: "ses_owner_test", AgentID: c.name[strings.LastIndex(c.name, "-")+1:],
			UserID: c.userID, Username: c.displayName, CreatedAt: aged, Draft: true, UpdatedAt: &aged,
			Files: map[string]lfs.FileRef{},
		}))
	}

	orphans, err := FindOrphanedDrafts(ledger, nil, localUserID)

	require.NoError(t, err)
	assert.Equal(t, []string{
		"2026-01-01T00-00-ann-OxMine1",
	}, orphans,
		"a stale draft owned by another coworker must not be reaped from this machine, "+
			"display names and username slugs do not authorize deletion; legacy drafts without user IDs fail closed")
}

func TestFindOrphanedDrafts_UnknownOwnerFailsClosed(t *testing.T) {
	ledger := t.TempDir()
	aged := time.Now().Add(-2 * OrphanedDraftAge).UTC()
	name := "2026-01-01T00-00-ann-OxOwner"
	dir := filepath.Join(ledger, "sessions", name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
		Version: "1.0", SessionName: name, AgentID: "OxOwner", UserID: "user-local", Username: "ann",
		CreatedAt: aged, Draft: true, UpdatedAt: &aged, Files: map[string]lfs.FileRef{},
	}))

	for _, ownerID := range []string{"", "user-other"} {
		orphans, err := FindOrphanedDrafts(ledger, nil, ownerID)
		require.NoError(t, err)
		assert.Empty(t, orphans, "unknown or different owner %q must reap nothing", ownerID)
	}
}

func TestFindOrphanedDrafts_RequiresAgentID(t *testing.T) {
	ledger := t.TempDir()
	aged := time.Now().Add(-2 * OrphanedDraftAge).UTC()
	name := "2026-01-01T00-00-ann-OxOwner"
	dir := filepath.Join(ledger, "sessions", name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
		Version: "1.0", SessionName: name, UserID: "user-local", Username: "ann",
		CreatedAt: aged, Draft: true, UpdatedAt: &aged, Files: map[string]lfs.FileRef{},
	}))

	orphans, err := FindOrphanedDrafts(ledger, nil, "user-local")
	require.NoError(t, err)
	assert.Empty(t, orphans)
}

// Ownership needs the stable user ID and a name of the shape this machine
// generates for that AI coworker. Each refusal below is a name another writer
// to the shared Ledger could commit to borrow a matching user ID's authority.
func TestDraftOwnedByUserID_RequiresTheGeneratedNameShape(t *testing.T) {
	meta := func(agentID string) *lfs.SessionMeta {
		return &lfs.SessionMeta{UserID: "user-local", AgentID: agentID}
	}
	for _, tt := range []struct {
		name, sessionName string
		meta              *lfs.SessionMeta
		want              bool
	}{
		{"generated name", "2026-01-01T00-00-ann-OxMine1", meta("OxMine1"), true},
		{"no metadata", "2026-01-01T00-00-ann-OxMine1", nil, false},
		{"agent ID that names nothing once sanitized", "2026-01-01T00-00-ann-OxMine1", meta("///"), false},
		{"only a timestamp", "2026-01-01T00-00", meta("OxMine1"), false},
		{"no separator after the timestamp", "2026-01-01T00-00_ann-OxMine1", meta("OxMine1"), false},
		{"not a timestamp", "2026-13-41T99-99-ann-OxMine1", meta("OxMine1"), false},
		{"another agent's suffix", "2026-01-01T00-00-ann-OxOther", meta("OxMine1"), false},
		{"no username before the agent ID", "2026-01-01T00-00--OxMine1", meta("OxMine1"), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, draftOwnedByUserID(tt.sessionName, tt.meta, "user-local"))
		})
	}
}
