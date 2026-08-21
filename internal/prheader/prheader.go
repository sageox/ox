// Package prheader renders the SageOx "credit line" that an AI coworker pastes
// at the TOP of a pull-request description body — the human-facing counterpart
// to the machine `SageOx-Session:` trailer (see cmd/ox/session_url.go). It names
// the team, links the session(s) and plan(s) that produced the change, and
// whispers a Tufte-style enrichment stat.
//
// Render is a PURE function: deterministic output for a given Input, no I/O, no
// globals mutated — mirroring internal/planhero so it is trivially table-tested.
// Every Input field degrades independently: no sessions, no plans, and no
// enrichment each still produce a valid, good-looking line.
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
// So the line is built from the primitives that survive: <picture> (the only
// theme-adaptive mechanism — swaps two image URLs by prefers-color-scheme),
// <img>, <a>, <sub>, <b>, and &nbsp;/&middot;/&emsp; entities. "Off to the right"
// is earned by trailing + receding (<sub>), or — in Tier B — a floated
// <img align="right">, the one primitive that hard-pins to the container edge
// without table chrome.
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
const (
	wordmarkLightURL = "https://sageox.ai/sageox-wordmark-light.png"
	wordmarkDarkURL  = "https://sageox.ai/sageox-wordmark-dark.png"
	// 18px reads as a wordmark next to ~14px GitHub body text without dominating
	// the row; align="middle" vertically centers it on the text line (falls back
	// to baseline harmlessly if a renderer drops the attribute).
	wordmarkHeight = "18"
	// The Tier-B floated enrichment strip sits slightly smaller than the wordmark
	// so it reads as subordinate — a whisper, not a second anchor.
	stripHeight = "16"
)

// Idempotency markers so a future server-side reconciler (see
// docs/specs/session-pr-issue-linkage.md) can find and replace the block in
// place rather than appending duplicates. Bump the version suffix on a
// breaking-layout change.
const (
	markerStart = "<!-- sageox:pr-header v1 -->"
	markerEnd   = "<!-- /sageox:pr-header -->"
)

// Signals is the enrichment summary a header can whisper. It mirrors the
// client-side plan.Signals shape (internal/plan) so the command maps one to the
// other without this package importing plan. Material gates the whole whisper:
// we only ever CLAIM "Guided by SageOx" when enrichment materially fired.
type Signals struct {
	Collisions   int  `json:"collisions"`
	PriorArt     int  `json:"prior_art"`
	ExpertRoutes int  `json:"expert_routes"`
	Material     bool `json:"material"`
}

// hasWhisper reports whether an enrichment whisper should render at all.
func (s Signals) hasWhisper() bool { return s.Material }

// Session and Plan carry a single pre-built, server-visible web URL. Link TEXT
// is a generic label chosen by this package ("Session 1", "Plan") — never a
// title — because the /c/ and /plan/ URLs are deliberately opaque so nothing
// about the work leaks into a public PR body.
type (
	Session struct{ URL string }
	Plan    struct{ URL string }
)

// StripURLs are the hosted light/dark PNG URLs for the Tier-B floated enrichment
// strip. When non-nil AND a whisper is warranted, Render emits the floated image
// instead of the <sub> text whisper. Nil selects Tier A (text) — the always-works
// default that needs no hosting.
type StripURLs struct {
	Light string
	Dark  string
}

// Input is the fully-resolved data a header renders from. The caller (the ox
// command) owns all resolution — config lookup, URL building, the optional strip
// upload — so this stays a pure render. TeamName is UNTRUSTED (team-editable
// config) and is HTML-escaped before it reaches the output.
type Input struct {
	TeamName string    // display name, e.g. "SageOx Internal"; "" omits the team segment
	TeamURL  string    // {endpoint}/t/{slug}; "" leaves the wordmark unlinked
	Sessions []Session // 0..N; ordered as displayed
	Plans    []Plan    // 0..N; ordered as displayed
	Signals  Signals
	ShowStat bool       // false suppresses the whisper regardless of Signals
	Strip    *StripURLs // non-nil => Tier B floated image whisper
}

