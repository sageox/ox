package main

import (
	"strings"
	"testing"

	"github.com/sageox/ox/internal/facts"
)

// --- A. Label generation ---

// TestCitationLabel covers the label format for every source type.
// Failure prevented: distilled summaries cite sources with ugly URLs or empty labels.
func TestCitationLabel(t *testing.T) {
	tests := []struct {
		name string
		fact facts.Fact
		want string
	}{
		{
			name: "discussion with title and date",
			fact: facts.Fact{
				SourceType:  facts.SourceDiscussion,
				SourceTitle: "Architecture sync",
				Timestamp:   "2026-03-10T14:23:00Z",
			},
			want: "Discussion: 2026-03-10 — Architecture sync",
		},
		{
			name: "discussion without title falls back to dirname",
			fact: facts.Fact{
				SourceType: facts.SourceDiscussion,
				SourceRef:  "discussions/2026-03-10-1423-ryan",
				Timestamp:  "2026-03-10T14:23:00Z",
			},
			want: "Discussion: 2026-03-10 — ryan",
		},
		{
			name: "session with title and date",
			fact: facts.Fact{
				SourceType:  facts.SourceSession,
				SourceTitle: "stateless refactor",
				Timestamp:   "2026-03-11T09:00:00Z",
			},
			want: "Session: 2026-03-11 — stateless refactor",
		},
		{
			name: "session without title falls back to dirname who-id",
			fact: facts.Fact{
				SourceType: facts.SourceSession,
				SourceRef:  "sessions/2026-01-06T14-32-ryan-Ox7f3a",
				Timestamp:  "2026-01-06T14:32:00Z",
			},
			want: "Session: 2026-01-06 — ryan-Ox7f3a",
		},
		{
			name: "github PR with URL",
			fact: facts.Fact{
				SourceType: facts.SourceGitHub,
				SourceRef:  "https://github.com/sageox/ox/pull/152",
				SourceURL:  "https://github.com/sageox/ox/pull/152",
			},
			want: "sageox/ox#152",
		},
		{
			name: "github issue with URL",
			fact: facts.Fact{
				SourceType: facts.SourceGitHub,
				SourceRef:  "https://github.com/sageox/ox/issues/476",
				SourceURL:  "https://github.com/sageox/ox/issues/476",
			},
			want: "sageox/ox#476",
		},
		{
			name: "github commit shortens sha",
			fact: facts.Fact{
				SourceType: facts.SourceGitHub,
				SourceURL:  "https://github.com/sageox/ox/commit/a1b2c3d4e5f6789",
			},
			want: "sageox/ox@a1b2c3d",
		},
		{
			name: "github fallback to title when URL unparseable",
			fact: facts.Fact{
				SourceType:  facts.SourceGitHub,
				SourceURL:   "not a url",
				SourceTitle: "Add rate limiting",
			},
			want: "Add rate limiting",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := citationLabel(tt.fact)
			if got != tt.want {
				t.Errorf("citationLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- B. Citation list construction ---

// TestBuildCitationsFromFacts_DedupesByURL ensures multiple facts pointing at
// the same source produce a single citation entry.
// Failure prevented: a discussion with 5 facts produces 5 duplicate citations,
// inflating the Sources section.
func TestBuildCitationsFromFacts_DedupesByURL(t *testing.T) {
	url := "https://sageox.ai/team/team_x/media/recordings/rec_y"
	fs := []facts.Fact{
		{Headline: "fact 1", SourceType: facts.SourceDiscussion, SourceURL: url, SourceTitle: "Sync", Timestamp: "2026-03-10T10:00:00Z"},
		{Headline: "fact 2", SourceType: facts.SourceDiscussion, SourceURL: url, SourceTitle: "Sync", Timestamp: "2026-03-10T10:00:00Z"},
		{Headline: "fact 3", SourceType: facts.SourceDiscussion, SourceURL: url, SourceTitle: "Sync", Timestamp: "2026-03-10T10:00:00Z"},
	}
	cites, factToNum := buildCitationsFromFacts(fs)
	if len(cites) != 1 {
		t.Fatalf("expected 1 citation after dedup, got %d", len(cites))
	}
	if cites[0].URL != url {
		t.Errorf("citation URL = %q, want %q", cites[0].URL, url)
	}
	for i := 0; i < 3; i++ {
		if factToNum[i] != 1 {
			t.Errorf("fact[%d] mapped to %d, want 1", i, factToNum[i])
		}
	}
}

// TestBuildCitationsFromFacts_OrderingByTimestamp verifies stable chronological order.
// Failure prevented: citation numbering jumps around between runs.
func TestBuildCitationsFromFacts_OrderingByTimestamp(t *testing.T) {
	fs := []facts.Fact{
		{SourceType: facts.SourceDiscussion, SourceURL: "url-c", SourceTitle: "C", Timestamp: "2026-03-12T00:00:00Z"},
		{SourceType: facts.SourceDiscussion, SourceURL: "url-a", SourceTitle: "A", Timestamp: "2026-03-10T00:00:00Z"},
		{SourceType: facts.SourceDiscussion, SourceURL: "url-b", SourceTitle: "B", Timestamp: "2026-03-11T00:00:00Z"},
	}
	cites, _ := buildCitationsFromFacts(fs)
	if len(cites) != 3 {
		t.Fatalf("got %d citations", len(cites))
	}
	if cites[0].URL != "url-a" || cites[1].URL != "url-b" || cites[2].URL != "url-c" {
		t.Errorf("ordering wrong: %v", []string{cites[0].URL, cites[1].URL, cites[2].URL})
	}
}

// TestBuildCitationsFromFacts_FallsBackToSourceRef ensures facts without a URL
// still get cited (just without a clickable link).
// Failure prevented: legacy fact files (no source_url) silently dropped from Sources.
func TestBuildCitationsFromFacts_FallsBackToSourceRef(t *testing.T) {
	fs := []facts.Fact{
		{SourceType: facts.SourceDiscussion, SourceRef: "discussions/2026-03-10-1423-ryan", Timestamp: "2026-03-10T00:00:00Z"},
	}
	cites, factToNum := buildCitationsFromFacts(fs)
	if len(cites) != 1 {
		t.Fatalf("expected 1 citation, got %d", len(cites))
	}
	if cites[0].URL != "" {
		t.Errorf("expected empty URL for ref-only fact, got %q", cites[0].URL)
	}
	if cites[0].Label == "" {
		t.Errorf("expected non-empty label even without URL")
	}
	if factToNum[0] != 1 {
		t.Errorf("fact[0] mapped to %d, want 1", factToNum[0])
	}
}

// TestBuildCitationsFromFacts_SkipsUncitableFacts ensures facts with no source
// info at all are skipped (rather than producing an empty citation).
// Failure prevented: a Sources section entry "1." with no label.
func TestBuildCitationsFromFacts_SkipsUncitableFacts(t *testing.T) {
	fs := []facts.Fact{
		{SourceType: facts.SourceObservation}, // no URL, no ref — uncitable
		{SourceType: facts.SourceDiscussion, SourceURL: "url-a", SourceTitle: "A", Timestamp: "2026-03-10T00:00:00Z"},
	}
	cites, factToNum := buildCitationsFromFacts(fs)
	if len(cites) != 1 {
		t.Fatalf("expected 1 citation, got %d", len(cites))
	}
	if _, ok := factToNum[0]; ok {
		t.Errorf("uncitable fact[0] should not be in factToNum, but got %d", factToNum[0])
	}
	if factToNum[1] != 1 {
		t.Errorf("fact[1] should map to 1, got %d", factToNum[1])
	}
}

// --- C. Marker renumbering ---

// TestRenumberCitationMarkers covers all the marker shapes the LLM emits.
// Failure prevented: weekly summary inherits stale per-day numbers and the
// reference definitions don't resolve.
func TestRenumberCitationMarkers(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		oldToNew map[int]int
		want     string
	}{
		{
			name:     "bare marker rewritten",
			text:     "The team chose Postgres. [3]",
			oldToNew: map[int]int{3: 7},
			want:     "The team chose Postgres. [7]",
		},
		{
			name:     "ref-link tail rewritten",
			text:     "We merged [PR #152][2] last week.",
			oldToNew: map[int]int{2: 9},
			want:     "We merged [PR #152][9] last week.",
		},
		{
			name:     "chained markers both rewritten",
			text:     "Adopted rate limiting [1][3] after debate.",
			oldToNew: map[int]int{1: 4, 3: 6},
			want:     "Adopted rate limiting [4][6] after debate.",
		},
		{
			name:     "unknown number left untouched (defensive)",
			text:     "Strange marker [99] in body.",
			oldToNew: map[int]int{1: 2},
			want:     "Strange marker [99] in body.",
		},
		{
			name:     "no markers at all",
			text:     "Just plain prose.",
			oldToNew: map[int]int{1: 2},
			want:     "Just plain prose.",
		},
		{
			name:     "empty map leaves text unchanged",
			text:     "Has [1] marker.",
			oldToNew: map[int]int{},
			want:     "Has [1] marker.",
		},
		{
			name:     "multiple markers in one line",
			text:     "First [1] middle [2] last [3].",
			oldToNew: map[int]int{1: 10, 2: 20, 3: 30},
			want:     "First [10] middle [20] last [30].",
		},
		{
			// gh #476 review finding: don't corrupt array indices in code samples.
			name:     "code fence content untouched",
			text:     "Body [1].\n\n```go\narr[1] = arr[2]\n```\n\nMore [1].",
			oldToNew: map[int]int{1: 9, 2: 8},
			want:     "Body [9].\n\n```go\narr[1] = arr[2]\n```\n\nMore [9].",
		},
		{
			name:     "multiple code fences with markers in between",
			text:     "Pre [1].\n```\nx[2]\n```\nMid [1].\n```py\ny[2]\n```\nPost [1].",
			oldToNew: map[int]int{1: 5, 2: 6},
			want:     "Pre [5].\n```\nx[2]\n```\nMid [5].\n```py\ny[2]\n```\nPost [5].",
		},
		{
			// defensive: an unterminated fence (should not happen in distilled
			// output, but markdown does allow it) — everything after the lone
			// fence is treated as code and left alone.
			name:     "unterminated code fence treats tail as code",
			text:     "Pre [1].\n```\ncode [1] tail",
			oldToNew: map[int]int{1: 7},
			want:     "Pre [7].\n```\ncode [1] tail",
		},
		{
			// gh #476 review round-2: inline backtick spans must also be
			// protected so prose like `arr[1]` doesn't get renumbered.
			name:     "inline backtick code span untouched",
			text:     "The lookup `arr[1]` is fine but cite [1] should renumber.",
			oldToNew: map[int]int{1: 4},
			want:     "The lookup `arr[1]` is fine but cite [4] should renumber.",
		},
		{
			name:     "multiple inline code spans on one line",
			text:     "See `args[2]` and `argv[1]`, then [1] [2].",
			oldToNew: map[int]int{1: 5, 2: 6},
			want:     "See `args[2]` and `argv[1]`, then [5] [6].",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renumberCitationMarkers(tt.text, tt.oldToNew)
			if got != tt.want {
				t.Errorf("renumberCitationMarkers() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- D. Sources section round-trip ---

// TestSourcesSectionRoundTrip verifies that render → parse produces the same
// citation list back. This is the contract weekly/monthly distillation relies on.
// Failure prevented: weekly stage cannot read the Sources section it needs to merge.
func TestSourcesSectionRoundTrip(t *testing.T) {
	cites := []citation{
		{Num: 1, Label: "Discussion: 2026-03-10 — Architecture sync", URL: "https://sageox.ai/team/t/media/recordings/rec_a", Key: "https://sageox.ai/team/t/media/recordings/rec_a"},
		{Num: 2, Label: "sageox/ox#152", URL: "https://github.com/sageox/ox/pull/152", Key: "https://github.com/sageox/ox/pull/152"},
		{Num: 3, Label: "Session: 2026-03-11 — refactor", URL: "https://sageox.ai/repo/r/sessions/s/view", Key: "https://sageox.ai/repo/r/sessions/s/view"},
	}
	rendered := renderSourcesSection(cites)
	if rendered == "" {
		t.Fatal("expected non-empty rendered section")
	}
	// wrap in a fake document to test header detection
	doc := "# Daily Memory — 2026-03-10\n\nBody text.\n\n" + rendered
	parsed := parseSourcesSection(doc)
	if len(parsed) != len(cites) {
		t.Fatalf("round-trip lost entries: got %d, want %d", len(parsed), len(cites))
	}
	for i, want := range cites {
		if parsed[i].Num != want.Num {
			t.Errorf("entry %d: Num = %d, want %d", i, parsed[i].Num, want.Num)
		}
		if parsed[i].Label != want.Label {
			t.Errorf("entry %d: Label = %q, want %q", i, parsed[i].Label, want.Label)
		}
		if parsed[i].URL != want.URL {
			t.Errorf("entry %d: URL = %q, want %q", i, parsed[i].URL, want.URL)
		}
	}
}

// TestParseSourcesSection_AbsentReturnsNil — defensive: weekly distill on a
// daily file with no Sources section must not panic.
func TestParseSourcesSection_AbsentReturnsNil(t *testing.T) {
	got := parseSourcesSection("# Daily Memory\n\nJust prose, no Sources section.\n")
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

// TestParseSourcesSection_StopsAtNextHeading ensures we don't grab content
// from a section after Sources (e.g., a future appendix).
func TestParseSourcesSection_StopsAtNextHeading(t *testing.T) {
	doc := `# Daily

## Sources

1. [Foo][1]

[1]: https://example.com

## Appendix

2. [Bar][2]

[2]: https://other.example.com
`
	cites := parseSourcesSection(doc)
	if len(cites) != 1 {
		t.Fatalf("expected 1 citation (Appendix excluded), got %d", len(cites))
	}
	if cites[0].Label != "Foo" {
		t.Errorf("got label %q, want Foo", cites[0].Label)
	}
}

// TestRenderSourcesSection_NoURLsRendersPlain ensures label-only entries
// (legacy facts without URLs) still appear in the section.
// Failure prevented: legacy data silently disappears from Sources.
func TestRenderSourcesSection_NoURLsRendersPlain(t *testing.T) {
	cites := []citation{
		{Num: 1, Label: "Discussion: legacy", Key: "discussions/legacy"},
	}
	out := renderSourcesSection(cites)
	if !strings.Contains(out, "1. Discussion: legacy") {
		t.Errorf("expected plain numbered entry, got:\n%s", out)
	}
	if strings.Contains(out, "[1]:") {
		t.Errorf("did not expect ref definitions for label-only entries:\n%s", out)
	}
}

// TestRenderSourcesSection_LabelEscapesBracket ensures labels containing `]`
// don't break the markdown link syntax.
func TestRenderSourcesSection_LabelEscapesBracket(t *testing.T) {
	cites := []citation{
		{Num: 1, Label: "Discussion: brackets [in title]", URL: "https://example.com/x", Key: "https://example.com/x"},
	}
	out := renderSourcesSection(cites)
	if !strings.Contains(out, `\[in title\]`) {
		t.Errorf("expected both brackets escaped in label:\n%s", out)
	}
}

// TestSourcesSectionRoundTrip_LabelsWithBrackets — CodeRabbit #1: a label
// like `Foo [bar]` must round-trip cleanly. Previously only `]` was escaped,
// which produced `[Foo [bar\]][1]` — broken markdown that parsers see as a
// nested link. Now both ends are escaped.
//
// Failure prevented: PR/discussion titles containing brackets render as
// non-clickable link fragments in the Sources section.
func TestSourcesSectionRoundTrip_LabelsWithBrackets(t *testing.T) {
	cites := []citation{
		{Num: 1, Label: "Foo [bar]", URL: "https://example.com/foo", Key: "https://example.com/foo"},
		{Num: 2, Label: "Has [both] brackets", URL: "https://example.com/b", Key: "https://example.com/b"},
		{Num: 3, Label: "Trailing ]", URL: "https://example.com/t", Key: "https://example.com/t"},
		{Num: 4, Label: "Leading [", URL: "https://example.com/l", Key: "https://example.com/l"},
	}
	rendered := renderSourcesSection(cites)
	doc := "# Daily\n\n" + rendered
	parsed := parseSourcesSection(doc)
	if len(parsed) != len(cites) {
		t.Fatalf("round-trip lost entries: got %d, want %d", len(parsed), len(cites))
	}
	for i, want := range cites {
		if parsed[i].Label != want.Label {
			t.Errorf("entry %d: Label = %q, want %q", i, parsed[i].Label, want.Label)
		}
		if parsed[i].URL != want.URL {
			t.Errorf("entry %d: URL = %q, want %q", i, parsed[i].URL, want.URL)
		}
	}
}

// TestCitationDate_PreservesSourceLocalDay — CodeRabbit round-4: citationDate
// must return the SOURCE-LOCAL calendar day, not the UTC day. For an offset
// timestamp near midnight, formatting after parseFactTimestamp's UTC
// normalization shifts the displayed day forward — wrong for human labels.
//
// Failure prevented: a discussion recorded at "2026-03-10 23:30 PST" appears
// in the daily summary as "Discussion: 2026-03-11" instead of "2026-03-10".
func TestCitationDate_PreservesSourceLocalDay(t *testing.T) {
	tests := []struct {
		name string
		ts   string
		want string
	}{
		{"PST near midnight stays on local day", "2026-03-10T23:30:00-08:00", "2026-03-10"},
		{"EST near midnight stays on local day", "2026-03-10T23:30:00-05:00", "2026-03-10"},
		{"UTC midday", "2026-03-10T12:00:00Z", "2026-03-10"},
		{"date-only string", "2026-03-10", "2026-03-10"},
		{"empty input", "", ""},
		{"unparseable input", "not a date", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := citationDate(tt.ts)
			if got != tt.want {
				t.Errorf("citationDate(%q) = %q, want %q", tt.ts, got, tt.want)
			}
		})
	}
}

// TestGithubShortLabel_FallbackChain — CodeRabbit round-4: documents the
// full fallback chain so that a citation NEVER renders with an empty label,
// and an unparseable github URL still produces something readable.
func TestGithubShortLabel_FallbackChain(t *testing.T) {
	tests := []struct {
		name        string
		url, ref, t string
		want        string
	}{
		{"parseable PR url", "https://github.com/sageox/ox/pull/486", "", "", "sageox/ox#486"},
		{"unparseable url falls back to title", "not a url", "", "Add rate limiting", "Add rate limiting"},
		{"unparseable url falls back to ref", "not a url", "ref-only", "", "ref-only"},
		{"unparseable url falls back to url itself when ref empty", "https://example.com/weird", "", "", "https://example.com/weird"},
		{"all empty falls back to GitHub", "", "", "", "GitHub"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := githubShortLabel(tt.url, tt.ref, tt.t)
			if got != tt.want {
				t.Errorf("githubShortLabel(%q, %q, %q) = %q, want %q", tt.url, tt.ref, tt.t, got, tt.want)
			}
		})
	}
}

// TestBuildCitationsFromFacts_OrdersByParsedTimeNotLexical — CodeRabbit
// round-3: timestamps with different RFC3339 offsets must sort by their
// real chronological instant, not by their raw string form. The two facts
// below represent the same wall-clock day but at different offsets — the
// EAST coast 9am instant happens BEFORE the west coast 9am instant.
//
// Failure prevented: distilled summaries renumber multi-region facts in the
// wrong order, breaking the chronological narrative the LLM relies on.
func TestBuildCitationsFromFacts_OrdersByParsedTimeNotLexical(t *testing.T) {
	// Both timestamps lexically: "2026-03-10T09:00:00-05:00" sorts AFTER
	// "2026-03-10T09:00:00-08:00" because "-05:00" > "-08:00" string-wise.
	// But chronologically, -05:00 9am is 14:00 UTC and -08:00 9am is 17:00 UTC,
	// so the -05:00 fact comes FIRST.
	fs := []facts.Fact{
		{
			Headline:   "west coast",
			SourceType: facts.SourceDiscussion,
			SourceURL:  "url-west",
			Timestamp:  "2026-03-10T09:00:00-08:00", // 17:00 UTC — should be #2
		},
		{
			Headline:   "east coast",
			SourceType: facts.SourceDiscussion,
			SourceURL:  "url-east",
			Timestamp:  "2026-03-10T09:00:00-05:00", // 14:00 UTC — should be #1
		},
	}
	cites, _ := buildCitationsFromFacts(fs)
	if len(cites) != 2 {
		t.Fatalf("expected 2 citations, got %d", len(cites))
	}
	if cites[0].URL != "url-east" {
		t.Errorf("first citation should be east coast (earlier UTC instant); got URL=%q", cites[0].URL)
	}
	if cites[1].URL != "url-west" {
		t.Errorf("second citation should be west coast; got URL=%q", cites[1].URL)
	}
}

// TestParseSourcesSection_IgnoresHeadingInsideCodeFence — CodeRabbit round-3:
// a body containing a markdown sample with `## Sources` inside a fenced code
// block must not trick the parser. Only headings OUTSIDE fences count.
//
// Failure prevented: agent guidance documenting the distilled format includes
// a code sample showing `## Sources`. parseSourcesSection slices from inside
// the fence, misses the real Sources section, and weekly merges silently
// drop all citations from that daily.
func TestParseSourcesSection_IgnoresHeadingInsideCodeFence(t *testing.T) {
	doc := "# Daily\n\n" +
		"## What We Learned\n\n" +
		"Here is what a Sources section looks like:\n\n" +
		"```markdown\n" +
		"## Sources\n\n" +
		"1. [Fake citation in code sample][1]\n\n" +
		"[1]: https://fake.example.com\n" +
		"```\n\n" +
		"## Sources\n\n" +
		"1. [Real citation][1]\n\n" +
		"[1]: https://real.example.com\n"
	cites := parseSourcesSection(doc)
	if len(cites) != 1 {
		t.Fatalf("expected 1 real citation, got %d: %+v", len(cites), cites)
	}
	if cites[0].URL != "https://real.example.com" {
		t.Errorf("parser sliced from code fence, got URL %q", cites[0].URL)
	}
	if cites[0].Label != "Real citation" {
		t.Errorf("parser picked up fake label, got %q", cites[0].Label)
	}
}

// TestSourcesSection_LabelOnlyKeyRoundTrip — CodeRabbit round-2 #6: label-only
// citations with distinct stable Keys (e.g., source_ref) must round-trip
// through render → parse without collapsing. The hidden ` <!-- ref:... -->`
// HTML comment carries the Key. Without this, two label-only sources sharing
// a label would silently merge during weekly/monthly distillation.
//
// Failure prevented: legacy / audio-only discussions with similar titles get
// merged into a single citation, losing one of the sources from the weekly.
func TestSourcesSection_LabelOnlyKeyRoundTrip(t *testing.T) {
	cites := []citation{
		{Num: 1, Label: "Discussion: 2026-03-10 — Sync", Key: "discussions/2026-03-10-1000-alice"},
		{Num: 2, Label: "Discussion: 2026-03-10 — Sync", Key: "discussions/2026-03-10-1500-bob"}, // SAME label, DIFFERENT source
	}
	rendered := renderSourcesSection(cites)
	if !strings.Contains(rendered, "<!-- ref:discussions/2026-03-10-1000-alice -->") {
		t.Errorf("expected ref comment for first cite:\n%s", rendered)
	}
	if !strings.Contains(rendered, "<!-- ref:discussions/2026-03-10-1500-bob -->") {
		t.Errorf("expected ref comment for second cite:\n%s", rendered)
	}

	// Round-trip: parse should preserve the distinct keys
	parsed := parseSourcesSection("# Daily\n\n" + rendered)
	if len(parsed) != 2 {
		t.Fatalf("expected 2 parsed citations, got %d: %+v", len(parsed), parsed)
	}
	if parsed[0].Key != "discussions/2026-03-10-1000-alice" {
		t.Errorf("parsed[0].Key = %q, want %q", parsed[0].Key, "discussions/2026-03-10-1000-alice")
	}
	if parsed[1].Key != "discussions/2026-03-10-1500-bob" {
		t.Errorf("parsed[1].Key = %q, want %q", parsed[1].Key, "discussions/2026-03-10-1500-bob")
	}

	// Cross-layer merge: these two distinct sources must NOT collapse
	_, merged := mergeCitations([]mergeInput{
		{Text: "x [1] [2]", Cites: parsed},
	})
	if len(merged) != 2 {
		t.Errorf("two distinct label-only sources collapsed during merge: got %d, want 2", len(merged))
	}
}

// TestParseSourcesSection_LegacyDailyWithoutRefComment — backwards compat:
// daily files written before the ref-comment change MUST still parse. Their
// label-only entries fall back to "label:" synthetic keys (and may collapse
// across distinct sources sharing a label, but that's a transient issue
// resolved on next regeneration).
func TestParseSourcesSection_LegacyDailyWithoutRefComment(t *testing.T) {
	doc := `# Daily

## Sources

1. Discussion: legacy entry
2. Session: another legacy
`
	parsed := parseSourcesSection(doc)
	if len(parsed) != 2 {
		t.Fatalf("expected 2 parsed citations, got %d", len(parsed))
	}
	if !strings.HasPrefix(parsed[0].Key, "label:") {
		t.Errorf("legacy entry should have label: key, got %q", parsed[0].Key)
	}
}

// TestParseSourcesSection_IgnoresBodyMentionOfHeading — CodeRabbit #2: a body
// that mentions "## Sources" in prose or a code sample (e.g., agent guidance
// describing the format) must not trick the parser into slicing the wrong
// region. Only a real H2 heading on its own line counts.
//
// Failure prevented: distill of a daily that quotes the format string itself
// produces a Sources section parsed from the wrong file region — citations
// silently disappear from weekly merges.
func TestParseSourcesSection_IgnoresBodyMentionOfHeading(t *testing.T) {
	doc := `# Daily Memory

## What We Learned

The format guide says "the file ends with ## Sources, like this:".

## Sources

1. [Real citation][1]

[1]: https://example.com/real
`
	cites := parseSourcesSection(doc)
	if len(cites) != 1 {
		t.Fatalf("expected 1 citation from real Sources section, got %d: %+v", len(cites), cites)
	}
	if cites[0].URL != "https://example.com/real" {
		t.Errorf("parser sliced from wrong location, got URL %q", cites[0].URL)
	}
}

// --- E. Multi-layer citation propagation ---

// TestMergeCitations_DedupesAcrossLayers is the core property the weekly /
// monthly stages rely on: a single source cited in multiple inputs ends up
// as ONE entry in the merged list, with all input markers rewritten to the
// same global number.
//
// Failure prevented: weekly summary has duplicate entries because day1 and
// day3 used different per-day numbering for the same discussion.
func TestMergeCitations_DedupesAcrossLayers(t *testing.T) {
	// day 1: discussion is [2]
	day1Text := "Architecture decision was made [2]."
	day1Cites := []citation{
		{Num: 1, Label: "Other", URL: "https://example.com/other", Key: "https://example.com/other"},
		{Num: 2, Label: "The discussion", URL: "https://example.com/disc", Key: "https://example.com/disc"},
	}
	// day 3: same discussion is [1]
	day3Text := "Following the [discussion][1], we shipped."
	day3Cites := []citation{
		{Num: 1, Label: "The discussion", URL: "https://example.com/disc", Key: "https://example.com/disc"},
	}

	rewritten, merged := mergeCitations([]mergeInput{
		{Text: day1Text, Cites: day1Cites},
		{Text: day3Text, Cites: day3Cites},
	})

	// expect exactly 2 unique citations (Other + The discussion)
	if len(merged) != 2 {
		t.Fatalf("expected 2 merged citations, got %d: %+v", len(merged), merged)
	}

	// "The discussion" should have ONE global number across both inputs
	var discNum int
	for _, c := range merged {
		if c.Label == "The discussion" {
			discNum = c.Num
		}
	}
	if discNum == 0 {
		t.Fatal("'The discussion' not found in merged list")
	}

	// both inputs should reference that global number
	day1Want := "Architecture decision was made [" + itoa(discNum) + "]."
	if rewritten[0] != day1Want {
		t.Errorf("day1 rewrite:\n got: %q\nwant: %q", rewritten[0], day1Want)
	}
	day3Want := "Following the [discussion][" + itoa(discNum) + "], we shipped."
	if rewritten[1] != day3Want {
		t.Errorf("day3 rewrite:\n got: %q\nwant: %q", rewritten[1], day3Want)
	}
}

// TestMergeCitations_UpgradesLabelOnlyToURLKeyed simulates a mid-release
// upgrade where day1 was distilled BEFORE #476 added source URLs (citation
// stored as label-only) and day2 was distilled AFTER (same source now has a
// URL). The merged weekly should:
//  1. Dedupe to ONE entry, not two
//  2. Use the URL so the merged Sources section is clickable
//  3. Renumber day1's bare marker to the unified global number, not leave
//     it pointing at a stale slot
//
// Failure prevented: weekly Sources sections produced during a release
// upgrade have duplicate Foo entries, OR earlier daily prose markers point
// at wrong/stale citations after merge.
func TestMergeCitations_UpgradesLabelOnlyToURLKeyed(t *testing.T) {
	// day1: 3 leading citations then Foo as label-only
	day1Text := "Y [1] Z [2] W [3] then Foo [4]."
	day1Cites := []citation{
		{Num: 1, Label: "Y", URL: "url:y", Key: "url:y"},
		{Num: 2, Label: "Z", URL: "url:z", Key: "url:z"},
		{Num: 3, Label: "W", URL: "url:w", Key: "url:w"},
		{Num: 4, Label: "Foo", Key: "label:Foo"}, // legacy: no URL
	}
	// day2: Foo NOW has a URL — should upgrade day1's label-only entry
	day2Text := "Foo [1]."
	day2Cites := []citation{
		{Num: 1, Label: "Foo", URL: "https://example.com/foo", Key: "https://example.com/foo"},
	}

	rewritten, merged := mergeCitations([]mergeInput{
		{Text: day1Text, Cites: day1Cites},
		{Text: day2Text, Cites: day2Cites},
	})

	// 1. Exactly 4 unique merged citations (dedupe Foo, not 5)
	if len(merged) != 4 {
		t.Fatalf("expected 4 merged citations after upgrade dedupe, got %d: %+v", len(merged), merged)
	}

	// 2. Foo's merged entry has the URL from day2
	var fooEntry *citation
	for i := range merged {
		if merged[i].Label == "Foo" {
			fooEntry = &merged[i]
		}
	}
	if fooEntry == nil {
		t.Fatal("Foo not in merged list")
	}
	if fooEntry.URL != "https://example.com/foo" {
		t.Errorf("Foo URL not upgraded: got %q, want %q", fooEntry.URL, "https://example.com/foo")
	}
	if fooEntry.Num != 4 {
		t.Errorf("Foo globalNum should stay at 4 after upgrade, got %d", fooEntry.Num)
	}

	// 3. Day1's [4] marker should still resolve to Foo's global number (4)
	if rewritten[0] != "Y [1] Z [2] W [3] then Foo [4]." {
		t.Errorf("day1 rewrite wrong:\n got: %q\nwant: %q", rewritten[0], "Y [1] Z [2] W [3] then Foo [4].")
	}
	// Day2's [1] marker should be renumbered to 4
	if rewritten[1] != "Foo [4]." {
		t.Errorf("day2 rewrite wrong:\n got: %q\nwant: %q", rewritten[1], "Foo [4].")
	}
}

// TestMergeCitations_DoesNotDedupeDifferentURLsWithSameLabel verifies the
// upgrade doesn't accidentally merge two genuinely different sources just
// because they share a label. Two PRs with the same title in different repos
// MUST stay distinct.
func TestMergeCitations_DoesNotDedupeDifferentURLsWithSameLabel(t *testing.T) {
	inputs := []mergeInput{
		{Text: "x [1]", Cites: []citation{{Num: 1, Label: "Add rate limiting", URL: "https://github.com/a/r/pull/1", Key: "https://github.com/a/r/pull/1"}}},
		{Text: "y [1]", Cites: []citation{{Num: 1, Label: "Add rate limiting", URL: "https://github.com/b/r/pull/1", Key: "https://github.com/b/r/pull/1"}}},
	}
	_, merged := mergeCitations(inputs)
	if len(merged) != 2 {
		t.Fatalf("two distinct URLs with same label should stay separate, got %d: %+v", len(merged), merged)
	}
}

// TestMergeCitations_DoesNotUpgradeStableKeyedByLabelAlone — CodeRabbit
// round-5: after #476 added stable keys to label-only citations (the hidden
// `<!-- ref:... -->` comment carries source_ref), promoting any
// label-matching entry by label alone is wrong. Two distinct discussions
// with the same title (different stable keys) followed by a URL-backed
// citation with the same title MUST stay as three distinct entries — we
// have no information linking the URL to either of the two stable keys.
//
// Failure prevented: a third-party URL silently attaches to whichever
// label-only source happened to be processed last, corrupting that
// citation's link target during weekly/monthly merges.
func TestMergeCitations_DoesNotUpgradeStableKeyedByLabelAlone(t *testing.T) {
	inputs := []mergeInput{
		{
			Text: "x [1]",
			Cites: []citation{
				{Num: 1, Label: "Foo", Key: "discussions/2026-03-10-1000-alice"}, // stable, no URL
			},
		},
		{
			Text: "y [1]",
			Cites: []citation{
				{Num: 1, Label: "Foo", Key: "discussions/2026-03-10-1500-bob"}, // stable, no URL
			},
		},
		{
			Text: "z [1]",
			Cites: []citation{
				{Num: 1, Label: "Foo", URL: "https://example.com/foo", Key: "https://example.com/foo"},
			},
		},
	}
	_, merged := mergeCitations(inputs)
	if len(merged) != 3 {
		t.Fatalf("expected 3 distinct merged citations (two stable label-only + one URL), got %d: %+v", len(merged), merged)
	}

	// Specifically: Alice's entry should NOT have been upgraded to the URL
	// (we can't know whether it's the right Foo).
	for _, c := range merged {
		if c.Key == "discussions/2026-03-10-1000-alice" && c.URL != "" {
			t.Errorf("Alice's stable-keyed entry was wrongly upgraded with URL %q", c.URL)
		}
		if c.Key == "discussions/2026-03-10-1500-bob" && c.URL != "" {
			t.Errorf("Bob's stable-keyed entry was wrongly upgraded with URL %q", c.URL)
		}
	}
}

// TestMergeCitations_SyntheticLabelOnlyOnceUpgraded — defensive: even with
// the synthetic-label restriction, once a synthetic entry has been upgraded
// it must NOT be upgraded a second time by a different URL with the same
// label. The second URL must become its own separate entry.
func TestMergeCitations_SyntheticLabelOnlyOnceUpgraded(t *testing.T) {
	inputs := []mergeInput{
		{Text: "a [1]", Cites: []citation{{Num: 1, Label: "Foo", Key: "label:Foo"}}}, // synthetic, no URL
		{Text: "b [1]", Cites: []citation{{Num: 1, Label: "Foo", URL: "https://example.com/first", Key: "https://example.com/first"}}},
		{Text: "c [1]", Cites: []citation{{Num: 1, Label: "Foo", URL: "https://example.com/second", Key: "https://example.com/second"}}},
	}
	_, merged := mergeCitations(inputs)
	if len(merged) != 2 {
		t.Fatalf("expected 2 merged citations (synthetic upgraded to first URL, second URL distinct), got %d: %+v", len(merged), merged)
	}
	// merged[0] = upgraded synthetic → first URL
	if merged[0].URL != "https://example.com/first" {
		t.Errorf("first merged URL = %q, want %q", merged[0].URL, "https://example.com/first")
	}
	// merged[1] = second URL, separate entry
	if merged[1].URL != "https://example.com/second" {
		t.Errorf("second merged URL = %q, want %q", merged[1].URL, "https://example.com/second")
	}
}

// TestMergeCitations_DoesNotMergeReverseDirection pins the deliberate
// asymmetry: when a URL-keyed entry comes FIRST and a label-only entry with
// the same label comes SECOND, they stay distinct. We can't safely assume
// the label-only is the same source — label collisions are real (two PRs
// titled identically in different repos). The forward direction is only
// safe because pre-#476 fact files have no URLs at all, so a label-only
// entry necessarily comes from before any URL-keyed entry for that source.
//
// Future maintainer note: if you're tempted to "fix" this asymmetry by
// deduping reversed-order entries too, read the docstring on mergeCitations
// first. This test will fail if you do — that failure is intentional.
func TestMergeCitations_DoesNotMergeReverseDirection(t *testing.T) {
	inputs := []mergeInput{
		{Text: "x [1]", Cites: []citation{{Num: 1, Label: "Foo", URL: "https://example.com/foo", Key: "https://example.com/foo"}}},
		{Text: "y [1]", Cites: []citation{{Num: 1, Label: "Foo", Key: "label:Foo"}}}, // legacy / different source
	}
	_, merged := mergeCitations(inputs)
	if len(merged) != 2 {
		t.Fatalf("URL-first then label-only-second must stay distinct (label collision risk), got %d: %+v", len(merged), merged)
	}
}

// TestMergeCitations_PreservesFirstAppearanceOrder — ordering by first
// appearance keeps the merged Sources section stable across runs.
func TestMergeCitations_PreservesFirstAppearanceOrder(t *testing.T) {
	inputs := []mergeInput{
		{Text: "x [1]", Cites: []citation{{Num: 1, Label: "Alpha", URL: "u-a", Key: "u-a"}}},
		{Text: "y [1]", Cites: []citation{{Num: 1, Label: "Beta", URL: "u-b", Key: "u-b"}}},
		{Text: "z [1]", Cites: []citation{{Num: 1, Label: "Alpha", URL: "u-a", Key: "u-a"}}},
	}
	_, merged := mergeCitations(inputs)
	if len(merged) != 2 {
		t.Fatalf("expected 2 unique merged citations, got %d", len(merged))
	}
	if merged[0].Label != "Alpha" || merged[1].Label != "Beta" {
		t.Errorf("first-appearance order broken: %v", []string{merged[0].Label, merged[1].Label})
	}
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	// quick hack for the test — this won't be called with large values
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}
