package plan

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"github.com/sageox/ox/internal/viz"
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

// isEnriched reports whether a plan carried SageOx enrichment: any annotation
// (deterministic OR judgment) or any context-bundle item. This is THE gate for
// every "did SageOx earn credit here" rule — LintBranding's footer credit and
// lintWordmark both use it, so the two can never drift apart on a judgment-only
// plan (which the renderer does give a wordmark: render.go sets FooterCredit on
// this same condition). countDeterministic is deliberately narrower and belongs
// only to the anchored-OX-marker rule.
func isEnriched(res Result) bool {
	return len(res.Annotations) > 0 || len(res.Context) > 0
}

// LintRender runs the full advisory contract over a rendered plan HTML: SageOx
// attribution (LintBranding, lintWordmark) plus diagram validity (LintMermaid,
// lintMermaidFontRace). It is the single entrypoint `ox plan lint` /
// `ox plan save` call. Fail-open: an empty page returns nil.
func LintRender(htmlBytes []byte, res Result) []Finding {
	out := LintBranding(htmlBytes, res)
	out = append(out, LintMermaid(htmlBytes)...)
	out = append(out, lintMermaidFontRace(htmlBytes)...)
	out = append(out, lintWordmark(htmlBytes, res)...)
	for _, f := range viz.Lint(htmlBytes, viz.LintOptions{TaggedOnly: true}) {
		out = append(out, Finding{Rule: f.Rule, Message: f.Message})
	}
	return out
}

// lintWordmark flags an enriched render missing the SageOx wordmark. The Go
// renderer injects it bottom-left as part of the ox chrome; this advisory
// exists for renders that bypass that path, where the mark is part of the spec
// but the author is fallible. Advisory, and only when the plan actually carried
// enrichment — the same conditionality as the footer credit.
func lintWordmark(html []byte, res Result) []Finding {
	if len(html) == 0 {
		return nil // nothing rendered; nothing to lint
	}
	if !isEnriched(res) {
		return nil
	}
	// Match the emitted marker, not prose. `data-ox-wordmark` is stamped on the
	// lockup by both the Go template and the `ox viz wordmark` snippet;
	// matching the SVG <title> text instead would let a page that merely
	// mentions "SageOx Wordmark" satisfy the rule, and would fail a correctly
	// placed mark whose SVG was minified or title-stripped.
	if bytes.Contains(html, []byte("data-ox-wordmark")) {
		return nil
	}
	return []Finding{{
		Rule:    "branding.wordmark-missing",
		Message: "enriched plan render carries no SageOx wordmark — add the bottom-left mark via `ox viz wordmark` (both theme variants, inline SVG)",
	}}
}

// mermaidScriptRe extracts <script> bodies so the font-race check reasons about
// executable code rather than visible prose. A plan that merely quotes
// "document.fonts.ready" in a code sample must neither trigger nor suppress it.
var mermaidScriptRe = regexp.MustCompile(`(?is)<script\b[^>]*>(.*?)</script>`)

// mermaidDiagramRe is the diagram-presence signal: an actual Mermaid container,
// not the word "mermaid" appearing anywhere in CSS, a URL, or body text.
var mermaidDiagramRe = regexp.MustCompile(`class="[^"]*\bmermaid\b`)

var (
	// jsBlockCommentRe strips /* ... */ comments from script bodies.
	jsBlockCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)
	// jsLineCommentRe strips // ... comments. The negative lookbehind for ':' is
	// spelled as a captured prefix because Go's regexp has no lookbehind — it
	// keeps "https://…" inside a string literal from being treated as a comment.
	jsLineCommentRe = regexp.MustCompile(`(^|[^:])//[^\n]*`)
)

// stripJSComments removes comments from script source so a rule can distinguish
// code that DOES something from a comment that merely talks about it. Without
// this, a scaffold comment reading "re-render on document.fonts.ready" silences
// the very rule that comment describes.
func stripJSComments(js string) string {
	js = jsBlockCommentRe.ReplaceAllString(js, " ")
	return jsLineCommentRe.ReplaceAllString(js, "$1")
}

