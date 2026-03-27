package glance

import (
	"slices"
	"time"
)

// VelocityPoint represents conflict density at a single time slice.
type VelocityPoint struct {
	Window    time.Time `json:"window"`    // start of time slice
	Conflicts int       `json:"conflicts"` // number of file conflicts in this slice
	Sessions  int       `json:"sessions"`  // number of sessions in this slice
	Density   float64   `json:"density"`   // conflicts per session (0 if no sessions)
}

// ConflictVelocity computes conflict density over sliding time windows.
// sliceWidth is the duration of each window; step is how far to advance between windows.
// For example, sliceWidth=24h step=24h gives daily non-overlapping buckets.
func ConflictVelocity(sessions []SessionRecord, since, until time.Time, sliceWidth, step time.Duration) []VelocityPoint {
	if len(sessions) == 0 || sliceWidth <= 0 || step <= 0 {
		return nil
	}

	// If until is zero, use the latest session time
	if until.IsZero() {
		for _, s := range sessions {
			if s.Time.After(until) {
				until = s.Time
			}
		}
		until = until.Add(time.Minute) // include the last session
	}

	var points []VelocityPoint

	for windowStart := since; windowStart.Before(until); windowStart = windowStart.Add(step) {
		windowEnd := windowStart.Add(sliceWidth)

		// Filter sessions to this window
		var windowSessions []SessionRecord
		for _, s := range sessions {
			if !s.Time.Before(windowStart) && s.Time.Before(windowEnd) {
				windowSessions = append(windowSessions, s)
			}
		}

		conflicts := DetectConflicts(windowSessions)
		conflictCount := len(conflicts.Overlaps)
		sessionCount := len(windowSessions)

		density := 0.0
		if sessionCount > 0 {
			density = float64(conflictCount) / float64(sessionCount)
		}

		points = append(points, VelocityPoint{
			Window:    windowStart,
			Conflicts: conflictCount,
			Sessions:  sessionCount,
			Density:   density,
		})
	}

	return points
}

// IsEscalating returns true if the last N velocity points show increasing conflict density.
// Requires at least minPoints consecutive increases.
func IsEscalating(points []VelocityPoint, minPoints int) bool {
	if len(points) < minPoints {
		return false
	}

	// Filter to points with sessions (skip empty windows)
	active := slices.DeleteFunc(slices.Clone(points), func(p VelocityPoint) bool {
		return p.Sessions == 0
	})

	if len(active) < minPoints {
		return false
	}

	// Check the last minPoints for strictly increasing conflict counts
	tail := active[len(active)-minPoints:]
	for i := 1; i < len(tail); i++ {
		if tail[i].Conflicts <= tail[i-1].Conflicts {
			return false
		}
	}
	return true
}
