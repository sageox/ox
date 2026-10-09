package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotifySessionStartedDeferredPreservesFreshRecordingState(t *testing.T) {
	projectRoot, state := startSessionNotifyRecording(t, false)
	pausedAt := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	stoppedAt := pausedAt.Add(time.Minute)
	wantLifecycle := []session.LifecycleEvent{
		{Action: session.LifecycleActionPause, At: pausedAt, Seq: 7, Offset: 4096},
	}
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, state.AgentID, func(current *session.RecordingState) {
		current.SourceOffset = 4096
		current.EntryCount = 7
		current.Lifecycle = wantLifecycle
		current.SuspendedAt = &pausedAt
		current.StoppedAt = &stoppedAt
	}))

	sent := false
	notifySessionStarted(projectRoot, state, func(api.SessionStartedNotification) error {
		sent = true
		return nil
	})

	assert.False(t, sent)
	assert.Equal(t, "deferred", state.LifecycleRegistrationState)
	assert.Empty(t, state.LifecycleRegistrationError)

	got := loadSessionNotifyRecording(t, projectRoot, state.AgentID)
	assert.Equal(t, "deferred", got.LifecycleRegistrationState)
	assert.Empty(t, got.LifecycleRegistrationError)
	assert.Equal(t, int64(4096), got.SourceOffset)
	assert.Equal(t, 7, got.EntryCount)
	assert.Equal(t, wantLifecycle, got.Lifecycle)
	require.NotNil(t, got.SuspendedAt)
	require.NotNil(t, got.StoppedAt)
	assert.Equal(t, pausedAt, *got.SuspendedAt)
	assert.Equal(t, stoppedAt, *got.StoppedAt)
}

func TestNotifySessionStartedNetworkResultPreservesConcurrentLifecycle(t *testing.T) {
	tests := []struct {
		name       string
		sendErr    error
		wantStatus string
		wantError  string
	}{
		{name: "confirmed", wantStatus: "confirmed"},
		{name: "pending", sendErr: errors.New("network unavailable"), wantStatus: "pending", wantError: "network unavailable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectRoot, state := startSessionNotifyRecording(t, true)
			requestStarted := make(chan api.SessionStartedNotification, 1)
			releaseRequest := make(chan struct{})
			done := make(chan struct{})

			go func() {
				defer close(done)
				notifySessionStarted(projectRoot, state, func(notification api.SessionStartedNotification) error {
					requestStarted <- notification
					<-releaseRequest
					return tt.sendErr
				})
			}()

			notification := awaitSessionStartedNotification(t, requestStarted)
			assert.Equal(t, state.SessionID, notification.SessionID)
			assert.Equal(t, state.AgentID, notification.AgentID)

			endedAt := time.Date(2026, 9, 14, 13, 0, 0, 0, time.UTC)
			wantLifecycle := []session.LifecycleEvent{
				{Action: session.LifecycleActionStop, At: endedAt, Seq: 11, Offset: 8192},
			}
			require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, state.AgentID, func(current *session.RecordingState) {
				current.SourceOffset = 8192
				current.EntryCount = 11
				current.Lifecycle = wantLifecycle
				current.StoppedAt = &endedAt
			}))

			close(releaseRequest)
			awaitSessionNotifyDone(t, done)

			assert.Equal(t, tt.wantStatus, state.LifecycleRegistrationState)
			assert.Equal(t, tt.wantError, state.LifecycleRegistrationError)
			got := loadSessionNotifyRecording(t, projectRoot, state.AgentID)
			assert.Equal(t, tt.wantStatus, got.LifecycleRegistrationState)
			assert.Equal(t, tt.wantError, got.LifecycleRegistrationError)
			assert.Equal(t, int64(8192), got.SourceOffset)
			assert.Equal(t, 11, got.EntryCount)
			assert.Equal(t, wantLifecycle, got.Lifecycle)
			require.NotNil(t, got.StoppedAt)
			assert.Equal(t, endedAt, *got.StoppedAt)
		})
	}
}

