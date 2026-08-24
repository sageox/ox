//go:build !short

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/pkg/sessionsummary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- GH #710 D1 (reframed): stranded transcripts ---
//
// The issue was filed as "undetected git-lfs misconfiguration". That
// diagnosis is wrong: ox ledgers ship no .gitattributes, so git's LFS
// filters never touch them, and the pointer files are ox's own — ledger
// clones are dehydrated BY DESIGN. The real defect is that ox read a stub
// as a transcript and looped. This check detects that content state.

// dehydratedLedger builds a ledger with one session, returning its dir.
func dehydratedLedger(t *testing.T, name string) (ledgerPath, sessionDir string) {
	t.Helper()
	ledgerPath = t.TempDir()
	sessionDir = filepath.Join(ledgerPath, "sessions", name)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	return ledgerPath, sessionDir
}

func writeStubbedSession(t *testing.T, sessionDir, name string, meta *lfs.SessionMeta) {
	t.Helper()
	require.NoError(t, lfs.WritePointerFile(filepath.Join(sessionDir, "raw.jsonl"),
		lfs.AssertUploaded(lfs.FileRef{OID: "sha256:abc123", Size: 4242})))
	if meta == nil {
		meta = lfs.NewSessionMeta(name, "testuser", "a1", "claude-code", time.Now().UTC()).Build()
	}
	require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, meta))
}

func TestSessionNeedsHydration(t *testing.T) {
	name := "2026-05-01T20-04-testuser-OxDHY"

	t.Run("stranded stub needing a summary", func(t *testing.T) {
		ledgerPath, sessionDir := dehydratedLedger(t, name)
		writeStubbedSession(t, sessionDir, name, nil)
		assert.True(t, sessionNeedsHydration(sessionDir, ledgerPath, name))
	})

	t.Run("dehydrated but already summarized", func(t *testing.T) {
		ledgerPath, sessionDir := dehydratedLedger(t, name)
		meta := lfs.NewSessionMeta(name, "testuser", "a1", "claude-code", time.Now().UTC()).Build()
		meta.Title = "A perfectly good title"
		writeStubbedSession(t, sessionDir, name, meta)

		assert.False(t, sessionNeedsHydration(sessionDir, ledgerPath, name),
			"dehydration is the NORMAL state — a session with a good summary needs nothing, "+
				"and flagging it would make doctor noisy enough to ignore")
	})

	t.Run("terminal status is not retried", func(t *testing.T) {
		ledgerPath, sessionDir := dehydratedLedger(t, name)
		meta := lfs.NewSessionMeta(name, "testuser", "a1", "claude-code", time.Now().UTC()).Build()
		meta.SummaryStatus = sessionsummary.SummaryStatusUnrecoverable
		writeStubbedSession(t, sessionDir, name, meta)

		assert.False(t, sessionNeedsHydration(sessionDir, ledgerPath, name),
			"re-reporting a terminal session forever is the loop this issue is about")
	})

	t.Run("hydrated copy in cache", func(t *testing.T) {
		ledgerPath, sessionDir := dehydratedLedger(t, name)
		writeStubbedSession(t, sessionDir, name, nil)

		cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", name)
		require.NoError(t, os.MkdirAll(cacheDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"),
			[]byte("header\ncontent\n"), 0o644))

		assert.False(t, sessionNeedsHydration(sessionDir, ledgerPath, name),
			"the daemon can already read the cached copy")
	})

	t.Run("real content on disk", func(t *testing.T) {
		ledgerPath, sessionDir := dehydratedLedger(t, name)
		require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"),
			[]byte("header\ncontent\n"), 0o644))
		meta := lfs.NewSessionMeta(name, "testuser", "a1", "claude-code", time.Now().UTC()).Build()
		require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, meta))

		assert.False(t, sessionNeedsHydration(sessionDir, ledgerPath, name))
	})
}

