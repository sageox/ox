// Package prheader renders the SageOx "credit line" that an AI coworker pastes
// at the TOP of a pull-request description body — the human-facing counterpart
// to the machine `SageOx-Session:` trailer (see cmd/ox/session_url.go). It links
// the session(s), plan(s), and discussion(s) that produced the change, and names
// the team they belong to.
//
// Render is a PURE function: deterministic output for a given Input, no I/O, no
// globals mutated — mirroring internal/planhero so it is trivially table-tested.
//
// # The one rule about when it renders
//
// A credit line exists so a reviewer can GO LOOK. Render therefore emits nothing
// unless the header carries at least one artifact link a reviewer can open. The
// team name alone is not a credit: the wordmark's /t/ link is chrome a reviewer
// never needs, and a mark with no session, plan, or discussion behind it is a
// logo stamp on someone else's pull request, not provenance. State the rule as a
// property of the CLASS — "at least one openable link" — so the next artifact
// type inherits the gate instead of re-deriving it.
//
// # Why the markup looks the way it does
//
// A GitHub PR body is sanitized harder than repo markdown. Verified failing on
// github.com and therefore deliberately avoided:
//   - ALL CSS is stripped (<style>, class, id, inline style=), so text color
//     cannot be branded — only an <img> carries brand color.
//   - A full-width borderless two-cell <table> does NOT work: Primer's
//     .markdown-body CSS forces width:max-content + 1px cell borders/padding and
//     beats width="100%", rendering a bordered spreadsheet row pinned left.
//
// So the line is built from the primitives that survive: a <blockquote> card
// (left accent bar + subtle tint — the one card-like chrome GitHub allows), a
// theme-adaptive <picture> wordmark (the only mechanism that swaps by
// prefers-color-scheme), <a>, <small>, <b>, and &nbsp;/&middot; entities. Two
// dividers carry meaning: a slash joins the wordmark to the team name (the
// owner/team breadcrumb — containment), a middle dot divides the peer links.
// The wordmark is an <img>, so it can link to the team page AND keep its brand
// color — a text <a> cannot, since GitHub forces every link to its own blue, so
// the team name stays plain <b> text and the artifact links are the only blue.
//
// # Vertical alignment: why <small> and no align attribute
//
// Everything sits on ONE line, and every glyph on that line must share a
// baseline. Two measured findings drive the markup:
//
//   - The kicker uses <small>, never <sub>. Both shrink text, but <sub> also
//     carries vertical-align:sub and drops its text BELOW the baseline the rest
//     of the row sits on — a visible wobble. <small> shrinks without moving the
//     baseline, and it fails safe: if a sanitizer ever drops it the text renders
//     full-size, still aligned.
//   - The wordmark carries NO align attribute (i.e. vertical-align:baseline).
//     Measured against a 14px system-font line with the mark at 16px:
//     align="middle" sinks the mark 4.5px below the text baseline; the default
//     baseline alignment leaves it 3.5px high. Two legacy values (absmiddle,
//     texttop) measure closer but are not in the HTML rendering spec and map
//     differently across engines, and align="top"/"bottom" are positioned
//     against the LINE BOX, so their offset would move the moment a taller
//     element joined the row. Only baseline and middle are both spec-defined and
//     text-relative; baseline is the closer of the two.
//
// The residual 3.5px is an ASSET defect, not one this package can fix: the
// wordmark PNG's own ink baseline sits ~77% down its canvas, so no vertical-align
// keyword can land it on the text baseline. The cause-fix is a re-export from
// sageox-design with the baseline at a known fraction; leaning on a browser quirk
// here would be a backstop hiding that, and would teach the next author nothing.
package prheader

import (
	"strconv"
	"strings"
)

