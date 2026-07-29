package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTestSessionRaw creates a session directory (if needed) and writes a
// production-shaped raw.jsonl header via the real session.Store writer, so
// the fixture matches exactly what recording actually produces. Pass "" for
// sessionID to reproduce a legacy header that predates ID-at-start
// (2026-07-18, 85f59500) — the SessionID field is omitempty, so it's simply
// absent from the header, not present-but-empty.
func writeTestSessionRaw(t *testing.T, ledgerPath, name, sessionID string) {
	t.Helper()
	store, err := session.NewStore(ledgerPath)
	require.NoError(t, err)
	w, err := store.CreateRaw(name)
	require.NoError(t, err)
	require.NoError(t, w.WriteHeader(&session.StoreMeta{
		Version:   "1.0",
		CreatedAt: time.Now(),
		SessionID: sessionID,
		AgentType: "claude-code",
		Username:  "user",
	}))
	require.NoError(t, w.Close())
}

// --- A. scanSessionIDDivergence: the states this check must tell apart ---

// TestScanSessionIDDivergence_AgreeingIDsPass verifies a session whose
// meta.json and raw.jsonl header carry the SAME session_id is counted as
// checked and never reported as diverged.
// Failure prevented: an off-by-something comparison flagging every healthy
// session as diverged would make the check useless noise from day one.
func TestScanSessionIDDivergence_AgreeingIDsPass(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	id := sessionid.GenerateSessionID()
	writeTestSessionMeta(t, sessionsDir, "healthy", id)
	writeTestSessionRaw(t, tmp, "healthy", id)

	result, err := scanSessionIDDivergence(sessionsDir)
	require.NoError(t, err)
	assert.Equal(t, 1, result.checked)
	assert.Empty(t, result.diverged)
	assert.Empty(t, result.unreadable)
}

// TestScanSessionIDDivergence_DivergedIDsReported is the core failure mode
// this check exists for: meta.json and the raw.jsonl header disagree.
// Failure prevented: silently missing this is exactly the wedge that let a
// two-writer race mint different IDs for one session and go undetected.
func TestScanSessionIDDivergence_DivergedIDsReported(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	metaID := sessionid.GenerateSessionID()
	headerID := sessionid.GenerateSessionID()
	require.NotEqual(t, metaID, headerID)
	writeTestSessionMeta(t, sessionsDir, "wedged", metaID)
	writeTestSessionRaw(t, tmp, "wedged", headerID)

	result, err := scanSessionIDDivergence(sessionsDir)
	require.NoError(t, err)
	assert.Equal(t, 1, result.checked)
	require.Len(t, result.diverged, 1)
	assert.Equal(t, "wedged", result.diverged[0].Name)
	assert.Equal(t, metaID, result.diverged[0].MetaID)
	assert.Equal(t, headerID, result.diverged[0].HeaderID)
	assert.Empty(t, result.unreadable)
}

// TestScanSessionIDDivergence_MetaJSONAbsentSkipped verifies a session
// directory with only raw.jsonl (no meta.json yet — still mid-upload) is
// silently skipped, not reported.
// Failure prevented: flagging every in-flight upload as "diverged" or
// "unreadable" would drown the real signal and duplicate
// session-upload-retry's territory.
func TestScanSessionIDDivergence_MetaJSONAbsentSkipped(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	writeTestSessionRaw(t, tmp, "no-meta-yet", sessionid.GenerateSessionID())

	result, err := scanSessionIDDivergence(sessionsDir)
	require.NoError(t, err)
	assert.Zero(t, result.checked)
	assert.Empty(t, result.diverged)
	assert.Empty(t, result.unreadable)
}

// TestScanSessionIDDivergence_RawJSONLAbsentSkipped verifies a committed
// session directory with meta.json but no raw.jsonl (e.g. content pruned,
// or a directory doctor's other checks haven't finished populating) is
// skipped rather than treated as corrupt.
func TestScanSessionIDDivergence_RawJSONLAbsentSkipped(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	writeTestSessionMeta(t, sessionsDir, "no-raw", sessionid.GenerateSessionID())

	result, err := scanSessionIDDivergence(sessionsDir)
	require.NoError(t, err)
	assert.Zero(t, result.checked)
	assert.Empty(t, result.diverged)
	assert.Empty(t, result.unreadable)
}

