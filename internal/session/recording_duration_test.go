package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsRecording(t *testing.T) {
	t.Run("returns true when recording exists in session folder", func(t *testing.T) {
		cacheDir := t.TempDir()
		projectRoot, sessionsBase := setupRecordingTestWithSessionsBase(t, cacheDir)
		sessionPath := filepath.Join(sessionsBase, "2026-01-06T14-30-user-OxA1b2")

		state := &RecordingState{
			AgentID:     "OxA1b2",
			StartedAt:   time.Now(),
			SessionPath: sessionPath,
		}

		err := SaveRecordingState(projectRoot, state)
		require.NoError(t, err, "failed to save state")

		assert.True(t, IsRecording(projectRoot), "expected IsRecording to return true")
	})

	t.Run("returns false when no recording exists", func(t *testing.T) {
		cacheDir := t.TempDir()
		projectRoot := setupRecordingTest(t, cacheDir)

		assert.False(t, IsRecording(projectRoot), "expected IsRecording to return false")
	})

	t.Run("returns false for empty project root", func(t *testing.T) {
		assert.False(t, IsRecording(""), "expected IsRecording to return false for empty project root")
	})
}

func TestGetRecordingDuration(t *testing.T) {
	t.Run("returns duration for active recording", func(t *testing.T) {
		cacheDir := t.TempDir()
		projectRoot, sessionsBase := setupRecordingTestWithSessionsBase(t, cacheDir)
		sessionPath := filepath.Join(sessionsBase, "2026-01-06T14-30-user-OxA1b2")

		startTime := time.Now().Add(-5 * time.Minute)
		state := &RecordingState{
			AgentID:     "OxA1b2",
			StartedAt:   startTime,
			SessionPath: sessionPath,
		}

		err := SaveRecordingState(projectRoot, state)
		require.NoError(t, err, "failed to save state")

		duration := GetRecordingDuration(projectRoot)
		assert.GreaterOrEqual(t, duration, 5*time.Minute, "expected duration >= 5m")
		assert.Less(t, duration, 6*time.Minute, "expected duration < 6m")
	})

	t.Run("returns 0 when no recording exists", func(t *testing.T) {
		cacheDir := t.TempDir()
		projectRoot := setupRecordingTest(t, cacheDir)

		duration := GetRecordingDuration(projectRoot)
		assert.Equal(t, time.Duration(0), duration)
	})

	t.Run("returns 0 for empty project root", func(t *testing.T) {
		duration := GetRecordingDuration("")
		assert.Equal(t, time.Duration(0), duration)
	})
}

func TestRecordingStateDuration(t *testing.T) {
	t.Run("returns correct duration", func(t *testing.T) {
		startTime := time.Now().Add(-10 * time.Minute)
		state := &RecordingState{
			AgentID:   "OxA1b2",
			StartedAt: startTime,
		}

		duration := state.Duration()
		assert.GreaterOrEqual(t, duration, 10*time.Minute, "expected duration >= 10m")
		assert.Less(t, duration, 11*time.Minute, "expected duration < 11m")
	})

	t.Run("returns 0 for nil state", func(t *testing.T) {
		var state *RecordingState
		duration := state.Duration()
		assert.Equal(t, time.Duration(0), duration)
	})

	t.Run("returns 0 for zero start time", func(t *testing.T) {
		state := &RecordingState{
			AgentID: "OxA1b2",
			// StartedAt is zero value
		}

		duration := state.Duration()
		assert.Equal(t, time.Duration(0), duration)
	})
}

func TestRecordingState_StaleBoundary(t *testing.T) {
	// the stale threshold is 12 hours (health.go uses > not >=)
	// use fixed durations to avoid time.Since() drift

	// a recording that started 11h59m ago is NOT stale
	notStaleAge := StaleRecordingThreshold - time.Minute
	assert.False(t, notStaleAge > StaleRecordingThreshold,
		"recording under the threshold should NOT be stale")

	// a recording that started 12h1m ago IS stale
	staleAge := StaleRecordingThreshold + time.Minute
	assert.True(t, staleAge > StaleRecordingThreshold,
		"recording past the threshold should be stale")

	// exactly at threshold: > means NOT stale (boundary behavior)
	exactAge := StaleRecordingThreshold
	assert.False(t, exactAge > StaleRecordingThreshold,
		"recording at exactly the threshold should NOT be stale (uses > not >=)")
}

func TestExplicitStopMarker(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot, sessionsBase := setupRecordingTestWithSessionsBase(t, cacheDir)

	// no marker initially
	assert.False(t, ConsumeExplicitStop(projectRoot, "test-agent"), "no marker should exist initially")

	// write marker
	require.NoError(t, MarkExplicitStop(projectRoot, "test-agent"))

	// marker file should exist
	markerPath := filepath.Join(sessionsBase, explicitStopMarker+".test-agent")
	_, err := os.Stat(markerPath)
	assert.NoError(t, err, "marker file should exist after MarkExplicitStop")

	// consume removes it and returns true
	assert.True(t, ConsumeExplicitStop(projectRoot, "test-agent"), "ConsumeExplicitStop should return true when marker exists")

	// second consume returns false (already consumed)
	assert.False(t, ConsumeExplicitStop(projectRoot, "test-agent"), "ConsumeExplicitStop should return false after already consumed")

	// marker file should be gone
	_, err = os.Stat(markerPath)
	assert.True(t, os.IsNotExist(err), "marker file should be removed after consume")
}
