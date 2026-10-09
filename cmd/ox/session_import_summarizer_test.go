package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/session/nativeimport"
)

// Each session is summarized by its own agent's CLI unless --summarizer names
// one. A CLI that is installed but not logged in holds back only the sessions
// routed to it, and --summarizer routes them to a CLI that works.
//
// Failure prevented: a session summarized by a CLI the coworker never logged
// in to, or a --summarizer choice shown in the preview and then ignored.
func TestImportE2E_EachSessionGoesToTheChosenSummarizer(t *testing.T) {
	f := newImportFixture(t)
	claude, codex := &fakeSummarizer{}, &fakeSummarizer{}
	f.runners = map[nativeimport.Agent]*fakeSummarizer{nativeimport.AgentClaude: claude, nativeimport.AgentCodex: codex}
	f.loggedOut = map[nativeimport.Agent]bool{nativeimport.AgentClaude: true}
	start := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start, prompt: loginPrompt, reply: "Fixed the cookie."})
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: start.Add(time.Hour), prompt: pushPrompt, reply: "Re-uploaded the object."})

	preview := f.run(t, importOptions{dryRun: true, jsonOut: true})
	require.NoError(t, preview.err, preview.out)
	assert.Equal(t, string(stateNeedsSummarizer), preview.session(t, e2eClaudeA).State, "claude is not logged in")
	assert.Contains(t, preview.session(t, e2eClaudeA).Reason, "use --summarizer")
	assert.Equal(t, string(stateReady), preview.session(t, e2eCodexA).State, "codex is unaffected")

	rescued := f.run(t, importOptions{yes: true, jsonOut: true, summarizer: nativeimport.AgentCodex})
	require.NoError(t, rescued.err, rescued.out)
	assert.Equal(t, "uploaded", rescued.session(t, e2eClaudeA).Outcome, "--summarizer codex carries the Claude session")
	assert.Equal(t, "uploaded", rescued.session(t, e2eCodexA).Outcome)
	assert.Equal(t, 2, codex.calls(), "codex summarized both")
	assert.Zero(t, claude.calls(), "the logged-out claude CLI is never run")

	f.loggedOut = nil
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeB, start: start.Add(2 * time.Hour), prompt: tokenPrompt, reply: "The deploy bot."})
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexB, start: start.Add(3 * time.Hour), prompt: loginPrompt, reply: "Fixed the cookie."})
	own := f.run(t, importOptions{yes: true, jsonOut: true, sessions: []string{e2eClaudeB}})
	require.NoError(t, own.err, own.out)
	assert.Equal(t, "uploaded", own.session(t, e2eClaudeB).Outcome)
	assert.Equal(t, 1, claude.calls(), "with both logged in, a Claude session goes to claude by default")
	assert.Equal(t, 2, codex.calls())

	chosen := f.run(t, importOptions{yes: true, jsonOut: true, summarizer: nativeimport.AgentClaude, sessions: []string{e2eCodexB}})
	require.NoError(t, chosen.err, chosen.out)
	assert.Equal(t, "uploaded", chosen.session(t, e2eCodexB).Outcome)
	assert.Equal(t, 2, claude.calls(), "--summarizer claude carries the Codex session")
	assert.Equal(t, 2, codex.calls(), "codex is not run for it")
}
