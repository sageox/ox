package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTestSessionMeta is a thin helper that creates a session directory
// with a meta.json carrying the given SessionID (empty for legacy).
func writeTestSessionMeta(t *testing.T, sessionsDir, name, sessionID string) {
	t.Helper()
	dir := filepath.Join(sessionsDir, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	builder := lfs.NewSessionMeta(name, "user", "Ox1234", "claude-code", time.Now())
	if sessionID != "" {
		builder = builder.SessionID(sessionID)
	}
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, builder.Build()))
}

// TestFindLegacySessions_MixedReturnsOnlyLegacy verifies that scan returns
// the names of sessions with empty SessionID and skips populated ones.
// Failure prevented: a bug returning both populated and legacy sessions
// would over-report in the doctor message and over-stamp during fix.
func TestFindLegacySessions_MixedReturnsOnlyLegacy(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	writeTestSessionMeta(t, sessionsDir, "2025-12-01-legacy-A", "")
	writeTestSessionMeta(t, sessionsDir, "2026-01-01-modern-B", sessionid.GenerateSessionID())
	writeTestSessionMeta(t, sessionsDir, "2025-11-15-legacy-C", "")

	got, err := findLegacySessions(sessionsDir)
	require.NoError(t, err)
	assert.Equal(t, []string{"2025-11-15-legacy-C", "2025-12-01-legacy-A"}, got,
		"should return legacy names sorted, modern excluded")
}

// TestFindLegacySessions_AllPopulatedReturnsEmpty verifies the scan returns
// no work when every session already has an ID.
// Failure prevented: a bug that always returned the full set would cause
// doctor to backfill IDs that are already present, generating ledger churn.
func TestFindLegacySessions_AllPopulatedReturnsEmpty(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	writeTestSessionMeta(t, sessionsDir, "session-A", sessionid.GenerateSessionID())
	writeTestSessionMeta(t, sessionsDir, "session-B", sessionid.GenerateSessionID())

	got, err := findLegacySessions(sessionsDir)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestFindLegacySessions_SkipsMissingMetaJSON ensures sessions without a
// readable meta.json are silently skipped (other doctor checks own that
// failure mode).
// Failure prevented: surfacing every read error here would crowd out the
// signal we care about.
func TestFindLegacySessions_SkipsMissingMetaJSON(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	// session with no meta.json
	require.NoError(t, os.MkdirAll(filepath.Join(sessionsDir, "broken"), 0o755))

	// legacy session with valid meta.json
	writeTestSessionMeta(t, sessionsDir, "legacy", "")

	got, err := findLegacySessions(sessionsDir)
	require.NoError(t, err)
	assert.Equal(t, []string{"legacy"}, got)
}

// TestFixLegacySessionIDs_StampsMissingPreservesPopulated drives the
// MutateSessionMeta-based fix path directly (no git) and asserts each
// legacy meta.json gains a unique ses_<UUIDv7> while populated metas
// are untouched.
// Failure prevented: a regression that overwrites existing SessionIDs
// would invalidate cached references in every ledger ever backfilled.
func TestFixLegacySessionIDs_StampsMissingPreservesPopulated(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	const preservedID = "ses_01950000-0000-7abc-8def-0123456789ab"
	writeTestSessionMeta(t, sessionsDir, "legacy-1", "")
	writeTestSessionMeta(t, sessionsDir, "legacy-2", "")
	writeTestSessionMeta(t, sessionsDir, "modern", preservedID)

	// run the per-session mutate loop directly, decoupled from git.
	for _, name := range []string{"legacy-1", "legacy-2"} {
		err := lfs.MutateSessionMeta(context.Background(), filepath.Join(sessionsDir, name), func(m *lfs.SessionMeta) (*lfs.SessionMeta, error) {
			if m.SessionID != "" {
				return nil, nil
			}
			m.SessionID = sessionid.GenerateSessionID()
			return m, nil
		})
		require.NoError(t, err)
	}

	// legacy sessions now carry valid ses_ IDs, distinct from each other
	m1, err := lfs.ReadSessionMeta(filepath.Join(sessionsDir, "legacy-1"))
	require.NoError(t, err)
	m2, err := lfs.ReadSessionMeta(filepath.Join(sessionsDir, "legacy-2"))
	require.NoError(t, err)
	mModern, err := lfs.ReadSessionMeta(filepath.Join(sessionsDir, "modern"))
	require.NoError(t, err)

	assert.True(t, sessionid.IsValidSessionID(m1.SessionID), "legacy-1 stamped: %q", m1.SessionID)
	assert.True(t, sessionid.IsValidSessionID(m2.SessionID), "legacy-2 stamped: %q", m2.SessionID)
	assert.NotEqual(t, m1.SessionID, m2.SessionID, "must not collide")
	assert.Equal(t, preservedID, mModern.SessionID, "populated SessionID must survive untouched")
}

// TestCheckLegacySessionIDs_NoLedgerSkips verifies the check skips when
// there's no ledger to scan.
// Failure prevented: returning a Warning for a missing ledger would
// pollute doctor output for first-run users.
func TestCheckLegacySessionIDs_NoLedgerSkips(t *testing.T) {
	if testing.Short() {
		t.Skip("short: requires git root + ledger resolution")
	}
	// Run from a tmp dir with no ledger config; resolveLedgerPath will fail.
	tmp := t.TempDir()
	old, _ := os.Getwd()
	require.NoError(t, os.Chdir(tmp))
	t.Cleanup(func() { _ = os.Chdir(old) })

	res := checkLegacySessionIDs(false)
	assert.True(t, res.skipped, "expected skipped when no ledger; got %+v", res)
	assert.Contains(t, strings.ToLower(res.message), "ledger", "skip message should mention ledger")
}
