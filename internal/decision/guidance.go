package decision

import (
	"fmt"
	"strings"
)

// buildGuidance composes the cross-agent authoring contract for the enrich
// Result. It leads with the specific evidence (the plan-enrich guidanceLead
// pattern), then the rules. Four situations: new+rich, new+zero, update+drift,
// update+bad-refs — plus the standing crediting/verification contract.
func buildGuidance(in Input, sum SignalSummary, conv Conventions, annotations []Annotation, items []ContextItem) string {
	var b strings.Builder

	isUpdate := in.Path != ""
	hasDrift := false
	for _, a := range annotations {
		if a.Type == BadgeDrift {
			hasDrift = true
		}
	}

	// Lead: the evidence.
	switch {
	case sum.UnresolvedRefs > 0:
		fmt.Fprintf(&b, "%d ref(s) in this DR do not resolve — fix or delete them before committing; an unresolvable citation is the failure this check exists to kill. ", sum.UnresolvedRefs)
	case isUpdate && hasDrift:
		b.WriteString("This DR has drifted: cited files changed after its date. If the decision still stands, add a dated amendment noting the new context; if not, amend or supersede per the corpus convention — your call. ")
	case sum.Related > 0 || sum.PriorSessions > 0:
		fmt.Fprintf(&b, "This topic has team history: %d related decision(s), %d prior session(s)/plan(s). Read the related DRs via ref_path and reconcile explicitly — state whether this aligns with, amends, or supersedes them; that judgment is yours, ox only surfaces candidates. Merge items that point at the same underlying fact into one citation rather than citing it three times; if a surfaced candidate turns out not to apply, say so and why instead of dropping it silently — a reviewer can't tell considered-and-rejected from missed. ", sum.Related, sum.PriorSessions)
	case !isUpdate && !sum.Degraded:
		b.WriteString("No related decisions, sessions, or plans matched this topic. Draft from first principles and say so — 'no prior team decision found' is itself a verifiable claim. Do not invent citations; a gap admitted beats a citation invented. ")
	}
	if sum.Degraded {
		// A source could not be read, so even surfaced matches are not exhaustive
		// and an empty result is not a checked absence. Never tell the AI coworker
		// to assert "no prior decision" as fact when retrieval was blind (#823).
		b.WriteString("Retrieval was DEGRADED — a decision source could not be read (an unreadable corpus, or a lookup that errored), so the surfaced context may be incomplete and any apparent absence is NOT a verified 'no prior decision'. Do not assert absence as fact. Investigate first: check the decision dir parses (a DR needs a number in filename/H1, or title + Status/Date), run `ox doctor`, and try `ox code search`. Only draft from first principles once you have confirmed there is genuinely nothing to reconcile. ")
	}

	// Conventions.
	if conv.NextNumber > 0 && in.Record.Number == 0 && !isUpdate {
		fmt.Fprintf(&b, "Use the suggested next number (%03d); never guess one", conv.NextNumber)
		if len(conv.NumberCollisions) > 0 {
			fmt.Fprintf(&b, " — this corpus already has duplicate numbers (%s), do not add more", strings.Join(conv.NumberCollisions, ", "))
		}
		b.WriteString(". ")
	}
	if len(conv.SectionsObserved) > 0 {
		fmt.Fprintf(&b, "Follow the house template (%s)", strings.Join(conv.SectionsObserved, " / "))
		if len(conv.StatusesObserved) > 0 {
			fmt.Fprintf(&b, "; statuses observed: %s", strings.Join(conv.StatusesObserved, ", "))
		}
		b.WriteString(". ")
	}
	if isUpdate && conv.AmendmentMarker != "" && strings.HasPrefix(strings.ToLower(in.Record.Status), "accepted") {
		fmt.Fprintf(&b, "This DR is Accepted: amend with a dated %s marker — never silently rewrite history. ", conv.AmendmentMarker)
	}

	// Crediting contract (the standing rules any agent must follow).
	if hasCites(items) {
		b.WriteString("Credit teammates by name and date in visible prose and paste the matching cite.comment VERBATIM beside the claim — never compose a SOURCE ref by hand. ")
	}
	b.WriteString("Keep SageOx credit subtle: invisible SOURCE comments plus the scored commit trailer; a visible SageOx credit only when surfaced context was genuinely non-obvious and changed the decision (max 2 per DR, 3 only if SageOx meaningfully steered it). ")
	b.WriteString("Verify before committing: re-run `ox decision enrich --file <path>` — it re-checks every ref. DRs are full-text searchable via `ox code search`.")

	return b.String()
}

func hasCites(items []ContextItem) bool {
	for _, it := range items {
		if it.Cite != nil {
			return true
		}
	}
	return false
}
