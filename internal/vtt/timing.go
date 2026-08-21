package vtt

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Timestamp parsing and cue/time slicing (additive extension).
//
// Timing model:
//   - Each cue carries a 1-based Index and a [Start, End) half-open interval
//     on the media clock.
//   - A cue whose timestamp line is malformed (or whose End <= Start) has an
//     EMPTY interval: it never overlaps any time window and never contains an
//     instant, but it keeps its Index and remains addressable by cue range.
//     Malformed timestamps leave Start and End at zero.
//   - Out-of-order timestamps are preserved as parsed; slicing scans all cues
//     rather than assuming monotonic order, so out-of-order files still slice
//     correctly.

// HasTiming reports whether the cue has a non-empty media-clock interval,
// i.e. its timestamps parsed and Start < End. Cues without timing are
// excluded from time-window and instant lookups but stay cue-addressable.
func (c Cue) HasTiming() bool {
	return c.End > c.Start
}

// parseTimestampPair parses a WebVTT timestamp line ("start --> end [settings]").
// Returns start and end durations; err is non-nil when either side is malformed.
func parseTimestampPair(line string) (start, end time.Duration, err error) {
	parts := strings.SplitN(line, "-->", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("timestamp line missing arrow: %q", line)
	}
	start, err = ParseTimestamp(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("start timestamp: %w", err)
	}
	// Cue settings (e.g. "align:start") may follow the end timestamp.
	endToken := strings.TrimSpace(parts[1])
	if i := strings.IndexAny(endToken, " \t"); i >= 0 {
		endToken = endToken[:i]
	}
	end, err = ParseTimestamp(endToken)
	if err != nil {
		return 0, 0, fmt.Errorf("end timestamp: %w", err)
	}
	return start, end, nil
}

// ParseTimestamp parses a WebVTT timestamp: hh:mm:ss.mmm with the hours
// component optional (mm:ss.mmm). Hours may exceed two digits; minutes and
// seconds are exactly two digits and < 60; the fraction is exactly three
// digits (milliseconds).
func ParseTimestamp(s string) (time.Duration, error) {
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		return 0, fmt.Errorf("timestamp %q: missing millisecond fraction", s)
	}
	frac := s[dot+1:]
	if len(frac) != 3 || !allDigits(frac) {
		return 0, fmt.Errorf("timestamp %q: fraction must be exactly three digits", s)
	}
	fields := strings.Split(s[:dot], ":")
	var hoursStr, minsStr, secsStr string
	switch len(fields) {
	case 2:
		minsStr, secsStr = fields[0], fields[1]
	case 3:
		hoursStr, minsStr, secsStr = fields[0], fields[1], fields[2]
	default:
		return 0, fmt.Errorf("timestamp %q: want [hh:]mm:ss.mmm", s)
	}

	var hours int64
	if hoursStr != "" {
		if len(hoursStr) < 1 || !allDigits(hoursStr) {
			return 0, fmt.Errorf("timestamp %q: malformed hours", s)
		}
		var err error
		hours, err = strconv.ParseInt(hoursStr, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("timestamp %q: hours out of range", s)
		}
	}
	if len(minsStr) != 2 || !allDigits(minsStr) {
		return 0, fmt.Errorf("timestamp %q: minutes must be exactly two digits", s)
	}
	if len(secsStr) != 2 || !allDigits(secsStr) {
		return 0, fmt.Errorf("timestamp %q: seconds must be exactly two digits", s)
	}
	mins, _ := strconv.ParseInt(minsStr, 10, 64)
	secs, _ := strconv.ParseInt(secsStr, 10, 64)
	if mins > 59 {
		return 0, fmt.Errorf("timestamp %q: minutes must be < 60", s)
	}
	if secs > 59 {
		return 0, fmt.Errorf("timestamp %q: seconds must be < 60", s)
	}
	millis, _ := strconv.ParseInt(frac, 10, 64)

	total := ((hours*60+mins)*60+secs)*1000 + millis
	return time.Duration(total) * time.Millisecond, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// SliceByCueRange returns the cues whose 1-based Index lies in the inclusive
// range [first, last], clamped to the available cues. clamped reports whether
// any clamping occurred (including a range entirely out of bounds, which
// yields an empty result). A reversed range (first > last) yields an empty
// result with clamped false — callers validate ranges before slicing.
func SliceByCueRange(cues []Cue, first, last int) (out []Cue, clamped bool) {
	if first > last {
		return nil, false
	}
	if first < 1 {
		first = 1
		clamped = true
	}
	if last > len(cues) {
		last = len(cues)
		clamped = true
	}
	if first > last {
		// Range fell entirely outside the available cues after clamping.
		return nil, true
	}
	out = make([]Cue, last-first+1)
	copy(out, cues[first-1:last])
	return out, clamped
}

// SliceByTimeWindow returns the cues whose half-open interval [Start, End)
// has a non-empty intersection with the CLOSED window [from, to]. A cue whose
// interval merely touches the window boundary at its Start (Start == to)
// overlaps, because the closed window includes that instant; a cue ending
// exactly at from does not (End is exclusive). Cues without timing (empty
// intervals) never match. A reversed window (from > to) yields nil.
func SliceByTimeWindow(cues []Cue, from, to time.Duration) []Cue {
	if from > to {
		return nil
	}
	var out []Cue
	for _, c := range cues {
		if !c.HasTiming() {
			continue
		}
		if c.Start <= to && c.End > from {
			out = append(out, c)
		}
	}
	return out
}

// CueAtInstant resolves a bare instant t to a cue: the cue containing t
// (Start <= t < End) if one exists, otherwise the nearest following cue (the
// smallest Start >= t). Returns ok=false when no cue contains or follows t.
// Cues without timing are ignored. When multiple cues contain t (overlapping
// or out-of-order files), the earliest by Index wins.
func CueAtInstant(cues []Cue, t time.Duration) (Cue, bool) {
	var following Cue
	haveFollowing := false
	for _, c := range cues {
		if !c.HasTiming() {
			continue
		}
		if c.Start <= t && t < c.End {
			return c, true
		}
		if c.Start >= t && (!haveFollowing || c.Start < following.Start) {
			following = c
			haveFollowing = true
		}
	}
	return following, haveFollowing
}
