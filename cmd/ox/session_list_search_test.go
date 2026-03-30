package main

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createSessionInDir creates a minimal session directory with a raw.jsonl header
// so the session store detects it as a valid session.
func createSessionInDir(t *testing.T, baseDir, sessionName string) {
	t.Helper()
	sessionDir := filepath.Join(baseDir, "sessions", sessionName)
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code","session_id":"test","started_at":"2026-01-01T00:00:00Z"}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(rawContent), 0644))
}

func TestSessionStore_FindsSessionInDirectory(t *testing.T) {
	t.Parallel()
	baseDir := t.TempDir()
	sessionName := "2026-01-01T00-00-testuser-OxFind"

	createSessionInDir(t, baseDir, sessionName)

	store, err := session.NewStore(baseDir)
	require.NoError(t, err)

	sessions, err := store.ListAllSessions()
	require.NoError(t, err)
	require.Len(t, sessions, 1, "store should find exactly one session")
	assert.Equal(t, sessionName, sessions[0].SessionName)
}

func TestSessionStore_ListsMultipleSessions(t *testing.T) {
	t.Parallel()
	baseDir := t.TempDir()

	names := []string{
		"2026-01-01T00-00-testuser-OxAAA1",
		"2026-01-02T00-00-testuser-OxBBB2",
		"2026-01-03T00-00-testuser-OxCCC3",
	}
	for _, name := range names {
		createSessionInDir(t, baseDir, name)
	}

	store, err := session.NewStore(baseDir)
	require.NoError(t, err)

	sessions, err := store.ListAllSessions()
	require.NoError(t, err)
	require.Len(t, sessions, 3, "store should find all 3 sessions")

	var found []string
	for _, s := range sessions {
		found = append(found, s.SessionName)
	}
	sort.Strings(found)
	sort.Strings(names)
	assert.Equal(t, names, found)
}

func TestSessionStore_ResolveSessionName_PartialMatch(t *testing.T) {
	t.Parallel()
	baseDir := t.TempDir()
	sessionName := "2026-01-01T00-00-testuser-OxABC1"

	createSessionInDir(t, baseDir, sessionName)

	store, err := session.NewStore(baseDir)
	require.NoError(t, err)

	resolved, err := store.ResolveSessionName("OxABC1")
	require.NoError(t, err)
	assert.Equal(t, sessionName, resolved, "partial suffix should resolve to full session name")
}

func TestSessionStore_ResolveSessionName_NotFound(t *testing.T) {
	t.Parallel()
	baseDir := t.TempDir()

	store, err := session.NewStore(baseDir)
	require.NoError(t, err)

	// ResolveSessionName returns the input unchanged when no match is found;
	// the resolved name simply won't correspond to an existing directory
	resolved, err := store.ResolveSessionName("nonexistent")
	require.NoError(t, err, "no match returns input as-is, not an error")
	assert.Equal(t, "nonexistent", resolved, "unmatched name should be returned unchanged")

	// verify the path doesn't actually exist
	sessionPath := store.GetSessionPath(resolved)
	_, statErr := os.Stat(sessionPath)
	assert.True(t, os.IsNotExist(statErr), "unresolved session path should not exist")
}

// TestSessionStore_DiscoversSessions verifies the Store contract:
// NewStore(baseDir) discovers all sessions under baseDir/sessions/<name>/.
// This is the foundation that runSessionList relies on for all 3 locations.
func TestSessionStore_DiscoversSessions(t *testing.T) {
	t.Parallel()
	baseDir := t.TempDir()

	names := []string{
		"2026-01-01T00-00-alice-OxAAA1",
		"2026-01-02T00-00-bob-OxBBB2",
		"2026-01-03T00-00-carol-OxCCC3",
	}
	for _, name := range names {
		createSessionInDir(t, baseDir, name)
	}

	store, err := session.NewStore(baseDir)
	require.NoError(t, err)

	sessions, err := store.ListAllSessions()
	require.NoError(t, err)
	require.Len(t, sessions, len(names), "store must discover all sessions in base dir")

	found := make(map[string]bool)
	for _, s := range sessions {
		found[s.SessionName] = true
	}
	for _, name := range names {
		assert.True(t, found[name], "session %s must be discoverable", name)
	}
}

// TestSessionStore_ConsistentNameAcrossLocations verifies that SessionInfo.SessionName
// is identical regardless of which base directory the store was created from.
// This invariant is what makes deduplication in runSessionList correct:
// runSessionList uses SessionName as the map key to deduplicate sessions found
// across XDG cache, ledger, and ledger cache directories.
func TestSessionStore_ConsistentNameAcrossLocations(t *testing.T) {
	t.Parallel()

	sessionName := "2026-01-15T10-00-testuser-OxSame"

	// same session in two different base dirs (simulating XDG cache vs ledger)
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	createSessionInDir(t, dir1, sessionName)
	createSessionInDir(t, dir2, sessionName)

	store1, err := session.NewStore(dir1)
	require.NoError(t, err)
	store2, err := session.NewStore(dir2)
	require.NoError(t, err)

	sessions1, err := store1.ListAllSessions()
	require.NoError(t, err)
	sessions2, err := store2.ListAllSessions()
	require.NoError(t, err)

	require.Len(t, sessions1, 1)
	require.Len(t, sessions2, 1)

	assert.Equal(t, sessions1[0].SessionName, sessions2[0].SessionName,
		"SessionName must be identical across stores for dedup to work -- "+
			"runSessionList uses SessionName as the dedup key")
}
