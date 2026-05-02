package main

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sageox/ox/internal/facts"
)

// citation is a single source we link from a distilled summary.
//
// One citation per unique source (deduped by URL when present, otherwise by
// source_ref). The citation pipeline (gh #476) carries these through every
// distill layer: facts → daily Sources section → weekly Sources section →
// monthly Sources section. The Num field is layer-local — at each layer the
// citation list is rebuilt from scratch and renumbered.
type citation struct {
	Num   int    // 1-based, used in [N] markers and ref-link definitions
	Label string // human-friendly text (e.g., "Discussion: 2026-03-10 — Architecture sync")
	URL   string // empty if no clickable URL is available — citation still rendered without link
	Key   string // dedup key: URL if non-empty, else source_ref
}

// buildCitationsFromFacts builds a deduplicated, ordered citation list from
// a flat slice of facts. Order is stable: timestamp ascending, then label lex.
//
// Returns the citation list AND a map from each fact's index in the input
// slice to its citation number. Multiple facts that share the same source
// (same dedup key) map to the same citation number.
//
// Facts with no resolvable source (no URL and no source_ref) are skipped —
// they cannot be cited.
func buildCitationsFromFacts(fs []facts.Fact) ([]citation, map[int]int) {
	type pendingFact struct {
		idx   int
		fact  facts.Fact
		key   string
		label string
	}

	// first pass: compute key and label for each fact, skip uncitable
	var pending []pendingFact
	for i, f := range fs {
		key := citationKey(f)
		if key == "" {
			continue
		}
		pending = append(pending, pendingFact{
			idx:   i,
			fact:  f,
			key:   key,
			label: citationLabel(f),
		})
	}

	// dedup by key, keeping the earliest timestamp for ordering and the first label seen
	type bucket struct {
		key   string
		label string
		url   string
		ts    time.Time // earliest parsed timestamp; zero if no fact had a parseable one
		idxs  []int     // input indices that belong to this bucket
	}
	bucketsByKey := make(map[string]*bucket)
	var keyOrder []string
	for _, p := range pending {
		// Parse the fact's timestamp into a real time.Time so multi-offset
		// RFC3339 inputs sort chronologically. Lexical comparison of raw
		// strings would order "2026-03-10T15:00:00-05:00" (20:00 UTC) BEFORE
		// "2026-03-10T13:00:00-07:00" (20:00 UTC) — same instant, wrong shape.
		factTime := parseFactTimestamp(p.fact.Timestamp)
		b, ok := bucketsByKey[p.key]
		if !ok {
			b = &bucket{
				key:   p.key,
				label: p.label,
				url:   p.fact.SourceURL,
				ts:    factTime,
			}
			bucketsByKey[p.key] = b
			keyOrder = append(keyOrder, p.key)
		} else {
			// keep the earliest timestamp so chronological ordering is stable
			if !factTime.IsZero() && (b.ts.IsZero() || factTime.Before(b.ts)) {
				b.ts = factTime
			}
			// promote a non-empty URL if we hadn't seen one
			if b.url == "" && p.fact.SourceURL != "" {
				b.url = p.fact.SourceURL
			}
		}
		b.idxs = append(b.idxs, p.idx)
	}

	buckets := make([]*bucket, 0, len(bucketsByKey))
	for _, k := range keyOrder {
		buckets = append(buckets, bucketsByKey[k])
	}
	// stable sort: timestamp ascending (parsed), then label lex.
	// Zero timestamps sort first so untimestamped facts cluster at the top.
	sort.SliceStable(buckets, func(i, j int) bool {
		if !buckets[i].ts.Equal(buckets[j].ts) {
			return buckets[i].ts.Before(buckets[j].ts)
		}
		return buckets[i].label < buckets[j].label
	})

	cites := make([]citation, 0, len(buckets))
	factToNum := make(map[int]int, len(pending))
	for i, b := range buckets {
		num := i + 1
		cites = append(cites, citation{
			Num:   num,
			Label: b.label,
			URL:   b.url,
			Key:   b.key,
		})
		for _, idx := range b.idxs {
			factToNum[idx] = num
		}
	}

	return cites, factToNum
}

// citationKey returns the dedup key for a fact: URL if available, else source_ref.
// Returns empty string for facts that cannot be cited (no URL, no ref).
func citationKey(f facts.Fact) string {
	if f.SourceURL != "" {
		return f.SourceURL
	}
	return f.SourceRef
}

