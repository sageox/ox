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

// validateDraftShape enforces the draft structural invariant at the writer
// boundary: a draft placeholder names NO artifacts in its manifest and carries
// NO summary text. If any future code path tries to persist a draft that
// violates that, the write is refused rather than producing a meta.json that
// lies to every downstream consumer about what is in the directory.
//
// Both halves are load-bearing:
//
//   - A draft with a populated Files manifest claims LFS OIDs that were never
//     uploaded. Committing that reference makes the ledger's pre-receive hook
//     reject every subsequent push with "LFS objects are missing" — for the
//     whole team, not just the author.
//   - A draft with summary text is a summary of a zero-turn session. The
//     ABSENCE of summary artifacts is the load-bearing "summary still owed"
//     signal that IsStubSummary and the daemon's anti-entropy both depend on;
//     a draft that fills it in makes every consumer believe the session is
//     already summarized.
//
// Same "encode the cross-layer invariant at the writer" pattern
// ValidateUserVisible established for leaked validator strings (ox-qqka),
// where every per-layer test passed while the invariant was violated because
// no test asserted the invariant itself.
func validateDraftShape(meta *SessionMeta) error {
	if !meta.IsDraft() {
		return nil
	}
	if len(meta.Files) > 0 {
		return fmt.Errorf("draft meta.json must not name artifacts in files (found %d); a draft directory contains only meta.json — see .claude/rules/cache-only-design.md", len(meta.Files))
	}
	if strings.TrimSpace(meta.Summary) != "" {
		return fmt.Errorf("draft meta.json must not carry summary text; drafts are counters-only and are purged wholesale at finalize")
	}
	if strings.TrimSpace(meta.SummaryStatus) != "" {
		return fmt.Errorf("draft meta.json must not carry summary_status %q; the absence of a summary is the signal that one is still owed", meta.SummaryStatus)
	}
	return nil
}

// Validate runs the full structural invariant check on a SessionMeta.
// Every writer goes through WriteSessionMetaOnly, which calls this — including
// MutateSessionMeta — so adding an invariant here covers every write path.
func (m *SessionMeta) Validate() error {
	if err := ValidateUserVisible(m); err != nil {
		return err
	}
	return validateDraftShape(m)
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
