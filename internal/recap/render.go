package recap

import (
	"fmt"
	"sort"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/sageox/ox/internal/cli"
)

// RenderHuman turns the evidence bundle into tight, honest prose for a bare
// terminal. It leads with named team-context artifacts (never a bare
// statistic), folds counts into sentences as quiet context, and — when the
// bundle is thin — leads with the next-action prescriptions. width is the target
// column count (80 is the design floor).
func RenderHuman(out *Output, width int) string {
	if width <= 0 {
		width = 80
	}
	var b strings.Builder

	// header
	b.WriteString(cli.StyleBrand.Render("SageOx recap"))
	b.WriteString(cli.StyleDim.Render(" · " + headerScope(out)))
	b.WriteString("\n\n")

	// Cold start is "never recorded anything" — NOT "no team context". A solo
	// player with a ledger has real (temporal) value to show.
	coldStart := out.Coverage.LedgerAllTime == 0

	if coldStart {
		renderColdStart(&b, out, width)
		renderTeamBuilt(&b, out, width)
		renderCoverageNote(&b, out, width)
		return b.String()
	}

	// Team knowledge leads when it reached the user; otherwise the solo
	// compounding-memory story leads. Both render when both exist.
	if len(out.ArtifactsReached) > 0 {
		renderArtifacts(&b, out, width)
	}
	renderKnowledgeFlow(&b, out, width)
	renderLedger(&b, out, width)
	renderDecisions(&b, out, width)
	renderPlans(&b, out, width)
	renderWork(&b, out, width)
	renderTeamBuilt(&b, out, width)
	renderNextActions(&b, out, width)
	renderCoverageNote(&b, out, width)

	return b.String()
}

// renderKnowledgeFlow renders the influence axis — how team knowledge changed
// the user's work. When the instrumentation lands it shows real causal chains;
// until then it renders a clearly-labeled placeholder rather than faking one.
func renderKnowledgeFlow(b *strings.Builder, out *Output, width int) {
	kf := out.KnowledgeFlow
	if kf == nil {
		return
	}
	if kf.Available && len(kf.Flows) > 0 {
		b.WriteString(cli.StyleBold.Render("How that knowledge changed your work:"))
		b.WriteString("\n")
		for _, f := range kf.Flows {
			writeWrapped(b, "· "+f, width, "  ")
		}
		b.WriteString("\n")
		return
	}
	if kf.Pending != "" {
		b.WriteString(cli.StyleBold.Render("How that knowledge changed your work"))
		b.WriteString(cli.StyleDim.Render(" — coming"))
		b.WriteString("\n")
		writeWrapped(b, cli.StyleDim.Render(kf.Pending), width, "  ")
		b.WriteString("\n")
	}
}

// renderLedger tells the solo (temporal) value story: the user's own recorded
// work is now searchable, reloadable memory. Always shown once a ledger exists —
// it is the value a team of one gets on day one.
func renderLedger(b *strings.Builder, out *Output, width int) {
	all := out.Coverage.LedgerAllTime
	if all == 0 {
		return
	}
	b.WriteString(cli.StyleBold.Render("Your ledger is compounding your own memory."))
	b.WriteString("\n")

	lead := fmt.Sprintf("Your work is captured as %s you can search and reload with "+
		"`ox query` — so you don't re-explain your codebase or re-solve what you already solved.",
		countPhrase(all, "recorded session", "recorded sessions"))
	writeWrapped(b, lead, width, "  ")

	if w := out.Coverage.SessionsInWindow; w > 0 && w < all {
		writeWrapped(b, cli.StyleDim.Render(fmt.Sprintf("Of those, %s fall within this window.", countPhrase(w, "session", "sessions"))), width, "  ")
	}
	b.WriteString("\n")
}

// renderPlans shows what SageOx caught before the user wrote code, from their
// own history — pure solo value.
func renderPlans(b *strings.Builder, out *Output, width int) {
	if len(out.PlansEnriched) == 0 {
		return
	}
	b.WriteString(cli.StyleBold.Render("Caught before you wrote code:"))
	b.WriteString("\n")
	for _, p := range out.PlansEnriched {
		topic := p.Topic
		if topic == "" {
			topic = p.Slug
		}
		writeWrapped(b, "· "+cli.StyleAccent.Render(topic)+cli.StyleDim.Render(" — "+planCaught(p)), width, "  ")
	}
	b.WriteString("\n")
}

