package tui

import (
	"strings"
	"time"
)

// Sparkline configuration defaults.
const (
	SparklineLevels       = 8  // number of vertical levels
	SparklineMaxLevel     = 7  // max index (SparklineLevels - 1)
	SparklineBuckets      = 48 // 4 hours at 5-minute intervals
	SparklineWindow       = 4 * time.Hour
	SparklineBucketsPerHr = 12 // 60 min / 5 min intervals
)

// SparklineChars are Unicode block characters for sparkline rendering (8 levels).
var SparklineChars = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// RenderSparkline creates a sparkline visualization from event timestamps.
// Buckets events into time slots and shows activity intensity.
// Returns the raw sparkline string (caller applies styling).
func RenderSparkline(timestamps []time.Time, buckets int, window time.Duration) string {
	if len(timestamps) == 0 || buckets <= 0 {
		return "─" + strings.Repeat("─", buckets)
	}

	now := time.Now()
	bucketDuration := window / time.Duration(buckets)
	counts := make([]int, buckets)

	for _, t := range timestamps {
		age := now.Sub(t)
		if age >= window || age < 0 {
			continue
		}
		// bucket 0 is oldest, bucket n-1 is most recent
		idx := buckets - 1 - int(age/bucketDuration)
		if idx >= 0 && idx < buckets {
			counts[idx]++
		}
	}

	// find max for scaling
	maxCount := 1
	for _, c := range counts {
		if c > maxCount {
			maxCount = c
		}
	}

	var sb strings.Builder
	for i, c := range counts {
		if i > 0 && i%SparklineBucketsPerHr == 0 {
			sb.WriteRune('|')
		}

		if c == 0 {
			sb.WriteRune(SparklineChars[0])
		} else {
			level := (c * SparklineMaxLevel) / maxCount
			if level < 1 {
				level = 1
			} else if level > SparklineMaxLevel {
				level = SparklineMaxLevel
			}
			sb.WriteRune(SparklineChars[level])
		}
	}

	return sb.String()
}

// RenderSparklineTimeMarkers renders time markers aligned below a 4h sparkline.
// Returns the raw marker string (caller applies styling).
func RenderSparklineTimeMarkers() string {
	const width = SparklineBuckets + 3 // 48 buckets + 3 hour separators = 51

	line := make([]byte, width)
	for i := range line {
		line[i] = ' '
	}

	copy(line[0:], "4h ago")
	copy(line[24:], "2h")
	copy(line[48:], "now")

	return string(line)
}
