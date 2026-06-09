package plan

import (
	"fmt"
	"regexp"
)

// BrandingFinding is one SageOx-attribution lint result on a rendered plan
// HTML. All findings are advisory (warn-level): linting NEVER blocks a render
// or a save (fail-open agent UX). A non-empty slice means the render did not
// honor the html-plan skill's attribution contract.
type BrandingFinding struct {
	Rule    string // stable id, e.g. "branding.footer-credit"
	Message string // human-readable, actionable
}

var (
	// the canonical OX marker is a focusable button named for screen readers
	// (extensions/claude/commands/ox-plan.md: `<button aria-label="SageOx insight">`).
	oxMarkerRe = regexp.MustCompile(`(?i)aria-label\s*=\s*["']SageOx insight["']`)

	// footer credit, e.g. "Team context enriched by SageOx" — substring match,
	// case-insensitive, wording-tolerant.
	footerCreditRe = regexp.MustCompile(`(?i)enriched by SageOx`)

	// banned: a LIVE remote avatar image. The mark must be data:-inlined or
	// inline-SVG so the page renders from file:// with no runtime network.
	remoteAvatarRe = regexp.MustCompile(`(?i)<img[^>]+src\s*=\s*["']https?://[^"']*avatars\.githubusercontent\.com`)
)

// LintBranding verifies a rendered plan HTML carries the conditional SageOx
// attribution the html-plan skill is spec'd to produce. The contract
// (extensions/claude/commands/ox-plan.md, "SageOx attribution — subtle, earned,
// conditional"):
//
//   - EARNED: when the plan carried enrichment — any deterministic badges OR
//     context-bundle items were present — the render MUST credit it: a footer
//     line ("…enriched by SageOx") and, when there are deterministic badges, at
//     least one anchored OX marker.
//   - NO OVERCLAIM: an un-enriched plan (no badges, empty context) must NOT
//     carry SageOx credit — there is nothing to credit.
//   - SELF-CONTAINED: the OX marker's avatar must never be a live remote
//     <img src>; it is data:-inlined or an inline-SVG monogram. Always checked.
//
// Returns nil when the page satisfies the contract. Fail-open: callers warn,
// never block.
func LintBranding(html []byte, res Result) []BrandingFinding {
	if len(html) == 0 {
		return nil // nothing rendered; nothing to lint
	}
	h := string(html)

	var findings []BrandingFinding

	// "carried enrichment" mirrors the skill's own gate: any deterministic
	// badges OR context-bundle items present.
	enriched := len(res.Annotations) > 0 || len(res.Context) > 0
	hasCredit := footerCreditRe.Match(html)

	switch {
	case enriched && !hasCredit:
		findings = append(findings, BrandingFinding{
			Rule:    "branding.footer-credit",
			Message: `plan carries SageOx enrichment but the render has no footer credit (expected a calm line like "Team context enriched by SageOx")`,
		})
	case !enriched && hasCredit:
		findings = append(findings, BrandingFinding{
			Rule:    "branding.overclaim",
			Message: "render credits SageOx but the plan carried no enrichment (no badges, empty context) — drop the credit; there is nothing to credit",
		})
	}

	// OX markers anchor deterministic signals; require at least one only when
	// such badges exist. Context-only enrichment earns the footer credit but
	// not necessarily a per-element marker.
	if len(res.Annotations) > 0 && !oxMarkerRe.MatchString(h) {
		findings = append(findings, BrandingFinding{
			Rule:    "branding.ox-marker",
			Message: fmt.Sprintf(`render has %d deterministic SageOx badge(s) but no anchored OX marker (expected a focusable <button aria-label="SageOx insight">)`, len(res.Annotations)),
		})
	}

	// Always enforced: the mark must be self-contained, never a live remote img.
	if remoteAvatarRe.MatchString(h) {
		findings = append(findings, BrandingFinding{
			Rule:    "branding.remote-avatar",
			Message: `OX marker uses a live remote avatar <img src="https://avatars.githubusercontent.com…"> — inline it as a data: URI or an inline SVG so the page renders from file:// with no network`,
		})
	}

	return findings
}
