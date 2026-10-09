package agentwork

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	failureReasonQuotaExhausted = "quota_exhausted"
	failureReasonUnknown        = "unknown"

	// quotaPauseFallback applies when the reset time is missing or unparsable.
	quotaPauseFallback = time.Hour
	// quotaPauseMax bounds a parsed reset so a misparse cannot disable
	// summarization indefinitely.
	quotaPauseMax = 7 * 24 * time.Hour
)

// quotaMarkers are lowercase substrings the claude CLI prints when the account
// is out of quota. A quota exit is global: every session fails the same way.
var quotaMarkers = []string{
	"hit your weekly limit",
	"hit your limit",
	"usage limit",
	"rate limit",
}

// quotaResetPattern matches "resets Oct 12 at 7am (America/Los_Angeles)" and
// "resets 5am (America/Los_Angeles)"; minutes are optional ("7:30pm").
var quotaResetPattern = regexp.MustCompile(
	`(?i)resets\s+(?:([a-z]{3})[a-z]*\s+(\d{1,2})\s+at\s+)?(\d{1,2})(?::(\d{2}))?\s*([ap]m)\s*\(([^)]+)\)`)

// classifyAgentFailure names why a non-zero agent exit happened so the log and
// the pause logic agree on one reason.
func classifyAgentFailure(output string) string {
	lower := strings.ToLower(output)
	for _, marker := range quotaMarkers {
		if strings.Contains(lower, marker) {
			return failureReasonQuotaExhausted
		}
	}
	return failureReasonUnknown
}

// parseQuotaReset resolves the reset time in the message to the next future
// occurrence in the zone the message names.
func parseQuotaReset(output string, now time.Time) (time.Time, bool) {
	m := quotaResetPattern.FindStringSubmatch(output)
	if m == nil {
		return time.Time{}, false
	}
	loc, err := time.LoadLocation(m[6])
	if err != nil {
		return time.Time{}, false
	}
	hour, _ := strconv.Atoi(m[3])
	minute := 0
	if m[4] != "" {
		minute, _ = strconv.Atoi(m[4])
	}
	if hour < 1 || hour > 12 || minute > 59 {
		return time.Time{}, false
	}
	hour %= 12
	if strings.EqualFold(m[5], "pm") {
		hour += 12
	}

	local := now.In(loc)
	if m[1] == "" {
		reset := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
		if !reset.After(now) {
			reset = reset.AddDate(0, 0, 1)
		}
		return reset, true
	}

	month, ok := parseMonthAbbrev(m[1])
	day, _ := strconv.Atoi(m[2])
	if !ok {
		return time.Time{}, false
	}
	reset := time.Date(local.Year(), month, day, hour, minute, 0, 0, loc)
	// time.Date normalizes overflow (Feb 31 -> Mar 3); reject instead of guessing
	if reset.Month() != month || reset.Day() != day {
		return time.Time{}, false
	}
	if !reset.After(now) {
		reset = time.Date(local.Year()+1, month, day, hour, minute, 0, 0, loc)
	}
	return reset, true
}

func parseMonthAbbrev(abbrev string) (time.Month, bool) {
	for month := time.January; month <= time.December; month++ {
		if strings.EqualFold(month.String()[:3], abbrev) {
			return month, true
		}
	}
	return 0, false
}

// quotaPauseUntil returns when summarization should resume: the parsed reset
// time, or an hour out when the message gives none, never more than a week.
func quotaPauseUntil(output string, now time.Time) time.Time {
	until, ok := parseQuotaReset(output, now)
	if !ok || !until.After(now) {
		until = now.Add(quotaPauseFallback)
	}
	if limit := now.Add(quotaPauseMax); until.After(limit) {
		until = limit
	}
	return until
}

// quotaPause is the handler-wide pause shared by every session.
type quotaPause struct {
	mu    sync.Mutex
	until time.Time
}

// start records the pause and reports whether it began a new pause (false when
// one was already active), so the caller announces it once.
func (p *quotaPause) start(until, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	active := now.Before(p.until)
	if until.After(p.until) {
		p.until = until
	}
	return !active
}

// check reports the active pause end. ended is true exactly once, on the first
// check after a pause expired, so the caller logs the resume once.
func (p *quotaPause) check(now time.Time) (until time.Time, paused, ended bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.until.IsZero() {
		return time.Time{}, false, false
	}
	if now.Before(p.until) {
		return p.until, true, false
	}
	p.until = time.Time{}
	return time.Time{}, false, true
}
