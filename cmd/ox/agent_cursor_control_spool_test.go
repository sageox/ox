package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/livesession"
	"github.com/stretchr/testify/require"
)

// Failure prevented (ox#1961 review): pausing a Cursor recording drains its
// export once more, up to the pause boundary, and moves the recording's cursor
// past what it drained. That drain's batch never reached the live stream, so
// turns exported just before the pause were missing from it with no gap in
// their place. The control hands its batch on with the pause state it was
// committed under: a turn drained before the pause streams, and one drained
// while paused (by resume) never does.
func TestCursorPauseAndResumeHandTheirDrainsToTheLiveStream(t *testing.T) {
	f := newCursorHostFixture(t)
	t.Setenv("SAGEOX_DAEMON", "false")
	require.NoError(t, config.SaveProjectConfig(f.root, &config.ProjectConfig{
		RepoID: "cursor-host-test", SessionRecording: "manual", SessionPublishing: config.SessionPublishingAuto,
	}))
	state := f.record(t)
	_, err := livesession.Prepare(t.Context(), livesession.Config{Endpoint: "https://live.test.sageox.ai", DataDir: t.TempDir()},
		state.SessionPath, livesession.OpenParams{SessionID: state.SessionID, SessionName: filepath.Base(state.SessionPath)})
	require.NoError(t, err)
	inst := &agentinstance.Instance{AgentID: state.AgentID}
	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
	public, private := cursorReviewTurn("PUBLIC_BEFORE_PAUSE"), cursorReviewTurn("PRIVATE_WHILE_PAUSED")

	f.write(t, public) // exported, and no watcher has drained it yet
	captureStdoutForPlanCLI(t, func() { require.NoError(t, runAgentSessionPause(inst, nil)) })
	rawAtPause, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	f.write(t, public+private)
	captureStdoutForPlanCLI(t, func() { require.NoError(t, runAgentSessionResume(inst, nil)) })

	rawAfter, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	require.True(t, bytes.Contains(rawAtPause, []byte("PUBLIC_BEFORE_PAUSE")), "precondition: pause's drain committed the pre-pause turn")
	require.True(t, bytes.Contains(rawAfter, []byte("PRIVATE_WHILE_PAUSED")), "precondition: resume's drain committed the paused turn")
	_, drained, ok := bytes.Cut(rawAtPause, []byte("\n")) // what pause's drain appended after the header
	require.True(t, ok, "precondition: raw.jsonl starts with its header line")
	spool := liveSpoolOf(t, state.SessionPath)
	require.True(t, bytes.Equal(drained, spool),
		"the stream must hold exactly the bytes pause's drain committed: spool has %d bytes, the drain committed %d", len(spool), len(drained))
	require.False(t, bytes.Contains(spool, []byte("PRIVATE_WHILE_PAUSED")), "a turn drained while paused reached the live stream")
}

// liveSpoolOf is the live spool of the session in dir; empty when none was written.
func liveSpoolOf(t *testing.T, dir string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "spool.jsonl"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	return b
}