// Brand wordmark assets. These are public, immutable, camo-fetchable PNGs served
// from the apex host, so they render even for a PR on a PRIVATE repo and are the
// same across dev/test/prod (a brand asset is environment-independent). Pairing:
// "-dark.png" is the light-ink variant intended FOR a dark canvas and therefore
// belongs in <source media="(prefers-color-scheme: dark)">; "-light.png" is the
// dark-ink default <img>. VERIFY this direction by eye before shipping — a
// backwards pairing renders clean and only vanishes in the "wrong" mode.
//
// KNOWN ASSET DEFECT (tracked for sageox-design, which owns brand assets): the
// two files are not one artwork at two inks. Light is 313x86 (ratio 3.640, 2px
// padding, ink baseline 76.7% down); dark is 512x133 (ratio 3.850, ink flush to
// all four edges, baseline 78.2%). At a fixed height the mark therefore changes
// width by ~6% when a reader switches theme. The height below is tuned to the
// light asset.
const (
	wordmarkLightURL = "https://sageox.ai/sageox-wordmark-light.png"
	wordmarkDarkURL  = "https://sageox.ai/sageox-wordmark-dark.png"
	// 16px reads as a wordmark next to ~14px GitHub body text without dominating
	// the row. Deliberately no align attribute — see the package doc.
	wordmarkHeight = "16"
	// brandName is the wordmark's own name. The team-name segment is suppressed
	// when it equals this (case-insensitively), so a team literally named "SageOx"
	// (the dogfood team) doesn't render the brand twice — mark then bold text.
	brandName = "SageOx"
)

// Idempotency markers so a future server-side reconciler (see
// docs/specs/session-pr-issue-linkage.md) can find and replace the block in
// place rather than appending duplicates. Bumped to v2 for the single-line
// layout: v1 blocks in existing PR bodies are a different shape.
const (
	markerStart = "<!-- sageox:pr-header v2 -->"
	markerEnd   = "<!-- /sageox:pr-header -->"
)

// Session, Plan, and Discussion each carry a single pre-built, server-visible web
// URL. Link TEXT is a generic label chosen by this package ("Session 1", "Plan")
// — never a title — because the /c/ and /plan/ URLs are deliberately opaque so
// nothing about the work leaks into a public PR body.
type (
	Session    struct{ URL string }
	Plan       struct{ URL string }
	Discussion struct{ URL string }
)

// Input is the fully-resolved data a header renders from. The caller (the ox
// command) owns all resolution — config lookup, URL building — so this stays a
// pure render. TeamName is UNTRUSTED (team-editable config) and is HTML-escaped
// before it reaches the output.
type Input struct {
	TeamName    string       // display name, e.g. "Acme Rockets"; "" omits the team segment
	TeamURL     string       // {endpoint}/t/{slug}; "" leaves the wordmark unlinked
	Sessions    []Session    // 0..N; ordered as displayed
	Plans       []Plan       // 0..N; ordered as displayed
	Discussions []Discussion // 0..N; ordered as displayed
}

// HasLinks reports whether the header carries at least one artifact link a
// reviewer can open — the single gate on rendering at all. Exported as the one
// source of the rule so the command (which explains the no-op on stderr) and
// Render can't drift.
func (in Input) HasLinks() bool {
	return len(in.Sessions)+len(in.Plans)+len(in.Discussions) > 0
}

// Render returns the paste-ready, GitHub-safe credit-line block: a single-line
// <blockquote> card wrapped in the idempotency markers, or "" when there is
// nothing to credit. It never errors on content — every field degrades
// independently — so it returns only a string.
func Render(in Input) string {
	// A header that links nothing credits nothing. See the package doc.
	if !in.HasLinks() {
		return ""
	}

	// Suppress the team name when it only repeats the brand the wordmark already
	// shows — the dogfood team is literally "SageOx", so mark + name would read
	// "SageOx / SageOx".
	teamName := strings.TrimSpace(in.TeamName)
	showTeam := teamName != "" && !strings.EqualFold(teamName, brandName)

	var row strings.Builder

	// "guided by" kicker, inline and baseline-stable: the attribution, so the
	// brand name is the mark itself and never repeated as plain text.
	row.WriteString("<small>guided&nbsp;by</small>&nbsp;")

	// Anchor: the theme-adaptive wordmark <picture>, linked (as an image, so it
	// keeps its brand color) to the team page.
	row.WriteString(wordmark(in.TeamURL))

	// Team name as plain muted <b> text, not a link: a reviewer reads it as chrome
	// and the wordmark already routes to the team page. Non-breaking so it never
	// wraps mid-name. Joined to the wordmark by a SLASH, not the peer dot: the
	// team lives *within* the brand (the owner/team breadcrumb), while the links
	// that follow are peers.
	if showTeam {
		row.WriteString(hierSep())
		row.WriteString("<b>")
		row.WriteString(noWrap(escapeHTML(teamName)))
		row.WriteString("</b>")
	}

	// The actionable links, grouped within a category by whitespace (Tufte
	// grouping), categories divided by a middle dot.
	writeLinks(&row, numberedLabels("Session", len(in.Sessions)), sessionSlice(in.Sessions))
	writeLinks(&row, numberedLabels("Plan", len(in.Plans)), planSlice(in.Plans))
	writeLinks(&row, numberedLabels("Discussion", len(in.Discussions)), discussionSlice(in.Discussions))

	var b strings.Builder
	b.WriteString(markerStart)
	b.WriteString("\n> ")
	b.WriteString(row.String())
	b.WriteString("\n")
	b.WriteString(markerEnd)
	return b.String()
}

