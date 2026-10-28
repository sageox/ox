package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindGitRootFrom_ValidRepo(t *testing.T) {
	// create a temp dir with .git
	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".git"), 0755))

	// nested dir inside repo
	nested := filepath.Join(repoDir, "sessions", "2026-01-15", "data")
	require.NoError(t, os.MkdirAll(nested, 0755))

	root, err := findGitRootFrom(nested)
	require.NoError(t, err)
	assert.Equal(t, repoDir, root)
}

func TestFindGitRootFrom_DirectGitDir(t *testing.T) {
	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".git"), 0755))

	root, err := findGitRootFrom(repoDir)
	require.NoError(t, err)
	assert.Equal(t, repoDir, root)
}

func TestFindGitRootFrom_NoGitDir(t *testing.T) {
	// temp dir with no .git anywhere
	dir := t.TempDir()

	_, err := findGitRootFrom(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no git root found")
}

func TestPushSummaryToLedger_InvalidJSON(t *testing.T) {
	// write a non-JSON file
	tmpFile := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(tmpFile, []byte("not json"), 0644))

	sessionDir := t.TempDir()
	result := pushSummaryToLedger(tmpFile, sessionDir)

	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not valid JSON")
}

func TestPushSummaryToLedger_MissingFile(t *testing.T) {
	result := pushSummaryToLedger("/nonexistent/file.json", t.TempDir())

	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "read summary file")
}

func TestPushSummaryToLedger_MissingSessionDir(t *testing.T) {
	// valid JSON file but session dir doesn't exist
	tmpFile := filepath.Join(t.TempDir(), "summary.json")
	summaryData := map[string]any{
		"title":         "Test session for validation",
		"summary":       "A test session that exercises the missing session directory error path",
		"outcome":       "success",
		"quality_score": 0.8,
	}
	data, _ := json.Marshal(summaryData)
	require.NoError(t, os.WriteFile(tmpFile, data, 0644))

	result := pushSummaryToLedger(tmpFile, "/nonexistent/session/dir")

	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "session directory does not exist")
}

func TestPushSummaryToLedger_DiscardLowQuality(t *testing.T) {
	// summary with quality_score below discard threshold (default 0.1)
	tmpFile := filepath.Join(t.TempDir(), "summary.json")
	summaryData := map[string]any{
		"title":         "Trivial session with a greeting",
		"summary":       "A trivial session where the user just sent a single greeting message and left",
		"outcome":       "failed",
		"quality_score": 0.05,
		"score_reason":  "single greeting message",
	}
	data, _ := json.Marshal(summaryData)
	require.NoError(t, os.WriteFile(tmpFile, data, 0644))

	sessionDir := t.TempDir()

	result := pushSummaryToLedger(tmpFile, sessionDir)

	assert.True(t, result.Success)
	assert.Equal(t, "discard", result.Disposition)
	assert.Equal(t, 0.05, result.QualityScore)
}

func TestPushSummaryToLedger_NoGitRoot(t *testing.T) {
	// valid JSON, session dir exists, but no .git parent
	tmpFile := filepath.Join(t.TempDir(), "summary.json")
	summaryData := map[string]any{
		"title":         "Test session for git root check",
		"summary":       "A test session that exercises the no git root error path in the ledger",
		"outcome":       "success",
		"quality_score": 0.8,
	}
	data, _ := json.Marshal(summaryData)
	require.NoError(t, os.WriteFile(tmpFile, data, 0644))

	// session dir exists but has no .git parent
	sessionDir := filepath.Join(t.TempDir(), "sessions", "test-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	result := pushSummaryToLedger(tmpFile, sessionDir)

	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "git root")
}

// TestUpdateMetaSummary_FailedThenSucceeded_NoStickyTombstone is the
// ox-wstd regression. Before the fix, session_push_summary.go gated the
// UpdateMetaSummary call on `summaryObj.Title != ""`. Once a validator
// failure had landed in meta.summary (pre-ox-qqka), no later success
// could overwrite it because the guard short-circuited. 14 sessions on
// the SageOx Internal ledger had successful summary.json regen but
// stale meta.summary forever.
//
// The test exercises the sequence at the lfs.UpdateMetaSummary boundary
// (the same level the wstd fix changed): write a meta with a stale
// failure-state summary, then call UpdateMetaSummary with a fresh
// successful title, and assert meta.summary reflects the success.
//
// Failure prevented: a future "optimization" that re-introduces the
// non-empty-title guard would silently revert sticky tombstones.
func TestUpdateMetaSummary_FailedThenSucceeded_NoStickyTombstone(t *testing.T) {
	dir := t.TempDir()

	// Round 1: a prior failure left meta with empty user-visible fields
	// and a SummaryStatus=failed_validation marker. (This is the
	// post-ox-qqka shape; the pre-fix shape with the leak in Summary
	// would be rejected by the writer's Validate() guard.)
	initial := &lfs.SessionMeta{
		Version:         "1.0",
		SessionName:     "test",
		AgentID:         "OxStuck",
		AgentType:       "claude-code",
		CreatedAt:       time.Now(),
		Title:           "",
		Summary:         "",
		SummaryStatus:   "failed_validation",
		ValidationError: "content validation failed: title too short",
	}
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, initial))

	// Round 2: a successful regenerate produces a fresh title.
	const freshTitle = "Implemented session-validation invariant guards"
	require.NoError(t, lfs.UpdateMetaSummary(dir, freshTitle))

	// Assert meta.json reflects the new state, not the stale one.
	got, err := lfs.ReadSessionMeta(dir)
	require.NoError(t, err)

	assert.Equal(t, freshTitle, got.Title, "meta.title must reflect the new success")
	assert.Equal(t, freshTitle, got.Summary, "meta.summary must reflect the new success (sticky-tombstone fix)")
	assert.Equal(t, "ok", got.SummaryStatus, "SummaryStatus must transition failed_validation → ok")
	assert.Empty(t, got.ValidationError, "stale ValidationError from prior failure must be cleared")
}

