//go:build !short

package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- GH #710 D3: doctor's retry upload must not strip meta.json ---
//
// This is the half of #710 the reporter actually saw. Their conflict was:
//
//	<<<<<<< HEAD
//	  "summary_status": "failed_validation",
//	  "validation_error": "content validation failed: title too short (0 chars, minimum 3)",
//	  "summary_attempts": 2,
//	=======
//	>>>>>>> <sha> (ox doctor: auto-commit ledger changes)
//
// The "theirs" side is empty because the retry-upload path rebuilt
// meta.json from a builder that never sets those three fields. All are
// `omitempty`, so they didn't go stale — they disappeared. doctor's
// auto-commit then committed the stripped file, both sides had edited the
// same lines, and every `git pull --rebase` conflicted identically until
// the ledger was 40 ahead / 41 behind and could not push.

// testSessionID stands in for the value resolveOrphanSessionID produces
// in production — already resolved (preferring a preserved meta.json id)
// before writeRetryUploadMeta is called.
const testSessionID = "ses_019c0000-0000-7000-8000-0000000000ff"

func retryOrphan(name string) orphanedSession {
	return orphanedSession{
		SessionName: name,
		Meta: &session.StoreMeta{
			AgentID:   "OxRTRY",
			AgentType: "claude-code",
			Username:  "testuser",
			Model:     "claude-opus-4",
		},
		EntryCount: 7,
	}
}

// TestWriteRetryUploadMeta_PreservesSummaryFailureFields is the literal
// reproduction of the reported conflict, as an assertion.
func TestWriteRetryUploadMeta_PreservesSummaryFailureFields(t *testing.T) {
	sessionDir := t.TempDir()
	name := "2026-05-01T20-04-testuser-OxRTRY"

	seeded := lfs.NewSessionMeta(name, "testuser", "OxRTRY", "claude-code", time.Now().UTC()).Build()
	seeded.SummaryStatus = "failed_validation"
	seeded.ValidationError = "content validation failed: title too short (0 chars, minimum 3)"
	seeded.SummaryAttempts = 2
	require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, seeded))

	_, err := writeRetryUploadMeta(sessionDir, t.TempDir(), retryOrphan(name), testSessionID, nil)
	require.NoError(t, err)

	got, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, "failed_validation", got.SummaryStatus,
		"summary_status must survive — dropping it is what produced the rebase conflict")
	assert.Equal(t, seeded.ValidationError, got.ValidationError, "validation_error must survive")
	assert.Equal(t, 2, got.SummaryAttempts,
		"summary_attempts must survive, or the retry cap silently re-arms as well")
}

// TestWriteRetryUploadMeta_PreservesUnownedFields covers the whole class,
// not just the three fields that happened to appear in the report.
func TestWriteRetryUploadMeta_PreservesUnownedFields(t *testing.T) {
	sessionDir := t.TempDir()
	name := "2026-05-01T20-04-testuser-OxRTR2"

	seeded := lfs.NewSessionMeta(name, "testuser", "OxRTR2", "claude-code", time.Now().UTC()).Build()
	seeded.Title = "A real summary title"
	seeded.Summary = "A real summary body that a previous successful finalize produced."
	seeded.Redactions = []lfs.RedactionPass{{
		PassID:    "019c0000-0000-7000-8000-0000000000bb",
		AppliedAt: time.Now().UTC(),
		AppliedBy: "ox session redact-history",
	}}
	seeded.ProducedCommits = []string{"deadbeefcafe"}
	seeded.ProducedPlans = []string{"2026-05-01-a-plan"}
	seeded.LinkedPRs = []string{"sageox/ox#710"}
	seeded.LinkedIssues = []string{"sageox/ox#732"}
	seeded.LinkageStatus = lfs.LinkageStatusNotified
	require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, seeded))

	_, err := writeRetryUploadMeta(sessionDir, t.TempDir(), retryOrphan(name), testSessionID, nil)
	require.NoError(t, err)

	got, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, seeded.Title, got.Title,
		"a re-upload must not blank a summary somebody already produced")
	assert.Equal(t, seeded.Summary, got.Summary)
	require.Len(t, got.Redactions, 1, "redaction audit history must survive a re-upload")
	assert.Equal(t, seeded.ProducedCommits, got.ProducedCommits)
	assert.Equal(t, seeded.ProducedPlans, got.ProducedPlans)
	assert.Equal(t, seeded.LinkedPRs, got.LinkedPRs)
	assert.Equal(t, seeded.LinkedIssues, got.LinkedIssues)
	assert.Equal(t, seeded.LinkageStatus, got.LinkageStatus)
}

