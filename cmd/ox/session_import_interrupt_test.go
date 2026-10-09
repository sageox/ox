package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session/nativeimport"
)

// An import interrupted while it summarizes still publishes what it already
// committed, stops the session in flight and starts nothing new. Each
// unfinished session is reported with the command that retries it.
//
// Failure prevented: Ctrl-C during a long summary killed the run with no
// report, leaving finished sessions committed but unpushed.
func TestImportE2E_InterruptPublishesWhatIsDone(t *testing.T) {
	f := newImportFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.ctx = ctx
	f.summarizer.before = func(prompt string) {
		if strings.Contains(prompt, pushPrompt) {
			cancel() // Ctrl-C while the second session is being summarized
		}
	}
	start := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start, prompt: loginPrompt, reply: "Fixed the cookie."})
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: start.Add(time.Hour), prompt: pushPrompt, reply: "Re-uploaded the object."})
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexB, start: start.Add(2 * time.Hour), prompt: tokenPrompt, reply: "The deploy bot."})

	r := f.run(t, importOptions{yes: true, jsonOut: true, parallel: 1})
	assert.ErrorIs(t, r.err, cli.ErrSilent)
	assert.Equal(t, "interrupted", postHogErrorKind(r.err, 1), "usage telemetry files a Ctrl-C as one")

	done := r.session(t, e2eClaudeA)
	assert.Equal(t, "uploaded", done.Outcome, "a session committed before the interrupt still goes out")
	assert.Equal(t, []string{done.SessionName}, remoteSessionDirs(t, f.barePath))
	assert.Empty(t, strings.TrimSpace(runGit(t, f.ledgerPath, "log", "--oneline", "@{upstream}..HEAD")), "nothing is left unpushed")

	inFlight := r.session(t, e2eCodexA)
	assert.Equal(t, "failed", inFlight.Outcome)
	assert.Equal(t, "interrupted", inFlight.Detail)
	assert.Contains(t, inFlight.Retry, e2eCodexA)

	notStarted := r.session(t, e2eCodexB)
	assert.Equal(t, "failed", notStarted.Outcome)
	assert.Contains(t, notStarted.Detail, "not started")
	assert.Contains(t, notStarted.Retry, e2eCodexB)
	assert.Equal(t, 2, f.summarizer.calls(), "no summary starts after the interrupt")
	assert.Contains(t, f.progress.String(), "Interrupted", "the coworker is told what happens next")
}

// A summary the validators rejected is retried; an interrupt during that retry
// must stop the session, not fall back to the deterministic summary and upload
// it anyway.
//
// Failure prevented: Ctrl-C published a session with a fallback summary the
// coworker never let finish.
func TestImportE2E_InterruptDuringRetryUploadsNothing(t *testing.T) {
	f := newImportFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.ctx = ctx
	var calls atomic.Int32
	f.summarizer.reply = func(string) string { return "not a summary" } // rejected by the validators
	f.summarizer.before = func(string) {
		if calls.Add(1) == 2 {
			cancel() // Ctrl-C during the retry
		}
	}
	start := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start, prompt: loginPrompt, reply: "Fixed the cookie."})

	r := f.run(t, importOptions{yes: true, jsonOut: true})
	assert.ErrorIs(t, r.err, cli.ErrSilent)
	assert.Equal(t, "interrupted", postHogErrorKind(r.err, 1), "usage telemetry files a Ctrl-C as one")
	s := r.session(t, e2eClaudeA)
	assert.Equal(t, "failed", s.Outcome)
	assert.Equal(t, "interrupted", s.Detail)
	assert.Equal(t, 2, f.summarizer.calls(), "no retry after the interrupt")
	assert.Empty(t, remoteSessionDirs(t, f.barePath))
	assert.Empty(t, strings.TrimSpace(runGit(t, f.ledgerPath, "log", "--oneline", "@{upstream}..HEAD")), "nothing was committed")
}

// A JSON run reports each session on stderr as it starts, because stdout is a
// single JSON document printed at the end and a summary can take minutes.
//
// Failure prevented: a long import looked hung, so the coworker interrupted it.
func TestImportE2E_JSONRunReportsProgress(t *testing.T) {
	f := newImportFixture(t)
	start := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start, prompt: loginPrompt, reply: "Fixed the cookie."})
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: start.Add(time.Hour), prompt: pushPrompt, reply: "Re-uploaded the object."})

	r := f.run(t, importOptions{yes: true, jsonOut: true}) // run parses stdout as one document
	require.NoError(t, r.err, r.out)
	lines := strings.Split(strings.TrimSpace(f.progress.String()), "\n")
	require.Len(t, lines, 4, f.progress.String())
	for _, want := range []string{
		"[1/2] summarizing claude " + nativeShortID(e2eClaudeA),
		"[2/2] summarizing codex " + nativeShortID(e2eCodexA),
		"[1/2] claude " + nativeShortID(e2eClaudeA) + " ready",
		"[2/2] codex " + nativeShortID(e2eCodexA) + " ready",
	} {
		assert.Contains(t, f.progress.String(), want)
	}
}
