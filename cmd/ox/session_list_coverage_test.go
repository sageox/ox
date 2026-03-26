package main

import (
	"testing"
	"time"
)

func TestFormatSessionDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{
			name:     "zero duration",
			duration: 0,
			want:     "-",
		},
		{
			name:     "negative duration",
			duration: -5 * time.Second,
			want:     "-",
		},
		{
			name:     "sub-second",
			duration: 500 * time.Millisecond,
			want:     "-",
		},
		{
			name:     "one second",
			duration: 1 * time.Second,
			want:     "1s",
		},
		{
			name:     "thirty seconds",
			duration: 30 * time.Second,
			want:     "30s",
		},
		{
			name:     "59 seconds",
			duration: 59 * time.Second,
			want:     "59s",
		},
		{
			name:     "exactly one minute",
			duration: 1 * time.Minute,
			want:     "1m",
		},
		{
			name:     "5 minutes 30 seconds rounds down",
			duration: 5*time.Minute + 30*time.Second,
			want:     "5m",
		},
		{
			name:     "59 minutes",
			duration: 59 * time.Minute,
			want:     "59m",
		},
		{
			name:     "exactly one hour",
			duration: 1 * time.Hour,
			want:     "1h",
		},
		{
			name:     "one hour thirty minutes",
			duration: 1*time.Hour + 30*time.Minute,
			want:     "1h30m",
		},
		{
			name:     "two hours no minutes",
			duration: 2 * time.Hour,
			want:     "2h",
		},
		{
			name:     "three hours fifteen minutes",
			duration: 3*time.Hour + 15*time.Minute,
			want:     "3h15m",
		},
		{
			name:     "24 hours",
			duration: 24 * time.Hour,
			want:     "24h",
		},
		{
			name:     "large duration 100 hours 5 minutes",
			duration: 100*time.Hour + 5*time.Minute,
			want:     "100h5m",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatSessionDuration(tt.duration)
			if got != tt.want {
				t.Errorf("formatSessionDuration(%v) = %q, want %q", tt.duration, got, tt.want)
			}
		})
	}
}
