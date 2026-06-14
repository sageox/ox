package plan

import (
	"fmt"
	"regexp"
)

// Finding is one advisory lint result on a rendered plan HTML — attribution
// (branding.*) or diagram (mermaid.*). All findings are warn-level: linting
// NEVER blocks a render or a save (fail-open agent UX). A non-empty slice means
// the render did not honor the html-plan contract or carries a diagram that
// will not render.
type Finding struct {
	Rule    string // stable id, e.g. "branding.footer-credit" / "mermaid.arrow-in-label"
	Message string // human-readable, actionable
}

// BrandingFinding is retained as an alias so existing callers/tests keep
// compiling; new code should use Finding.
type BrandingFinding = Finding

// countDeterministic counts ox-computed (deterministic) annotations — the ones
// that surface as anchored OX markers. Judgment badges are agent-authored and
// not rendered as markers, so they don't trigger the marker requirement.
func countDeterministic(res Result) int {
	n := 0
	for _, a := range res.Annotations {
		if a.Kind == BadgeDeterministic {
			n++
		}
	}
	return n
}

// LintRender runs the full advisory contract over a rendered plan HTML: SageOx
// attribution (LintBranding) plus diagram validity (LintMermaid). It is the
// single entrypoint `ox plan lint` / `ox plan save` call. Fail-open: an empty
// page returns nil.
func LintRender(htmlBytes []byte, res Result) []Finding {
	out := LintBranding(htmlBytes, res)
	out = append(out, LintMermaid(htmlBytes)...)
	return out
}

var (
	// the canonical OX marker is a focusable button named for screen readers
	// (extensions/claude/skills/ox-plan/SKILL.md: `<button aria-label="SageOx insight">`).
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
// (extensions/claude/skills/ox-plan/SKILL.md, "SageOx attribution — subtle, earned,
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

	// OX markers anchor DETERMINISTIC signals; require one only when such badges
	// exist. A judgment-only plan (e.g. a rigor badge) or context-only enrichment
	// earns the footer credit but renders no per-element marker, so counting all
	// annotations here would false-positive on those plans.
	if det := countDeterministic(res); det > 0 && !oxMarkerRe.MatchString(h) {
		findings = append(findings, BrandingFinding{
			Rule:    "branding.ox-marker",
			Message: fmt.Sprintf(`render has %d deterministic SageOx badge(s) but no anchored OX marker (expected a focusable <button aria-label="SageOx insight">)`, det),
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
