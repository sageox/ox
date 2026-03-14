package main

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon"
	"github.com/stretchr/testify/assert"
)

func TestFormatIndexTiming(t *testing.T) {
	tests := []struct {
		name   string
		result *daemon.CodeIndexResult
		want   string
	}{
		{
			name: "typical index run",
			result: &daemon.CodeIndexResult{
				IndexDurationMs:   7000,
				SymbolDurationMs:  1200,
				CommentDurationMs: 800,
				TotalDurationMs:   9000,
			},
			want: "total 9s: index 7s, symbols 1s, comments 1s",
		},
		{
			name: "zero durations (incremental no-op)",
			result: &daemon.CodeIndexResult{
				IndexDurationMs:   500,
				SymbolDurationMs:  0,
				CommentDurationMs: 0,
				TotalDurationMs:   500,
			},
			want: "total 1s: index 1s, symbols <1s, comments <1s",
		},
		{
			name:   "all zeros",
			result: &daemon.CodeIndexResult{},
			want:   "total <1s: index <1s, symbols <1s, comments <1s",
		},
		{
			name: "large repo with minutes",
			result: &daemon.CodeIndexResult{
				IndexDurationMs:   82000,
				SymbolDurationMs:  15000,
				CommentDurationMs: 8000,
				TotalDurationMs:   105000,
			},
			want: "total 1m 45s: index 1m 22s, symbols 15s, comments 8s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatIndexTiming(tt.result)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatDurationBrief(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"zero", 0, "<1s"},
		{"sub-second rounds up", 500 * time.Millisecond, "1s"},
		{"very small", 100 * time.Millisecond, "<1s"},
		{"one second", 1 * time.Second, "1s"},
		{"seconds", 7 * time.Second, "7s"},
		{"minute boundary", 60 * time.Second, "1m"},
		{"minutes and seconds", 90 * time.Second, "1m 30s"},
		{"hour boundary", 1 * time.Hour, "1h"},
		{"hours and minutes", 90 * time.Minute, "1h 30m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDurationBrief(tt.d)
			assert.Equal(t, tt.want, got)
		})
	}
}
