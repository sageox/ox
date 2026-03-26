package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// --- timeSinceError ---

func TestTimeSinceError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		when time.Time
		want string
	}{
		{"just now (sub-minute)", time.Now().Add(-10 * time.Second), "just now"},
		{"exactly now", time.Now(), "just now"},
		{"1 minute ago", time.Now().Add(-90 * time.Second), "1 minute"},
		{"2 minutes ago", time.Now().Add(-2 * time.Minute), "2 minutes"},
		{"59 minutes ago", time.Now().Add(-59 * time.Minute), "59 minutes"},
		{"1 hour ago", time.Now().Add(-90 * time.Minute), "1 hour"},
		{"2 hours ago", time.Now().Add(-2 * time.Hour), "2 hours"},
		{"23 hours ago", time.Now().Add(-23 * time.Hour), "23 hours"},
		{"1 day ago", time.Now().Add(-25 * time.Hour), "1 day"},
		{"2 days ago", time.Now().Add(-48 * time.Hour), "2 days"},
		{"7 days ago", time.Now().Add(-7 * 24 * time.Hour), "7 days"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := timeSinceError(tt.when)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTimeSinceError_SingularPlural(t *testing.T) {
	t.Parallel()

	// verify singular forms don't have trailing 's'
	oneMin := timeSinceError(time.Now().Add(-61 * time.Second))
	assert.Equal(t, "1 minute", oneMin)

	oneHour := timeSinceError(time.Now().Add(-61 * time.Minute))
	assert.Equal(t, "1 hour", oneHour)

	oneDay := timeSinceError(time.Now().Add(-25 * time.Hour))
	assert.Equal(t, "1 day", oneDay)

	// verify plural forms
	twoMins := timeSinceError(time.Now().Add(-2 * time.Minute))
	assert.Equal(t, "2 minutes", twoMins)

	twoHours := timeSinceError(time.Now().Add(-2 * time.Hour))
	assert.Equal(t, "2 hours", twoHours)

	twoDays := timeSinceError(time.Now().Add(-49 * time.Hour))
	assert.Equal(t, "2 days", twoDays)
}

func TestTimeSinceError_BoundaryAtOneMinute(t *testing.T) {
	t.Parallel()
	// exactly at the minute boundary
	got := timeSinceError(time.Now().Add(-60 * time.Second))
	assert.Equal(t, "1 minute", got)
}

func TestTimeSinceError_BoundaryAtOneHour(t *testing.T) {
	t.Parallel()
	got := timeSinceError(time.Now().Add(-60 * time.Minute))
	assert.Equal(t, "1 hour", got)
}

func TestTimeSinceError_BoundaryAtOneDay(t *testing.T) {
	t.Parallel()
	got := timeSinceError(time.Now().Add(-24 * time.Hour))
	assert.Equal(t, "1 day", got)
}
