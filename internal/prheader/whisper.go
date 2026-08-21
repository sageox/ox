package prheader

import (
	"strconv"
	"strings"
)

// whisperParts returns the ordered, zero-skipped, correctly-pluralized enrichment
// fragments as PLAIN text (real spaces, no entities), in reviewer-facing language.
// Order — prior art → collisions — puts the "someone already did this" signal
// first, since that is the one most likely to change a reviewer's read of the PR.
// The brand attribution lives in the "guided by" kicker on the wordmark, so it is
// deliberately NOT repeated here. The two rendered forms (markup and alt) both
// build on this so they can never drift.
func whisperParts(s Signals) []string {
	var parts []string
	if s.PriorArt > 0 {
		parts = append(parts, strconv.Itoa(s.PriorArt)+" related session"+plural(s.PriorArt))
	}
	if s.Collisions > 0 {
		parts = append(parts, strconv.Itoa(s.Collisions)+" concurrent edit"+plural(s.Collisions)+" flagged")
	}
	return parts
}

// whisperMarkup renders the stats for inside a <sub>: each fragment is
// non-breaking (atomic, never splits mid-phrase), but fragments are joined by a
// breakable space + middle dot so the caption can wrap between stats on a narrow
// (mobile) viewport instead of overflowing the container.
func whisperMarkup(s Signals) string {
	parts := whisperParts(s)
	for i, p := range parts {
		parts[i] = noWrap(escapeHTML(p))
	}
	return strings.Join(parts, " &middot; ")
}

// whisperAlt renders the same content as a plain-text sentence for the Tier-B
// floated image's alt attribute — so a screen reader or a broken-image fallback
// still reads the full credit. It is intentionally NOT non-breaking (alt text is
// read, not laid out).
func whisperAlt(s Signals) string {
	return strings.Join(whisperParts(s), " · ")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
