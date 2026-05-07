package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/pkg/sessionsummary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRepairMetaSummary_CleanMetaIsSkipped_Idempotency: a session that
// has already been repaired (or was never broken) MUST NOT trigger a
// rewrite. Idempotency matters because:
//
//  1. Running the tool daily (or from anti-entropy) must not churn
//     mtimes — that would rewrite ledger commits and create noise on
//     every team member's git log.
//  2. Idempotency lets us safely add the call to startup paths without
//     thinking about whether it's "the first run".
//
// Failure prevented: any future change that unconditionally rewrites
// meta.json (e.g. "always re-stamp SummaryStatus") would silently make
// the tool non-idempotent.
func TestRepairMetaSummary_CleanMetaIsSkipped_Idempotency(t *testing.T) {
	sd := t.TempDir()
	clean := &lfs.SessionMeta{
		Version: "1.0", SessionName: "s", AgentID: "Ox", AgentType: "claude-code",
		CreatedAt: time.Now(),
		Title:     "Real session title",
		Summary:   "Real session title",
	}
	require.NoError(t, lfs.WriteSessionMetaOnly(sd, clean))

	mtimeBefore := metaMtime(t, sd)
	time.Sleep(10 * time.Millisecond) // ensure any rewrite would change mtime

	oc := repairSessionMetaSummary(sd, false /*dryRun*/)
	assert.True(t, oc.Skipped, "clean meta must be reported as skipped")
	assert.False(t, oc.ChangedTitle)
	assert.False(t, oc.ChangedSummary)
	assert.False(t, oc.ChangedStatus)

	assert.Equal(t, mtimeBefore, metaMtime(t, sd),
		"a clean meta.json must not be rewritten (idempotency: avoids ledger-mtime churn)")
}

// TestRepairMetaSummary_NoSummaryJSON_ClearsAndMarksFailedValidation:
// the leaky meta has no clean replacement available. Expected behavior:
// clear user-visible fields, stamp summary_status=failed_validation,
// preserve the diagnostic in validation_error.
//
// Failure prevented: a future "simplification" that just deletes the
// fields without setting summary_status would lose the failure signal,
// and consumers couldn't distinguish "never tried" from "tried and
// failed". (That distinction is exactly what doctor needs to know
// whether to retry.)
func TestRepairMetaSummary_NoSummaryJSON_ClearsAndMarksFailedValidation(t *testing.T) {
	sd := t.TempDir()
	leakDiagnostic := "Summary failed content validation: title too short (0 chars, minimum 3)"

	// We intentionally bypass the writer's Validate() guard for setup
	// — we need to materialize a pre-fix meta.json on disk to test the
	// repair path. Real-world ledgers carry exactly this shape today.
	require.NoError(t, writeRawMeta(sd, map[string]any{
		"version":      "1.0",
		"session_name": "s", "agent_id": "Ox", "agent_type": "claude-code",
		"created_at": time.Now().Format(time.RFC3339Nano),
		"title":      "",
		"summary":    leakDiagnostic,
	}))

	oc := repairSessionMetaSummary(sd, false)
	require.Empty(t, oc.Error)
	assert.False(t, oc.RecoveredFromJSON, "no summary.json present, recovery impossible")
	assert.True(t, oc.ChangedSummary)

	got, err := lfs.ReadSessionMeta(sd)
	require.NoError(t, err)
	assert.Empty(t, got.Title)
	assert.Empty(t, got.Summary, "leaked diagnostic must be cleared from user-visible field")
	assert.Equal(t, sessionsummary.SummaryStatusFailedValidation, got.SummaryStatus,
		"failure must be communicated structurally, not by absence-of-text")
	assert.Equal(t, leakDiagnostic, got.ValidationError,
		"diagnostic must be preserved in ops-facing field, not lost")
}