func TestSummaryStillNeeded(t *testing.T) {
	tests := []struct {
		name   string
		status string
		title  string
		want   bool
		why    string
	}{
		{"never summarized", "", "", true, "the ordinary pending case"},
		{"pending", sessionsummary.SummaryStatusPending, "", true, "in flight, still needs content"},
		{"failed validation", sessionsummary.SummaryStatusFailedValidation, "", true, "retryable"},
		{"unrecoverable", sessionsummary.SummaryStatusUnrecoverable, "", false, "terminal by definition"},
		{"has a title", "", "Something readable", false, "a human can already read this"},
		{"ok", sessionsummary.SummaryStatusOK, "Title", false, "done"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := &lfs.SessionMeta{SummaryStatus: tt.status, Title: tt.title}
			assert.Equal(t, tt.want, summaryStillNeeded(meta), tt.why)
		})
	}
}

// TestDehydratedWarning_NeverMentionsGitLFS is the cheapest durable
// enforcement of .claude/rules/lfs-no-git-lfs-binary.md at the boundary
// where it actually matters: the text a human reads and acts on.
//
// The #710 reporter followed the git-lfs trail and, by their own account,
// their workaround would convert data/plans/*.md into pointers on the
// daemon's next commit — corrupting the ledger for every teammate. ox
// must never send anyone down that path.
func TestDehydratedWarning_NeverMentionsGitLFS(t *testing.T) {
	stranded := []dehydratedSession{
		{Name: "2026-05-01T20-04-testuser-OxDHY", Reason: "connection refused"},
		{Name: "2026-05-02T09-11-testuser-OxDH2", Reason: "unauthorized"},
	}

	result := dehydratedWarning(stranded, nil, fmt.Errorf("boom"))
	full := result.name + " " + result.message + " " + result.detail

	for _, banned := range []string{"git-lfs", "git lfs", ".gitattributes", "filter=lfs", "install git"} {
		assert.NotContains(t, strings.ToLower(full), strings.ToLower(banned),
			"remediation must never mention %q — ox implements LFS in pure Go and "+
				"ledger clones are dehydrated by design", banned)
	}

	assert.Contains(t, result.detail, "ox doctor --fix", "must give the actual remedy")
	assert.Contains(t, result.detail, "ox session download", "and the single-session escape hatch")
}

func TestDehydratedWarning_TruncatesLongLists(t *testing.T) {
	var stranded []dehydratedSession
	for i := range 12 {
		stranded = append(stranded, dehydratedSession{Name: fmt.Sprintf("session-%02d", i)})
	}

	result := dehydratedWarning(stranded, nil, nil)

	assert.Contains(t, result.message, "12")
	assert.Contains(t, result.detail, "and 7 more", "long lists must be truncated, not dumped")
	assert.NotContains(t, result.detail, "session-11")
}

// TestSessionNeedsHydration_QuietOnHealthyLedger — a ledger full of
// properly summarized dehydrated sessions is the common case, and none of
// them may be flagged. Exercises the per-session predicate across a whole
// ledger rather than checkSessionDehydrated itself, which needs a resolved
// ledger path and a live content-store client.
func TestSessionNeedsHydration_QuietOnHealthyLedger(t *testing.T) {
	ledgerPath := t.TempDir()
	for i := range 50 {
		name := fmt.Sprintf("2026-05-01T20-%02d-testuser-OxOK%02d", i, i)
		sessionDir := filepath.Join(ledgerPath, "sessions", name)
		require.NoError(t, os.MkdirAll(sessionDir, 0o755))
		meta := lfs.NewSessionMeta(name, "testuser", "a1", "claude-code", time.Now().UTC()).Build()
		meta.Title = "Summarized just fine"
		writeStubbedSession(t, sessionDir, name, meta)
	}

	var stranded []dehydratedSession
	entries, err := os.ReadDir(filepath.Join(ledgerPath, "sessions"))
	require.NoError(t, err)
	for _, e := range entries {
		if sessionNeedsHydration(filepath.Join(ledgerPath, "sessions", e.Name()), ledgerPath, e.Name()) {
			stranded = append(stranded, dehydratedSession{Name: e.Name()})
		}
	}

	assert.Empty(t, stranded,
		"50 dehydrated-but-summarized sessions is a healthy ledger, not 50 problems")
}

