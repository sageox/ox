package session

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testAgentID returns a filesystem-safe agent ID derived from the test name.
// Subtests include '/' which would create subdirectories in the scores path.
func testAgentID(t *testing.T, prefix string) string {
	t.Helper()
	safe := strings.ReplaceAll(t.Name(), "/", "-")
	return prefix + "-" + safe
}

// --- A. Round-trip persistence ---

// TestSageoxScore_RoundTrip verifies write then read returns the same score and reason.
// Failure prevented: silent data corruption or serialization mismatch.
func TestSageoxScore_RoundTrip(t *testing.T) {
	agentID := testAgentID(t, "roundtrip")
	t.Cleanup(func() { _ = CleanupSageoxScore(agentID) })

	err := WriteSageoxScore(agentID, 0.75, "strong architectural guidance")
	require.NoError(t, err)

	sf, err := ReadSageoxScore(agentID)
	require.NoError(t, err)
	require.NotNil(t, sf)

	assert.Equal(t, 0.75, sf.Score)
	assert.Equal(t, "strong architectural guidance", sf.Reason)
	assert.False(t, sf.UpdatedAt.IsZero(), "UpdatedAt should be set")
}

// --- B. Missing file handling ---

// TestSageoxScore_ReadMissing verifies that reading a nonexistent score returns nil, nil.
// Failure prevented: spurious errors on fresh installs or first-boot.
func TestSageoxScore_ReadMissing(t *testing.T) {
	sf, err := ReadSageoxScore("nonexistent-agent-id-" + t.Name())
	assert.NoError(t, err)
	assert.Nil(t, sf)
}

// --- C. Input validation ---

// TestSageoxScore_Validation covers all invalid input rejection paths.
// Failure prevented: storing garbage data that corrupts downstream consumers.
func TestSageoxScore_Validation(t *testing.T) {
	tests := []struct {
		name    string
		agentID string
		score   float64
		wantErr string
	}{
		{
			name:    "empty agent ID",
			agentID: "",
			score:   0.5,
			wantErr: "agent ID must not be empty",
		},
		{
			name:    "negative score",
			agentID: "agent-neg",
			score:   -0.1,
			wantErr: "score must be between 0.0 and 1.0",
		},
		{
			name:    "score above 1",
			agentID: "agent-high",
			score:   1.1,
			wantErr: "score must be between 0.0 and 1.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := WriteSageoxScore(tt.agentID, tt.score, "")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestSageoxScore_ReadEmptyAgentID verifies read also rejects empty agent ID.
// Failure prevented: reading from a bare directory path instead of a file.
func TestSageoxScore_ReadEmptyAgentID(t *testing.T) {
	sf, err := ReadSageoxScore("")
	assert.Error(t, err)
	assert.Nil(t, sf)
	assert.Contains(t, err.Error(), "agent ID must not be empty")
}

// --- D. Boundary values ---

// TestSageoxScore_BoundaryValues verifies that exactly 0.0 and 1.0 are valid scores.
// Failure prevented: off-by-one in range check excluding valid extremes.
func TestSageoxScore_BoundaryValues(t *testing.T) {
	tests := []struct {
		name  string
		score float64
	}{
		{name: "zero", score: 0.0},
		{name: "one", score: 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agentID := testAgentID(t, "boundary")
			t.Cleanup(func() { _ = CleanupSageoxScore(agentID) })

			err := WriteSageoxScore(agentID, tt.score, "")
			require.NoError(t, err)

			sf, err := ReadSageoxScore(agentID)
			require.NoError(t, err)
			require.NotNil(t, sf)
			assert.Equal(t, tt.score, sf.Score)
		})
	}
}

// --- E. Reason field ---

// TestSageoxScore_ReasonPersisted verifies the optional reason field round-trips.
// Failure prevented: reason silently dropped during serialization.
func TestSageoxScore_ReasonPersisted(t *testing.T) {
	agentID := testAgentID(t, "reason")
	t.Cleanup(func() { _ = CleanupSageoxScore(agentID) })

	reason := "influenced API design and error handling patterns"
	err := WriteSageoxScore(agentID, 0.9, reason)
	require.NoError(t, err)

	sf, err := ReadSageoxScore(agentID)
	require.NoError(t, err)
	require.NotNil(t, sf)
	assert.Equal(t, reason, sf.Reason)
}

// TestSageoxScore_EmptyReason verifies omitempty behavior for empty reason.
// Failure prevented: "reason":"" appearing in JSON when it should be omitted.
func TestSageoxScore_EmptyReason(t *testing.T) {
	agentID := testAgentID(t, "no-reason")
	t.Cleanup(func() { _ = CleanupSageoxScore(agentID) })

	err := WriteSageoxScore(agentID, 0.5, "")
	require.NoError(t, err)

	// verify the JSON on disk omits the reason key
	data, err := os.ReadFile(scorePath(agentID))
	require.NoError(t, err)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))
	_, hasReason := raw["reason"]
	assert.False(t, hasReason, "empty reason should be omitted from JSON via omitempty")
}