// TestScanSessionIDDivergence_BothAbsentSkipped verifies an empty session
// directory (neither file present) does not crash the scan and produces no
// report of any kind.
func TestScanSessionIDDivergence_BothAbsentSkipped(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	require.NoError(t, os.MkdirAll(filepath.Join(sessionsDir, "empty"), 0o755))

	result, err := scanSessionIDDivergence(sessionsDir)
	require.NoError(t, err)
	assert.Zero(t, result.checked)
	assert.Empty(t, result.diverged)
	assert.Empty(t, result.unreadable)
}

// TestScanSessionIDDivergence_LegacyHeaderNoIDNotReported is the explicit
// regression guard for the EXPECTED-not-broken case called out in the
// task: raw.jsonl headers written before ID-at-start (2026-07-18,
// 85f59500) carry no session_id at all. meta.json may still have a
// (later-minted) ID. This must never be reported as a divergence or as
// corruption.
func TestScanSessionIDDivergence_LegacyHeaderNoIDNotReported(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	writeTestSessionMeta(t, sessionsDir, "pre-rollout-header", sessionid.GenerateSessionID())
	writeTestSessionRaw(t, tmp, "pre-rollout-header", "") // legacy: no session_id field

	result, err := scanSessionIDDivergence(sessionsDir)
	require.NoError(t, err)
	assert.Zero(t, result.checked, "not comparable — must not count toward checked")
	assert.Empty(t, result.diverged)
	assert.Empty(t, result.unreadable)
}

// TestScanSessionIDDivergence_LegacyMetaNoIDNotReported mirrors the above
// for the other carrier: meta.json predates the session_id field (owned by
// doctor_session_ids.go's opt-in backfill), while the header has a valid
// ID. Must not be reported here either.
func TestScanSessionIDDivergence_LegacyMetaNoIDNotReported(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	writeTestSessionMeta(t, sessionsDir, "pre-rollout-meta", "") // legacy: no SessionID field
	writeTestSessionRaw(t, tmp, "pre-rollout-meta", sessionid.GenerateSessionID())

	result, err := scanSessionIDDivergence(sessionsDir)
	require.NoError(t, err)
	assert.Zero(t, result.checked)
	assert.Empty(t, result.diverged)
	assert.Empty(t, result.unreadable)
}

// TestScanSessionIDDivergence_CorruptMetaJSONReportedUnreadable verifies a
// meta.json that fails to parse is surfaced in `unreadable`, never silently
// treated as "fine" (CLAUDE.md: "missing values are as broken as wrong
// values").
func TestScanSessionIDDivergence_CorruptMetaJSONReportedUnreadable(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	dir := filepath.Join(sessionsDir, "corrupt-meta")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte("{not valid json"), 0o644))
	writeTestSessionRaw(t, tmp, "corrupt-meta", sessionid.GenerateSessionID())

	result, err := scanSessionIDDivergence(sessionsDir)
	require.NoError(t, err)
	assert.Zero(t, result.checked)
	assert.Empty(t, result.diverged)
	require.Len(t, result.unreadable, 1)
	assert.Contains(t, result.unreadable[0], "corrupt-meta")
	assert.Contains(t, result.unreadable[0], "meta.json")
}

// TestScanSessionIDDivergence_CorruptRawHeaderReportedUnreadable verifies a
// raw.jsonl whose first line is not valid JSON is surfaced in `unreadable`,
// not folded into "legacy, no ID."
func TestScanSessionIDDivergence_CorruptRawHeaderReportedUnreadable(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	dir := filepath.Join(sessionsDir, "corrupt-raw")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	writeTestSessionMeta(t, sessionsDir, "corrupt-raw", sessionid.GenerateSessionID())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte("not json at all\n"), 0o644))

	result, err := scanSessionIDDivergence(sessionsDir)
	require.NoError(t, err)
	assert.Zero(t, result.checked)
	assert.Empty(t, result.diverged)
	require.Len(t, result.unreadable, 1)
	assert.Contains(t, result.unreadable[0], "corrupt-raw")
	assert.Contains(t, result.unreadable[0], "raw.jsonl")
}

