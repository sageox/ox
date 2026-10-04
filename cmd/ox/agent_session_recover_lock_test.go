//go:build !short

package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/require"
)

// TestRecoverViaNormalStopWaitsForTheRawCaptureLock verifies recovery cannot
// process a recording while a hook or watcher still holds the raw.jsonl lock.
// "Recover" is run against recordings that only LOOK dead; processing truncates
// an unacknowledged batch, so racing a live append tears the transcript.
// Failure prevented: session recover corrupting a recording that is still being
// written.
func TestRecoverViaNormalStopWaitsForTheRawCaptureLock(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, state)
	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")

	held := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- fileutil.WithFileLock(context.Background(), rawPath, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	recovered := make(chan struct{})
	go func() {
		defer close(recovered)
		// The outcome is irrelevant here: only WHEN it runs is under test.
		_ = recoverViaNormalStop(&agentinstance.Instance{AgentID: agentID}, projectRoot, state)
	}()

	select {
	case <-recovered:
		t.Fatal("recovery processed the recording while the raw capture lock was held")
	case <-time.After(500 * time.Millisecond):
	}

	close(release)
	require.NoError(t, <-holderDone)
	select {
	case <-recovered:
	case <-time.After(30 * time.Second):
		t.Fatal("recovery never ran after the raw capture lock was released")
	}
}