// planCaught phrases what an enriched plan flagged, counts folded into the
// sentence.
func planCaught(p PlanEnriched) string {
	var parts []string
	if p.Collisions > 0 {
		parts = append(parts, countPhrase(p.Collisions, "collision with open work", "collisions with open work"))
	}
	if p.PriorArt > 0 {
		parts = append(parts, countPhrase(p.PriorArt, "prior-art match", "prior-art matches"))
	}
	if p.ExpertRoutes > 0 {
		parts = append(parts, countPhrase(p.ExpertRoutes, "expert route", "expert routes"))
	}
	return strings.Join(parts, ", ")
}

// countPhrase renders "N thing", picking singular/plural — a count folded into a
// noun phrase so it never stands alone as a bare statistic.
func countPhrase(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", n, plural)
}

func headerScope(out *Output) string {
	who := out.User
	if who == "" {
		who = "you"
	}
	return who + " · " + windowLabel(out)
}

// windowLabel is the human phrase for the reporting window, e.g. "last 30 days".
func windowLabel(out *Output) string {
	if out.WindowLabel != "" {
		return out.WindowLabel
	}
	return "since " + out.Since.Format("2006-01-02")
}

func renderArtifacts(b *strings.Builder, out *Output, width int) {
	arts := make([]ArtifactReach, len(out.ArtifactsReached))
	copy(arts, out.ArtifactsReached)
	// Lead with the artifact carrying the most substance — a real quotable
	// snippet, then reach — so the Constitution/glossary lead, not a tiny
	// template. Richness beats a bare session count.
	sort.SliceStable(arts, func(i, j int) bool {
		if (len(arts[i].Snippet) > 0) != (len(arts[j].Snippet) > 0) {
			return len(arts[i].Snippet) > 0
		}
		if len(arts[i].Snippet) != len(arts[j].Snippet) {
			return len(arts[i].Snippet) > len(arts[j].Snippet)
		}
		return arts[i].Sessions > arts[j].Sessions
	})

	b.WriteString(cli.StyleBold.Render("Your team's judgment reached your work, not a blank page."))
	b.WriteString("\n\n")

	// Lead artifact — full treatment, quoted.
	lead := arts[0]
	fmt.Fprintf(b, "  %s %s\n", cli.StyleBrand.Render(titleOr(lead)), cli.StyleDim.Render("("+lead.Doc+")"))
	line := reachLead(lead)
	if lead.Snippet != "" {
		line += " " + quote(lead.Snippet)
	}
	writeWrapped(b, line, width, "    ")
	b.WriteString("\n")

	// The rest — compact names, no repeated snippet.
	if len(arts) > 1 {
		names := make([]string, 0, len(arts)-1)
		for _, a := range arts[1:] {
			names = append(names, titleOr(a))
		}
		writeWrapped(b, cli.StyleDim.Render("Also in your context: "+strings.Join(names, ", ")), width, "  ")
	}

	// Shared receipt line — the sessions this knowledge reached, ONCE (deduped
	// across artifacts; the per-artifact repetition was noise).
	if shared := dedupSampleWork(arts); len(shared) > 0 {
		writeWrapped(b, cli.StyleDim.Render("Reached your work on: "+strings.Join(shared, "; ")), width, "  ")
	}
	b.WriteString("\n")
}

// titleOr returns the artifact's title, falling back to its doc name.
func titleOr(a ArtifactReach) string {
	if a.Title != "" {
		return a.Title
	}
	return a.Doc
}

// dedupSampleWork returns the union of session titles the artifacts reached,
// stable-ordered and capped — so the report names the work once instead of
// repeating the same session list under every artifact.
func dedupSampleWork(arts []ArtifactReach) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range arts {
		for _, s := range a.SampleWork {
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
			if len(out) >= maxSampleWork {
				return out
			}
		}
	}
	return out
}

// reachLead phrases the reach as a sentence — the count lives mid-sentence, so
// no line ever leads with a bare statistic.
func reachLead(a ArtifactReach) string {
	if a.Sessions <= 1 {
		return "Was in your context during your work."
	}
	return fmt.Sprintf("Was in your context across %d of your sessions.", a.Sessions)
}

func renderDecisions(b *strings.Builder, out *Output, width int) {
	if len(out.SettledDecisions) == 0 {
		return
	}
	b.WriteString(cli.StyleBold.Render("Decisions you inherited, already settled:"))
	b.WriteString("\n")
	for _, d := range out.SettledDecisions {
		line := quote(d.What)
		if d.Owner != "" {
			line += cli.StyleDim.Render(" — owner: " + d.Owner)
		}
		line += cli.StyleDim.Render(" — from " + d.Session)
		writeWrapped(b, "· "+line, width, "  ")
	}
	b.WriteString("\n")
}