func TestNotifySessionStartedDoesNotRecreateRemovedRecording(t *testing.T) {
	projectRoot, state := startSessionNotifyRecording(t, true)
	requestStarted := make(chan api.SessionStartedNotification, 1)
	releaseRequest := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		notifySessionStarted(projectRoot, state, func(notification api.SessionStartedNotification) error {
			requestStarted <- notification
			<-releaseRequest
			return nil
		})
	}()

	_ = awaitSessionStartedNotification(t, requestStarted)
	require.NoError(t, session.ClearRecordingStateForAgent(projectRoot, state.AgentID))
	close(releaseRequest)
	awaitSessionNotifyDone(t, done)

	assert.Equal(t, "confirmed", state.LifecycleRegistrationState)
	got, err := session.LoadRecordingStateForAgent(projectRoot, state.AgentID)
	require.NoError(t, err)
	assert.Nil(t, got)
	_, err = os.Stat(filepath.Join(state.SessionPath, ".recording.json"))
	assert.True(t, os.IsNotExist(err), "notification result must not recreate a removed marker")
}

func TestNotifySessionStartedDoesNotUpdateReplacementRecording(t *testing.T) {
	projectRoot, state := startSessionNotifyRecording(t, true)
	requestStarted := make(chan api.SessionStartedNotification, 1)
	releaseRequest := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		notifySessionStarted(projectRoot, state, func(notification api.SessionStartedNotification) error {
			requestStarted <- notification
			<-releaseRequest
			return errors.New("late failure")
		})
	}()

	_ = awaitSessionStartedNotification(t, requestStarted)
	require.NoError(t, session.ClearRecordingStateForAgent(projectRoot, state.AgentID))
	replacement := *state
	replacement.SessionID = sessionid.GenerateSessionID()
	replacement.SessionPath = filepath.Join(filepath.Dir(state.SessionPath), "replacement-recording")
	replacement.OutputFile = filepath.Join(replacement.SessionPath, "raw.jsonl")
	replacement.SourceOffset = 12288
	replacement.EntryCount = 17
	replacement.LifecycleRegistrationState = "deferred"
	replacement.LifecycleRegistrationError = "replacement state"
	replacement.Lifecycle = []session.LifecycleEvent{{
		Action: session.LifecycleActionPause,
		At:     time.Date(2026, 9, 14, 14, 0, 0, 0, time.UTC),
		Seq:    17,
		Offset: 12288,
	}}
	require.NoError(t, session.SaveRecordingState(projectRoot, &replacement))

	close(releaseRequest)
	awaitSessionNotifyDone(t, done)

	assert.Equal(t, "pending", state.LifecycleRegistrationState)
	assert.Equal(t, "late failure", state.LifecycleRegistrationError)
	got := loadSessionNotifyRecording(t, projectRoot, state.AgentID)
	assert.Equal(t, replacement.SessionID, got.SessionID)
	assert.Equal(t, "deferred", got.LifecycleRegistrationState)
	assert.Equal(t, "replacement state", got.LifecycleRegistrationError)
	assert.Equal(t, int64(12288), got.SourceOffset)
	assert.Equal(t, 17, got.EntryCount)
	assert.Equal(t, replacement.Lifecycle, got.Lifecycle)
	_, err := os.Stat(filepath.Join(state.SessionPath, ".recording.json"))
	assert.True(t, os.IsNotExist(err), "notification result must not recreate the replaced marker")
}

func startSessionNotifyRecording(t *testing.T, withUserTurn bool) (string, *session.RecordingState) {
	t.Helper()
	projectRoot := setupSessionTestProject(t)
	state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID:       "OxNotifyCheckpoint",
		AdapterName:   "test-adapter",
		AgentType:     "test-agent",
		Username:      "testuser",
		WorkspacePath: projectRoot,
		Branch:        "test-branch",
	})
	require.NoError(t, err)
	if withUserTurn {
		require.NoError(t, os.WriteFile(filepath.Join(state.SessionPath, "raw.jsonl"), []byte("{\"type\":\"user\",\"content\":\"controlled test prompt\"}\n"), 0600))
	}
	return projectRoot, state
}

func loadSessionNotifyRecording(t *testing.T, projectRoot, agentID string) *session.RecordingState {
	t.Helper()
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, state)
	return state
}

func awaitSessionStartedNotification(t *testing.T, started <-chan api.SessionStartedNotification) api.SessionStartedNotification {
	t.Helper()
	select {
	case notification := <-started:
		return notification
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for session-start request")
		return api.SessionStartedNotification{}
	}
}

func awaitSessionNotifyDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for session-start result")
	}
}