// lintMermaidFontRace flags the webfont measurement race: a page that renders
// Mermaid and loads a webfont but never re-renders on document.fonts.ready will
// clip node labels mid-word once the wider font swaps in. Advisory — the fix is
// one line and the symptom is invisible until someone screenshots the diagram.
func lintMermaidFontRace(html []byte) []Finding {
	if len(html) == 0 {
		return nil
	}
	h := string(html)
	// Needs all three: a real diagram, a webfont, and Mermaid actually invoked.
	if !mermaidDiagramRe.MatchString(h) || !strings.Contains(h, "fonts.googleapis.com") {
		return nil
	}
	var scripts strings.Builder
	for _, m := range mermaidScriptRe.FindAllStringSubmatch(h, -1) {
		scripts.WriteString(m[1])
		scripts.WriteString("\n")
	}
	js := stripJSComments(scripts.String())
	if !strings.Contains(js, "mermaid") {
		return nil // fonts + a .mermaid block, but nothing renders it
	}
	// Require the font-ready path to actually be invoked, not just named: a
	// comment or a prose snippet describing the fix must not silence the rule.
	if strings.Contains(js, "fonts.ready") {
		return nil
	}
	return []Finding{{
		Rule:    "mermaid.font-race",
		Message: "page renders Mermaid and loads a webfont but never re-renders on document.fonts.ready — node labels will clip mid-word when the font swaps in; add document.fonts.ready.then(()=>renderMermaid())",
	}}
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

	// artifact-mode CSP checks: a page published as a Claude Code Artifact is
	// served under a strict CSP that blocks every cross-origin resource load and
	// all fetch/XHR/WebSocket. These match the resource-loading constructs only —
	// NOT <a href="https://…"> navigation, which is allowed and is how the SageOx
	// enrichment references stay clickable in a published artifact. The host part
	// is `(?:https?:)?//` so protocol-relative refs (`src="//cdn…"`) are caught
	// too, and the EventSource check is scoped to the `new EventSource(`
	// instantiation so it can't false-positive on plan prose that merely mentions
	// the word.
	extScriptRe   = regexp.MustCompile(`(?i)<script[^>]+src\s*=\s*["'](?:https?:)?//`)
	extLinkRe     = regexp.MustCompile(`(?i)<link[^>]+href\s*=\s*["'](?:https?:)?//`)
	extImgRe      = regexp.MustCompile(`(?i)<img[^>]+src\s*=\s*["'](?:https?:)?//`)
	cssURLRe      = regexp.MustCompile(`(?i)url\(\s*["']?(?:https?:)?//`)
	eventSourceRe = regexp.MustCompile(`(?i)\bnew\s+EventSource\s*\(`)
)

// LintArtifact verifies a rendered plan HTML is safe to publish as a Claude Code
// Artifact: strictly self-contained, with no construct that the artifact CSP
// would block at view time. It checks resource loads (external script/style/
// font/img, CSS url() to a remote host) and the SSE review layer (EventSource).
// It deliberately does NOT flag <a href="https://…"> — outbound navigation is
// allowed, and the SageOx enrichment links rely on it. Fail-open: an empty page
// returns nil; callers warn, never block.
func LintArtifact(htmlBytes []byte) []Finding {
	if len(htmlBytes) == 0 {
		return nil
	}
	h := string(htmlBytes)
	var findings []Finding
	check := func(re *regexp.Regexp, rule, msg string) {
		if re.MatchString(h) {
			findings = append(findings, Finding{Rule: rule, Message: msg})
		}
	}
	check(extScriptRe, "artifact.external-script",
		`artifact render loads a <script src="https://…"> — the artifact CSP blocks it; vendor and inline the script instead`)
	check(extLinkRe, "artifact.external-stylesheet",
		`artifact render loads a remote <link href="https://…"> (stylesheet/font/preconnect) — the artifact CSP blocks it; inline the CSS and drop the web-font link (system-font fallback)`)
	check(extImgRe, "artifact.external-image",
		`artifact render loads a remote <img src="https://…"> — the artifact CSP blocks it; embed it as a data: URI or inline SVG`)
	check(cssURLRe, "artifact.external-css-url",
		`artifact render references a remote url(https://…) in CSS — the artifact CSP blocks it; inline the asset as a data: URI`)
	check(eventSourceRe, "artifact.eventsource",
		`artifact render includes an EventSource (SSE review loop) — the artifact CSP blocks it; render --artifact omits the review layer`)
	return findings
}

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

	// "carried enrichment" mirrors the skill's own gate: any annotation OR
	// context-bundle item present. Shared with lintWordmark via isEnriched so
	// the credit and the mark can never disagree about what "enriched" means.
	enriched := isEnriched(res)
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

