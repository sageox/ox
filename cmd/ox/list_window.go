package main

import (
	"strings"
	"time"

	"github.com/sageox/ox/internal/errkind"
	"github.com/sageox/ox/internal/glance"
)

// annotationTimeOrdered marks a list command whose rows are ordered by time.
// flag_vocabulary_test.go requires every command carrying it to expose
// --limit, --since, --until, and --json: AI coworkers generalize a flag they
// learned on one sibling to the next, so the siblings must agree.
const annotationTimeOrdered = "ox:time-ordered"

// parseListSince parses a --since value with the grammar shared by every list
// command and glance: a duration (30m, 2h, 7d, 1w) counted back from now, an
// RFC3339 instant, or a YYYY-MM-DD date. Empty means no bound (zero time).
func parseListSince(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := glance.ParseTimeFlag(raw)
	if err != nil {
		return time.Time{}, errkind.Errorf(errkind.Usage, "invalid --since value %q: use a duration (30m, 2h, 7d, 1w), RFC3339, or a date like 2026-10-01", raw)
	}
	return t, nil
}

// parseListUntil parses --until with the same grammar as parseListSince. A
// bare YYYY-MM-DD date means "through the end of that day" — the way a person
// says it — so it becomes the next local midnight, an exclusive bound.
func parseListUntil(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := glance.ParseTimeFlag(raw)
	if err != nil {
		return time.Time{}, errkind.Errorf(errkind.Usage, "invalid --until value %q: use a duration (30m, 2h, 7d, 1w), RFC3339, or a date like 2026-10-01", raw)
	}
	if _, dateErr := time.ParseInLocation("2006-01-02", strings.TrimSpace(raw), time.Local); dateErr == nil {
		t = t.AddDate(0, 0, 1)
	}
	return t, nil
}

// resolveTimeWindow parses a --since/--until pair. A zero time means that end
// is open. An inverted window is a usage error: it can only ever be empty, and
// an empty result is indistinguishable from "nothing happened".
func resolveTimeWindow(sinceRaw, untilRaw string) (since, until time.Time, err error) {
	since, err = parseListSince(sinceRaw)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	until, err = parseListUntil(untilRaw)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if err := checkTimeWindow(since, until); err != nil {
		return time.Time{}, time.Time{}, err
	}
	return since, until, nil
}

// checkTimeWindow rejects a window whose start is after its end.
func checkTimeWindow(since, until time.Time) error {
	if !since.IsZero() && !until.IsZero() && since.After(until) {
		return errkind.Errorf(errkind.Usage, "--since is later than --until: the window is empty; --since is the older bound, e.g. --since 7d --until 1d")
	}
	return nil
}

// inTimeWindow reports whether t lies in [since, until). A zero bound is open.
// A zero t cannot satisfy a set bound: an unknown instant is not evidence the
// row falls inside the window.
func inTimeWindow(t, since, until time.Time) bool {
	if since.IsZero() && until.IsZero() {
		return true
	}
	if t.IsZero() {
		return false
	}
	if !since.IsZero() && t.Before(since) {
		return false
	}
	if !until.IsZero() && !t.Before(until) {
		return false
	}
	return true
}
