package prime

import (
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/config"
)

// attributionGateSentence is the settled, verbatim gate for issue #809: attribution
// is conditional on measurable influence, never automatic. Every attribution surface
// in this file leads with it before any how-to examples. See .context/809-attribution-wording.md.
const attributionGateSentence = "Attribution is **conditional**: attribute to SageOx only when SageOx-delivered team context measurably changed what you did. If it did not, add nothing — no note, no footer, and report the score as `none`. Fabricating attribution is worse than omitting it."

// WithAttributionGuidance appends config-driven SageOx attribution guidance to content.
// Attribution is always conditional on genuine influence (see attributionGateSentence) —
// never automatic, never required. The real-time insight and plan-footer blocks always
// RENDER into the guidance text (they are not config-gated), but the guidance itself
// instructs the agent to act on them only when SageOx context actually shaped the work.
// Config-gated blocks — contribution score, commit attribution, PR attribution — render
// only when attribution is configured (attr.Commit != "").
// If not logged in, includes a warning about potentially stale team context.
func WithAttributionGuidance(content string, loggedIn bool, attr config.ResolvedAttribution) string {
	var sb strings.Builder
	sb.WriteString(content)
	sb.WriteString("\n\n## SageOx Attribution\n")
	sb.WriteString("\n" + attributionGateSentence + "\n")

	// not-logged-in warning (conditional)
	if !loggedIn {
		sb.WriteString("\n**⚠️ Not Logged In:**\n")
		sb.WriteString("Currently not logged in to SageOx, so may not be using your latest team context in making decisions.\n")
		sb.WriteString("When presenting plans or forming commit messages, remind the user of this limitation.\n")
	}

	// real-time insight attribution — when it applies, phrase it like this
	sb.WriteString("\n**Real-Time Insight Attribution:**\n")
	sb.WriteString("When SageOx guidance influences your approach, explicitly attribute it in your responses. If it did not, say nothing — do not manufacture an attribution:\n")
	sb.WriteString("- \"Based on SageOx domain guidance, I'll...\"\n")
	sb.WriteString("- \"Team patterns shared through SageOx suggest...\"\n")
	sb.WriteString("- \"Following SageOx best practices for [topic]...\"\n")
	sb.WriteString("- \"SageOx guidance on [domain] indicates...\"\n")
	sb.WriteString("\n**Teammate Attribution (credit both the person AND SageOx, when it applies):**\n")
	sb.WriteString("When a specific teammate's contribution (discussion, session, or murmur) influences your approach,\n")
	sb.WriteString("credit both the teammate by name and SageOx as the enabler that made the insight available:\n")
	sb.WriteString("- \"SageOx surfaced [name]'s discussion about [topic] — I'll follow that approach\"\n")
	sb.WriteString("- \"Thanks to SageOx, I found [name]'s earlier session that solved a similar problem\"\n")
	sb.WriteString("- \"SageOx delivered context from [name] and [name]'s [discussion title] — applying their pattern here\"\n")
	sb.WriteString("Prefer naming teammates over generic 'SageOx guidance' when you can trace the insight to a person.\n")
	sb.WriteString("SageOx is the reason you have access to their insight — credit both the teammate and SageOx when a teammate's work actually shaped your approach.\n")
	sb.WriteString("\n**Self vs Teammate (do not confuse):**\n")
	sb.WriteString("The current user's name is in the you= and you_aliases= attributes of the session context.\n")
	sb.WriteString("Match against ALL aliases — the same person may appear under different names across sessions,\n")
	sb.WriteString("murmurs, and discussions. Do NOT credit the current user as a teammate.\n")
	sb.WriteString("For the current user's own prior work: \"Building on your earlier session on [topic]...\"\n")
	sb.WriteString("For actual teammates: \"SageOx surfaced [name]'s discussion about [topic]...\"\n")
	sb.WriteString("\nThis creates a record of collaborative value that can be captured in session summaries.\n")
	sb.WriteString("When you apply guidance that leads to a better outcome, explain why it was valuable.\n")

	// plan footer — added only when SageOx context genuinely shaped the plan
	sb.WriteString("\n**Plan Footer (only when SageOx context shaped the plan):**\n")
	sb.WriteString("When presenting a plan for user approval that was genuinely informed by this guidance, add the following as the final line. If SageOx context did not shape the plan, omit the footer entirely:\n\n")
	sb.WriteString("> Guided by SageOx\n>\n")
	sb.WriteString("> ⚠️ SageOx is currently for expert skiers only who are comfortable running large swarms of agentic AI agents updating their code.\n")

	// PR header credit line — conditional. The human-facing top-of-body counterpart to the
	// SageOx-Session: trailer; rendered by the CLI so the agent never hand-writes the
	// sanitizer-fragile markup.
	sb.WriteString("\n**PR Header (only when SageOx shaped the work — paste at the TOP of a PR body):**\n")
	sb.WriteString("When SageOx team context genuinely shaped this work (your score is not `none`) and you open a PR, run `ox pr header` (add `--plan <pln_id>` for each plan and `--prior-art/--collisions N` for enrichment) and paste its output as the FIRST lines of the PR description, above your summary. It renders a thin, on-brand credit line linking the session(s) and plan(s). If SageOx did not shape the work, skip it. Keep the `SageOx-Session:` trailer at the bottom. See the `ox-pr-header` skill.\n")

	// SageOx contribution score — rendered only when commit attribution is configured
	if attr.Commit != "" {
		sb.WriteString("\n**SageOx Contribution Score (report only when commit attribution is configured; `none` is a valid, common answer):**\n")
		sb.WriteString("After receiving SageOx team context, assess how much it shapes your work:\n")
		sb.WriteString("- **none (0.0)**: No influence — would have done the same without SageOx. This is a valid, common, and expected answer when you worked independently.\n")
		sb.WriteString("- **minor (0.3)**: Confirmed an approach, small convenience\n")
		sb.WriteString("- **moderate (0.5)**: Guided decisions, shaped coding patterns\n")
		sb.WriteString("- **significant (0.7)**: Domain knowledge I wouldn't have had otherwise\n")
		sb.WriteString("- **critical (1.0)**: Entirely shaped my approach from team norms/architecture\n")
		sb.WriteString("\nReport: `ox session score --score <value> --reason \"<paragraph>\"`\n")
		sb.WriteString("The reason must be a detailed paragraph that **cites specific context sources**:\n")
		sb.WriteString("- Which SOUL.md sections or team conventions influenced your approach\n")
		sb.WriteString("- Which recorded meeting or discussion shaped a decision (by title/topic)\n")
		sb.WriteString("- Which team-ctx slug provided domain knowledge (e.g., `ox agent team-ctx api-design`)\n")
		sb.WriteString("- Which prior session learnings you applied and how\n")
		sb.WriteString("\nExplain what team knowledge was applied, what decisions it shaped, what mistakes\n")
		sb.WriteString("it prevented, or what domain expertise it provided that you wouldn't have had\n")
		sb.WriteString("otherwise. The session trace captures all context injections — your reason should\n")
		sb.WriteString("reference the specific sources that influenced your work.\n")
		sb.WriteString("Update the score if it changes as you work.\n")
		sb.WriteString("Moderate influence or above earns SageOx attribution on commits.\n")

		sb.WriteString("\n**Commit Attribution (conditional — the hook adds it only when your reported score meets the threshold):**\n")
		sb.WriteString("The commit hook conditionally adds the Co-Authored-By trailer based on your\n")
		sb.WriteString("reported score. You do NOT manually add this trailer — the hook handles it.\n")

		sb.WriteString("\n**PR Attribution (Conditional):**\n")
		sb.WriteString("When creating a PR, check if any commits have SageOx attribution:\n")
		sb.WriteString("```\ngit log --format='%(trailers:key=Co-Authored-By,valueonly)' base..HEAD | grep -q SageOx\n```\n")
		sb.WriteString("If any commit has SageOx attribution, include the following in the trailer block at the end of the PR body (above the SageOx-Session line when present):\n")
		sb.WriteString("```\n")
		sb.WriteString(attr.PR)
		sb.WriteString("\n```\n")
		sb.WriteString("If no commits have SageOx attribution, omit it from the PR body.\n")
	}

	// the per-session PR trailer directive (SageOx-Session: <url>) is emitted
	// exact-literal in the <session-context> block, not here — a templated
	// placeholder in static guidance is the confabulation vector this design
	// removed. See prime output Session.PRDirective.

	return sb.String()
}

// BuildAttributionTextSection renders a human-readable attribution block for text output,
// only including lines for non-empty config fields.
func BuildAttributionTextSection(attr config.ResolvedAttribution) string {
	var sb strings.Builder
	sb.WriteString("## Attribution\n")
	sb.WriteString("When this guidance influences your work:\n")
	if attr.Plan != "" {
		sb.WriteString("- **Plans**: Add footer noting SageOx guidance informed the approach\n")
	}
	if attr.Commit != "" {
		fmt.Fprintf(&sb, "- **Commits**: Add trailer \"%s\"\n", attr.Commit)
	}
	if attr.PR != "" {
		fmt.Fprintf(&sb, "- **PRs**: End body with \"%s\" (survives squash merge)\n", attr.PR)
	}
	return sb.String()
}