// LintSessionLink warns when a plan whose provenance carries a session
// identity renders with no /c/ conversation link back to that exact session.
// The Go renderer injects the footer link deterministically
// (RenderOptions.SessionURL); this advisory exists for agent-authored renders
// (the ox-plan skill) where the link is part of the spec but the author is
// fallible. Exact-ID match only — a link to a different session is as wrong
// as none. Fail-open and advisory: callers warn, never block. Empty page or
// empty sessionID returns nil.
func LintSessionLink(htmlBytes []byte, sessionID string) []Finding {
	if len(htmlBytes) == 0 || sessionID == "" {
		return nil
	}
	if bytes.Contains(htmlBytes, []byte("/c/"+sessionID)) {
		return nil
	}
	return []Finding{{
		Rule:    "session.link-missing",
		Message: fmt.Sprintf("plan provenance records session %s but the render carries no /c/ conversation link back to it — pass RenderOptions.SessionURL (Go render) or add the footer session link (agent render)", sessionID),
	}}
}

// craftDeviceRe detects whether the rendered page carries a device mockup. The
// diagram side is the broader "any visual present" predicate (vizPresenceRe).
var craftDeviceRe = regexp.MustCompile(`class="device`)

// vizPresenceRe matches ANY visual the plan renderer can emit: a Mermaid or
// swimlane diagram, an inline SVG chart (donut/radar/line/treemap/sankey/chord/
// sparkline all emit <svg), or a hand-authored viz container. Defined in ONE place
// and drift-guarded by TestAnyVizPresent_CoversRenderers, so a renderer whose
// output it can't see fails CI rather than silently re-opening the missing-diagram
// false-positive — nagging a plan that DID visualize, just not in Mermaid.
var vizPresenceRe = regexp.MustCompile(`<svg|class="(?:mermaid|swim|barc|linec|riskm|statrow|ftree|heat|multiples|spark|ba|pbar-fig|pmapv|donut|radar|quad|tmap|sankey|chord|device)`)

// anyVizPresent reports whether the rendered page drew at least one visual.
func anyVizPresent(html string) bool { return vizPresenceRe.MatchString(html) }

// CraftReport is the realization side of the design-craft check: how many craft
// expectations enrich produced (a diagram suggested, a user-facing surface
// detected) and how many the rendered page realized. The render path records it as
// the `plan_craft` metric — hints_emitted vs hints_realized, aggregated across the
// ledger, is the "did the agent act on the visual hint" rate — and surfaces the
// unrealized Gaps as advisory nudges.
type CraftReport struct {
	Emitted  int
	Realized int
	Gaps     []Finding
}

// CraftRealization compares what ox EXPECTED (computed at enrich, surfaced
// cross-agent in Result.Guidance) against what the page DREW. Detection lives at
// enrich (DiagramHints / MockupSection); this is the thin, belt-and-suspenders
// realization check for an agent already in the ox render flow — NOT the primary
// cross-agent lever. Fail-open: an empty page yields a zero report. Precision over
// recall: a diagram expectation is met by ANY visual (a chart counts — "show,
// don't tell" holds even when the form differs from the hint), so a
// well-visualized plan is never nagged for the wrong diagram shape.
func CraftRealization(res Result, htmlBytes []byte) CraftReport {
	var rep CraftReport
	if len(htmlBytes) == 0 {
		return rep
	}
	h := string(htmlBytes)
	hasViz := anyVizPresent(h)

	// ox suggested a diagram somewhere — realized if the page drew any visual.
	if len(res.DiagramHints) > 0 {
		rep.Emitted++
		if hasViz {
			rep.Realized++
		} else {
			d := res.DiagramHints[0]
			rep.Gaps = append(rep.Gaps, Finding{
				Rule: "craft.missing-diagram",
				Message: fmt.Sprintf(
					`ox suggested a %s for "%s" (%s) but the plan drew no diagram or chart — a hero visual makes the shape readable in seconds (see `+"`ox viz`"+`)`,
					d.SuggestedType, d.Section, d.Reason),
			})
		}
	}

	// The plan changes a user-facing surface (detected at enrich) — realized only
	// by an actual device mockup.
	if res.MockupSection != "" {
		rep.Emitted++
		if craftDeviceRe.MatchString(h) {
			rep.Realized++
		} else {
			rep.Gaps = append(rep.Gaps, Finding{
				Rule: "craft.missing-mockup",
				Message: fmt.Sprintf(
					`"%s" changes a user-facing surface but the plan has no mockup — show the resulting UI state inline (`+"`ox viz device-mockup`"+`), don't describe it in prose`,
					res.MockupSection),
			})
		}
	}
	return rep
}

// LintCraft is the advisory view of CraftRealization — the unrealized craft gaps,
// printed (never blocking) after a render.
func LintCraft(res Result, htmlBytes []byte) []Finding {
	return CraftRealization(res, htmlBytes).Gaps
}
