package prheader

import (
	"strconv"
	"strings"
)

// whisperLead is the honest, Material-gated claim. It is the whole whisper when
// every count is zero (Material can be true on a signal this package doesn't
// enumerate), and the lead-in otherwise. We never claim it unless Signals.Material.
const whisperLead = "Guided by SageOx"

// whisperParts returns the ordered, zero-skipped, correctly-pluralized phrase
// fragments of the enrichment whisper as PLAIN text (real spaces, no entities).
// Order — prior art → collisions → expert routes — puts the "someone already did
// this" signal first, since that is the one most likely to change a reviewer's
// read of the PR. The two rendered forms (markup and alt) both build on this so
// they can never drift.
func whisperParts(s Signals) []string {
	parts := []string{whisperLead}
	if s.PriorArt > 0 {
		parts = append(parts, strconv.Itoa(s.PriorArt)+" prior art")
	}
	if s.Collisions > 0 {
		parts = append(parts, strconv.Itoa(s.Collisions)+" collision"+plural(s.Collisions))
	}
	if s.ExpertRoutes > 0 {
		parts = append(parts, strconv.Itoa(s.ExpertRoutes)+" expert route"+plural(s.ExpertRoutes))
	}
	return parts
}

// whisperMarkup renders the Tier-A whisper for inside a <sub>: every part is
// non-breaking and parts are divided by a non-breaking middle dot, matching the
// line's category separator so the whole thing reads as one calm caption.
func whisperMarkup(s Signals) string {
	parts := whisperParts(s)
	for i, p := range parts {
		parts[i] = noWrap(escapeHTML(p))
	}
	return strings.Join(parts, "&nbsp;&middot;&nbsp;")
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
