package main

import (
	"fmt"
	"strings"
	"time"
)

// distillHistoryUsageError is the typed usage error the distill history command layer
// returns from flag and time-input resolution. It carries a stable
// error code so the JSON envelope's error.code field stays wire-stable
// across Units 3–5 without each call site having to hardcode the
// string. Every fatal user-input failure in the distill history read
// surface funnels through this type.
//
// The code is always "usage_error" for inputs this file resolves;
// Units 4/5 introduce other codes via their own error types.
type distillHistoryUsageError struct {
	Code    string
	Message string
}

func (e *distillHistoryUsageError) Error() string { return e.Message }

func newJournalUsageError(msg string) *distillHistoryUsageError {
	return &distillHistoryUsageError{Code: "usage_error", Message: msg}
}

// resolveDistillHistoryWindow parses --since, --until, and --tz flag values
// into a UTC [since, until] window. Returns a *distillHistoryUsageError for
// any malformed or conflicting combination; nil on success. The `now`
// parameter is injected so tests can pin the default-until path
// deterministically.
//
// Grammar (from distill-history-read-plan.md §3 Unit 3):
//
//   - Relative --since=<dur> (Go duration literal): since = now - dur,
//     until defaults to now.
//   - Absolute --since=<ts> / --until=<ts>:
//   - Offset-bearing ("...Z" or "...±HH:MM") → used directly; pairing
//     with --tz is a usage error ("conflicting timezone").
//   - Naked (no zone) → interpreted against --tz if given, else UTC.
//   - --tz with an unknown IANA zone is a usage error.
//   - --since without --until defaults until = now.
//   - --until without --since is a usage error.
//
// The returned instants are raw (pre-rounding). The reader rounds them
// to the half-open [day-floor, day-ceil) UTC window inside ListEntries,
// so no cmd/ox-side rounding helper is needed.
func resolveDistillHistoryWindow(sinceStr, untilStr, tzStr string, now time.Time) (time.Time, time.Time, error) {
	if sinceStr == "" {
		return time.Time{}, time.Time{}, newJournalUsageError("--since is required")
	}

	var loc *time.Location
	if tzStr != "" {
		l, err := time.LoadLocation(tzStr)
		if err != nil {
			return time.Time{}, time.Time{}, newJournalUsageError(
				fmt.Sprintf("invalid timezone %q: %v", tzStr, err))
		}
		loc = l
	}

	// Relative path: --since as a Go duration literal.
	if dur, err := time.ParseDuration(sinceStr); err == nil {
		if dur < 0 {
			return time.Time{}, time.Time{}, newJournalUsageError(
				fmt.Sprintf("--since duration must be non-negative, got %v", dur))
		}
		since := now.Add(-dur).UTC()
		if untilStr == "" {
			return since, now.UTC(), nil
		}
		// Durations only apply to --since per spec; an explicit --until
		// must be an absolute timestamp.
		until, err := parseJournalAbsoluteTimestamp("--until", untilStr, loc, tzStr != "")
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		return since, until, nil
	}

	// Absolute path.
	since, err := parseJournalAbsoluteTimestamp("--since", sinceStr, loc, tzStr != "")
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if untilStr == "" {
		return since, now.UTC(), nil
	}
	until, err := parseJournalAbsoluteTimestamp("--until", untilStr, loc, tzStr != "")
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return since, until, nil
}

// parseJournalAbsoluteTimestamp parses one timestamp according to the
// Unit 3 grammar. If the literal carries an explicit zone ("Z" suffix
// or "±HH:MM" offset) the zone is used directly and pairing with --tz
// (indicated by tzProvided) is a usage error. Otherwise the literal is
// interpreted against `loc` if non-nil, else UTC.
//
// Accepted layouts, in the order tried:
//
//   - Zoned: RFC3339Nano, RFC3339, "YYYY-MM-DDTHH:MM±HH:MM"
//   - Naked: "YYYY-MM-DDTHH:MM:SS", "YYYY-MM-DDTHH:MM", "YYYY-MM-DD"
func parseJournalAbsoluteTimestamp(flagName, s string, loc *time.Location, tzProvided bool) (time.Time, error) {
	hasZone := timestampHasExplicitZone(s)
	if hasZone && tzProvided {
		return time.Time{}, newJournalUsageError(
			fmt.Sprintf("conflicting timezone specification on %s: drop --tz or drop the offset", flagName))
	}

	if hasZone {
		for _, layout := range []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02T15:04Z07:00",
		} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UTC(), nil
			}
		}
		return time.Time{}, newJournalUsageError(
			fmt.Sprintf("%s: cannot parse %q as a timestamp", flagName, s))
	}

	targetLoc := time.UTC
	if loc != nil {
		targetLoc = loc
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02",
	} {
		if t, err := time.ParseInLocation(layout, s, targetLoc); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, newJournalUsageError(
		fmt.Sprintf("%s: cannot parse %q as a timestamp", flagName, s))
}

// timestampHasExplicitZone reports whether a timestamp literal carries
// a trailing zone specifier — either "Z" or "±HH:MM".
func timestampHasExplicitZone(s string) bool {
	if strings.HasSuffix(s, "Z") {
		return true
	}
	if len(s) < 6 {
		return false
	}
	tail := s[len(s)-6:]
	if (tail[0] == '+' || tail[0] == '-') && tail[3] == ':' {
		return true
	}
	return false
}
