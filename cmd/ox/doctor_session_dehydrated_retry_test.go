package main

// Customer promise under test: session-recording/auto-record.feature, Rule
// "Background housekeeping never writes off a session's summary". A session
// whose first summary failed stays retryable, and `ox doctor --fix` is how a
// coworker asks for that retry: it downloads the transcript and the daemon
// summarizes it.
//
// Failure prevented (GH #1107): once the finalize scan learned to skip
// read-only downloads, a transcript the doctor downloaded for a session whose
// ledger entry records a failed attempt looked exactly like a download, and
// the retry never ran.
//
// Red-first (verified while authoring): drop the WriteNeedsSummaryMarker call
// from hydrateStrandedSessions → this fails on "the doctor's download must
// lead to one summary attempt" (0 LLM runs, no new commit).

import (
	"encoding/json"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoctorSessionContentFix_RetriesFailedSummary(t *testing.T) {
	f := newDownloadLedgerFixture(t)
	const name = "2026-09-24T23-03-riley-OxRiLy"
	f.publishFinishedSession(t, name, "", "failed_validation")
	remoteBefore := runGit(t, f.barePath, "rev-parse", "HEAD")

	// When: Riley runs `ox doctor --fix`, which downloads the transcript.
	res := checkSessionDehydrated(true)
	require.True(t, res.passed, "precondition: the doctor must download the transcript: %s", res.message)

	// And the daemon's finalize scan runs.
	queued, llmRuns := runFinalizePass(t, f.projectRoot, f.ledgerPath)

	// Then: the summary is retried and the result reaches the Ledger.
	assert.Equal(t, 1, queued)
	assert.Equal(t, 1, llmRuns, "the doctor's download must lead to one summary attempt")
	assert.NotEqual(t, remoteBefore, runGit(t, f.barePath, "rev-parse", "HEAD"), "the retried summary must reach the remote")
	var remoteMeta lfs.SessionMeta
	require.NoError(t, json.Unmarshal([]byte(runGit(t, f.barePath, "show", "HEAD:sessions/"+name+"/meta.json")), &remoteMeta))
	assert.Equal(t, "Rewritten on Devon's machine", remoteMeta.Title)
	assert.Equal(t, "ok", remoteMeta.SummaryStatus)
}