// TestWriteRetryUploadMeta_UpdatesOwnedFields proves the preservation
// isn't achieved by simply doing nothing.
func TestWriteRetryUploadMeta_UpdatesOwnedFields(t *testing.T) {
	sessionDir := t.TempDir()
	name := "2026-05-01T20-04-testuser-OxRTR3"

	seeded := lfs.NewSessionMeta(name, "testuser", "OxRTR3", "claude-code", time.Now().UTC()).Build()
	seeded.EntryCount = 1
	require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, seeded))

	refs := map[string]lfs.FileRef{
		"raw.jsonl": {Storage: lfs.StorageLFS, OID: "sha256:abc", Size: 1234},
	}
	_, err := writeRetryUploadMeta(sessionDir, t.TempDir(), retryOrphan(name), testSessionID, refs)
	require.NoError(t, err)

	got, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, 7, got.EntryCount, "entry_count is owned by this path and must be updated")
	assert.Equal(t, "claude-opus-4", got.Model, "model is owned by this path")
	require.Contains(t, got.Files, "raw.jsonl", "the LFS refs are the whole point of the retry")
	assert.Equal(t, "sha256:abc", got.Files["raw.jsonl"].OID)
}

// TestWriteRetryUploadMeta_PreservesTerminalStopReason — doctor
// re-uploading content says nothing about why the session ended.
func TestWriteRetryUploadMeta_PreservesTerminalStopReason(t *testing.T) {
	sessionDir := t.TempDir()
	name := "2026-05-01T20-04-testuser-OxRTR4"

	seeded := lfs.NewSessionMeta(name, "testuser", "OxRTR4", "claude-code", time.Now().UTC()).Build()
	seeded.StopReason = "rate_limited"
	seeded.StopDetail = "5-hour limit reached"
	require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, seeded))

	_, err := writeRetryUploadMeta(sessionDir, t.TempDir(), retryOrphan(name), testSessionID, nil)
	require.NoError(t, err)

	got, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, "rate_limited", got.StopReason)
	assert.Equal(t, "5-hour limit reached", got.StopDetail)
}

// TestWriteRetryUploadMeta_SeedsFreshMetaWhenAbsent — the genuinely-new
// orphan case, where there is nothing to preserve. Must still produce a
// usable meta.json rather than erroring or writing an empty one.
func TestWriteRetryUploadMeta_SeedsFreshMetaWhenAbsent(t *testing.T) {
	sessionDir := t.TempDir()
	name := "2026-05-01T20-04-testuser-OxRTR5"

	require.NoFileExists(t, filepath.Join(sessionDir, "meta.json"))

	_, err := writeRetryUploadMeta(sessionDir, t.TempDir(), retryOrphan(name), testSessionID, nil)
	require.NoError(t, err)

	got, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, name, got.SessionName)
	assert.Equal(t, "claude-code", got.AgentType)
	assert.Equal(t, 7, got.EntryCount)
	assert.Equal(t, session.StopReasonRecovered, got.StopReason,
		"a session with no recorded stop reason gets the fallback")
	assert.NotEmpty(t, got.SessionID, "a fresh meta must still carry a durable session id")
}

// TestWriteRetryUploadMeta_HonorsResolvedSessionID — identity comes from
// resolveOrphanSessionID (which prefers a preserved meta.json id), and a
// retry must write exactly that. A rotated id breaks every conversation
// URL circulated while the session was live.
func TestWriteRetryUploadMeta_HonorsResolvedSessionID(t *testing.T) {
	sessionDir := t.TempDir()
	name := "2026-05-01T20-04-testuser-OxRTR6"

	_, err := writeRetryUploadMeta(sessionDir, t.TempDir(), retryOrphan(name), testSessionID, nil)
	require.NoError(t, err)
	first, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, testSessionID, first.SessionID)

	_, err = writeRetryUploadMeta(sessionDir, t.TempDir(), retryOrphan(name), testSessionID, nil)
	require.NoError(t, err)
	second, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)

	assert.Equal(t, testSessionID, second.SessionID,
		"session_id must be stable across retries")
}

// TestWriteRetryUploadMeta_NilMetaIsAnErrorNotAPanic — a corrupt raw.jsonl
// yields an orphan with no header metadata. Dereferencing it would panic
// inside a doctor sweep, taking down the whole run over one bad session.
func TestWriteRetryUploadMeta_NilMetaIsAnErrorNotAPanic(t *testing.T) {
	orphan := orphanedSession{SessionName: "2026-05-01T20-04-testuser-OxNIL", Meta: nil}

	_, err := writeRetryUploadMeta(t.TempDir(), t.TempDir(), orphan, testSessionID, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no header metadata")
}
