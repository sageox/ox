package lfs

import (
	"fmt"
	"regexp"
	"strings"
)

// LeakySummaryPrefixes lists the known sentinel strings that have, at
// various points, leaked from validators / fallback stubs into the
// user-visible meta.Summary or meta.Title fields. ValidateUserVisible
// rejects any meta whose Title or Summary begins with one of these.
//
// Why this exists as a hard guard:
//
// The ox-qqka audit found 14 sessions on the SageOx Internal ledger
// where a validator failure had been written verbatim into
// meta.Summary, then surfaced through the api-go list handler and
// rendered by the web UI as the row title. The bug slipped past every
// per-layer test because no test asserted the cross-layer invariant
// "user-visible fields never carry an internal error message". This
// list is that invariant, encoded.
//
// Belt-and-suspenders: even after the producer-side fixes (ox-qqka,
// ox-wstd) ship, the writer rejects any future regression at the
// boundary. New leak shapes get appended here as we find them.
var LeakySummaryPrefixes = []string{
	"Summary failed content validation",
	"Summary failed richness validation",
	"Summary generation failed",
}

// statsStubPattern matches the historical "N user messages, N
// assistant responses" daemon-stats stub that older code paths used as
// a placeholder summary when the LLM hadn't produced one. It is also
// not an acceptable user-visible string. See pkg/sessionsummary's
// local_summary.go for the producer; that producer is fine when its
// output goes into ScoreReason or a debug field, but it must never
// land as the user-visible summary.
var statsStubPattern = regexp.MustCompile(`^[0-9]+ user messages?, [0-9]+ assistant responses?`)

// ValidateUserVisible reports an error when meta carries a known
// leaky string in a user-visible field (Title or Summary). It is the
// invariant we want enforced at every write — see WriteSessionMeta /
// WriteSessionMetaOnly which both call it.
//
// Empty Title and Summary are LEGAL (a session with no successful
// summary yet). What is illegal is a non-empty value that is actually
// a validator/diagnostic string disguised as a title.
//
// nil meta is reported as nil so callers can chain validation without
// a separate nil-check; nil-meta is rejected later in WriteSessionMeta.
func ValidateUserVisible(meta *SessionMeta) error {
	if meta == nil {
		return nil
	}
	if leak := leakySummaryReason(meta.Title); leak != "" {
		return fmt.Errorf("meta.title is a leaked validator string (%s); user-visible fields must not carry internal error messages — see ox-qqka", leak)
	}
	if leak := leakySummaryReason(meta.Summary); leak != "" {
		return fmt.Errorf("meta.summary is a leaked validator string (%s); user-visible fields must not carry internal error messages — see ox-qqka", leak)
	}
	return nil
}

// Validate runs the full structural invariant check on a SessionMeta.
// Today that's just ValidateUserVisible; if more invariants are added,
// this is where they go.
func (m *SessionMeta) Validate() error {
	return ValidateUserVisible(m)
}

// IsLeakySummaryString reports whether s matches one of the known
// validator/error string patterns that leaked into user-visible
// fields. Exported so the retro-cleanup tool (ox-l4mj) can use the
// same definition the writer rejects on. Empty strings are NOT leaky
// (an empty user-visible field is legal — see ValidateUserVisible).
func IsLeakySummaryString(s string) bool {
	return leakySummaryReason(s) != ""
}

// leakySummaryReason returns a short label describing why s is leaky,
// or empty string if it is not. Centralizes the shape so callers don't
// each duplicate the prefix list.
//
// We trim leading/trailing whitespace before matching so a disguised
// diagnostic like "  Summary failed content validation: x" or
// "\tSummary generation failed: x" cannot bypass the writer-side guard
// or the retro-cleanup tool. Without this, a future caller could
// persist a padded sentinel to meta.json and the on-disk truth would
// carry the leak even if every render-time mitigation already trims.
func leakySummaryReason(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return ""
	}
	for _, prefix := range LeakySummaryPrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return "prefix=" + prefix
		}
	}
	if statsStubPattern.MatchString(trimmed) {
		return "shape=stats-stub"
	}
	return ""
}