// TestRepairMetaSummary_RecoversFromCleanSummaryJSON: the most
// important case for the SageOx Internal ledger — a successful
// summary.json regen exists but meta.summary stayed stuck on the
// failure stub due to ox-wstd. Expected: pull the title from
// summary.json, clear the leak, mark as ok.
//
// This is the user-visible payoff of running the tool: 14 stuck
// sessions go from displaying "Summary failed content validation: ..."
// in the UI to displaying their real title.
func TestRepairMetaSummary_RecoversFromCleanSummaryJSON(t *testing.T) {
	sd := t.TempDir()

	require.NoError(t, writeRawMeta(sd, map[string]any{
		"version":      "1.0",
		"session_name": "s", "agent_id": "Ox", "agent_type": "claude-code",
		"created_at": time.Now().Format(time.RFC3339Nano),
		"title":      "",
		"summary":    "Summary failed content validation: title too short",
	}))

	// summary.json has a real, post-regen title (the wstd-stuck case).
	const recoveredTitle = "Implemented manifest invariants and retro-cleanup tool"
	summaryJSON := map[string]any{
		"title":   recoveredTitle,
		"summary": "Detailed summary text that the regen successfully produced.",
	}
	bytes, _ := json.Marshal(summaryJSON)
	require.NoError(t, os.WriteFile(filepath.Join(sd, "summary.json"), bytes, 0644))

	oc := repairSessionMetaSummary(sd, false)
	require.Empty(t, oc.Error)
	assert.True(t, oc.RecoveredFromJSON)
	assert.True(t, oc.ChangedTitle)
	assert.True(t, oc.ChangedSummary)

	got, err := lfs.ReadSessionMeta(sd)
	require.NoError(t, err)
	assert.Equal(t, recoveredTitle, got.Title)
	assert.Equal(t, "Detailed summary text that the regen successfully produced.", got.Summary,
		"prefer summary.json's distinct summary over mirroring the title when both are clean")
	assert.Equal(t, sessionsummary.SummaryStatusOK, got.SummaryStatus)
	assert.Empty(t, got.ValidationError, "stale validation error must be cleared on recovery")
}

// TestRepairMetaSummary_DryRunMakesNoChanges: --dry-run must report
// what WOULD change but leave meta.json untouched on disk. Critical
// for ops review before running for real on a large ledger.
//
// Failure prevented: a future refactor merges the dry-run and
// committed paths and accidentally writes during dry-run.
func TestRepairMetaSummary_DryRunMakesNoChanges(t *testing.T) {
	sd := t.TempDir()
	require.NoError(t, writeRawMeta(sd, map[string]any{
		"version": "1.0", "session_name": "s", "agent_id": "Ox", "agent_type": "claude-code",
		"created_at": time.Now().Format(time.RFC3339Nano),
		"summary":    "Summary failed content validation: x",
	}))
	mtimeBefore := metaMtime(t, sd)
	time.Sleep(10 * time.Millisecond)

	oc := repairSessionMetaSummary(sd, true /*dryRun*/)
	assert.True(t, oc.ChangedSummary, "dry-run must still report what would change")

	assert.Equal(t, mtimeBefore, metaMtime(t, sd), "dry-run must not write to disk")

	// And the on-disk meta still has the leak (not yet cleared).
	rawBytes, err := os.ReadFile(filepath.Join(sd, "meta.json"))
	require.NoError(t, err)
	assert.Contains(t, string(rawBytes), "Summary failed content validation",
		"dry-run must leave the leak in place — caller must run again without --dry-run to actually fix")
}

// TestRepairMetaSummary_TitleLeak: the leak can land in meta.title too,
// not just meta.summary. Older ledger entries from the
// 91/155-empty-title bug (ox-g5zw) backfill might have had the
// diagnostic written to title. The detector must catch both fields.
func TestRepairMetaSummary_TitleLeak(t *testing.T) {
	sd := t.TempDir()
	require.NoError(t, writeRawMeta(sd, map[string]any{
		"version": "1.0", "session_name": "s", "agent_id": "Ox", "agent_type": "claude-code",
		"created_at": time.Now().Format(time.RFC3339Nano),
		"title":      "Summary generation failed: bad json",
		"summary":    "",
	}))

	oc := repairSessionMetaSummary(sd, false)
	require.Empty(t, oc.Error)
	assert.True(t, oc.ChangedTitle)

	got, err := lfs.ReadSessionMeta(sd)
	require.NoError(t, err)
	assert.Empty(t, got.Title, "leak in title must be cleared")
}