// TestScanSessionIDDivergence_EmptyRawFileReportedUnreadable verifies a
// zero-byte raw.jsonl (truncated write, disk full, crash mid-write) is
// surfaced as unreadable rather than silently treated as legacy.
func TestScanSessionIDDivergence_EmptyRawFileReportedUnreadable(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	dir := filepath.Join(sessionsDir, "empty-raw")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	writeTestSessionMeta(t, sessionsDir, "empty-raw", sessionid.GenerateSessionID())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(""), 0o644))

	result, err := scanSessionIDDivergence(sessionsDir)
	require.NoError(t, err)
	require.Len(t, result.unreadable, 1)
	assert.Contains(t, result.unreadable[0], "empty-raw")
}

// TestScanSessionIDDivergence_UnrecognizedHeaderShapeReportedUnreadable
// verifies a raw.jsonl whose first line is valid JSON but neither a
// type=="header" envelope nor a "_meta" envelope is treated as corrupt,
// not as an empty/legacy header.
func TestScanSessionIDDivergence_UnrecognizedHeaderShapeReportedUnreadable(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	dir := filepath.Join(sessionsDir, "weird-shape")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	writeTestSessionMeta(t, sessionsDir, "weird-shape", sessionid.GenerateSessionID())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(`{"foo":"bar"}`+"\n"), 0o644))

	result, err := scanSessionIDDivergence(sessionsDir)
	require.NoError(t, err)
	require.Len(t, result.unreadable, 1)
	assert.Contains(t, result.unreadable[0], "weird-shape")
}

// TestScanSessionIDDivergence_NativeHeaderInvalidIDIsUnreadable covers the
// layer below the shape check: the header shape is fine, but the session_id
// VALUE is corrupt. ParseStoreMeta only admits ses_-prefixed strings into
// StoreMeta.SessionID, so a malformed value reads back as "" — identical to a
// legacy header that never carried the field.
//
// Failure prevented: a corrupted identity carrier silently classified as
// "legacy, predates ID-at-start" and omitted from ox doctor entirely, which is
// the same swallow-corruption-as-fine failure as the shape gap, one level down.
func TestScanSessionIDDivergence_NativeHeaderInvalidIDIsUnreadable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
	}{
		{"missing ses_ prefix", `{"type":"header","metadata":{"agent_id":"OxAbc1","session_id":"019f9478-022c-7f2a-a5cd-9e7f23679a4e"}}`},
		{"not a UUID at all", `{"type":"header","metadata":{"agent_id":"OxAbc1","session_id":"ses_garbage"}}`},
		{"wrong type entirely", `{"type":"header","metadata":{"agent_id":"OxAbc1","session_id":12345}}`},
		{"empty string", `{"type":"header","metadata":{"agent_id":"OxAbc1","session_id":""}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			sessionsDir := filepath.Join(tmp, "sessions")
			dir := filepath.Join(sessionsDir, "corrupt-id")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			writeTestSessionMeta(t, sessionsDir, "corrupt-id", sessionid.GenerateSessionID())
			require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(tc.header+"\n"), 0o644))

			result, err := scanSessionIDDivergence(sessionsDir)
			require.NoError(t, err)
			require.Len(t, result.unreadable, 1,
				"a corrupt session_id must be reported, not skipped as legacy")
			assert.Contains(t, result.unreadable[0], "corrupt-id")
		})
	}
}

// TestScanSessionIDDivergence_ImportHeaderNonSesIDStaysQuiet is the
// false-positive guard for the check above. The alternative _meta format
// OVERLOADS session_id as an agent identifier — ParseStoreMeta falls it back
// to StoreMeta.AgentID precisely when it is not ses_-prefixed, and the
// documented import format in .claude/rules/session-capture.md ships
// `"session_id":"manual"`.
//
// Failure prevented: applying the native-header validity rule to _meta would
// report every imported and adapter-produced session as unreadable — turning
// a corruption detector into a wall of noise that gets ignored.
func TestScanSessionIDDivergence_ImportHeaderNonSesIDStaysQuiet(t *testing.T) {
	for _, header := range []string{
		`{"_meta":{"schema_version":"1","agent_type":"claude-code","session_id":"manual"}}`,
		`{"_meta":{"schema_version":"1","agent_type":"cursor","session_id":"some-agent-uuid"}}`,
	} {
		tmp := t.TempDir()
		sessionsDir := filepath.Join(tmp, "sessions")
		dir := filepath.Join(sessionsDir, "imported")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		writeTestSessionMeta(t, sessionsDir, "imported", sessionid.GenerateSessionID())
		require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(header+"\n"), 0o644))

		result, err := scanSessionIDDivergence(sessionsDir)
		require.NoError(t, err)
		assert.Empty(t, result.unreadable,
			"the _meta agent-identifier overload is not corruption: %s", header)
		assert.Empty(t, result.diverged)
	}
}

// TestScanSessionIDDivergence_HeaderShapeMustMatchTheReader covers the gap a
// bare key-presence check leaves open. Each of these first lines CONTAINS a
// "metadata" or "_meta" key, so a presence check waves it through — but
// session.ReadHeaderSessionID rejects every one of them (it requires an
// object AND, for the native shape, type=="header"), returning "".
//
// Failure prevented: that empty ID reads as "legacy session, predates
// ID-at-start" and the session is skipped SILENTLY. A corrupt header would
// then be indistinguishable from a pre-rollout one — the precise
// corruption-swallowed-as-fine outcome this whole check exists to catch.
func TestScanSessionIDDivergence_HeaderShapeMustMatchTheReader(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first string
	}{
		{"metadata present but not an object", `{"type":"header","metadata":"not-an-object"}`},
		{"metadata object without the header tag", `{"metadata":{"session_id":"ses_x"}}`},
		{"metadata object on an ordinary entry", `{"type":"message","metadata":{"session_id":"ses_x"}}`},
		{"_meta present but not an object", `{"_meta":"not-an-object"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			sessionsDir := filepath.Join(tmp, "sessions")
			dir := filepath.Join(sessionsDir, "bad-header")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			writeTestSessionMeta(t, sessionsDir, "bad-header", sessionid.GenerateSessionID())
			require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(tc.first+"\n"), 0o644))

			result, err := scanSessionIDDivergence(sessionsDir)
			require.NoError(t, err)
			require.Len(t, result.unreadable, 1,
				"a shape the reader rejects must surface as unreadable, not be skipped as legacy")
			assert.Contains(t, result.unreadable[0], "bad-header")
			assert.Empty(t, result.diverged)
		})
	}
}

