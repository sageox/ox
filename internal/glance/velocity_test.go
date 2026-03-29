package glance

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestConflictVelocity_DailyBuckets(t *testing.T) {
	day := 24 * time.Hour
	t0 := time.Date(2026, 3, 10, 0, 0, 0, 0, time.Local)

	murmurs := []MurmurRecord{
		// Day 1: alice+bob overlap on pull.go → 1 conflict
		{User: "alice", Time: t0.Add(9 * time.Hour), Files: []string{"internal/sync/pull.go"}},
		{User: "bob", Time: t0.Add(10 * time.Hour), Files: []string{"internal/api/foo.go"}},
		{User: "carol", Time: t0.Add(11 * time.Hour), Files: []string{"internal/sync/pull.go"}},

		// Day 2: 3 different file conflicts
		{User: "alice", Time: t0.Add(day + 9*time.Hour), Files: []string{"internal/sync/pull.go", "internal/sync/merge.go"}},
		{User: "bob", Time: t0.Add(day + 10*time.Hour), Files: []string{"internal/sync/merge.go", "internal/api/sync.go"}},
		{User: "carol", Time: t0.Add(day + 11*time.Hour), Files: []string{"internal/sync/pull.go"}},
		{User: "dave", Time: t0.Add(day + 12*time.Hour), Files: []string{"internal/api/sync.go"}},

		// Day 3: even more conflicts — 4 authors all touching overlapping files
		{User: "alice", Time: t0.Add(2*day + 9*time.Hour), Files: []string{"internal/sync/pull.go", "internal/sync/merge.go", "internal/sync/state.go", "internal/sync/resolve.go"}},
		{User: "bob", Time: t0.Add(2*day + 10*time.Hour), Files: []string{"internal/sync/pull.go", "internal/sync/state.go", "internal/sync/resolve.go"}},
		{User: "carol", Time: t0.Add(2*day + 11*time.Hour), Files: []string{"internal/sync/merge.go", "internal/sync/state.go", "internal/sync/resolve.go"}},
		{User: "dave", Time: t0.Add(2*day + 12*time.Hour), Files: []string{"internal/sync/pull.go", "internal/sync/merge.go"}},
	}

	points := ConflictVelocity(murmurs, t0, t0.Add(3*day), day, day)

	assert.Len(t, points, 3, "should have 3 daily buckets")

	// Day 1: 1 conflict (pull.go: alice+carol)
	assert.Equal(t, 1, points[0].Conflicts, "day 1 conflicts")
	assert.Equal(t, 3, points[0].Murmurs, "day 1 murmurs")

	// Day 2: more conflicts
	assert.Greater(t, points[1].Conflicts, points[0].Conflicts, "day 2 should have more conflicts than day 1")

	// Day 3: even more
	assert.Greater(t, points[2].Conflicts, points[1].Conflicts, "day 3 should have more conflicts than day 2")
}

func TestConflictVelocity_EmptyWindow(t *testing.T) {
	t0 := time.Date(2026, 3, 10, 0, 0, 0, 0, time.Local)
	day := 24 * time.Hour

	// Sessions only on day 1, but we ask for 3 days
	murmurs := []MurmurRecord{
		{User: "alice", Time: t0.Add(9 * time.Hour), Files: []string{"f1"}},
	}

	points := ConflictVelocity(murmurs, t0, t0.Add(3*day), day, day)
	assert.Len(t, points, 3)
	assert.Equal(t, 1, points[0].Murmurs)
	assert.Equal(t, 0, points[1].Murmurs, "day 2 should be empty")
	assert.Equal(t, 0, points[2].Murmurs, "day 3 should be empty")
}

func TestIsEscalating_True(t *testing.T) {
	points := []VelocityPoint{
		{Conflicts: 1, Murmurs: 4},
		{Conflicts: 3, Murmurs: 6},
		{Conflicts: 8, Murmurs: 10},
	}

	assert.True(t, IsEscalating(points, 3))
}

func TestIsEscalating_False_Decreasing(t *testing.T) {
	points := []VelocityPoint{
		{Conflicts: 8, Murmurs: 10},
		{Conflicts: 3, Murmurs: 6},
		{Conflicts: 1, Murmurs: 4},
	}

	assert.False(t, IsEscalating(points, 3))
}

func TestIsEscalating_False_TooFewPoints(t *testing.T) {
	points := []VelocityPoint{
		{Conflicts: 1, Murmurs: 4},
	}

	assert.False(t, IsEscalating(points, 3))
}

func TestIsEscalating_SkipsEmptyWindows(t *testing.T) {
	points := []VelocityPoint{
		{Conflicts: 1, Murmurs: 4},
		{Conflicts: 0, Murmurs: 0}, // empty window — skipped
		{Conflicts: 3, Murmurs: 6},
		{Conflicts: 8, Murmurs: 10},
	}

	assert.True(t, IsEscalating(points, 3))
}
