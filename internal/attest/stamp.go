package attest

import (
	"regexp"
	"strings"
	"time"
)

// Stamp is the provenance comment that sits directly above a `@validated`
// scenario, recording WHEN it last ran green and WHICH run said so:
//
//	@critical @validated
//	# validated: 2026-08-12 · Tilt · run_msqd0fj4-2223
//	Scenario Outline: A member can view their team's repository
//
// The tag alone is a claim; this comment is the only pointer to the evidence
// behind it. Today that pointer dangles — the run it names lives in a gitignored
// directory on one laptop — which is the entire reason a durable attestation
// record exists.
type Stamp struct {
	// Date is the stamp date as written, "" when the comment omitted one.
	Date string `json:"date,omitempty"`
	// Env is the free-text environment ("Tilt"), "" when absent.
	Env string `json:"env,omitempty"`
	// RunID is the run this stamp points at, "" when the comment omitted one.
	RunID string `json:"run_id,omitempty"`
	// Raw is the comment text after "validated:", verbatim. Kept so a stamp we
	// only partly understood still shows a human exactly what was written,
	// rather than silently rendering as empty.
	Raw string `json:"raw"`
}

// Age returns how long ago the stamp was written, and whether it could be told.
// A stamp with no parseable date is not old — it is unknown, and the two must
// not collapse into the same answer.
func (s *Stamp) Age(now time.Time) (time.Duration, bool) {
	if s == nil || s.Date == "" {
		return 0, false
	}
	t, err := time.Parse("2006-01-02", s.Date)
	if err != nil {
		return 0, false
	}
	return now.Sub(t), true
}

// Decay thresholds, mirroring the web app's coverage view so the CLI and the
// browser cannot tell a reader two different stories about the same stamp.
//
// A stamp inside FreshDays is taken at face value — roughly one merge cadence's
// worth of drift. Past StaleDays it is an anecdote rather than a gate: the
// harness sits in no CI job, so nothing has re-proven it since a human ran it
// by hand.
const (
	FreshDays = 14
	StaleDays = 42
)

var (
	// The stamp comment. Deliberately lenient about the separator and the field
	// order: the two things that carry meaning are the date and the run id, and
	// over-fitting to the current "·" would turn a cosmetic edit into a silently
	// unreadable stamp.
	reStampComment = regexp.MustCompile(`^\s*#\s*validated:\s*(.+?)\s*$`)
	reStampDate    = regexp.MustCompile(`\b(\d{4}-\d{2}-\d{2})\b`)
	reStampRunID   = regexp.MustCompile(`\b(run_[A-Za-z0-9][A-Za-z0-9_-]*)\b`)
)

// parseStampComment recognizes a `# validated: ...` provenance line.
//
// Returns ok=true for any line with that prefix, EVEN when neither a date nor a
// run id could be extracted. That is intentional: a stamp we cannot fully read
// is a finding, not a non-event, and returning ok=false would let it fall
// through to the generic comment branch and disappear.
func parseStampComment(line string) (*Stamp, bool) {
	m := reStampComment.FindStringSubmatch(line)
	if m == nil {
		return nil, false
	}
	raw := m[1]
	stamp := &Stamp{Raw: raw}
	if d := reStampDate.FindStringSubmatch(raw); d != nil {
		stamp.Date = d[1]
	}
	if r := reStampRunID.FindStringSubmatch(raw); r != nil {
		stamp.RunID = r[1]
	}
	stamp.Env = extractEnv(raw, stamp.Date, stamp.RunID)
	return stamp, true
}

// extractEnv salvages the human-readable environment from the middle of the
// comment by removing the two fields we recognize and the separators around
// them. Best-effort by design — Env is display sugar, never a decision input.
func extractEnv(raw, date, runID string) string {
	rest := raw
	if date != "" {
		rest = strings.Replace(rest, date, "", 1)
	}
	if runID != "" {
		rest = strings.Replace(rest, runID, "", 1)
	}
	rest = strings.Trim(rest, " ·|,-\t")
	rest = strings.TrimSpace(rest)
	// Collapse a leftover separator pair ("· ·") that removing the middle field
	// can leave behind.
	rest = strings.Trim(strings.ReplaceAll(rest, "· ·", ""), " ·")
	return strings.TrimSpace(rest)
}