// sep is the calm PEER divider — a non-breaking middle dot, the Linear/Apple
// separator, never a shields.io pipe. It divides sibling categories: the team,
// the sessions, the plans, the discussions.
func sep() string { return "&nbsp;&middot;&nbsp;" }

// hierSep is the CONTAINMENT divider — a non-breaking slash, the owner/team
// breadcrumb every reviewer already reads (GitHub org/repo, Linear Team/Project).
// It joins the wordmark to the team name so "SageOx / Acme Rockets" reads as one
// unit — the team within the brand — distinct from the peer links after it.
func hierSep() string { return "&nbsp;/&nbsp;" }

// wordmark builds the theme-adaptive <picture>, wrapped in an <a> to the team
// page when one is known. An unlinked wordmark (teamURL == "") still renders.
// No align attribute: the default baseline alignment measured closest among the
// spec-defined, text-relative options. See the package doc.
func wordmark(teamURL string) string {
	pic := "<picture>" +
		`<source media="(prefers-color-scheme: dark)" srcset="` + escapeHTML(wordmarkDarkURL) + `">` +
		`<img alt="` + escapeHTML(brandName) + `" height="` + wordmarkHeight + `" src="` + escapeHTML(wordmarkLightURL) + `">` +
		"</picture>"
	if strings.TrimSpace(teamURL) == "" {
		return pic
	}
	return `<a href="` + escapeHTML(teamURL) + `">` + pic + "</a>"
}

// writeLinks appends one category of links: a leading category separator, then
// each label linked to its URL, grouped by a double non-breaking space so
// "Session 1" and "Session 2" read as one cluster without a divider between them.
// A zero-length category writes nothing.
func writeLinks(b *strings.Builder, labels []string, urls []string) {
	if len(urls) == 0 {
		return
	}
	b.WriteString(sep())
	for i, u := range urls {
		if i > 0 {
			b.WriteString("&nbsp;&nbsp;")
		}
		b.WriteString(`<a href="` + escapeHTML(u) + `">` + noWrap(labels[i]) + "</a>")
	}
}

func sessionSlice(s []Session) []string {
	out := make([]string, len(s))
	for i := range s {
		out[i] = s[i].URL
	}
	return out
}

func planSlice(p []Plan) []string {
	out := make([]string, len(p))
	for i := range p {
		out[i] = p[i].URL
	}
	return out
}

func discussionSlice(d []Discussion) []string {
	out := make([]string, len(d))
	for i := range d {
		out[i] = d[i].URL
	}
	return out
}

// numberedLabels returns n labels: [noun] for n==1, else [noun 1 .. noun n].
// A single item stays unnumbered ("Plan") because a lone "Plan 1" reads as if a
// "Plan 2" were missing.
// n<=0 returns an empty slice (the category then renders nothing).
func numberedLabels(noun string, n int) []string {
	if n <= 0 {
		return nil
	}
	if n == 1 {
		return []string{noun}
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = noun + "&nbsp;" + strconv.Itoa(i+1)
	}
	return out
}