// TestMarkSessionUnrecoverable_PreservesOtherFields ties D1 back to D3:
// the terminal marking must go through the RMW primitive, or the fix for
// one half of #710 would reintroduce the other half.
func TestMarkSessionUnrecoverable_PreservesOtherFields(t *testing.T) {
	sessionDir := t.TempDir()
	name := "2026-05-01T20-04-testuser-OxMARK"

	meta := lfs.NewSessionMeta(name, "testuser", "a1", "claude-code", time.Now().UTC()).Build()
	meta.RepoID = "repo-xyz"
	meta.ProducedCommits = []string{"abc123"}
	meta.LinkedPRs = []string{"sageox/ox#710"}
	require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, meta))

	markSessionUnrecoverable(sessionDir)

	got, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, sessionsummary.SummaryStatusUnrecoverable, got.SummaryStatus)
	assert.Equal(t, lfs.MaxSummaryAttempts, got.SummaryAttempts,
		"both gates must be set — shouldRetryEmptySummary checks status AND attempts")
	assert.Equal(t, "repo-xyz", got.RepoID, "must not strip unowned fields (GH #710 D3)")
	assert.Equal(t, []string{"abc123"}, got.ProducedCommits)
	assert.Equal(t, []string{"sageox/ox#710"}, got.LinkedPRs)
	assert.False(t, lfs.IsLeakySummaryString(got.ValidationError),
		"the diagnostic must pass the leak validator or WriteSessionMetaOnly rejects it")
}

// TestDehydratedPermanentLoss_IsNotAPass — a session whose transcript was
// never uploaded is gone for good. Reporting that as "downloaded 0
// transcripts, all good" would hide real data loss behind a green check.
func TestDehydratedPermanentLoss_IsNotAPass(t *testing.T) {
	lost := []dehydratedSession{
		{Name: "2026-05-01T20-04-testuser-OxGONE", Permanent: true},
	}

	result := dehydratedPermanentLoss(lost, 3)

	assert.True(t, result.warning, "permanent loss must surface, not pass silently")
	assert.False(t, result.passed)
	assert.Contains(t, result.message, "permanently unavailable")
	assert.Contains(t, result.detail, "OxGONE", "name the affected sessions")
	assert.Contains(t, result.detail, "will not be retried",
		"say it is terminal so this reads as a one-time report, not a recurring nag")
	assert.Contains(t, result.detail, "Recovered 3", "still credit what did succeed")

	full := result.name + result.message + result.detail
	for _, banned := range []string{"git-lfs", "git lfs", ".gitattributes"} {
		assert.NotContains(t, strings.ToLower(full), banned)
	}
}

// TestMarkSessionUnrecoverable_ReportsFailure — the caller decides
// between "settled" and "still broken" based on this error, so swallowing
// it would let a session be reported as resolved when nothing was written.
func TestMarkSessionUnrecoverable_ReportsFailure(t *testing.T) {
	// no meta.json on disk: the mutator returns nil (nothing to write),
	// which must not be reported as an error either.
	require.NoError(t, markSessionUnrecoverable(t.TempDir()),
		"a session with no meta.json is a no-op, not a failure")
}

// TestDehydratedWarning_ReportsLostAlongsideRetryable — permanently-lost
// sessions are marked terminal, so the pass that detects them is the ONLY
// pass that will ever collect them. Reporting just the retryable ones
// would drop the loss report on the floor for good.
func TestDehydratedWarning_ReportsLostAlongsideRetryable(t *testing.T) {
	retryable := []dehydratedSession{{Name: "2026-05-01T20-04-testuser-OxRETRY", Reason: "connection refused"}}
	lost := []dehydratedSession{{Name: "2026-05-01T20-04-testuser-OxGONE", Permanent: true}}

	result := dehydratedWarning(retryable, lost, nil)

	assert.True(t, result.warning)
	assert.Contains(t, result.detail, "OxRETRY", "the retryable one is still reported")
	assert.Contains(t, result.detail, "OxGONE",
		"the permanently-lost one must be reported on this same pass — it is marked "+
			"terminal, so no later run will collect it")
	assert.Contains(t, result.detail, "cannot be recovered")
	// Assert BOTH counts with their numbers. Checking only the "permanently
	// lost" phrase would still pass if the retryable count — or either
	// number — were dropped from the headline.
	assert.Contains(t, result.message, "1 not available locally")
	assert.Contains(t, result.message, "1 permanently lost")
}
