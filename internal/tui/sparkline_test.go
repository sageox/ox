package tui

import (
	"strings"
	"testing"
	"time"
)

func TestRenderSparkline_EmptyTimestamps(t *testing.T) {
	result := RenderSparkline(nil, 10, time.Hour)
	if !strings.HasPrefix(result, "─") {
		t.Errorf("empty timestamps should produce dashes, got %q", result)
	}
	runes := []rune(result)
	if len(runes) != 11 { // "─" + 10 "─"
		t.Errorf("expected 11 runes, got %d", len(runes))
	}
}

func TestRenderSparkline_ZeroBuckets(t *testing.T) {
	now := time.Now()
	result := RenderSparkline([]time.Time{now}, 0, time.Hour)
	if result != "─" {
		t.Errorf("zero buckets should produce single dash, got %q", result)
	}
}

func TestRenderSparkline_SingleRecentEvent(t *testing.T) {
	now := time.Now()
	result := RenderSparkline([]time.Time{now.Add(-time.Second)}, 4, time.Hour)
	runes := []rune(result)
	// last bucket should have activity (non-lowest char)
	lastChar := runes[len(runes)-1]
	if lastChar == SparklineChars[0] {
		t.Errorf("most recent bucket should show activity for a recent event")
	}
}

func TestRenderSparkline_EventOutsideWindow(t *testing.T) {
	now := time.Now()
	// event is 5 hours ago, window is 4 hours
	result := RenderSparkline([]time.Time{now.Add(-5 * time.Hour)}, 4, 4*time.Hour)
	for _, r := range result {
		if r != SparklineChars[0] && r != '|' {
			t.Errorf("event outside window should produce all-low sparkline, got char %c", r)
		}
	}
}

func TestRenderSparkline_FutureEvent(t *testing.T) {
	now := time.Now()
	result := RenderSparkline([]time.Time{now.Add(time.Hour)}, 4, time.Hour)
	for _, r := range result {
		if r != SparklineChars[0] && r != '|' {
			t.Errorf("future event (negative age) should be excluded, got char %c", r)
		}
	}
}

func TestRenderSparkline_HourSeparators(t *testing.T) {
	// with 48 buckets and 4-hour window, separators at every 12 buckets
	// empty sparkline has no separators (all dashes); verify with timestamps below
	_ = RenderSparkline(nil, SparklineBuckets, SparklineWindow)
	now := time.Now()
	ts := make([]time.Time, 0, 48)
	for i := range 48 {
		ts = append(ts, now.Add(-time.Duration(i)*5*time.Minute))
	}
	result := RenderSparkline(ts, SparklineBuckets, SparklineWindow)
	separatorCount := strings.Count(result, "|")
	if separatorCount != 3 {
		t.Errorf("expected 3 hour separators in 4-hour sparkline, got %d", separatorCount)
	}
}

func TestRenderSparkline_ScalesCorrectly(t *testing.T) {
	now := time.Now()
	// cluster many events in one bucket
	ts := make([]time.Time, 50)
	for i := range ts {
		ts[i] = now.Add(-time.Second * time.Duration(i))
	}
	result := RenderSparkline(ts, 4, time.Hour)
	runes := []rune(result)
	lastChar := runes[len(runes)-1]
	// the bucket with 50 events should be at max level
	if lastChar != SparklineChars[SparklineMaxLevel] {
		t.Errorf("bucket with all events should be at max level, got %c", lastChar)
	}
}

func TestRenderSparklineTimeMarkers(t *testing.T) {
	result := RenderSparklineTimeMarkers()
	if !strings.Contains(result, "4h ago") {
		t.Error("should contain '4h ago'")
	}
	if !strings.Contains(result, "2h") {
		t.Error("should contain '2h'")
	}
	if !strings.Contains(result, "now") {
		t.Error("should contain 'now'")
	}
}
