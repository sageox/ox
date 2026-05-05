package lfs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTestMeta is a small helper used by the recovery tests. It
// writes a minimal SessionMeta to <dir>/meta.json so each test reads
// like a setup + invariant check rather than 30 lines of boilerplate.
func writeTestMeta(t *testing.T, dir string, m *SessionMeta) {
	t.Helper()
	if m.SessionName == "" {
		m.SessionName = filepath.Base(dir)
	}
	if m.Files == nil {
		m.Files = make(map[string]FileRef)
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	if m.Version == "" {
		m.Version = "1.0"
	}
	require.NoError(t, WriteSessionMetaOnly(dir, m))
}

// writeTestSummary writes summary.json with a single Title field —
// enough for readSummaryJSONTitle's recovery path. Other fields are
// irrelevant to the helper.
func writeTestSummary(t *testing.T, dir, title string) {
	t.Helper()
	body := map[string]string{"title": title}
	b, err := json.Marshal(body)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.json"), b, 0644))
}

// TestRecoverEmptyTitleMeta_HealthyMetaSkipped is the idempotency
// floor: a meta with a real title MUST NOT be touched, even if its
// summary.json says something different. We do not second-guess a
// title that the UI is already rendering.
//
// Failure prevented: a future "improvement" treats summary.json as
// the source of truth and clobbers user-edited or LLM-tuned titles
// with stale recovery values.
func TestRecoverEmptyTitleMeta_HealthyMetaSkipped(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{Title: "Real Title", Summary: "real"})
	writeTestSummary(t, dir, "Different Title From summary.json")

	out := RecoverEmptyTitleMeta(dir, false)
	assert.True(t, out.Skipped, "healthy meta must be skipped")

	got, err := ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, "Real Title", got.Title, "must not overwrite a healthy title")
}

// TestRecoverEmptyTitleMeta_UnrecoverableTerminalSkipped covers the
// terminal state. After MaxSummaryAttempts the daemon stamps
// SummaryStatus=unrecoverable; subsequent autofix passes must NOT
// re-engage and re-bump the counter, otherwise terminal becomes a
// misnomer.
func TestRecoverEmptyTitleMeta_UnrecoverableTerminalSkipped(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{Title: "", SummaryStatus: "unrecoverable", SummaryAttempts: MaxSummaryAttempts})

	out := RecoverEmptyTitleMeta(dir, false)
	assert.True(t, out.Skipped, "unrecoverable meta is terminal; must be skipped")
	assert.False(t, out.BumpedAttempts, "must not bump attempt counter past terminal")
}

// TestRecoverEmptyTitleMeta_RecoversFromSummaryJSON is the happy
// repair path. Empty meta.title + clean summary.json → meta is
// updated, status flips to ok, attempt counter clears.
func TestRecoverEmptyTitleMeta_RecoversFromSummaryJSON(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{
		Title:           "",
		SummaryStatus:   "failed_validation",
		ValidationError: "content validation failed: title too short",
		SummaryAttempts: 2,
	})
	writeTestSummary(t, dir, "Recovered Title From summary.json")

	out := RecoverEmptyTitleMeta(dir, false)
	assert.True(t, out.RecoveredFromJSON, "must flag recovery so caller can log/emit")
	assert.False(t, out.Skipped)

	got, err := ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, "Recovered Title From summary.json", got.Title)
	assert.Equal(t, "ok", got.SummaryStatus, "status must transition failed_validation → ok")
	assert.Empty(t, got.ValidationError, "stale ops diagnostic must be cleared")
	assert.Equal(t, 0, got.SummaryAttempts, "attempt counter must reset on success")
}

// TestRecoverEmptyTitleMeta_BumpsAttemptsWithoutSummary covers the
// degenerate case the user actually has on disk: meta.title is empty
// and summary.json is also empty (the daemon's failure stub). We can't
// recover anything, but we must still make progress toward the
// terminal state so the autofix scheduler eventually stops trying.
func TestRecoverEmptyTitleMeta_BumpsAttemptsWithoutSummary(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{Title: "", SummaryStatus: "failed_validation", SummaryAttempts: 0})
	writeTestSummary(t, dir, "") // empty title in summary.json too

	out := RecoverEmptyTitleMeta(dir, false)
	assert.True(t, out.BumpedAttempts, "no recovery available → must bump attempts")
	assert.False(t, out.FlippedTerminal, "should not flip terminal on the first bump")

	got, err := ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, 1, got.SummaryAttempts, "attempts must increment exactly once")
	assert.Equal(t, "failed_validation", got.SummaryStatus, "status stays failed_validation until cap")
}

