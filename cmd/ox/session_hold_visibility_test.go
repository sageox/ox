package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: one `ox doctor` run publishes a session that an older ox
// stopped in manual mode (no hold yet): the upload retry ran before the step
// that holds such sessions (GH #1093).
func TestDoctor_HoldsUnmarkedManualSessionBeforeRetryingUploads(t *testing.T) {
	f := newDownloadLedgerFixture(t)
	t.Setenv("OX_SESSION_PUBLISHING", "")
	// As installed, the Ledger folder is named after the repo ID: that is how
	// the hold scan finds this repo's XDG cache.
	repoID := filepath.Base(f.ledgerPath)
	require.NoError(t, os.WriteFile(filepath.Join(f.projectRoot, ".sageox", "config.json"),
		[]byte(`{"config_version":"2","repo_id":"`+repoID+`","session_publishing":"manual"}`), 0o644))
	require.Equal(t, config.SessionPublishingManual, config.GetSessionPublishing(f.projectRoot))
	require.Equal(t, repoID, getRepoIDOrDefault(f.projectRoot))
	const name = "2026-10-01T10-00-devon-OxPrEh"
	cacheDir := filepath.Join(session.GetContextPath(getRepoIDOrDefault(f.projectRoot)), "sessions", name)
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	writeTestRawJSONLWithEntries(t, filepath.Join(cacheDir, ledgerFileRaw), 4)
	t.Chdir(f.projectRoot)
	remoteBefore := runGit(t, f.barePath, "rev-parse", "HEAD")

	checkSessionHealth(doctorOptions{})

	assert.Equal(t, remoteBefore, runGit(t, f.barePath, "rev-parse", "HEAD"), "doctor must not publish a manual-mode session")
	assert.True(t, session.IsHeld(cacheDir), "doctor holds it instead")
}

// Failure prevented: an AI coworker running agent doctor is told nothing about
// sessions the coworker keeps on this machine, or is nudged to publish them
// without being asked (GH #1095).
func TestBuildAgentDoctorOutput_ReportsHeldSessionsWithoutPublishing(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	projectRoot, ledgerPath := t.TempDir(), t.TempDir()
	runGit(t, projectRoot, "init")
	runGit(t, ledgerPath, "init")
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, ".sageox"), 0o755))
	require.NoError(t, config.SaveLocalConfig(projectRoot, &config.LocalConfig{Ledger: &config.LedgerConfig{Path: ledgerPath}}))
	t.Chdir(projectRoot)

	cache := filepath.Join(ledgerPath, ".sageox", "cache", "sessions")
	mk := func(name string) string {
		dir := filepath.Join(cache, name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		writeTestRawJSONLWithEntries(t, filepath.Join(dir, ledgerFileRaw), 2)
		return dir
	}
	const held, waiting = "2026-10-05T09-00-a-OxHeld", "2026-10-05T09-01-a-OxWait"
	require.NoError(t, session.WriteHoldMarker(mk(held), session.HoldManualPublishing, "test"))
	mk(waiting)

	out := buildAgentDoctorOutput("OxTest", projectRoot)

	assert.Equal(t, []string{held}, out.HeldSessions)
	assert.Equal(t, []string{waiting}, out.CacheOnlySessions)
	assert.Contains(t, out.NextSteps,
		"Held on this machine: "+held+". Publish only if the coworker asks: 'ox session upload "+held+"'")
	assert.True(t, session.IsHeld(filepath.Join(cache, held)), "reporting a hold never releases it")
	assert.NoDirExists(t, filepath.Join(ledgerPath, "sessions"), "agent doctor never publishes")
}

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