// --- F. Cleanup ---

// TestSageoxScore_Cleanup verifies file removal and subsequent read returns nil.
// Failure prevented: stale score files persisting after agent teardown.
func TestSageoxScore_Cleanup(t *testing.T) {
	agentID := testAgentID(t, "cleanup")

	err := WriteSageoxScore(agentID, 0.6, "test")
	require.NoError(t, err)

	err = CleanupSageoxScore(agentID)
	require.NoError(t, err)

	sf, err := ReadSageoxScore(agentID)
	assert.NoError(t, err)
	assert.Nil(t, sf, "score should be nil after cleanup")
}

// TestSageoxScore_CleanupIdempotent verifies cleanup on missing file is a no-op.
// Failure prevented: error noise when cleaning up already-removed scores.
func TestSageoxScore_CleanupIdempotent(t *testing.T) {
	err := CleanupSageoxScore("does-not-exist-" + t.Name())
	assert.NoError(t, err)
}

// TestSageoxScore_CleanupEmptyAgentID verifies cleanup with empty ID is a safe no-op.
// Failure prevented: accidentally deleting the scores directory itself.
func TestSageoxScore_CleanupEmptyAgentID(t *testing.T) {
	err := CleanupSageoxScore("")
	assert.NoError(t, err)
}

// --- G. Overwrite ---

// TestSageoxScore_Overwrite verifies that writing twice updates the stored value.
// Failure prevented: stale scores surviving an update.
func TestSageoxScore_Overwrite(t *testing.T) {
	agentID := testAgentID(t, "overwrite")
	t.Cleanup(func() { _ = CleanupSageoxScore(agentID) })

	require.NoError(t, WriteSageoxScore(agentID, 0.3, "initial"))

	require.NoError(t, WriteSageoxScore(agentID, 0.8, "updated"))

	sf, err := ReadSageoxScore(agentID)
	require.NoError(t, err)
	require.NotNil(t, sf)
	assert.Equal(t, 0.8, sf.Score)
	assert.Equal(t, "updated", sf.Reason)
}

// --- H. Stale score cleanup ---

// TestCleanupStaleScores_RemovesInactive verifies stale scores are removed
// while active agent scores are preserved.
// Failure prevented: score files accumulating indefinitely for expired agents.
func TestCleanupStaleScores_RemovesInactive(t *testing.T) {
	active := testAgentID(t, "active")
	stale1 := testAgentID(t, "stale1")
	stale2 := testAgentID(t, "stale2")
	t.Cleanup(func() {
		_ = CleanupSageoxScore(active)
		_ = CleanupSageoxScore(stale1)
		_ = CleanupSageoxScore(stale2)
	})

	require.NoError(t, WriteSageoxScore(active, 0.7, "active"))
	require.NoError(t, WriteSageoxScore(stale1, 0.3, "stale"))
	require.NoError(t, WriteSageoxScore(stale2, 0.5, "stale"))

	removed, err := CleanupStaleScores(map[string]bool{active: true})
	require.NoError(t, err)
	assert.Equal(t, 2, removed)

	// active score preserved
	sf, err := ReadSageoxScore(active)
	require.NoError(t, err)
	assert.NotNil(t, sf)

	// stale scores gone
	sf, err = ReadSageoxScore(stale1)
	require.NoError(t, err)
	assert.Nil(t, sf)

	sf, err = ReadSageoxScore(stale2)
	require.NoError(t, err)
	assert.Nil(t, sf)
}

// TestCleanupStaleScores_EmptyDir verifies cleanup on nonexistent dir is a no-op.
// Failure prevented: error on fresh install with no scores directory.
func TestCleanupStaleScores_EmptyDir(t *testing.T) {
	removed, err := CleanupStaleScores(map[string]bool{})
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, removed, 0)
}