// TestRepairMetaSummary_LeakyCleanupConvergesToTerminal: a leaky meta
// with no recoverable summary.json gets cleaned on the first pass
// (leak removed, status stamped failed_validation), then progresses
// through the bounded empty-title retry on each subsequent pass and
// converges to SummaryStatusUnrecoverable in a finite number of runs.
//
// This replaces the older "second run is a no-op" assertion. The
// no-op model was correct when only the leaky-string repair existed,
// but the empty-title repair (added to handle the post-Apr-27 failure
// shape on the SageOx Internal ledger) is an additional pass with its
// own bounded convergence: it MUST keep working on still-broken
// sessions, not fall silent. The new contract:
//
//   - Healthy meta: no-op (covered by TestRepairMetaSummary_CleanMetaIsSkipped_Idempotency)
//   - Terminal meta: no-op (proven below by the post-cap re-run)
//   - In-flight broken meta: bounded progress toward terminal
//
// Failure prevented: any future "simplification" that drops the
// empty-title pass and reverts to silent skip — the user's existing
// broken sessions would never get repaired by the autofix scheduler.
func TestRepairMetaSummary_LeakyCleanupConvergesToTerminal(t *testing.T) {
	sd := t.TempDir()
	require.NoError(t, writeRawMeta(sd, map[string]any{
		"version": "1.0", "session_name": "s", "agent_id": "Ox", "agent_type": "claude-code",
		"created_at": time.Now().Format(time.RFC3339Nano),
		"summary":    "Summary failed content validation: x",
	}))

	// First pass: leaky cleanup. Clears the leak, stamps status.
	first := repairSessionMetaSummary(sd, false)
	require.Empty(t, first.Error)
	require.False(t, first.Skipped, "first pass must clean the leak")

	// Subsequent passes: empty-title progression. Each bumps attempts;
	// the call that hits MaxSummaryAttempts flips to unrecoverable.
	for i := 1; i <= lfs.MaxSummaryAttempts; i++ {
		oc := repairSessionMetaSummary(sd, false)
		require.Empty(t, oc.Error, "pass %d", i)
	}
	got, err := lfs.ReadSessionMeta(sd)
	require.NoError(t, err)
	assert.Equal(t, sessionsummary.SummaryStatusUnrecoverable, got.SummaryStatus,
		"after MaxSummaryAttempts empty-title passes, status must be terminal")

	// Terminal sessions are now no-ops — the autofix loop closes here.
	terminal := repairSessionMetaSummary(sd, false)
	assert.True(t, terminal.Skipped, "terminal session must short-circuit subsequent calls")
}

// TestRepairMetaSummary_OnlyLeakyFieldIsCleared: when one user-visible
// field is leaky and the other carries legitimate content, the repair
// must clear ONLY the leaky one. Pre-fix the code blanked both fields
// whenever either matched IsLeakySummaryString, erasing real titles by
// mistake. CodeRabbit caught this on PR #566.
//
// Failure prevented: a session whose meta.title is "Migrated auth to
// OAuth2" but whose meta.summary somehow ended up with the validator
// stub gets its real title nuked when we run the repair. This test
// pins the corrected behavior.
func TestRepairMetaSummary_OnlyLeakyFieldIsCleared(t *testing.T) {
	sd := t.TempDir()
	const goodTitle = "Migrated auth to OAuth2"

	require.NoError(t, writeRawMeta(sd, map[string]any{
		"version":      "1.0",
		"session_name": "s", "agent_id": "Ox", "agent_type": "claude-code",
		"created_at": time.Now().Format(time.RFC3339Nano),
		"title":      goodTitle,                              // legitimate
		"summary":    "Summary failed content validation: x", // leaky
	}))

	oc := repairSessionMetaSummary(sd, false)
	require.Empty(t, oc.Error)
	assert.False(t, oc.ChangedTitle, "the clean title must NOT be cleared")
	assert.True(t, oc.ChangedSummary, "the leaky summary must be cleared")

	got, err := lfs.ReadSessionMeta(sd)
	require.NoError(t, err)
	assert.Equal(t, goodTitle, got.Title, "real title preserved")
	assert.Empty(t, got.Summary, "leaky summary cleared")
	// ValidationError must come from the leaky summary, not the clean title —
	// otherwise pickDiagnosticForOps's "longer string wins" heuristic would
	// have mis-attributed the title (longer than the leak prefix) into ops.
	assert.Contains(t, got.ValidationError, "Summary failed content validation",
		"diagnostic must be sourced from the actually-leaky field")
	assert.NotContains(t, got.ValidationError, goodTitle,
		"clean title must not be copied into ValidationError")
}

