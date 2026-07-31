package agentwork

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/pkg/sessionsummary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- GH #710 D3: meta.json field preservation ---
//
// The daemon's finalize path used to rebuild meta.json from a fresh
// SessionMetaBuilder, which populates only the handful of fields the
// daemon sets. Everything else — identity, provenance, redaction history,
// linkage — was silently dropped on every finalize.
//
// That is worse than a local loss. The ledger appends its own
// {Auto, "sessions/"} resolve rule (internal/ledger/ledger.go) and
// resolves to the LOCAL side, so a stripped meta.json wins the next
// rebase and erases those fields from shared history for every teammate.
// The reporter of #710 saw the other half of the same bug: doctor's
// auto-commit committing a stripped file while origin still had the
// fields, producing an unresolvable conflict on the same 6 files forever.

const preserveSuccessOutput = `{"title":"Real Title","summary":"This is a successful summary of the session that passes all the validators in place.","key_actions":["did the work","wrote the tests","shipped it"],"outcome":"success","topics_found":["x"],"quality_score":0.8}`

const preserveFailingOutput = `{"title":"x","summary":"Some real-looking summary text that is long enough to pass length checks.","key_actions":["a"],"outcome":"success","topics_found":["x"],"quality_score":0.8}`

// seedRichMeta writes a meta.json carrying every field the daemon does
// NOT own. Each one is a field a real session accumulates from a
// different subsystem, and each was dropped pre-fix.
func seedRichMeta(t *testing.T, sessionDir, sessionName string) *lfs.SessionMeta {
	t.Helper()

	resets := time.Date(2026, 5, 1, 23, 0, 0, 0, time.UTC)
	meta := lfs.NewSessionMeta(sessionName, "testuser", "agent-1", "claude-code",
		time.Date(2026, 5, 1, 20, 4, 0, 0, time.UTC)).Build()

	meta.SessionID = "ses_019c0000-0000-7000-8000-000000000001"
	meta.UserID = "user-abc"         // resolved at login
	meta.RepoID = "repo-xyz"         // resolved from the project marker
	meta.Model = "claude-opus-4"     // recorded by the CLI at session stop
	meta.StopReason = "rate_limited" // a REAL terminal reason, not "recovered"
	meta.StopDetail = "5-hour limit reached"
	meta.StopSource = "structured"
	meta.StopPatternID = "anthropic-rate-limit"
	meta.StopResetsAtRaw = "11pm"
	meta.StopResetsAt = &resets
	meta.Redactions = []lfs.RedactionPass{{
		PassID:    "019c0000-0000-7000-8000-0000000000aa",
		AppliedAt: time.Date(2026, 5, 1, 21, 0, 0, 0, time.UTC),
		AppliedBy: "ox session redact-history",
	}}
	meta.ProducedCommits = []string{"abc123def456"}
	meta.ProducedPlans = []string{"2026-05-01-some-plan"}
	meta.LinkedPRs = []string{"sageox/ox#710"}
	meta.LinkedIssues = []string{"sageox/ox#732"}
	meta.LinkageStatus = lfs.LinkageStatusNotified

	require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, meta))
	return meta
}

// assertRichMetaPreserved checks every seeded field survived.
func assertRichMetaPreserved(t *testing.T, got *lfs.SessionMeta, want *lfs.SessionMeta) {
	t.Helper()

	assert.Equal(t, want.UserID, got.UserID, "user_id dropped")
	assert.Equal(t, want.RepoID, got.RepoID, "repo_id dropped — the session detaches from its repo")
	assert.Equal(t, want.Model, got.Model, "model dropped")
	assert.Equal(t, want.SessionID, got.SessionID, "session_id must never rotate")

	assert.Equal(t, want.StopDetail, got.StopDetail, "stop_detail dropped")
	assert.Equal(t, want.StopSource, got.StopSource, "stop_source dropped")
	assert.Equal(t, want.StopPatternID, got.StopPatternID, "stop_pattern_id dropped")
	assert.Equal(t, want.StopResetsAtRaw, got.StopResetsAtRaw, "stop_resets_at_raw dropped")
	require.NotNil(t, got.StopResetsAt, "stop_resets_at dropped")
	assert.True(t, want.StopResetsAt.Equal(*got.StopResetsAt), "stop_resets_at changed")

	require.Len(t, got.Redactions, 1,
		"redaction history dropped — this is an audit record, and losing it means "+
			"nobody can prove which detector catalog ran over this transcript")
	assert.Equal(t, want.Redactions[0].PassID, got.Redactions[0].PassID)
	assert.Equal(t, want.Redactions[0].AppliedBy, got.Redactions[0].AppliedBy)

	assert.Equal(t, want.ProducedCommits, got.ProducedCommits, "produced_commits dropped")
	assert.Equal(t, want.ProducedPlans, got.ProducedPlans, "produced_plans dropped")
	assert.Equal(t, want.LinkedPRs, got.LinkedPRs, "linked_prs dropped")
	assert.Equal(t, want.LinkedIssues, got.LinkedIssues, "linked_issues dropped")
	assert.Equal(t, want.LinkageStatus, got.LinkageStatus, "linkage_status dropped")
}

