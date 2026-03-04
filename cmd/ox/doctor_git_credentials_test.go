package main

import (
	"testing"
	"time"
)

func TestFormatDurationRough(t *testing.T) {
	tests := []struct {
		d        time.Duration
		expected string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{3 * time.Hour, "3h"},
		{48 * time.Hour, "2d"},
		{-5 * time.Minute, "5m"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatDurationRough(tt.d)
			if result != tt.expected {
				t.Errorf("formatDurationRough(%v) = %q, want %q", tt.d, result, tt.expected)
			}
		})
	}
}

func TestFormatDurationRough_BoundaryValues(t *testing.T) {
	// exactly 1 minute
	if got := formatDurationRough(time.Minute); got != "1m" {
		t.Errorf("1m: got %q", got)
	}
	// exactly 1 hour
	if got := formatDurationRough(time.Hour); got != "1h" {
		t.Errorf("1h: got %q", got)
	}
	// exactly 24 hours
	if got := formatDurationRough(24 * time.Hour); got != "1d" {
		t.Errorf("24h: got %q", got)
	}
	// zero
	if got := formatDurationRough(0); got != "0s" {
		t.Errorf("0: got %q, want 0s", got)
	}
}