// Render returns the paste-ready, GitHub-safe credit-line block, wrapped in the
// idempotency markers. It never errors on content — every field degrades
// independently — so it returns only a string; the signature keeps a value
// receiver-free pure shape for the callers and tests.
func Render(in Input) string {
	whisper := in.ShowStat && in.Signals.hasWhisper()
	tierB := whisper && in.Strip != nil && in.Strip.Light != "" && in.Strip.Dark != ""

	// A lone wordmark carries no information — no team, no links, no whisper.
	// Return "" so the caller emits nothing rather than stamping a bare logo
	// onto a PR body. (A named team alone is still payload and renders.)
	if strings.TrimSpace(in.TeamName) == "" && len(in.Sessions) == 0 && len(in.Plans) == 0 && !whisper {
		return ""
	}

	var b strings.Builder

	// Tier B: the floated strip MUST come first in source order so it floats onto
	// the same line as the text that follows it.
	if tierB {
		b.WriteString(floatedStrip(in.Strip.Light, in.Strip.Dark, whisperAlt(in.Signals)))
	}

	// Anchor: the wordmark <picture>, optionally linked to the team page. This is
	// the one brand-colored, theme-adaptive element.
	b.WriteString(wordmark(in.TeamURL))

	// Team name in <b> — the "whose" of the PR. Non-breaking so it never wraps
	// mid-name.
	if name := strings.TrimSpace(in.TeamName); name != "" {
		b.WriteString(sep())
		b.WriteString("<b>")
		b.WriteString(noWrap(escapeHTML(name)))
		b.WriteString("</b>")
	}

	// Sessions and plans: live text links, grouped within a category by whitespace
	// (Tufte grouping), categories divided by a middle dot.
	writeLinks(&b, numberedLabels("Session", len(in.Sessions)), sessionSlice(in.Sessions))
	writeLinks(&b, numberedLabels("Plan", len(in.Plans)), planSlice(in.Plans))

	// Tier A whisper: trailing, receded into <sub>. Skipped entirely under Tier B
	// (the floated image carries it) or when not warranted.
	if whisper && !tierB {
		b.WriteString("&emsp;<sub>")
		b.WriteString(whisperMarkup(in.Signals))
		b.WriteString("</sub>")
	}

	return markerStart + "\n" + b.String() + "\n" + markerEnd
}

// sep is the calm category divider — a non-breaking middle dot, the Linear/Apple
// separator, never a shields.io pipe.
func sep() string { return "&nbsp;&middot;&nbsp;" }

// wordmark builds the theme-adaptive <picture>, wrapped in an <a> to the team
// page when one is known. An unlinked wordmark (teamURL == "") still renders.
func wordmark(teamURL string) string {
	pic := "<picture>" +
		`<source media="(prefers-color-scheme: dark)" srcset="` + escapeHTML(wordmarkDarkURL) + `">` +
		`<img alt="SageOx" height="` + wordmarkHeight + `" align="middle" src="` + escapeHTML(wordmarkLightURL) + `">` +
		"</picture>"
	if strings.TrimSpace(teamURL) == "" {
		return pic
	}
	return `<a href="` + escapeHTML(teamURL) + `">` + pic + "</a>"
}

// floatedStrip builds the Tier-B enrichment image: a right-floated <picture>
// whose alt carries the full sentence so screen readers and a broken-image
// fallback still read the credit.
func floatedStrip(light, dark, alt string) string {
	return "<picture>" +
		`<source media="(prefers-color-scheme: dark)" srcset="` + escapeHTML(dark) + `">` +
		`<img alt="` + escapeHTML(alt) + `" align="right" height="` + stripHeight + `" src="` + escapeHTML(light) + `">` +
		"</picture>"
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
