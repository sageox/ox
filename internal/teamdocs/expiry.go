package teamdocs

import "time"

// ExpiryLayout is the only accepted valid-through form. A date needs no timezone
// to answer "has this gone stale", and accepting several formats would mean two
// authors writing the same shelf life two ways.
const ExpiryLayout = "2006-01-02"

// ExpiredOn reports whether validThrough has passed as of now, and the number of
// days overdue.
//
// Empty means evergreen — most conventions never rot, and requiring a date on
// every one would turn a signal into noise. An UNPARSEABLE value is deliberately
// not treated as expired: a typo must not retire a team's knowledge, and the
// caller surfaces it as a malformed field instead. ok is false in both cases.
//
// The comparison is date-only and inclusive of the final day: an entry valid
// through the 21st is still valid all of the 21st, which is what an author means
// when they write a date rather than a timestamp.
func ExpiredOn(validThrough string, now time.Time) (days int, ok bool) {
	if validThrough == "" {
		return 0, false
	}
	end, err := time.Parse(ExpiryLayout, validThrough)
	if err != nil {
		return 0, false
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if !today.After(end) {
		return 0, false
	}
	return int(today.Sub(end).Hours() / 24), true
}

// MalformedExpiry reports a valid-through that is present but unparseable, so a
// caller can say "this date is wrong" rather than silently treating the entry as
// evergreen — which is how a typo turns a shelf life into a permanent exemption.
func MalformedExpiry(validThrough string) bool {
	if validThrough == "" {
		return false
	}
	_, err := time.Parse(ExpiryLayout, validThrough)
	return err != nil
}