// citationLabel produces a short human-readable label for a citation.
// Strategy depends on source type — see #476 design notes.
func citationLabel(f facts.Fact) string {
	date := citationDate(f.Timestamp)
	switch f.SourceType {
	case facts.SourceGitHub:
		return githubShortLabel(f.SourceURL, f.SourceRef, f.SourceTitle)
	case facts.SourceDiscussion:
		title := f.SourceTitle
		if title == "" {
			title = trimDirnameDate(f.SourceRef)
		}
		if date != "" && title != "" {
			return fmt.Sprintf("Discussion: %s — %s", date, title)
		}
		if title != "" {
			return "Discussion: " + title
		}
		return "Discussion"
	case facts.SourceSession:
		title := f.SourceTitle
		if title == "" {
			title = trimDirnameDate(f.SourceRef)
		}
		if date != "" && title != "" {
			return fmt.Sprintf("Session: %s — %s", date, title)
		}
		if title != "" {
			return "Session: " + title
		}
		return "Session"
	case facts.SourceObservation:
		if date != "" {
			return "Observation " + date
		}
		return "Observation"
	}
	// fallback: source_ref or generic label
	if f.SourceTitle != "" {
		return f.SourceTitle
	}
	if f.SourceRef != "" {
		return f.SourceRef
	}
	return "Source"
}

// citationDate extracts a YYYY-MM-DD slice from an ISO 8601 timestamp.
// Returns empty if the timestamp is empty or unparseable.
//
// IMPORTANT: returns the SOURCE-LOCAL calendar date, not the UTC date. For
// `2026-03-10T23:30:00-08:00` we want the human-facing label to read
// `2026-03-10` (the local day), not `2026-03-11` (the UTC day after the
// offset shift). We rely on the YYYY-MM-DD prefix from the raw string
// because parseFactTimestamp normalizes to UTC (correct for sorting, wrong
// for display). Only fall back to the parsed date for non-RFC3339 inputs
// that don't have a leading date.
func citationDate(ts string) string {
	if len(ts) >= 10 {
		prefix := ts[:10]
		if _, err := time.Parse("2006-01-02", prefix); err == nil {
			return prefix
		}
	}
	if t := parseFactTimestamp(ts); !t.IsZero() {
		return t.Format("2006-01-02")
	}
	return ""
}

// parseFactTimestamp parses a fact's Timestamp field into a time.Time.
// Accepts RFC3339 (with any offset) and bare YYYY-MM-DD. Returns zero time
// for empty / unparseable input — chronological sort callers should treat
// zero as "earliest" or "unknown".
//
// Lexical comparison of raw RFC3339 strings is wrong for mixed-offset inputs
// (e.g., "...T15:00:00-05:00" and "...T13:00:00-07:00" are the same instant
// but sort apart), so callers needing chronological order MUST go through
// this helper, not direct string comparison.
func parseFactTimestamp(ts string) time.Time {
	if ts == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse("2006-01-02", ts); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// trimDirnameDate strips a leading "<path>/" prefix and a "YYYY-MM-DD-HHMM-"
// or "YYYY-MM-DDTHH-MM-" prefix from a dirname-style ref so the remainder
// can be used as a label. Falls back to the original ref if no pattern matches.
func trimDirnameDate(ref string) string {
	if ref == "" {
		return ""
	}
	// strip leading "type/" prefix (e.g. "discussions/", "sessions/")
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		ref = ref[i+1:]
	}
	// session: YYYY-MM-DDTHH-MM-<who>-<id>
	if m := sessionDirNameRe.FindStringSubmatch(ref); m != nil {
		return m[2] // who-id portion
	}
	// discussion: YYYY-MM-DD-HHMM-<who>
	if m := discussionDirNameRe.FindStringSubmatch(ref); m != nil {
		return m[2]
	}
	return ref
}

var (
	sessionDirNameRe    = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})T\d{2}-\d{2}-(.+)$`)
	discussionDirNameRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})-\d{4}-(.+)$`)
)

