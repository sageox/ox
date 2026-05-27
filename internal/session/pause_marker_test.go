package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newPauseMarkerProject reuses the established recording-test helper so the
// marker helpers find a valid canonical sessions write path.
func newPauseMarkerProject(t *testing.T) string {
	t.Helper()
	cacheDir := t.TempDir()
	return setupRecordingTest(t, cacheDir)
}

func TestMarkAndPeekExplicitPause(t *testing.T) {
	projectRoot := newPauseMarkerProject(t)

	require.NoError(t, MarkExplicitPause(projectRoot, "Oxa7b3", 42))

	seq, at, found := PeekExplicitPause(projectRoot, "Oxa7b3")
	require.True(t, found)
	assert.Equal(t, 42, seq)
	assert.False(t, at.IsZero())
}

func TestPeekExplicitPause_NotFound(t *testing.T) {
	projectRoot := newPauseMarkerProject(t)

	_, _, found := PeekExplicitPause(projectRoot, "Oxnope")
	assert.False(t, found)
}

func TestPeekExplicitPause_DoesNotConsume(t *testing.T) {
	projectRoot := newPauseMarkerProject(t)

	require.NoError(t, MarkExplicitPause(projectRoot, "Oxa7b3", 7))

	for i := 0; i < 3; i++ {
		_, _, found := PeekExplicitPause(projectRoot, "Oxa7b3")
		assert.Truef(t, found, "peek attempt %d should still find marker", i+1)
	}
}

func TestConsumeExplicitPause_RemovesMarker(t *testing.T) {
	projectRoot := newPauseMarkerProject(t)

	require.NoError(t, MarkExplicitPause(projectRoot, "Oxa7b3", 7))
	seq, found := ConsumeExplicitPause(projectRoot, "Oxa7b3")
	assert.True(t, found)
	assert.Equal(t, 7, seq)

	// second consume returns false
	_, found = ConsumeExplicitPause(projectRoot, "Oxa7b3")
	assert.False(t, found)
}

func TestClearExplicitPause(t *testing.T) {
	projectRoot := newPauseMarkerProject(t)

	require.NoError(t, MarkExplicitPause(projectRoot, "Oxa7b3", 9))
	assert.True(t, ClearExplicitPause(projectRoot, "Oxa7b3"))
	assert.False(t, ClearExplicitPause(projectRoot, "Oxa7b3"), "second clear is a no-op")
}

func TestMarkExplicitPause_PerAgentIsolation(t *testing.T) {
	projectRoot := newPauseMarkerProject(t)

	require.NoError(t, MarkExplicitPause(projectRoot, "Oxa", 1))
	require.NoError(t, MarkExplicitPause(projectRoot, "Oxb", 2))

	_, _, foundA := PeekExplicitPause(projectRoot, "Oxa")
	_, _, foundB := PeekExplicitPause(projectRoot, "Oxb")
	assert.True(t, foundA)
	assert.True(t, foundB)

	// clearing one does not affect the other
	assert.True(t, ClearExplicitPause(projectRoot, "Oxa"))
	_, _, foundB2 := PeekExplicitPause(projectRoot, "Oxb")
	assert.True(t, foundB2, "marker for Oxb must survive Oxa's clear")
}

func TestMarkExplicitPause_EmptyArgs(t *testing.T) {
	assert.Error(t, MarkExplicitPause("", "Oxa", 1))
	assert.Error(t, MarkExplicitPause("/tmp", "", 1))
}
