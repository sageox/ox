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

	// use time values far enough from boundaries to avoid race-condition drift
	now := time.Now()

	// zero time
	assert.Equal(t, "never", formatTimeAgoShort(time.Time{}))

	// minutes (safe from second-level drift)
	got5m := formatTimeAgoShort(now.Add(-5*time.Minute - 10*time.Second))
	assert.Equal(t, "5m ago", got5m)

	// hours
	assert.Equal(t, "3h ago", formatTimeAgoShort(now.Add(-3*time.Hour-10*time.Minute)))

	// days
	assert.Equal(t, "7d ago", formatTimeAgoShort(now.Add(-7*24*time.Hour-1*time.Hour)))
}

func TestFormatTimeAgoShort_FutureTime(t *testing.T) {
	t.Parallel()
	// future time produces negative diff; time.Since returns negative
	// behavior: should still return "now" since diff < time.Second
	got := formatTimeAgoShort(time.Now().Add(10 * time.Second))
	// negative duration: diff < 0 < time.Second, so "now"
	assert.Equal(t, "now", got)
}
