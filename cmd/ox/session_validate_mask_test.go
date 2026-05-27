package main

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
)

func TestValidateMaskInvariant_NoLifecycle(t *testing.T) {
	// No lifecycle + counts match → no error.
	assert.Empty(t, validateMaskInvariant(10, 10, nil))

	// No lifecycle but entries dropped → error (something masked entries without lifecycle).
	assert.Contains(t, validateMaskInvariant(10, 7, nil), "no lifecycle events but 3 entries dropped")
}

func TestValidateMaskInvariant_MatchingMask(t *testing.T) {
	lifecycle := []session.LifecycleEvent{
		{Action: session.LifecycleActionPause, Seq: 3, At: time.Now()},
		{Action: session.LifecycleActionResume, Seq: 7, At: time.Now()},
	}
	// Original 10, after mask 6 (4 dropped: indices 3..6)
	assert.Empty(t, validateMaskInvariant(10, 6, lifecycle))
}

func TestValidateMaskInvariant_LeakDetected(t *testing.T) {
	lifecycle := []session.LifecycleEvent{
		{Action: session.LifecycleActionPause, Seq: 3, At: time.Now()},
		{Action: session.LifecycleActionResume, Seq: 7, At: time.Now()},
	}
	// Mask should drop 4 entries (indices 3..6) but caller passed 10→10 (no drop).
	got := validateMaskInvariant(10, 10, lifecycle)
	assert.Contains(t, got, "mask invariant")
	assert.Contains(t, got, "lifecycle expects 4 masked entries but 0 were dropped")
}

func TestValidateMaskInvariant_OpenEndedPauseToEOS(t *testing.T) {
	lifecycle := []session.LifecycleEvent{
		{Action: session.LifecycleActionPause, Seq: 5, At: time.Now()},
	}
	// Original 10, open-ended pause from 5 → exclude indices 5..9 (5 entries) → 5 remain
	assert.Empty(t, validateMaskInvariant(10, 5, lifecycle))

	// Tampered: only 1 dropped instead of 5
	got := validateMaskInvariant(10, 9, lifecycle)
	assert.Contains(t, got, "lifecycle expects 5")
}
