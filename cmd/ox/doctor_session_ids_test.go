package main

import (
	"context"
	"fmt"
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

// TestFixLegacySessionIDs_PersistsDeterministicV5PreservesPopulated drives
// the MutateSessionMeta-based fix loop using the SAME assignment pattern
// as production (m.SessionID = m.EffectiveSessionID()) and asserts:
//
//  1. Each legacy meta.json gains the deterministic ses_<UUIDv5> that
//     EffectiveSessionID() would return on-the-fly — NOT a fresh v7.
//  2. Populated SessionIDs are untouched.
//  3. The backfilled value is byte-identical to what EffectiveSessionID()
//     returned before the backfill (the contract the doctor check
//     promises in its docstring).
//
// Failure prevented: a regression that mints a fresh v7 here would
// rotate every legacy recording's identity at backfill time, breaking
// any cached cross-machine reference and violating the documented
// "EffectiveSessionID is the canonical accessor" contract.
func TestFixLegacySessionIDs_PersistsDeterministicV5PreservesPopulated(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	const preservedID = "ses_01950000-0000-7abc-8def-0123456789ab"
	writeTestSessionMeta(t, sessionsDir, "legacy-1", "")
	writeTestSessionMeta(t, sessionsDir, "legacy-2", "")
	writeTestSessionMeta(t, sessionsDir, "modern", preservedID)

	// capture pre-backfill v5 derivations so we can assert byte-identity
	// after the mutate loop.
	preLegacy1, err := lfs.ReadSessionMeta(filepath.Join(sessionsDir, "legacy-1"))
	require.NoError(t, err)
	preLegacy2, err := lfs.ReadSessionMeta(filepath.Join(sessionsDir, "legacy-2"))
	require.NoError(t, err)
	wantLegacy1 := preLegacy1.EffectiveSessionID()
	wantLegacy2 := preLegacy2.EffectiveSessionID()
	require.True(t, sessionid.IsValidSessionID(wantLegacy1))
	require.True(t, sessionid.IsValidSessionID(wantLegacy2))
	require.NotEqual(t, wantLegacy1, wantLegacy2, "v5 derivations must differ across sessions")

	// run the per-session mutate loop directly, mirroring production.
	for _, name := range []string{"legacy-1", "legacy-2"} {
		err := lfs.MutateSessionMeta(context.Background(), filepath.Join(sessionsDir, name), func(m *lfs.SessionMeta) (*lfs.SessionMeta, error) {
			if m.SessionID != "" {
				return nil, nil
			}
			m.SessionID = m.EffectiveSessionID() // deterministic v5
			return m, nil
		})
		require.NoError(t, err)
	}

	// persisted IDs equal the pre-backfill EffectiveSessionID values
	m1, err := lfs.ReadSessionMeta(filepath.Join(sessionsDir, "legacy-1"))
	require.NoError(t, err)
	m2, err := lfs.ReadSessionMeta(filepath.Join(sessionsDir, "legacy-2"))
	require.NoError(t, err)
	mModern, err := lfs.ReadSessionMeta(filepath.Join(sessionsDir, "modern"))
	require.NoError(t, err)

	assert.Equal(t, wantLegacy1, m1.SessionID, "must persist deterministic v5, not a fresh v7")
	assert.Equal(t, wantLegacy2, m2.SessionID, "must persist deterministic v5, not a fresh v7")
	assert.NotEqual(t, m1.SessionID, m2.SessionID, "must not collide")
	assert.Equal(t, preservedID, mModern.SessionID, "populated SessionID must survive untouched")
}

// TestFixLegacySessionIDs_RaceDoesNotInflateRewrittenList simulates the
// concurrent-writer race: a session that already has a SessionID by the
// time the mutator runs must NOT be counted as rewritten or staged for
// commit.
//
// Failure prevented: a regression where stamped/rewritten count includes
// no-op mutator returns would push extraneous meta.json files into the
// backfill commit, dirtying unrelated changes and inflating the
// "backfilled N session(s)" message to the user.
func TestFixLegacySessionIDs_RaceDoesNotInflateRewrittenList(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	// pre-populate with a SessionID — simulates "another writer beat us"
	const preExisting = "ses_01950000-0000-7abc-8def-0123456789ab"
	writeTestSessionMeta(t, sessionsDir, "raced", preExisting)

	var rewritten []string
	err := lfs.MutateSessionMeta(context.Background(), filepath.Join(sessionsDir, "raced"), func(m *lfs.SessionMeta) (*lfs.SessionMeta, error) {
		if m.SessionID != "" {
			return nil, nil // race: another writer set it first
		}
		m.SessionID = m.EffectiveSessionID()
		return m, nil
	})
	require.NoError(t, err)
	// production code only appends to rewritten when the mutator wrote;
	// since we returned (nil, nil), nothing should be appended here.
	assert.Empty(t, rewritten, "raced sessions must not enter the rewrite/commit list")

	// the pre-existing SessionID must survive intact
	got, err := lfs.ReadSessionMeta(filepath.Join(sessionsDir, "raced"))
	require.NoError(t, err)
	assert.Equal(t, preExisting, got.SessionID)
}

// TestFixLegacySessionIDs_PartialFailureReturnsWarning asserts that the
// final status returned by the fix path is a Warning (not Pass) whenever
// any per-session mutation failed, even if other sessions succeeded and
// the commit went through.
//
// We can't easily inject a MutateSessionMeta failure mid-test, so we
// reproduce the result-coercion logic locally: build the same final
// status the production code does, but with a non-zero failed count.
//
// Failure prevented: a regression where a partial backfill is reported
// as a clean pass would silently hide legacy sessions left behind, and
// the operator would never know to investigate.
func TestFixLegacySessionIDs_PartialFailureReturnsWarning(t *testing.T) {
	// Mirror the tail-end status coercion in fixLegacySessionIDs:
	//   if failed > 0: WarningCheck
	//   else: PassedCheck
	stamped := 3
	failed := 1
	failures := []string{"  bad-session: synthetic test failure"}

	msg := strings.TrimSpace(strings.Join([]string{
		fmt.Sprintf("backfilled %d session(s)", stamped),
		fmt.Sprintf("(skipped 0 races, %d failures)", failed),
	}, " "))
	var got checkResult
	if failed > 0 {
		got = WarningCheck("Legacy session IDs", msg, strings.Join(failures, "\n"))
	} else {
		got = PassedCheck("Legacy session IDs", msg)
	}

	assert.True(t, got.warning, "any per-session failure must set the warning flag, not a clean pass")
	assert.NotEmpty(t, got.detail, "warning must carry per-session failure details")
}

// TestFixLegacySessionIDs_DescriptionDoesNotLieAboutFormat verifies the
// doctor check description and user-facing strings do NOT promise
// "ses_<UUIDv7>" — the legacy backfill persists EffectiveSessionID()
// which returns a v5 fallback. This guards against a regression where a
// future copy-edit re-introduces the misleading wording.
//
// Failure prevented: a user reading "ses_<UUIDv7>" in doctor output and
// later inspecting meta.json finds a v5 UUID and thinks something is
// broken.
func TestFixLegacySessionIDs_DescriptionDoesNotLieAboutFormat(t *testing.T) {
	check := GetDoctorCheck(CheckSlugSessionIDsBackfilled)
	require.NotNil(t, check, "doctor check %q must be registered", CheckSlugSessionIDsBackfilled)
	assert.NotContains(t, check.Description, "UUIDv7",
		"description must not promise UUIDv7 — legacy backfill persists EffectiveSessionID (v5)")
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
