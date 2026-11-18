package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/agentx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/oxactivity"
	"github.com/sageox/ox/internal/session"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// Status line digest: Codex, and Claude Code inside Conductor, have no status
// bar, so the ox line is shown as one systemMessage at the end of a turn, and
// only when it changed. These tests drive the real Stop and PostToolUse
// handlers and parse stdout the way the host does.

const (
	codexDigestSessionID = "codex-thread-digest"
	// codexDigestAgentID is a well-formed agent ID: status line state is keyed
	// by it and rejects a malformed one.
	codexDigestAgentID = "OxCd01"
)

// codexDigestEnv is codexActivityEnv plus what the status line needs: an
// initialized project and a session marker tying the Codex session to the
// test agent. Returns the project root as hooks name it.
func codexDigestEnv(t *testing.T, whispers []whisperstore.WhisperEntry) string {
	t.Helper()
	isolateSessionMarkerDir(t)
	stubOrchestrator(t, "")
	root := codexActivityEnv(t, whispers)
	require.NoError(t, config.SaveProjectConfig(root, &config.ProjectConfig{RepoID: "repo_codex_digest"}))
	require.NoError(t, WriteSessionMarker(&SessionMarker{
		AgentID: codexDigestAgentID, AgentSessionID: codexDigestSessionID, PrimedAt: time.Now(),
	}))
	resolved, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	return resolved
}

func codexDigestCtx(root, agentType, phase string, in *agentx.HookInput) *HookContext {
	if in == nil {
		in = &agentx.HookInput{}
	}
	in.SessionID = codexDigestSessionID
	return &HookContext{
		Phase: phase, AgentType: agentType, ProjectRoot: root,
		Input: in, Marker: &SessionMarker{AgentID: codexDigestAgentID, AgentSessionID: codexDigestSessionID},
	}
}

// digestStatusLine is the notice's first line: ox's status line. The turn's
// flow lanes follow it (flow_lanes_test.go covers those).
func digestStatusLine(message string) string {
	line, _, _ := strings.Cut(message, "\n")
	return line
}

// runStopHook runs the real Stop handler and returns its stdout.
func runStopHook(t *testing.T, root, agentType string) string {
	t.Helper()
	var hookErr error
	out := string(captureRealStdout(t, func() {
		hookErr = handleStop(codexDigestCtx(root, agentType, phaseStop, nil))
	}))
	require.NoError(t, hookErr)
	return out
}

// runOxCall runs the real PostToolUse handler for one shell command.
func runOxCall(t *testing.T, root, command string) {
	t.Helper()
	in := &agentx.HookInput{ToolName: "exec_command", ToolInput: shellInput(t, "cmd", command)}
	captureRealStdout(t, func() {
		require.NoError(t, handleAfterTool(codexDigestCtx(root, "codex", phaseAfterTool, in)))
	})
}

// TestCodexStop_ShowsNoStatusline: in Codex the ox notice is the flow lanes
// alone, shown under each ox call (flow_lanes_test.go); the status line is
// not part of it. Failure prevented: a "● ox  1 plan" line reappearing at the
// end of Codex turns, or a lane already shown under its call repeated at Stop.
func TestCodexStop_ShowsNoStatusline(t *testing.T) {
	root := codexDigestEnv(t, nil)

	assert.Empty(t, runStopHook(t, root, "codex"), "nothing happened yet: no notice")

	runOxCall(t, root, "ox plan view x")
	runOxCall(t, root, "ox agent "+codexDigestAgentID+" team-ctx")
	assert.Empty(t, runStopHook(t, root, "codex"), "counts changed, lanes already shown: Stop stays silent")
}

// TestCodexStop_DigestSilentCases covers the turns that must print nothing.
// Failure prevented: a notice in a session with the status line turned off,
// an unprimed session, or another harness getting Codex's JSON document.
func TestCodexStop_DigestSilentCases(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		setup     func(t *testing.T, root string)
	}{
		{name: "status line off", agentType: "codex", setup: func(t *testing.T, root string) {
			runOxCall(t, root, "ox plan view x")
			setStatusline(t, false)
		}},
		{name: "counts recorded for another session", agentType: "codex", setup: func(t *testing.T, root string) {
			require.NoError(t, oxactivity.RecordInfluence(root, codexDigestAgentID, "another-thread", "plan", "x"))
		}},
		{name: "claude code outside conductor has its status bar", agentType: "claude-code", setup: func(t *testing.T, root string) {
			runOxCall(t, root, "ox plan view x")
		}},
		{name: "claude code in another orchestrator", agentType: "claude-code", setup: func(t *testing.T, root string) {
			runOxCall(t, root, "ox plan view x")
			stubOrchestrator(t, "openclaw")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := codexDigestEnv(t, nil)
			tt.setup(t, root)
			assert.Empty(t, runStopHook(t, root, tt.agentType))
		})
	}
}