// TestScanSessionIDDivergence_DehydratedRawSkipped verifies a raw.jsonl
// that is an LFS pointer stub (dehydrated clone — no local content) is
// skipped, never reported as corrupt or diverged: the header genuinely
// cannot be read locally, which is not evidence of anything.
func TestScanSessionIDDivergence_DehydratedRawSkipped(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	dir := filepath.Join(sessionsDir, "dehydrated")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	writeTestSessionMeta(t, sessionsDir, "dehydrated", sessionid.GenerateSessionID())
	require.NoError(t, lfs.WritePointerFile(filepath.Join(dir, "raw.jsonl"), lfs.FileRef{
		OID: "sha256:" + strings.Repeat("a", 64), Size: 4096,
	}))

	result, err := scanSessionIDDivergence(sessionsDir)
	require.NoError(t, err)
	assert.Zero(t, result.checked)
	assert.Empty(t, result.diverged)
	assert.Empty(t, result.unreadable)
}

// TestScanSessionIDDivergence_IgnoresNonDirEntries verifies stray files
// directly under sessions/ (e.g. .gitignore) don't crash the scan.
func TestScanSessionIDDivergence_IgnoresNonDirEntries(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, ".gitignore"), []byte("cache/\n"), 0o644))

	result, err := scanSessionIDDivergence(sessionsDir)
	require.NoError(t, err)
	assert.Zero(t, result.checked)
	assert.Empty(t, result.diverged)
	assert.Empty(t, result.unreadable)
}

// TestScanSessionIDDivergence_MultipleDivergedSortedByName verifies
// multiple diverged sessions are returned in a deterministic (sorted)
// order, so doctor output and tests don't flap on map/readdir ordering.
func TestScanSessionIDDivergence_MultipleDivergedSortedByName(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	for _, name := range []string{"zeta", "alpha", "mu"} {
		metaID := sessionid.GenerateSessionID()
		headerID := sessionid.GenerateSessionID()
		writeTestSessionMeta(t, sessionsDir, name, metaID)
		writeTestSessionRaw(t, tmp, name, headerID)
	}

	result, err := scanSessionIDDivergence(sessionsDir)
	require.NoError(t, err)
	require.Len(t, result.diverged, 3)
	assert.Equal(t, []string{"alpha", "mu", "zeta"}, []string{
		result.diverged[0].Name, result.diverged[1].Name, result.diverged[2].Name,
	})
}