// TestRepairMetaSummary_MissingMetaJSON_IsSkippedNotErrored: a session
// folder with no meta.json (e.g. a recording that crashed before stop)
// must hit the Skipped path. Pre-fix the wrapped error from
// lfs.ReadSessionMeta broke os.IsNotExist detection and these sessions
// were silently flagged as errors by the repair tool. Caught by
// CodeRabbit on PR #566.
//
// Failure prevented: a large ledger with many in-flight sessions
// reports a sea of false-positive errors that mask real problems.
func TestRepairMetaSummary_MissingMetaJSON_IsSkippedNotErrored(t *testing.T) {
	sd := t.TempDir() // no meta.json inside

	oc := repairSessionMetaSummary(sd, false)
	assert.True(t, oc.Skipped, "session without meta.json must be skipped, not errored")
	assert.Empty(t, oc.Error)
}

// TestRepairMetaSummary_PreservesFilesManifest: the OID manifest
// (meta.Files) must survive the rewrite verbatim. A repair that
// accidentally dropped or mutated the manifest would orphan LFS blobs
// and break every later read of the session.
func TestRepairMetaSummary_PreservesFilesManifest(t *testing.T) {
	sd := t.TempDir()
	require.NoError(t, writeRawMeta(sd, map[string]any{
		"version": "1.0", "session_name": "s", "agent_id": "Ox", "agent_type": "claude-code",
		"created_at": time.Now().Format(time.RFC3339Nano),
		"summary":    "Summary failed content validation: x",
		"files": map[string]any{
			"raw.jsonl": map[string]any{"storage": "lfs", "oid": "sha256:abc", "size": 12345},
		},
	}))

	oc := repairSessionMetaSummary(sd, false)
	require.Empty(t, oc.Error)

	got, err := lfs.ReadSessionMeta(sd)
	require.NoError(t, err)
	require.Contains(t, got.Files, "raw.jsonl")
	assert.Equal(t, "sha256:abc", got.Files["raw.jsonl"].OID)
	assert.Equal(t, int64(12345), got.Files["raw.jsonl"].Size)
}

