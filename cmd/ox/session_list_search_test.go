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

	// ResolveSessionName returns the input unchanged when no match is found
	// (caller gets a "not found" error when trying to use the path)
	resolved, err := store.ResolveSessionName("nonexistent")
	require.NoError(t, err, "no match returns input as-is, not an error")
	assert.Equal(t, "nonexistent", resolved, "unmatched name should be returned unchanged")

	// verify the path doesn't actually exist
	sessionPath := store.GetSessionPath(resolved)
	_, statErr := os.Stat(sessionPath)
	assert.True(t, os.IsNotExist(statErr), "unresolved session path should not exist")
}

// TestSessionListLocations_AllThreeSources validates that sessions from all 3
// storage locations (XDG cache, ledger/sessions, ledger/.sageox/cache/sessions)
// are discoverable using the same Store API that runSessionList uses.
func TestSessionListLocations_AllThreeSources(t *testing.T) {
	t.Parallel()

	// simulate the 3 locations as separate directories
	xdgCacheDir := t.TempDir()
	ledgerDir := t.TempDir()
	ledgerCacheDir := t.TempDir()

	// place a unique session in each location
	createSessionInDir(t, xdgCacheDir, "2026-01-01T00-00-testuser-OxXDG1")
	createSessionInDir(t, ledgerDir, "2026-01-02T00-00-testuser-OxLDG1")
	createSessionInDir(t, ledgerCacheDir, "2026-01-03T00-00-testuser-OxCAC1")

	// create stores for each location (mirrors runSessionList logic)
	xdgStore, err := session.NewStore(xdgCacheDir)
	require.NoError(t, err)
	ledgerStore, err := session.NewStore(ledgerDir)
	require.NoError(t, err)
	cacheStore, err := session.NewStore(ledgerCacheDir)
	require.NoError(t, err)

	// collect sessions from all stores (same dedup logic as runSessionList)
	allSessions, err := xdgStore.ListAllSessions()
	require.NoError(t, err)

	existing := make(map[string]bool)
	for _, s := range allSessions {
		existing[s.SessionName] = true
	}

	ledgerSessions, err := ledgerStore.ListAllSessions()
	require.NoError(t, err)
	for _, ls := range ledgerSessions {
		if !existing[ls.SessionName] {
			existing[ls.SessionName] = true
			allSessions = append(allSessions, ls)
		}
	}

	cacheSessions, err := cacheStore.ListAllSessions()
	require.NoError(t, err)
	for _, cs := range cacheSessions {
		if !existing[cs.SessionName] {
			existing[cs.SessionName] = true
			allSessions = append(allSessions, cs)
		}
	}

	require.Len(t, allSessions, 3, "should find sessions from all 3 locations")

	var names []string
	for _, s := range allSessions {
		names = append(names, s.SessionName)
	}
	sort.Strings(names)
	assert.Equal(t, []string{
		"2026-01-01T00-00-testuser-OxXDG1",
		"2026-01-02T00-00-testuser-OxLDG1",
		"2026-01-03T00-00-testuser-OxCAC1",
	}, names)
}

// TestSessionListLocations_DeduplicatesAcrossStores verifies that when the same
// session name exists in multiple store locations, it appears exactly once after
// deduplication (matching runSessionList behavior).
func TestSessionListLocations_DeduplicatesAcrossStores(t *testing.T) {
	t.Parallel()

	dir1 := t.TempDir()
	dir2 := t.TempDir()

	// same session name in both locations
	duplicateName := "2026-01-01T00-00-testuser-OxDUP1"
	createSessionInDir(t, dir1, duplicateName)
	createSessionInDir(t, dir2, duplicateName)

	store1, err := session.NewStore(dir1)
	require.NoError(t, err)
	store2, err := session.NewStore(dir2)
	require.NoError(t, err)

	// collect with dedup (same pattern as runSessionList)
	sessions, err := store1.ListAllSessions()
	require.NoError(t, err)

	existing := make(map[string]bool)
	for _, s := range sessions {
		existing[s.SessionName] = true
	}

	store2Sessions, err := store2.ListAllSessions()
	require.NoError(t, err)
	for _, s := range store2Sessions {
		if !existing[s.SessionName] {
			existing[s.SessionName] = true
			sessions = append(sessions, s)
		}
	}

	require.Len(t, sessions, 1, "duplicate session name should appear exactly once")
	assert.Equal(t, duplicateName, sessions[0].SessionName)
}