// TestFinalize_PreservesUnownedFields is the core GH #710 regression test.
// It fails hard against the pre-fix builder-based write.
func TestFinalize_PreservesUnownedFields(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipGit = true
	handler.skipLFS = true

	name := "2026-05-01T20-04-testuser-OxPRSV"
	ledgerPath := createTestSession(t, name, nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", name)
	seeded := seedRichMeta(t, sessionDir, name)

	item := &WorkItem{
		ID: "prsv", Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}
	require.NoError(t, handler.ProcessResult(item, &RunResult{
		Output: preserveSuccessOutput, Duration: time.Second, ExitCode: 0,
	}))

	got, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)

	assertRichMetaPreserved(t, got, seeded)

	// and the fields the daemon DOES own must actually be updated —
	// otherwise "preserve everything" would trivially pass by doing nothing.
	assert.Equal(t, "Real Title", got.Title, "the daemon must still write the summary it produced")
	assert.NotEmpty(t, got.Summary)
}

// TestFinalize_PreservesUnownedFieldsAcrossRepeatedRuns covers the class
// rather than a single call: anti-entropy re-finalizes the same session on
// later cycles, and each pass must be non-destructive. A fix that
// preserved fields on the first write but re-stripped on the second (the
// second MutateSessionMeta based on a stale in-memory struct) would pass
// the test above and still lose the data.
func TestFinalize_PreservesUnownedFieldsAcrossRepeatedRuns(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipGit = true
	handler.skipLFS = true

	name := "2026-05-01T20-04-testuser-OxPRS2"
	ledgerPath := createTestSession(t, name, nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", name)
	seeded := seedRichMeta(t, sessionDir, name)

	item := &WorkItem{
		ID: "prs2", Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}

	for i := range 3 {
		require.NoError(t, handler.ProcessResult(item, &RunResult{
			Output: preserveSuccessOutput, Duration: time.Second, ExitCode: 0,
		}), "run %d", i+1)

		got, err := lfs.ReadSessionMeta(sessionDir)
		require.NoError(t, err)
		assertRichMetaPreserved(t, got, seeded)
	}
}

// TestFinalize_PreservesUnownedFieldsOnValidationFailure covers the exact
// path the #710 reporter was stuck in: the summary keeps failing
// validation, the daemon keeps re-finalizing, and every one of those
// passes was stripping the file that then conflicted against origin.
func TestFinalize_PreservesUnownedFieldsOnValidationFailure(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipGit = true
	handler.skipLFS = true

	name := "2026-05-01T20-04-testuser-OxPRS3"
	ledgerPath := createTestSession(t, name, nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", name)
	seeded := seedRichMeta(t, sessionDir, name)

	item := &WorkItem{
		ID: "prs3", Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}
	require.NoError(t, handler.ProcessResult(item, &RunResult{
		Output: preserveFailingOutput, Duration: time.Second, ExitCode: 0,
	}))

	got, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)

	assertRichMetaPreserved(t, got, seeded)
	assert.Equal(t, sessionsummary.SummaryStatusFailedValidation, got.SummaryStatus)
	assert.Equal(t, 1, got.SummaryAttempts)
}

// TestFinalize_SuccessClearsFailureState is the guard against
// over-correcting. The failure fields must be assigned unconditionally,
// NOT preserve-if-empty — otherwise a session that once failed would stay
// marked failed forever, and the retry cap would never re-arm. This is the
// opposite bug to the one #710 fixes, and equally real.
func TestFinalize_SuccessClearsFailureState(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipGit = true
	handler.skipLFS = true

	name := "2026-05-01T20-04-testuser-OxPRS4"
	ledgerPath := createTestSession(t, name, nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", name)

	meta := lfs.NewSessionMeta(name, "testuser", "agent-1", "claude-code", time.Now().UTC()).Build()
	meta.SummaryStatus = sessionsummary.SummaryStatusFailedValidation
	meta.ValidationError = "content validation failed: title too short (0 chars, minimum 3)"
	meta.SummaryAttempts = 2
	require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, meta))

	item := &WorkItem{
		ID: "prs4", Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}
	require.NoError(t, handler.ProcessResult(item, &RunResult{
		Output: preserveSuccessOutput, Duration: time.Second, ExitCode: 0,
	}))

	got, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, 0, got.SummaryAttempts, "a success must reset the retry budget")
	assert.Empty(t, got.ValidationError, "a success must clear the stale diagnostic")
	assert.NotEqual(t, sessionsummary.SummaryStatusFailedValidation, got.SummaryStatus,
		"a success must not leave the session marked failed")
}

