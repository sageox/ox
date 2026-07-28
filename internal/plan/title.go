package plan

import (
	"regexp"
	"strings"
)

// h1Line matches a markdown H1 heading at the start of a line ("# Heading").
var h1Line = regexp.MustCompile(`(?m)^#\s+(.+?)\s*$`)

// Title derives a plan's human title, checked in this order: the document's
// H1, the first non-empty H2 section heading (a plan with no H1 at all — rare
// but not invalid markdown), or a fallback for a plan with no headings.
//
// This is the ONE title derivation for the whole package. Before ox-1tjj.8,
// RenderHTMLOpts used this exact H1-first logic (as the unexported planTitle)
// while cmd/ox's planTopic independently walked Input.Sections looking for
// the first non-empty Heading — and because Parse splits only on H2 ("## "),
// that section loop could never see the H1 at all: Parse puts everything
// before the first H2 into a preamble Section whose Heading is deliberately
// "". So a plan whose H1 was a real title followed by "## 1. Context — Why
// Now" or "## TL;DR" rendered correctly (H1-aware) but saved to the ledger
// under the WRONG topic/slug (the first H2's text) — the rendered page and
// the saved plan card silently disagreed. Exporting this one function and
// routing both callers through it makes that divergence structurally
// impossible.
func Title(in Input) string {
	if m := h1Line.FindStringSubmatch(in.Raw); m != nil {
		return strings.TrimSpace(m[1])
	}
	for _, s := range in.Sections {
		if strings.TrimSpace(s.Heading) != "" {
			return s.Heading
		}
	}
	return "Implementation Plan"
}