// TestScanSessionIDDivergence_NoSessionsDirReturnsNotExist verifies the
// scan surfaces a wrapped fs.ErrNotExist for a missing sessions/ directory
// so the caller can turn it into a Skip rather than a scan failure.
func TestScanSessionIDDivergence_NoSessionsDirReturnsNotExist(t *testing.T) {
	tmp := t.TempDir()
	_, err := scanSessionIDDivergence(filepath.Join(tmp, "sessions"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

// --- B. checkSessionIDDivergence: the doctor-check wrapper ---

// TestCheckSessionIDDivergence_NoLedgerSkips mirrors
// TestCheckLegacySessionIDs_NoLedgerSkips: running from a directory with no
// ledger configured must Skip, not Warn — a first-run/no-ledger user
// should never see this check flagged.
func TestCheckSessionIDDivergence_NoLedgerSkips(t *testing.T) {
	if testing.Short() {
		t.Skip("short: requires git root + ledger resolution")
	}
	tmp := t.TempDir()
	old, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tmp))
	t.Cleanup(func() { _ = os.Chdir(old) })

	res := checkSessionIDDivergence()
	assert.True(t, res.skipped, "expected skipped when no ledger; got %+v", res)
	assert.Contains(t, strings.ToLower(res.message), "ledger")
}

// TestCheckSessionIDDivergence_RegisteredCheckOnly locks in the auto-fix
// decision documented on checkSessionIDDivergence: this check must never
// silently gain FixLevelAuto (or any fix behavior) without a deliberate,
// reviewed change, since a wrong auto-repair of a committed session_id is
// expensive and hard to detect after the fact.
func TestCheckSessionIDDivergence_RegisteredCheckOnly(t *testing.T) {
	check := GetDoctorCheck(CheckSlugSessionIDDivergence)
	require.NotNil(t, check, "doctor check %q must be registered", CheckSlugSessionIDDivergence)
	assert.Equal(t, FixLevelCheckOnly, check.FixLevel,
		"session ID divergence must stay report-only — see the doc comment on checkSessionIDDivergence")
	assert.Equal(t, "Sessions", check.Category)
}

// --- C. sessionIDDivergenceFailure: message/detail shape ---

// TestSessionIDDivergenceFailure_ReportsBothDivergedAndUnreadable verifies
// neither bucket silently disappears from the summary when both are
// non-empty — the "unreadable/corrupt files must not be silently swallowed
// as fine" requirement.
func TestSessionIDDivergenceFailure_ReportsBothDivergedAndUnreadable(t *testing.T) {
	result := sessionIDScanResult{
		checked:    1,
		diverged:   []sessionIDDivergence{{Name: "wedged", MetaID: "ses_meta", HeaderID: "ses_header"}},
		unreadable: []string{"broken: meta.json: parse session meta: unexpected EOF"},
	}

	got := sessionIDDivergenceFailure("Session ID divergence", result)
	assert.True(t, got.warning)
	assert.True(t, got.passed, "WarningCheck sets passed=true — non-blocking, still visible")
	assert.Contains(t, got.message, "1 diverged")
	assert.Contains(t, got.message, "1 unreadable")
	assert.Contains(t, got.detail, "wedged")
	assert.Contains(t, got.detail, "ses_meta")
	assert.Contains(t, got.detail, "ses_header")
	assert.Contains(t, got.detail, "broken: meta.json")
	assert.Contains(t, got.detail, "No auto-fix")
}

// TestSessionIDDivergenceFailure_CapsSampleAtFive verifies a long list of
// diverged sessions is truncated with a "+N more" marker rather than
// dumping an unbounded list into doctor output.
func TestSessionIDDivergenceFailure_CapsSampleAtFive(t *testing.T) {
	var diverged []sessionIDDivergence
	for i := 0; i < 8; i++ {
		diverged = append(diverged, sessionIDDivergence{
			Name: filepath.Join("session", string(rune('a'+i))), MetaID: "ses_a", HeaderID: "ses_b",
		})
	}
	result := sessionIDScanResult{diverged: diverged}

	got := sessionIDDivergenceFailure("Session ID divergence", result)
	assert.Contains(t, got.message, "8 diverged")
	assert.Contains(t, got.detail, "+3 more")
}