// TestFinalize_PreservesTerminalStopReason pins the preserve-if-set rule
// for StopReason. The daemon's "recovered" is a fallback for sessions
// nobody stopped cleanly; overwriting a real terminal reason destroys the
// record of WHY the session ended, which the stop_detail / stop_source /
// stop_resets_at group exists to explain.
func TestFinalize_PreservesTerminalStopReason(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipGit = true
	handler.skipLFS = true

	name := "2026-05-01T20-04-testuser-OxPRS5"
	ledgerPath := createTestSession(t, name, nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", name)
	seedRichMeta(t, sessionDir, name)

	item := &WorkItem{
		ID: "prs5", Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}
	require.NoError(t, handler.ProcessResult(item, &RunResult{
		Output: preserveSuccessOutput, Duration: time.Second, ExitCode: 0,
	}))

	got, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, "rate_limited", got.StopReason,
		"a real terminal stop reason must not be overwritten with the generic fallback")
}

// TestFinalize_StampsRecoveredWhenNoStopReason is the other half of
// preserve-if-set: a session with no recorded stop reason still gets the
// fallback, so the preservation rule can't silently disable it.
func TestFinalize_StampsRecoveredWhenNoStopReason(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipGit = true
	handler.skipLFS = true

	name := "2026-05-01T20-04-testuser-OxPRS6"
	ledgerPath := createTestSession(t, name, nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", name)

	item := &WorkItem{
		ID: "prs6", Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}
	require.NoError(t, handler.ProcessResult(item, &RunResult{
		Output: preserveSuccessOutput, Duration: time.Second, ExitCode: 0,
	}))

	got, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, session.StopReasonRecovered, got.StopReason,
		"a session with no recorded stop reason must still be marked recovered")
}

// TestMergeFileRefs_PreservesGitStoredEntries covers the CodeRabbit
// finding on this PR: the upload-only path assigned the LFS ref map to
// meta.Files wholesale. lfs.UploadSessionFiles deliberately omits
// summary.json (the CLI registers it as Storage=git because its bytes
// live in the git tree), so a wholesale assign dropped that registration
// — the same field-stripping class this PR exists to fix, reintroduced
// one function over.
func TestMergeFileRefs_PreservesGitStoredEntries(t *testing.T) {
	h := NewSessionFinalizeHandler(slog.Default())

	existing := map[string]lfs.FileRef{
		"summary.json": {Storage: lfs.StorageGit, Size: 512},
		"raw.jsonl":    {Storage: lfs.StorageLFS, OID: "sha256:old", Size: 100},
	}
	incoming := map[string]lfs.FileRef{
		"raw.jsonl": {Storage: lfs.StorageLFS, OID: "sha256:new", Size: 200},
	}

	merged := h.mergeFileRefs(existing, incoming, "s")

	require.Contains(t, merged, "summary.json",
		"an entry the upload didn't produce must survive the merge")
	assert.True(t, merged["summary.json"].IsGit())
	assert.Equal(t, "sha256:new", merged["raw.jsonl"].OID, "the uploaded ref must win for its own key")
}

// TestMergeFileRefs_GitWinsOverLFS is the data-loss guard: demoting a
// git-stored entry to an LFS ref would make WritePointerFiles overwrite
// the real summary.json with a ~130-byte pointer stub.
func TestMergeFileRefs_GitWinsOverLFS(t *testing.T) {
	h := NewSessionFinalizeHandler(slog.Default())

	merged := h.mergeFileRefs(
		map[string]lfs.FileRef{"summary.json": {Storage: lfs.StorageGit, Size: 512}},
		map[string]lfs.FileRef{"summary.json": {Storage: lfs.StorageLFS, OID: "sha256:x", Size: 130}},
		"s",
	)

	assert.True(t, merged["summary.json"].IsGit(),
		"Storage=git must win, or the real file is replaced by a pointer stub")
	assert.Equal(t, int64(512), merged["summary.json"].Size)
}

// TestMergeFileRefs_EmptyExistingTakesIncoming — the first-upload case.
func TestMergeFileRefs_EmptyExistingTakesIncoming(t *testing.T) {
	h := NewSessionFinalizeHandler(slog.Default())
	incoming := map[string]lfs.FileRef{"raw.jsonl": {Storage: lfs.StorageLFS, OID: "sha256:a"}}

	assert.Equal(t, incoming, h.mergeFileRefs(nil, incoming, "s"))
}
