package main

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/oxactivity"
	"github.com/sageox/ox/internal/session"
)

// `ox statusline --watch` runs in Conductor's Run pane, where nothing names
// the session: it is found from the worktree.

// startWorktreeRecording records agentID's harness session sid in root.
func startWorktreeRecording(t *testing.T, root, agentID, sid string) {
	t.Helper()
	_, err := session.StartRecording(root, session.StartRecordingOptions{
		AgentID: agentID, AgentSessionID: sid, AdapterName: "claude-code", WorkspacePath: root,
	})
	require.NoError(t, err)
}

// TestStatuslineWatch_FindsTheWorktreesSession is the customer promise: in a
// Conductor workspace, the Run pane shows this chat's ox line. Failure
// prevented: a Run pane stuck at "● ox" though the chat beside it is pulling
// team context.
func TestStatuslineWatch_FindsTheWorktreesSession(t *testing.T) {
	root := codexDigestEnv(t, nil) // primed: a marker ties the session to the agent
	cache := t.TempDir()
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	startWorktreeRecording(t, root, codexDigestAgentID, codexDigestSessionID)
	require.NoError(t, oxactivity.RecordInfluence(root, codexDigestAgentID, codexDigestSessionID, "plan", "x"))

	assert.Equal(t, codexDigestSessionID, resolveStatuslineSessionForDir(root))
	assert.Equal(t, "● ox  1 plan  ● rec", statuslineWatchFrame(root, statuslineOptions{}))
}

// TestStatuslineWatch_NoSingleSessionShowsIdle covers the worktrees whose
// session cannot be named. Failure prevented: one chat's counts shown for
// another chat in the same Conductor workspace, or for another worktree.
func TestStatuslineWatch_NoSingleSessionShowsIdle(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string)
	}{
		{name: "no recording", setup: func(t *testing.T, root string) {}},
		{name: "two chats recording in one workspace", setup: func(t *testing.T, root string) {
			startWorktreeRecording(t, root, codexDigestAgentID, codexDigestSessionID)
			startWorktreeRecording(t, root, "OxCd02", "second-chat")
		}},
		{name: "the only chat here has exited", setup: func(t *testing.T, root string) {
			startWorktreeRecording(t, root, codexDigestAgentID, codexDigestSessionID)
			markRecordingExited(t, root, codexDigestAgentID)
		}},
		{name: "recording in another worktree", setup: func(t *testing.T, root string) {
			_, err := session.StartRecording(root, session.StartRecordingOptions{
				AgentID: codexDigestAgentID, AgentSessionID: codexDigestSessionID,
				AdapterName: "claude-code", WorkspacePath: t.TempDir(),
			})
			require.NoError(t, err)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := codexDigestEnv(t, nil)
			cache := t.TempDir()
			t.Setenv("HOME", cache)
			t.Setenv("XDG_CACHE_HOME", cache)
			require.NoError(t, oxactivity.RecordInfluence(root, codexDigestAgentID, codexDigestSessionID, "plan", "x"))
			tt.setup(t, root)

			assert.Empty(t, resolveStatuslineSessionForDir(root))
			assert.Equal(t, "● ox", statuslineWatchFrame(root, statuslineOptions{}))
		})
	}
}

// TestWatchStatusline_RedrawsOnlyOnChange keeps the pane quiet: a frame is
// written once, again only when it changes, in place on a terminal and as
// its own line elsewhere.
func TestWatchStatusline_RedrawsOnlyOnChange(t *testing.T) {
	for _, inPlace := range []bool{true, false} {
		frames := []string{"● ox", "● ox", "● ox  1 plan", "● ox  1 plan"}
		var out bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		tick := make(chan time.Time)
		done := make(chan struct{})
		i := 0
		go func() {
			watchStatusline(ctx, &out, func() watchFrame { f := frames[i]; i++; return watchFrame{status: f} }, tick, inPlace)
			close(done)
		}()
		for range len(frames) - 1 {
			tick <- time.Time{}
		}
		cancel()
		<-done

		if inPlace {
			assert.Equal(t, "\r\x1b[2K● ox\r\x1b[2K● ox  1 plan\n", out.String())
		} else {
			assert.Equal(t, "● ox\n● ox  1 plan\n", out.String())
		}
	}
}

// markRecordingExited points agentID's recording at a process that has exited.
func markRecordingExited(t *testing.T, root, agentID string) {
	t.Helper()
	cmd := exec.Command("true")
	require.NoError(t, cmd.Run())
	require.NoError(t, session.UpdateRecordingStateForAgent(root, agentID, func(s *session.RecordingState) {
		s.ParentPID = cmd.Process.Pid
	}))
}

// TestStatuslineWatch_IgnoresExitedChats keeps the pane working after a chat
// crashed or closed without stopping its recording. Failure prevented: one
// stale recording leaving Conductor's Run pane at "● ox" for good.
func TestStatuslineWatch_IgnoresExitedChats(t *testing.T) {
	root := codexDigestEnv(t, nil)
	cache := t.TempDir()
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	startWorktreeRecording(t, root, "OxCd02", "crashed-chat")
	markRecordingExited(t, root, "OxCd02")
	startWorktreeRecording(t, root, codexDigestAgentID, codexDigestSessionID)

	assert.Equal(t, codexDigestSessionID, resolveStatuslineSessionForDir(root))
}

// TestStatusline_LivePanesAreLongRunning: --watch and --wrap run for as long
// as the pane or the wrapped app does. Failure prevented: a pane left open for
// hours reported as one command's latency, while the status bar's own
// invocations must stay measured.
func TestStatusline_LivePanesAreLongRunning(t *testing.T) {
	for flag, want := range map[string]bool{"watch": true, "wrap": true, "json": false, "": false} {
		for _, name := range []string{"watch", "wrap", "json"} {
			require.NoError(t, statuslineCmd.Flags().Set(name, "false"))
		}
		if flag != "" {
			require.NoError(t, statuslineCmd.Flags().Set(flag, "true"))
		}
		assert.Equal(t, want, cli.IsLongRunning(statuslineCmd), "flag=%q", flag)
	}
	for _, name := range []string{"watch", "wrap", "json"} {
		require.NoError(t, statuslineCmd.Flags().Set(name, "false"))
	}
}