// TestRepairMetaSummary_EmptyTitle_RecoversFromSummaryJSON covers the
// post-Apr-27 failure shape we actually see on the SageOx Internal
// ledger: meta.title is empty, no leaky string anywhere, summary.json
// happens to carry a real title (e.g., a future regenerate succeeded).
// Pre-this-change, the repair tool early-exited on `!titleLeaky &&
// !summaryLeaky` and did nothing — the row stayed "Summary unavailable"
// in the UI forever.
//
// Failure prevented: the empty-title rows on the user's existing
// ledger never get repaired, even when summary.json has a clean
// title to recover from.
func TestRepairMetaSummary_EmptyTitle_RecoversFromSummaryJSON(t *testing.T) {
	sd := t.TempDir()
	require.NoError(t, writeRawMeta(sd, map[string]any{
		"version":      "1.0",
		"session_name": "s", "agent_id": "Ox", "agent_type": "claude-code",
		"created_at":       time.Now().Format(time.RFC3339Nano),
		"title":            "",
		"summary":          "",
		"summary_status":   "failed_validation",
		"validation_error": "content validation failed: title too short (0 chars, minimum 3)",
		"summary_attempts": 1,
	}))
	require.NoError(t, os.WriteFile(filepath.Join(sd, "summary.json"),
		[]byte(`{"title":"Real recovered title","summary":"the body"}`), 0644))

	oc := repairSessionMetaSummary(sd, false)
	require.Empty(t, oc.Error)
	assert.True(t, oc.RecoveredFromJSON, "must recover the title from summary.json")
	assert.True(t, oc.ChangedTitle, "title field must be reported as changed")

	got, err := lfs.ReadSessionMeta(sd)
	require.NoError(t, err)
	assert.Equal(t, "Real recovered title", got.Title)
	assert.Equal(t, sessionsummary.SummaryStatusOK, got.SummaryStatus, "status must transition failed_validation → ok")
	assert.Empty(t, got.ValidationError, "stale ops diagnostic must be cleared on recovery")
}

// TestRepairMetaSummary_EmptyTitle_BumpsAttemptsToTerminal proves the
// retry cap converges. With no clean summary.json to recover from,
// the tool bumps SummaryAttempts each invocation and at
// MaxSummaryAttempts flips to unrecoverable. Subsequent passes
// short-circuit so the autofix loop terminates cleanly.
//
// Failure prevented: an unbounded autofix loop on a session whose
// summary.json is permanently empty (e.g., raw.jsonl is corrupt and
// every regenerate yields the same failure stub).
func TestRepairMetaSummary_EmptyTitle_BumpsAttemptsToTerminal(t *testing.T) {
	sd := t.TempDir()
	require.NoError(t, writeRawMeta(sd, map[string]any{
		"version":      "1.0",
		"session_name": "s", "agent_id": "Ox", "agent_type": "claude-code",
		"created_at":     time.Now().Format(time.RFC3339Nano),
		"title":          "",
		"summary_status": "failed_validation",
	}))

	// Run MaxSummaryAttempts times — last call must flip to terminal.
	for i := 1; i <= lfs.MaxSummaryAttempts; i++ {
		oc := repairSessionMetaSummary(sd, false)
		require.Empty(t, oc.Error, "attempt %d", i)
		got, err := lfs.ReadSessionMeta(sd)
		require.NoError(t, err)
		if i < lfs.MaxSummaryAttempts {
			assert.Equal(t, sessionsummary.SummaryStatusFailedValidation, got.SummaryStatus,
				"attempt %d: status should still be failed_validation before cap", i)
		} else {
			assert.Equal(t, sessionsummary.SummaryStatusUnrecoverable, got.SummaryStatus,
				"final attempt must flip to unrecoverable")
		}
		assert.Equal(t, i, got.SummaryAttempts, "attempt %d: SummaryAttempts must increment", i)
	}

	// Idempotency floor: terminal sessions short-circuit. The repair
	// tool must NOT keep bumping SummaryAttempts past the cap.
	oc := repairSessionMetaSummary(sd, false)
	assert.True(t, oc.Skipped || (!oc.ChangedStatus && !oc.ChangedTitle),
		"terminal session must be a no-op on subsequent passes")
}

// metaMtime returns meta.json's modtime, failing the test on stat
// errors. Used to assert idempotency / dry-run invariants.
func metaMtime(t *testing.T, sessionDir string) time.Time {
	t.Helper()
	info, err := os.Stat(filepath.Join(sessionDir, "meta.json"))
	require.NoError(t, err)
	return info.ModTime()
}

// writeRawMeta writes a meta.json directly via json.Marshal so tests
// can materialize pre-fix on-disk shapes that the writer's Validate()
// guard would refuse. Used only by repair-tool tests; production code
// must always go through lfs.WriteSessionMeta(Only).
func writeRawMeta(sessionDir string, fields map[string]any) error {
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return err
	}
	bytes, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(sessionDir, "meta.json"), bytes, 0644)
}