// TestConductorClaudeStop_RecordingMark ends the digest with the recording
// mark while the session is recorded, as the status bar does. Failure
// prevented: a Conductor coworker never being told their session is being recorded and shared.
func TestConductorClaudeStop_RecordingMark(t *testing.T) {
	root := codexDigestEnv(t, nil)
	stubOrchestrator(t, "conductor")
	cache := t.TempDir()
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	_, err := session.StartRecording(root, session.StartRecordingOptions{AgentID: codexDigestAgentID, AdapterName: "claude-code"})
	require.NoError(t, err)
	runOxCall(t, root, "ox plan view x")

	doc := decodeCodexHookDoc(t, runStopHook(t, root, "claude-code"))
	assert.Equal(t, "● ox  1 plan  ● rec", digestStatusLine(doc.SystemMessage))
}

// TestCodexStop_WhispersWaitForThePrompt keeps Stop's stdout a valid Stop
// document. Failure prevented: a pending whisper printed at Stop inside a
// PostToolUse envelope, which Codex's Stop schema rejects (dropping the
// digest with it), and the whisper marked delivered without the model ever
// reading it.
func TestCodexStop_WhispersWaitForThePrompt(t *testing.T) {
	withFakeRunner(t, &fakeRunner{})
	root := codexDigestEnv(t, []whisperstore.WhisperEntry{teammateMurmur()})
	runOxCall(t, root, "ls") // delivers nothing countable

	out := runStopHook(t, root, "codex")
	assert.NotContains(t, out, "hookSpecificOutput")
	assert.NotContains(t, out, "Refactoring auth middleware")
}

// stubOrchestrator makes the hooks see orchestrator ("" for none), so a test
// never depends on whether it runs inside Conductor.
func stubOrchestrator(t *testing.T, orchestrator string) {
	t.Helper()
	prev := detectOrchestrator
	detectOrchestrator = func() string { return orchestrator }
	t.Cleanup(func() { detectOrchestrator = prev })
}

// TestConductorClaudeStop_ShowsStatuslineDigest is the customer promise for
// Conductor: it runs Claude Code through its SDK, so the status bar never
// draws, and the coworker sees the ox line at the end of a turn instead —
// once per change. Failure prevented: a Conductor coworker never seeing what
// SageOx contributed to the session.
func TestConductorClaudeStop_ShowsStatuslineDigest(t *testing.T) {
	root := codexDigestEnv(t, nil)
	stubOrchestrator(t, "conductor")

	assert.Empty(t, runStopHook(t, root, "claude-code"), "nothing counted yet: no notice")

	runOxCall(t, root, "ox plan view x")
	out := runStopHook(t, root, "claude-code")
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &doc), "stdout must be one JSON document: %q", out)
	require.Len(t, doc, 1, "only systemMessage: the digest must never steer Claude Code's control flow")
	message, _ := doc["systemMessage"].(string)
	assert.Equal(t, "● ox  1 plan", digestStatusLine(message))
	assert.NotContains(t, out, "\x1b", "hook protocol text carries no escape bytes")

	assert.Empty(t, runStopHook(t, root, "claude-code"), "an unchanged line is not shown twice")
}

// TestConductorClaudeStop_WhispersWaitForThePrompt keeps Stop's stdout one
// JSON document in Conductor. Failure prevented: a pending whisper printed as
// plain text ahead of the digest, which makes Claude Code drop the notice,
// and the whisper marked delivered though the model never reads Stop stdout.
func TestConductorClaudeStop_WhispersWaitForThePrompt(t *testing.T) {
	withFakeRunner(t, &fakeRunner{})
	root := codexDigestEnv(t, []whisperstore.WhisperEntry{teammateMurmur()})
	stubOrchestrator(t, "conductor")
	require.NoError(t, oxactivity.RecordInfluence(root, codexDigestAgentID, codexDigestSessionID, "plan", "x"))

	out := runStopHook(t, root, "claude-code")
	assert.NotContains(t, out, "Refactoring auth middleware")
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &doc), "stdout must be one JSON document: %q", out)
	assert.Equal(t, "● ox  1 plan", doc["systemMessage"])
}
