package daemon

import (
	"testing"
	"time"
)

func TestJitteredDuration(t *testing.T) {
	tests := []struct {
		name     string
		base     time.Duration
		jitter   float64
		wantBase bool // if true, expect exact base
	}{
		{"zero jitter returns base", 15 * time.Second, 0, true},
		{"negative jitter returns base", 15 * time.Second, -0.1, true},
		{"zero base returns zero", 0, 0.10, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := jitteredDuration(tt.base, tt.jitter)
			if tt.wantBase && got != tt.base {
				t.Errorf("got %v, want %v", got, tt.base)
			}
		})
	}

	// verify jitter produces values within expected range
	t.Run("10% jitter stays within bounds", func(t *testing.T) {
		base := 15 * time.Second
		lo := time.Duration(float64(base) * 0.90)
		hi := time.Duration(float64(base) * 1.10)

		for i := 0; i < 100; i++ {
			got := jitteredDuration(base, 0.10)
			if got < lo || got > hi {
				t.Errorf("iteration %d: got %v, want between %v and %v", i, got, lo, hi)
			}
		}
	})

	// verify variance (not all same)
	t.Run("produces varied values", func(t *testing.T) {
		seen := make(map[time.Duration]bool)
		for i := 0; i < 20; i++ {
			seen[jitteredDuration(15*time.Second, 0.10)] = true
		}
		if len(seen) < 2 {
			t.Error("expected varied output, got all identical")
		}
	})
}
