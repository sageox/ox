package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: doctor and status report nothing to do while sessions sit
// unpublished in the cache (GH #1095, #1077), or list one twice, or list a
// published one as waiting.
func TestListUnpublishedCacheSessions(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ledgerPath := t.TempDir()
	cache := filepath.Join(ledgerPath, ".sageox", "cache", "sessions")
	xdg := session.HeldSessionDirs(ledgerPath)[1]
	mk := func(base, name string) string {
		dir := filepath.Join(base, name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		writeTestRawJSONLWithEntries(t, filepath.Join(dir, ledgerFileRaw), 2)
		return dir
	}

	require.NoError(t, session.WriteHoldMarker(mk(cache, "2026-10-05T09-00-a-OxHeld"), session.HoldManualPublishing, "t"))
	mk(xdg, "2026-10-05T09-00-a-OxHeld") // unheld copy elsewhere: still one held entry
	mk(cache, "2026-10-05T09-01-a-OxWait")
	mk(cache, "2026-10-05T09-02-a-OxDrft")
	writeLedgerMeta(t, filepath.Join(ledgerPath, "sessions", "2026-10-05T09-02-a-OxDrft"), `{"version":"1.0","draft":true}`)
	mk(cache, "2026-10-05T09-03-a-OxPubl")
	writeLedgerMeta(t, filepath.Join(ledgerPath, "sessions", "2026-10-05T09-03-a-OxPubl"), `{"version":"1.0","title":"done"}`)
	live := mk(cache, "2026-10-05T09-04-a-OxLive")
	require.NoError(t, os.WriteFile(filepath.Join(live, ".recording.json"), []byte(`{}`), 0o644))
	empty := filepath.Join(cache, "2026-10-05T09-05-a-OxEmty")
	require.NoError(t, os.MkdirAll(empty, 0o755))
	writeTestRawJSONL(t, filepath.Join(empty, ledgerFileRaw))

	got := listUnpublishedCacheSessions(ledgerPath)

	assert.Equal(t, []string{"2026-10-05T09-00-a-OxHeld"}, got.Held)
	assert.Equal(t, []string{"2026-10-05T09-01-a-OxWait", "2026-10-05T09-02-a-OxDrft"}, got.Waiting)
}

// Failure prevented: a session stopped in manual mode before holds existed has
// no marker and is published by the daemon; doctor holds it, and only in
// manual mode. Doctor never publishes either kind.
func TestCheckHeldSessions_HoldsUnmarkedSessionsOnlyInManualMode(t *testing.T) {
	for _, mode := range []string{"manual", "auto"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			t.Setenv("OX_USER_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
			t.Setenv("OX_SESSION_PUBLISHING", mode)
			ledgerPath := t.TempDir()
			dir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", "2026-10-05T09-01-a-OxOld")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			writeTestRawJSONLWithEntries(t, filepath.Join(dir, ledgerFileRaw), 2)

			result, show := checkHeldSessions(t.TempDir(), ledgerPath)

			require.True(t, show)
			assert.Equal(t, mode == "manual", session.IsHeld(dir))
			assert.Equal(t, "info", result.priority, "information only")
			assert.Contains(t, result.detail, "ox session upload <name>")
			assert.NoDirExists(t, filepath.Join(ledgerPath, "sessions"), "doctor never publishes a held or waiting session")
		})
	}
}