func renderWork(b *strings.Builder, out *Output, width int) {
	withCommits := 0
	for _, w := range out.YourWork {
		if len(w.Commits) > 0 {
			withCommits++
		}
	}
	if withCommits == 0 {
		return
	}
	b.WriteString(cli.StyleBold.Render("What you shipped, with a session receipt:"))
	b.WriteString("\n")
	for _, w := range out.YourWork {
		if len(w.Commits) == 0 {
			continue
		}
		title := w.Title
		if title == "" {
			title = w.Session
		}
		writeWrapped(b, "· "+cli.StyleAccent.Render(title), width, "  ")
		for _, c := range w.Commits {
			writeWrapped(b, cli.StyleDim.Render(c), width, "      ")
		}
	}
	b.WriteString("\n")
}

func renderTeamBuilt(b *strings.Builder, out *Output, width int) {
	if len(out.TeamContextBuilt) == 0 {
		return
	}
	b.WriteString(cli.StyleBold.Render("What your team has built into shared context (reaches everyone):"))
	b.WriteString("\n")
	for _, t := range out.TeamContextBuilt {
		title := t.Title
		if title == "" {
			title = t.Doc
		}
		label := title
		if t.Kind == "discussion" {
			label = "Discussion: " + title
		}
		writeWrapped(b, "· "+label+cli.StyleDim.Render(" ("+t.Doc+")"), width, "  ")
	}
	b.WriteString("\n")
}

func renderColdStart(b *strings.Builder, out *Output, width int) {
	msg := "No recorded sessions yet in this window, so there's no personalized " +
		"value to show — but your value starts the moment you begin."
	writeWrapped(b, cli.StyleBold.Render(msg), width, "")
	b.WriteString("\n")
	if len(out.NextActions) > 0 {
		b.WriteString(cli.StyleBold.Render("Do this next:"))
		b.WriteString("\n")
		renderActionList(b, out.NextActions, width)
		b.WriteString("\n")
	}
}

func renderNextActions(b *strings.Builder, out *Output, width int) {
	if len(out.NextActions) == 0 {
		return
	}
	b.WriteString(cli.StyleBold.Render("To get more value:"))
	b.WriteString("\n")
	renderActionList(b, out.NextActions, width)
}

func renderActionList(b *strings.Builder, actions []NextAction, width int) {
	for _, a := range actions {
		writeWrapped(b, cli.StyleAccent.Render("★ "+a.Action), width, "  ")
		writeWrapped(b, cli.StyleDim.Render(a.Why), width, "    ")
	}
}

// renderCoverageNote adds the one honest line about traces we couldn't read.
func renderCoverageNote(b *strings.Builder, out *Output, width int) {
	if out.Coverage.TracesDehydrated > 0 {
		msg := fmt.Sprintf(
			"(%d session traces live in LFS and weren't downloaded — run `ox session view <name> --context` to fetch one.)",
			out.Coverage.TracesDehydrated)
		writeWrapped(b, cli.StyleDim.Render(msg), width, "")
	}
}

// quote wraps text in typographic quotes for a verbatim citation.
func quote(s string) string {
	return "“" + s + "”"
}

// writeWrapped word-wraps plain text to width with a leading indent, preserving
// any embedded style escapes by measuring on the visible text. Styled tokens are
// treated atomically (not split mid-escape).
func writeWrapped(b *strings.Builder, text string, width int, indent string) {
	avail := width - len(indent)
	if avail < 20 {
		avail = 20
	}
	for _, line := range wrapVisible(text, avail) {
		b.WriteString(indent)
		b.WriteString(line)
		b.WriteString("\n")
	}
}

// wrapVisible wraps on spaces using visible-width measurement so ANSI style
// sequences don't count toward the column budget.
func wrapVisible(text string, width int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	var cur strings.Builder
	curLen := 0
	for _, w := range words {
		wl := lipgloss.Width(w)
		if curLen > 0 && curLen+1+wl > width {
			lines = append(lines, cur.String())
			cur.Reset()
			curLen = 0
		}
		if curLen > 0 {
			cur.WriteByte(' ')
			curLen++
		}
		cur.WriteString(w)
		curLen += wl
	}
	if curLen > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}