// TestRecoverEmptyTitleMeta_FlipsToUnrecoverableAtCap is the cap
// behavior. After MaxSummaryAttempts bumps the status flips to
// unrecoverable, breaking the autofix loop on the next pass.
//
// Failure prevented: an unbounded autofix loop on a session whose
// raw.jsonl is corrupt, prompt is too large for the model, or
// summary.json is permanently empty. Without the cap, the daemon
// would re-finalize this session every 30 minutes forever.
func TestRecoverEmptyTitleMeta_FlipsToUnrecoverableAtCap(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{Title: "", SummaryStatus: "failed_validation", SummaryAttempts: MaxSummaryAttempts - 1})

	out := RecoverEmptyTitleMeta(dir, false)
	assert.True(t, out.FlippedTerminal, "the bump that hits the cap must flip to terminal")

	got, err := ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, "unrecoverable", got.SummaryStatus)
	assert.Equal(t, MaxSummaryAttempts, got.SummaryAttempts)

	// Idempotency floor: a second call on the now-terminal meta must
	// be a no-op.
	out2 := RecoverEmptyTitleMeta(dir, false)
	assert.True(t, out2.Skipped, "terminal state must short-circuit subsequent calls")
}

// TestRecoverEmptyTitleMeta_LeakySummaryRejected ensures that a
// summary.json whose title is itself a known validator-leak string
// does NOT get promoted into meta — that would silently re-introduce
// the ox-qqka leak the producer-side fixes already closed.
func TestRecoverEmptyTitleMeta_LeakySummaryRejected(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{Title: "", SummaryStatus: "failed_validation"})
	writeTestSummary(t, dir, "Summary failed content validation: title too short")

	out := RecoverEmptyTitleMeta(dir, false)
	assert.False(t, out.RecoveredFromJSON, "must not promote a leaky title")
	assert.True(t, out.BumpedAttempts, "should fall through to the bump path")
}

// TestRecoverEmptyTitleMeta_DryRunWritesNothing confirms dryRun=true
// reports the planned outcome without touching disk.
func TestRecoverEmptyTitleMeta_DryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{Title: "", SummaryStatus: "failed_validation"})
	writeTestSummary(t, dir, "Recovered Title")

	out := RecoverEmptyTitleMeta(dir, true /*dryRun*/)
	assert.True(t, out.RecoveredFromJSON, "dry-run still reports the planned outcome")

	got, err := ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Empty(t, got.Title, "dry-run must not modify meta.json on disk")
}

// TestResetInlineSummaryEligible_ResetsFileReadBugSessions verifies that
// sessions marked unrecoverable due to the pre-0.7.2 file-read prompt bug
// ("title too short") are reset for re-summarization with the inline prompt.
//
// Failure prevented: 40+ sessions stuck permanently in "unrecoverable"
// state even after the daemon is fixed to use inline prompts.
func TestResetInlineSummaryEligible_ResetsFileReadBugSessions(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{
		Title:           "",
		SummaryStatus:   "unrecoverable",
		SummaryAttempts: MaxSummaryAttempts,
		ValidationError: "content validation failed: title too short (0 chars, minimum 3)",
	})

	reset := ResetInlineSummaryEligible(dir, false, nil, "")
	assert.True(t, reset)

	got, err := ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, "", got.SummaryStatus, "status must be cleared for re-attempt")
	assert.Equal(t, 0, got.SummaryAttempts, "attempts must be reset to zero")
	assert.Equal(t, "", got.ValidationError, "validation error must be cleared")
}

// TestResetInlineSummaryEligible_SkipsHealthySessions verifies that sessions
// with successful summaries are not touched.
func TestResetInlineSummaryEligible_SkipsHealthySessions(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{
		Title:         "Working Session",
		SummaryStatus: "ok",
	})

	reset := ResetInlineSummaryEligible(dir, false, nil, "")
	assert.False(t, reset)
}

// TestResetInlineSummaryEligible_SkipsUnrelatedUnrecoverable verifies that
// sessions marked unrecoverable for OTHER reasons (not the file-read bug)
// are not reset.
func TestResetInlineSummaryEligible_SkipsUnrelatedUnrecoverable(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{
		Title:           "",
		SummaryStatus:   "unrecoverable",
		SummaryAttempts: MaxSummaryAttempts,
		ValidationError: "richness validation failed: key_actions empty",
	})

	reset := ResetInlineSummaryEligible(dir, false, nil, "")
	assert.False(t, reset, "should not reset sessions that failed for other reasons")
}

// TestResetInlineSummaryEligible_DryRun verifies no disk write in dry-run mode.
func TestResetInlineSummaryEligible_DryRun(t *testing.T) {
	dir := t.TempDir()
	writeTestMeta(t, dir, &SessionMeta{
		Title:           "",
		SummaryStatus:   "unrecoverable",
		SummaryAttempts: MaxSummaryAttempts,
		ValidationError: "content validation failed: title too short (0 chars, minimum 3)",
	})

	reset := ResetInlineSummaryEligible(dir, true, nil, "")
	assert.True(t, reset)

	got, err := ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, "unrecoverable", got.SummaryStatus, "dry-run must not modify disk")
}
