package main

import (
	"path/filepath"
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

// Codex status line digest: Codex has no status bar, so the ox line is shown
// as one systemMessage at the end of a turn, and only when it changed. These
// tests drive the real Stop and PostToolUse handlers and parse stdout the way
// Codex does.

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

// TestCodexStop_ShowsStatuslineDigest is the customer promise: after a Codex
// turn that pulled team context, the coworker sees what ox gave the session,
// once. Failure prevented: Codex and the ChatGPT Codex app showing nothing of
// what SageOx contributed, or repeating the same line after every turn.
func TestCodexStop_ShowsStatuslineDigest(t *testing.T) {
	root := codexDigestEnv(t, nil)

	assert.Empty(t, runStopHook(t, root, "codex"), "nothing counted yet: no notice")

	runOxCall(t, root, "ox plan view x")
	runOxCall(t, root, "ox agent "+codexDigestAgentID+" team-ctx")

	out := runStopHook(t, root, "codex")
	doc := decodeCodexHookDoc(t, out)
	assert.Equal(t, "● ox  1 plan · 1 doc", doc.SystemMessage)
	assert.Nil(t, doc.HookSpecificOutput, "Codex's Stop schema rejects hookSpecificOutput")
	assert.Nil(t, doc.Continue, "the digest must never steer Codex's control flow")
	assert.Nil(t, doc.Decision)
	assert.NotContains(t, out, "\x1b", "hook protocol text carries no escape bytes")

	assert.Empty(t, runStopHook(t, root, "codex"), "an unchanged line is not shown twice")

	runOxCall(t, root, "ox plan view y")
	doc = decodeCodexHookDoc(t, runStopHook(t, root, "codex"))
	assert.Equal(t, "● ox  2 plans · 1 doc", doc.SystemMessage, "a new item shows the updated line")
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
		{name: "claude code stop is unchanged", agentType: "claude-code", setup: func(t *testing.T, root string) {
			runOxCall(t, root, "ox plan view x")
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

// TestCodexStop_RecordingMark ends the digest with the recording mark while
// the session is recorded, as the status bar does. Failure prevented: a Codex
// coworker never being told their session is being recorded and shared.
func TestCodexStop_RecordingMark(t *testing.T) {
	root := codexDigestEnv(t, nil)
	cache := t.TempDir()
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	_, err := session.StartRecording(root, session.StartRecordingOptions{AgentID: codexDigestAgentID, AdapterName: "codex"})
	require.NoError(t, err)
	runOxCall(t, root, "ox plan view x")

	doc := decodeCodexHookDoc(t, runStopHook(t, root, "codex"))
	assert.Equal(t, "● ox  1 plan  ● rec", doc.SystemMessage)
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
