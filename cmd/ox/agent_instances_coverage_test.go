package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// --- shortenPath ---

func TestShortenPath(t *testing.T) {
	t.Parallel()

	home, _ := os.UserHomeDir()

	tests := []struct {
		name string
		path string
		want string
	}{
		{"empty path returns dash", "", "-"},
		{"short path unchanged", "/tmp/project", "/tmp/project"},
		{"home dir replaced with tilde", home + "/code/project", "~/code/project"},
		{"home dir exact match", home, "~"},
		{
			"very long path truncated to last two parts",
			home + "/deeply/nested/directory/structure/my-project",
			".../structure/my-project",
		},
		{
			"long path without home prefix",
			"/very/long/absolute/path/that/exceeds/the/maximum/length/allowed/project",
			".../allowed/project",
		},
		{
			"path exactly at max length not truncated",
			// 38 chars max; build a path that's exactly 38
			strings.Repeat("a", 38),
			strings.Repeat("a", 38),
		},
		{
			"path one over max gets truncated",
			strings.Repeat("x", 39),
			"..." + strings.Repeat("x", 35),
		},
		{
			"root path unchanged",
			"/",
			"/",
		},
		{
			"single component path",
			"/project",
			"/project",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := shortenPath(tt.path)
			assert.LessOrEqual(t, len(got), 38,
				"shortened path should not exceed 38 chars, got %d: %q", len(got), got)

			// for non-empty, non-dash results, verify basic properties
			if tt.path != "" {
				assert.NotEqual(t, "-", got)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestShortenPath_HomeEnvNotSet(t *testing.T) {
	// when home dir can't be resolved, path should still work
	// (os.UserHomeDir uses env; if it returns empty, HasPrefix("", x) is false)
	got := shortenPath("/some/absolute/path")
	assert.Equal(t, "/some/absolute/path", got)
}

// --- formatTimeAgoShort ---

func TestFormatTimeAgoShort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		when time.Time
		want string
	}{
		{"zero time returns never", time.Time{}, "never"},
		{"just now (sub-second)", time.Now(), "now"},
		{"few seconds ago", time.Now().Add(-5 * time.Second), "5s ago"},
		{"30 seconds ago", time.Now().Add(-30 * time.Second), "30s ago"},
		{"59 seconds ago", time.Now().Add(-59 * time.Second), "59s ago"},
		{"1 minute ago", time.Now().Add(-90 * time.Second), "1m ago"},
		{"5 minutes ago", time.Now().Add(-5 * time.Minute), "5m ago"},
		{"59 minutes ago", time.Now().Add(-59 * time.Minute), "59m ago"},
		{"1 hour ago", time.Now().Add(-90 * time.Minute), "1h ago"},
		{"3 hours ago", time.Now().Add(-3 * time.Hour), "3h ago"},
		{"23 hours ago", time.Now().Add(-23 * time.Hour), "23h ago"},
		{"1 day ago", time.Now().Add(-25 * time.Hour), "1d ago"},
		{"7 days ago", time.Now().Add(-7 * 24 * time.Hour), "7d ago"},
		{"30 days ago", time.Now().Add(-30 * 24 * time.Hour), "30d ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatTimeAgoShort(tt.when)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatTimeAgoShort_FutureTime(t *testing.T) {
	t.Parallel()
	// future time produces negative diff; time.Since returns negative
	// behavior: should still return "now" since diff < time.Second
	got := formatTimeAgoShort(time.Now().Add(10 * time.Second))
	// negative duration: diff < 0 < time.Second, so "now"
	assert.Equal(t, "now", got)
}