// githubShortLabel converts a github URL to a short label like "owner/repo#152"
// or "owner/repo@abc1234".
//
// Fallback chain (in order):
//  1. parseGitHubURLLabel(urlStr || ref) — short form for github.com URLs
//  2. title — the PR/issue title verbatim
//  3. ref — the raw source_ref (usually the URL)
//  4. urlStr — the raw URL when ref is empty but URL is unparseable
//  5. "GitHub" — final placeholder so a citation never has an empty label
//
// Inputs:
//   - urlStr: the URL to parse (preferred)
//   - ref: source_ref (often the same as urlStr for github)
//   - title: PR/issue title from source_title
func githubShortLabel(urlStr, ref, title string) string {
	tryURL := urlStr
	if tryURL == "" {
		tryURL = ref
	}
	if tryURL != "" {
		if label := parseGitHubURLLabel(tryURL); label != "" {
			return label
		}
	}
	if title != "" {
		return title
	}
	if ref != "" {
		return ref
	}
	if urlStr != "" {
		return urlStr
	}
	return "GitHub"
}

// parseGitHubURLLabel extracts a short label from a github URL. Supports:
//   - https://github.com/<owner>/<repo>/pull/<n>   → owner/repo#n
//   - https://github.com/<owner>/<repo>/issues/<n> → owner/repo#n
//   - https://github.com/<owner>/<repo>/commit/<sha> → owner/repo@<sha[:7]>
//
// Returns empty string for unrecognized URL shapes.
func parseGitHubURLLabel(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.Host != "github.com" && !strings.HasSuffix(u.Host, ".github.com") {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 {
		return ""
	}
	owner, repo, kind, id := parts[0], parts[1], parts[2], parts[3]
	switch kind {
	case "pull", "issues":
		// strip non-numeric suffixes (e.g. /files, /commits) — only the number matters
		if num, err := strconv.Atoi(id); err == nil {
			return fmt.Sprintf("%s/%s#%d", owner, repo, num)
		}
	case "commit":
		short := id
		if len(short) > 7 {
			short = short[:7]
		}
		return fmt.Sprintf("%s/%s@%s", owner, repo, short)
	}
	return ""
}

// citationMarkerRe matches a `[N]` token (bare reference OR the trailing `[N]`
// of a `[anchor][N]` reference link). Captures the digits.
//
// We deliberately match a narrow shape — just `[<digits>]` — so the same regex
// covers both citation forms. The trade-off is that any bracketed number in
// the body matches, including incidental ones like `arr[10]` or `args[2]` if
// they appear in code samples. To avoid corrupting code, renumberCitationMarkers
// skips content inside fenced code blocks.
var citationMarkerRe = regexp.MustCompile(`\[(\d+)\]`)

// codeFenceRe matches a triple-backtick line (the start or end of a fenced
// code block). The fence may have a language tag or trailing whitespace.
var codeFenceRe = regexp.MustCompile("(?m)^```[^\n]*$")

// inlineCodeRe matches a markdown inline code span: a single-backtick run
// containing no backticks or newlines. Used to skip `arr[1]`-style content
// during citation marker renumbering.
//
// Limitation: doesn't recognize multi-backtick spans (“ “ ` “ “) — those
// are rare in distilled prose. CommonMark allows them but our LLM output
// doesn't emit them.
var inlineCodeRe = regexp.MustCompile("`[^`\n]+`")

// renumberCitationMarkers rewrites every `[N]` marker in text using oldToNew.
// Numbers not in the map are left untouched (defensive — shouldn't happen in
// practice, but we'd rather leave a stale marker than corrupt the document).
//
// Content inside markdown fenced code blocks (``` ... ```) AND inline code
// spans (`...`) is left untouched, so bracketed numerics in code samples
// like `arr[10]` are not corrupted.
//
// Both bare `[N]` and the trailing `[N]` of a `[anchor][N]` reference link
// match the regex and get rewritten — that's exactly what we want.
func renumberCitationMarkers(text string, oldToNew map[int]int) string {
	if len(oldToNew) == 0 {
		return text
	}
	rewrite := func(seg string) string {
		return citationMarkerRe.ReplaceAllStringFunc(seg, func(match string) string {
			// match is "[N]" — extract N
			inner := match[1 : len(match)-1]
			old, err := strconv.Atoi(inner)
			if err != nil {
				return match
			}
			new, ok := oldToNew[old]
			if !ok {
				return match
			}
			return fmt.Sprintf("[%d]", new)
		})
	}
	// Skip both fenced blocks and inline code spans. Inline-code skipping is
	// done as a second-level walk inside each non-fenced segment.
	return walkOutsideCodeFences(text, func(seg string) string {
		return walkOutsideInlineCode(seg, rewrite)
	})
}

// findSourcesHeadingEnd returns the byte offset just AFTER the first
// `## Sources` heading line that lies OUTSIDE any fenced code block.
// Returns -1 if no real heading is found.
//
// A body that quotes the distilled format in a markdown code sample (e.g.,
// agent guidance describing the format) would otherwise produce a
// false-positive match inside the fence and slice from the wrong region.
func findSourcesHeadingEnd(md string) int {
	// Scan line by line so we can track fence state cheaply. CommonMark
	// fences are recognized only at the start of a line.
	cursor := 0
	insideCode := false
	for cursor < len(md) {
		nl := strings.IndexByte(md[cursor:], '\n')
		var line string
		var lineEnd int
		if nl < 0 {
			line = md[cursor:]
			lineEnd = len(md)
		} else {
			line = md[cursor : cursor+nl]
			lineEnd = cursor + nl + 1
		}
		// fence toggle: a line starting with ``` (CommonMark fence)
		if strings.HasPrefix(line, "```") {
			insideCode = !insideCode
		} else if !insideCode {
			// match the heading shape ourselves: "## Sources" possibly
			// followed by trailing whitespace.
			if line == "## Sources" || (strings.HasPrefix(line, "## Sources") && strings.TrimSpace(line[len("## Sources"):]) == "") {
				if nl < 0 {
					return len(md)
				}
				return cursor + nl + 1 // position right after the newline
			}
		}
		cursor = lineEnd
	}
	return -1
}

// walkOutsideInlineCode applies fn to every part of seg that is OUTSIDE an
// inline backtick code span, leaving spans verbatim. Used by
// renumberCitationMarkers to avoid renumbering bracketed numerics in
// inline-code samples like `arr[1]`.
func walkOutsideInlineCode(seg string, fn func(string) string) string {
	spans := inlineCodeRe.FindAllStringIndex(seg, -1)
	if len(spans) == 0 {
		return fn(seg)
	}
	var sb strings.Builder
	cursor := 0
	for _, sp := range spans {
		sb.WriteString(fn(seg[cursor:sp[0]]))
		sb.WriteString(seg[sp[0]:sp[1]]) // inline code verbatim
		cursor = sp[1]
	}
	sb.WriteString(fn(seg[cursor:]))
	return sb.String()
}

// walkOutsideCodeFences applies fn to every section of text that's OUTSIDE a
// fenced code block, leaving fenced sections verbatim. Fences are matched on
// their own line (per CommonMark) by codeFenceRe.
//
// If the text has an odd number of fences (unterminated block), everything
// after the last fence is treated as code and left alone.
func walkOutsideCodeFences(text string, fn func(string) string) string {
	fences := codeFenceRe.FindAllStringIndex(text, -1)
	if len(fences) == 0 {
		return fn(text)
	}
	var sb strings.Builder
	cursor := 0
	insideCode := false
	for _, m := range fences {
		// segment from cursor to fence start
		if !insideCode {
			sb.WriteString(fn(text[cursor:m[0]]))
		} else {
			sb.WriteString(text[cursor:m[0]])
		}
		// the fence line itself: write verbatim (it's structural markdown)
		sb.WriteString(text[m[0]:m[1]])
		cursor = m[1]
		insideCode = !insideCode
	}
	// tail after the last fence
	if !insideCode {
		sb.WriteString(fn(text[cursor:]))
	} else {
		// unterminated fence — leave verbatim to avoid corrupting code
		sb.WriteString(text[cursor:])
	}
	return sb.String()
}

// renderSourcesSection produces the Sources block appended to every distilled
// summary file. Format:
//
//	## Sources
//
//	1. [Discussion: 2026-03-10 — Architecture sync][1]
//	2. [sageox/ox#152][2]
//	3. Session: 2026-03-11 — stateless refactor <!-- ref:sessions/2026-03-11T09-00-ryan-Oxabcd -->
//
//	[1]: https://sageox.ai/team/team_xxx/media/recordings/rec_yyy
//	[2]: https://github.com/sageox/ox/pull/152
//
// The numbered list is the human-scannable index. The reference definitions
// underneath let `[anchor][N]` and bare `[N]` markers in the body resolve to
// real links. Entries without a URL render as plain text — still cited, just
// not clickable.
//
// Label-only entries (no URL) get a trailing HTML comment containing the
// citation Key (typically the source_ref like `sessions/<dirname>` or
// `discussions/<dirname>`). Most markdown renderers strip HTML comments, so
// the human reader sees only the label — but parseSourcesSection reads the
// comment back to recover the stable identifier. Without this, two distinct
// label-only sources that happen to share a label (e.g., two short
// discussion titles) would collapse during weekly/monthly merging.
//
// Returns empty string if cites is empty (caller suppresses the section
// entirely in that case).
func renderSourcesSection(cites []citation) string {
	if len(cites) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Sources\n\n")
	// numbered list
	for _, c := range cites {
		if c.URL != "" {
			fmt.Fprintf(&sb, "%d. [%s][%d]\n", c.Num, escapeMarkdownLinkText(c.Label), c.Num)
		} else {
			// label-only: append a hidden ref comment so the parser can
			// recover the stable Key on round-trip. Skip the comment if the
			// Key looks like the synthetic "label:..." form (no useful info).
			if c.Key != "" && !strings.HasPrefix(c.Key, "label:") {
				fmt.Fprintf(&sb, "%d. %s <!-- ref:%s -->\n", c.Num, c.Label, c.Key)
			} else {
				fmt.Fprintf(&sb, "%d. %s\n", c.Num, c.Label)
			}
		}
	}
	// reference link definitions (only for cites with a URL)
	hasURL := false
	for _, c := range cites {
		if c.URL != "" {
			hasURL = true
			break
		}
	}
	if hasURL {
		sb.WriteString("\n")
		for _, c := range cites {
			if c.URL == "" {
				continue
			}
			fmt.Fprintf(&sb, "[%d]: %s\n", c.Num, c.URL)
		}
	}
	return sb.String()
}

// escapeMarkdownLinkText escapes both `[` and `]` so labels containing either
// bracket don't break the markdown link syntax. A label like `Foo [bar]` would
// otherwise turn into `[Foo [bar\]][1]`, which markdown parsers see as a nested
// link and render incorrectly. Escaping both ends keeps the link clickable.
func escapeMarkdownLinkText(s string) string {
	return strings.NewReplacer(
		`[`, `\[`,
		`]`, `\]`,
	).Replace(s)
}

// parseSourcesSection extracts citation entries from a "## Sources" section
// previously written by renderSourcesSection. Returns nil if no Sources
// section is present in the input markdown.
//
// Used by the weekly/monthly distill stages: each layer parses the previous
// layer's Sources, merges them, renumbers, and emits a fresh Sources section.
//
// Tolerant parser: numbered entries with reference-style links are parsed
// fully; bare-text entries (no URL) are parsed with empty URL; malformed
// lines are skipped.
func parseSourcesSection(md string) []citation {
	// locate the "## Sources" line — anchored to a real heading on its own
	// line AND outside any fenced code block. The fence check prevents a
	// body that quotes the format string in a markdown sample from tricking
	// the parser into slicing from inside the fence.
	headerEnd := findSourcesHeadingEnd(md)
	if headerEnd < 0 {
		return nil
	}
	rest := md[headerEnd:]
	if next := nextH2HeadingIndex(rest); next >= 0 {
		rest = rest[:next]
	}

	// collect URL definitions: [N]: url
	urls := make(map[int]string)
	for _, line := range strings.Split(rest, "\n") {
		line = strings.TrimSpace(line)
		if m := refLinkDefRe.FindStringSubmatch(line); m != nil {
			if num, err := strconv.Atoi(m[1]); err == nil {
				urls[num] = m[2]
			}
		}
	}

	// collect numbered list entries: "1. [label][N]" or "1. label"
	// Each line may end with a hidden ` <!-- ref:<key> -->` comment carrying
	// a stable identifier (e.g., source_ref) for label-only entries. Strip
	// it before matching the line so the regexes don't have to know about it.
	var cites []citation
	for _, line := range strings.Split(rest, "\n") {
		line = strings.TrimSpace(line)
		var refKey string
		if m := refCommentRe.FindStringSubmatchIndex(line); m != nil {
			refKey = strings.TrimSpace(line[m[2]:m[3]])
			line = strings.TrimSpace(line[:m[0]])
		}
		if m := numberedRefEntryRe.FindStringSubmatch(line); m != nil {
			num, _ := strconv.Atoi(m[1])
			label := unescapeMarkdownLinkText(m[2])
			refNum, _ := strconv.Atoi(m[3])
			url := urls[refNum]
			// Key priority: hidden ref comment > URL > label fallback. The
			// comment wins when present so a label-only entry that was
			// later upgraded to URL still has a stable Key from its origin.
			key := refKey
			if key == "" {
				key = url
			}
			if key == "" {
				key = "label:" + label
			}
			cites = append(cites, citation{
				Num:   num,
				Label: label,
				URL:   url,
				Key:   key,
			})
			continue
		}
		if m := numberedPlainEntryRe.FindStringSubmatch(line); m != nil {
			num, _ := strconv.Atoi(m[1])
			label := strings.TrimSpace(m[2])
			// Prefer the hidden ref comment as the Key so two distinct
			// label-only sources that share a label don't collapse during
			// cross-layer merging. Falls back to "label:" only when no
			// comment was present (legacy daily files written before this
			// change, or render paths that didn't have a Key).
			key := refKey
			if key == "" {
				key = "label:" + label
			}
			cites = append(cites, citation{
				Num:   num,
				Label: label,
				Key:   key,
			})
		}
	}
	return cites
}

var (
	// refLinkDefRe matches `[N]: url`
	refLinkDefRe = regexp.MustCompile(`^\[(\d+)\]:\s*(\S+)\s*$`)
	// numberedRefEntryRe matches `1. [label][N]`. Label accepts escaped
	// brackets `\[` and `\]` (produced by escapeMarkdownLinkText) plus any
	// non-bracket character. Without the escape branch, a label like
	// `Foo \[bar\]` would terminate the label early at the unescaped close.
	numberedRefEntryRe = regexp.MustCompile(`^(\d+)\.\s+\[((?:\\\[|\\\]|[^\[\]])+)\]\[(\d+)\]\s*$`)
	// numberedPlainEntryRe matches `1. plain text label` (no link)
	numberedPlainEntryRe = regexp.MustCompile(`^(\d+)\.\s+(.+)$`)
	// refCommentRe matches a trailing ` <!-- ref:<key> -->` HTML comment
	// used to carry a stable Key for label-only citation entries across
	// render→parse cycles (CodeRabbit round-2 feedback).
	refCommentRe = regexp.MustCompile(`\s*<!--\s*ref:([^>]*?)\s*-->\s*$`)
)

func unescapeMarkdownLinkText(s string) string {
	return strings.NewReplacer(
		`\[`, `[`,
		`\]`, `]`,
	).Replace(s)
}

// nextH2HeadingIndex returns the byte offset of the next "## " heading in s,
// or -1 if none. Looks for a "##" at the start of a line followed by a space.
func nextH2HeadingIndex(s string) int {
	// search for "\n## " — the simple case. Also handle the edge where the
	// next heading is at the very start of the slice (shouldn't happen since
	// we just consumed "## Sources", but harmless).
	if i := strings.Index(s, "\n## "); i >= 0 {
		return i
	}
	return -1
}

// mergeInput is one (text, citations) pair fed into mergeCitations. Used by
// the weekly and monthly distill stages: each daily/weekly is parsed into
// one of these and the slice is merged into a unified global citation list.
type mergeInput struct {
	Text  string     // the input text — citation markers will be renumbered
	Cites []citation // citations parsed from the input's "## Sources" section
}

// mergeCitations takes a slice of (text, citations) pairs from a previous layer
// and produces:
//   - a slice of rewritten input texts with citation markers renumbered to a
//     unified global numbering
//   - the merged, deduplicated, renumbered global citation list
//
// Dedup is by citation Key (URL if present, else label-synthetic key). Order
// in the merged list is preserved by first appearance.
//
// **Forward-upgrade dedup (synthetic labels only)**: a citation can appear
// in one input as a SYNTHETIC label-only entry (`Key: "label:Foo"`, no URL,
// no stable identifier — the shape produced by pre-#476 daily files) and in
// a later input as URL-keyed (`Key: "https://..."`). When that happens, we
// promote the synthetic entry to use the URL and dedupe under the URL key.
// The old synthetic label key is intentionally retained in keyToGlobalNum
// so the second pass can still renumber the earlier input's bare markers.
//
// We do NOT promote stable-keyed label-only entries (e.g., `Key:
// "discussions/2026-03-10-1000-alice"`) by label match. Two distinct
// discussions can share a title — promoting by label alone would attach a
// URL to the wrong source. Once a citation has a stable key from its
// origin, it stays distinct unless the keys themselves match.
//
// We also do NOT do the reverse direction: a label-only citation that
// appears AFTER a URL-keyed one with the same label is kept as a separate
// entry, because label collisions are real (two PRs titled "Add rate
// limiting" in different repos). The forward direction for synthetic labels
// is only safe because pre-#476 daily files have no URLs OR stable keys at
// all — every synthetic-label entry necessarily predates any URL-keyed
// entry for the same source.
//
// This is the core of the weekly/monthly citation propagation logic. The LLM
// at each layer never has to do arithmetic — it just preserves whatever
// numbers it sees in the input.
func mergeCitations(inputs []mergeInput) ([]string, []citation) {
	// build the merged citation list, first-appearance ordered, deduped by key
	var merged []citation
	keyToGlobalNum := make(map[string]int)
	// syntheticLabelToGlobalNum tracks ONLY entries whose Key is a synthetic
	// "label:..." form (legacy daily files written before #476 added stable
	// keys to label-only citations). These are the only entries safe to
	// upgrade by label match — entries with stable keys are NOT touched
	// because the label alone can't disambiguate between them.
	//
	// Example of why this matters: two distinct discussions with the same
	// title (key=discussions/A and key=discussions/B), then a later URL-backed
	// citation with the same title. We have NO information linking the URL
	// to A vs B, so we must NOT promote either of them by label alone.
	syntheticLabelToGlobalNum := make(map[string]int)
	for _, in := range inputs {
		for _, c := range in.Cites {
			if c.Key == "" {
				c.Key = "label:" + c.Label
			}
			if _, exists := keyToGlobalNum[c.Key]; exists {
				continue
			}
			// Upgrade path: this is a URL-keyed citation, but we previously
			// saw the same label as a SYNTHETIC label-only entry (no stable
			// key). Promote that synthetic entry to use the URL.
			//
			// IMPORTANT: keep the OLD label key in keyToGlobalNum pointing at
			// the same globalNum so that the second pass (renumbering markers
			// in earlier inputs) can still resolve the label-only key. Without
			// this, an earlier input's `[K]` marker for the label-only Foo
			// would silently become a stale number pointing at the wrong cite.
			if c.URL != "" {
				if existingNum, ok := syntheticLabelToGlobalNum[c.Label]; ok {
					existing := &merged[existingNum-1]
					if existing.URL == "" && strings.HasPrefix(existing.Key, "label:") {
						// promote the synthetic label-only entry to use the URL
						existing.URL = c.URL
						existing.Key = c.Key
						keyToGlobalNum[c.Key] = existingNum
						// (old synthetic label key intentionally retained)
						// Drop the label→num index now that the synthetic
						// slot has been consumed; future URL entries with
						// the same label must NOT promote it again.
						delete(syntheticLabelToGlobalNum, c.Label)
						continue
					}
				}
			}
			globalNum := len(merged) + 1
			keyToGlobalNum[c.Key] = globalNum
			// Only synthetic label-only entries are eligible for future
			// label-based promotion. Stable-keyed and URL-keyed entries are
			// NOT recorded here.
			if strings.HasPrefix(c.Key, "label:") && c.URL == "" {
				syntheticLabelToGlobalNum[c.Label] = globalNum
			}
			merged = append(merged, citation{
				Num:   globalNum,
				Label: c.Label,
				URL:   c.URL,
				Key:   c.Key,
			})
		}
	}

	// rewrite each input's markers from local-to-input numbers to global numbers
	rewritten := make([]string, len(inputs))
	for i, in := range inputs {
		oldToNew := make(map[int]int, len(in.Cites))
		for _, c := range in.Cites {
			key := c.Key
			if key == "" {
				key = "label:" + c.Label
			}
			if global, ok := keyToGlobalNum[key]; ok {
				oldToNew[c.Num] = global
			}
		}
		rewritten[i] = renumberCitationMarkers(in.Text, oldToNew)
	}
	return rewritten, merged
}
