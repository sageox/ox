package plan

// html_meta.go stamps a rendered plan's self-identifying <meta> tags into its
// <head> at save time: sageox:plan (the stable pln_ id), sageox:plan-slug, and
// sageox:plan-title. This is what lets a standalone copy of plan.html —
// downloaded, emailed, dropped into a chat thread — still resolve back to its
// plan with no other context (the ledger, meta.json, events.jsonl) at hand.
//
// Applied once, uniformly, from Save() rather than from the Go template
// (plan.html.tmpl/renderData): plan.html reaching Save() comes from two
// sources — the built-in RenderHTMLOpts template AND an agent-authored render
// passed in via `ox plan save --html` — and only Save() knows the pln_ id
// (minted or resolved from events.jsonl) in time to stamp either source. A
// template field would only ever cover the first source, still leaving the
// second needing this same string-level injection.

import (
	"fmt"
	"html"
	"regexp"
)

// The <meta name="..."> values StampHTMLMeta writes and looks for.
const (
	planMetaName      = "sageox:plan"
	planSlugMetaName  = "sageox:plan-slug"
	planTitleMetaName = "sageox:plan-title"
)

// headCloseRe anchors the insertion point for tags StampHTMLMeta doesn't find
// already present. Inserting just before </head> — rather than right after
// <head> — leaves <meta charset> undisturbed as one of the first bytes in the
// document, which HTML5 encoding sniffing prefers.
var headCloseRe = regexp.MustCompile(`(?i)</head\s*>`)

// planMetaRe/planSlugMetaRe/planTitleMetaRe match an existing
// StampHTMLMeta-written tag, so a re-save replaces it in place instead of
// duplicating it. The realistic case this guards: a skill reads back a
// previously-stored plan.html (already carrying these tags from an earlier
// save), edits content, and resubmits it via `ox plan save --html`.
// Precompiled at package init (mirrors this package's other HTML-scanning
// regexes in lint.go) rather than per call — StampHTMLMeta runs once per
// Save(), but there is no reason to recompile 3 constant patterns each time.
var (
	planMetaRe      = mustMetaTagRe(planMetaName)
	planSlugMetaRe  = mustMetaTagRe(planSlugMetaName)
	planTitleMetaRe = mustMetaTagRe(planTitleMetaName)
)

func mustMetaTagRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)<meta\s+name=["']` + regexp.QuoteMeta(name) + `["']\s+content=["'][^"']*["']\s*/?>`)
}

// StampHTMLMeta ensures htmlBytes' <head> carries the plan's 3 self-identifying
// meta tags (sageox:plan / sageox:plan-slug / sageox:plan-title), replacing any
// that already match StampHTMLMeta's own canonical form and inserting whichever
// are missing. htmlBytes == nil returns nil (nothing to stamp — the auto-save
// path never carries a render). A document with no </head> is returned
// byte-for-byte unmodified: best-effort, since a malformed or non-HTML payload
// must never break the save that owns it.
func StampHTMLMeta(htmlBytes []byte, planID, slug, title string) []byte {
	if htmlBytes == nil {
		return nil
	}

	tags := []struct {
		name  string
		value string
		re    *regexp.Regexp
	}{
		{planMetaName, planID, planMetaRe},
		{planSlugMetaName, slug, planSlugMetaRe},
		{planTitleMetaName, title, planTitleMetaRe},
	}

	s := string(htmlBytes)
	var toInsert []string
	for _, t := range tags {
		tag := fmt.Sprintf(`<meta name="%s" content="%s">`, t.name, html.EscapeString(t.value))
		if t.re.MatchString(s) {
			// ReplaceAllLiteralString: tag is inserted verbatim, never as a `$`
			// backreference template — a title containing a literal '$' must not
			// be misinterpreted by ReplaceAllString's expansion syntax.
			s = t.re.ReplaceAllLiteralString(s, tag)
		} else {
			toInsert = append(toInsert, tag)
		}
	}
	if len(toInsert) == 0 {
		return []byte(s)
	}

	loc := headCloseRe.FindStringIndex(s)
	if loc == nil {
		return []byte(s) // no </head> to anchor on; leave the document as-is
	}
	insertion := ""
	for _, tag := range toInsert {
		insertion += tag + "\n"
	}
	return []byte(s[:loc[0]] + insertion + s[loc[0]:])
}
