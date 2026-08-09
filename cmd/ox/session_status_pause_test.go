package main

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
)

// --- ADR-020 multi-session pause-state surface (PR #627 follow-up) ---
// Failure prevented: sessionRecordingEntry was missing all pause-related
// fields, so `ox session status --json` with multiple recordings could not
// tell which ones were suspended. The single-session JSON branch had the
// fields; the multi-session loop dropped them silently.

// TestBuildSessionRecordingEntry_PropagatesSuspendedFields — Failure
// prevented: a suspended recording is indistinguishable from an active
// one in the multi-session JSON.
func TestBuildSessionRecordingEntry_PropagatesSuspendedFields(t *testing.T) {
	pausedAt := time.Now().Add(-2 * time.Minute).UTC()
	state := &session.RecordingState{
		AgentID:              "OxsusA",
		StartedAt:            time.Now().Add(-10 * time.Minute).UTC(),
		EntryCount:           42,
		SuspendedAt:          &pausedAt,
		PauseCount:           3,
		InheritedPause:       true,
		InheritedFromSession: "2026-05-28-prev-session",
	}

	got := buildSessionRecordingEntry(state, nil, "unknown", draftStatusContext{})

	assert.True(t, got.Suspended, "Suspended must be true when SuspendedAt is set")
	assert.NotEmpty(t, got.SuspendedAt, "SuspendedAt RFC3339 must be populated")
	assert.NotEmpty(t, got.SuspendedDuration, "SuspendedDuration must be populated")
	assert.Equal(t, 3, got.PauseCount)
	assert.True(t, got.InheritedPause)
	assert.Equal(t, "2026-05-28-prev-session", got.InheritedFromSession)
}

// TestBuildSessionRecordingEntry_ActiveSessionHasNoSuspendFields —
// Failure prevented: pause fields leak onto non-suspended rows (e.g.,
// stale memory from a prior iteration), confusing consumers that branch
// on `suspended: true`.
func TestBuildSessionRecordingEntry_ActiveSessionHasNoSuspendFields(t *testing.T) {
	state := &session.RecordingState{
		AgentID:    "OxactA",
		StartedAt:  time.Now().Add(-5 * time.Minute).UTC(),
		EntryCount: 7,
		// SuspendedAt intentionally nil
	}

	got := buildSessionRecordingEntry(state, nil, "alive", draftStatusContext{})

	assert.False(t, got.Suspended)
	assert.Empty(t, got.SuspendedAt)
	assert.Empty(t, got.SuspendedDuration)
	assert.Zero(t, got.PauseCount)
	assert.False(t, got.InheritedPause)
	assert.Empty(t, got.InheritedFromSession)
}