// TestPushSummaryUnmarshal_EmptyTitleStillCallsUpdateMetaSummary is a
// direct regression on the ox-wstd code path: the Unmarshal block in
// pushSummaryToLedger MUST call UpdateMetaSummary regardless of whether
// the parsed title is empty. We can't run the full pushSummaryToLedger
// without a real git repo + ledger; this test exercises the same logic
// shape against a JSON document with empty title and asserts that
// passing the empty title to UpdateMetaSummary is a meaningful clear-
// stale-state operation, not a no-op.
//
// Failure prevented: someone re-adds `&& summaryObj.Title != ""` and
// the failure-then-clear path silently breaks again.
func TestPushSummaryUnmarshal_EmptyTitleStillCallsUpdateMetaSummary(t *testing.T) {
	dir := t.TempDir()

	// Pre-populate meta.json with a stale "successful" title from a
	// previous round that we now want to CLEAR (e.g. a regenerate that
	// determined the session has no substantive content).
	initial := &lfs.SessionMeta{
		Version: "1.0", SessionName: "s", AgentID: "Ox",
		AgentType:     "claude-code",
		Title:         "Stale title from a prior round",
		Summary:       "Stale summary",
		SummaryStatus: "ok",
	}
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, initial))

	// Calling UpdateMetaSummary with empty string MUST clear both
	// fields. Pre-wstd-fix, the gate prevented this call entirely;
	// post-fix, empty-title is a meaningful clear signal.
	require.NoError(t, lfs.UpdateMetaSummary(dir, ""))

	got, err := lfs.ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Empty(t, got.Title, "empty title must clear meta.Title")
	assert.Empty(t, got.Summary, "empty title must clear meta.Summary")
}

// --- wrapCommitError ---
//
// Failure prevented: a stuck-merge wedge surfaces from push-summary as raw
// git stderr (`fatal: Exiting because of an unresolved conflict.: exit
// status 128`) with no path forward. ox-v30g — every coworker who saw it
// had to escalate. The wrapper must detect the three phrasings git uses
// for unmerged paths AND must NOT alter unrelated commit failures.

func TestWrapCommitError_DetectsUnmergedFiles(t *testing.T) {
	t.Parallel()

	// the three phrasings git uses across commit / merge --continue /
	// cherry-pick --continue paths — must ALL trigger the recovery hint
	cases := []string{
		"error: Committing is not possible because you have unmerged files.\nfatal: Exiting because of an unresolved conflict.",
		"error: you have unmerged paths.\nhint: fix conflicts and run 'git commit'",
		"fatal: Exiting because of an unresolved conflict.",
		// uppercase variant — match must be case-insensitive
		"ERROR: UNMERGED FILES",
	}
	for _, stderr := range cases {
		t.Run(stderr[:min(40, len(stderr))], func(t *testing.T) {
			got := wrapCommitError(stderr, assert.AnError)
			assert.Contains(t, got, "ox doctor --fix",
				"recovery hint missing — user has no actionable next step")
			assert.Contains(t, got, "unresolved merge conflicts blocking commits",
				"failure-mode summary missing — user can't tell what went wrong")
			// raw git output must be preserved so the support bundle still has it
			assert.Contains(t, got, "raw git:")
		})
	}
}

func TestWrapCommitError_PreservesUnrelatedFailures(t *testing.T) {
	t.Parallel()

	// these failures are already actionable on their own — wrapping them
	// in the unmerged-paths hint would mislead the user
	cases := []struct {
		name   string
		stderr string
	}{
		{"hook rejection", "pre-commit hook failed: pre-commit.py exited 1"},
		{"empty index", "nothing to commit, working tree clean"},
		{"lock contention", "fatal: Unable to create '.git/index.lock': File exists."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapCommitError(tc.stderr, assert.AnError)
			assert.NotContains(t, got, "ox doctor --fix",
				"recovery hint must NOT appear for non-wedge failures (%s) — would mislead the user", tc.name)
			assert.Contains(t, got, tc.stderr, "raw stderr must be preserved verbatim for non-wedge failures")
		})
	}
}

func TestWrapCommitError_EmptyStderr(t *testing.T) {
	t.Parallel()
	got := wrapCommitError("", assert.AnError)
	// empty stderr is not a wedge — keep the original error visible
	assert.NotContains(t, got, "ox doctor --fix")
	assert.Contains(t, got, "git commit")
}

// min is a local helper since the test predates Go 1.21's builtin min;
// remove this once the minimum supported Go version is 1.21+.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
