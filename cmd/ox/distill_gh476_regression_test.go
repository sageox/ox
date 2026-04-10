package main

import (
	"strings"
	"testing"

	"github.com/sageox/ox/internal/agentcli"
	"github.com/sageox/ox/internal/facts"
)

// --- A. End-to-end formatDailyMemory with citations ---

// TestFormatDailyMemory_AppendsSourcesSection — gh #476 happy path. Given a
// citation list, the rendered daily contains both the Sources numbered list
// AND the reference link definitions, plus the existing frontmatter.
//
// Failure prevented: the bug from #476 — daily summary cites a discussion
// learning but the reader has no way to drill into the original recording.
func TestFormatDailyMemory_AppendsSourcesSection(t *testing.T) {
	cites := []citation{
		{
			Num:   1,
			Label: "Discussion: 2026-03-10 — Architecture sync",
			URL:   "https://sageox.ai/team/team_x/media/recordings/rec_y",
			Key:   "https://sageox.ai/team/team_x/media/recordings/rec_y",
		},
		{
			Num:   2,
			Label: "sageox/ox#152",
			URL:   "https://github.com/sageox/ox/pull/152",
			Key:   "https://github.com/sageox/ox/pull/152",
		},
	}
	body := "## What We Learned\n\n- Natural language is more merge-friendly than code. [1]\n\n## What Shipped\n\n- Adopted rate limiting in [PR #152][2]."
	out := formatDailyMemory("2026-03-10", body, 0, 5, []string{"memory/.discussion-facts/x.jsonl", "memory/.github-facts/y.jsonl"}, cites)

	// Frontmatter still present (cache index input list)
	if !strings.HasPrefix(out, "---\nsources:\n") {
		t.Errorf("missing YAML frontmatter, got:\n%s", out)
	}
	if !strings.Contains(out, "memory/.discussion-facts/x.jsonl") {
		t.Errorf("frontmatter missing fact file path")
	}

	// Body preserved with citation markers intact
	if !strings.Contains(out, "Natural language is more merge-friendly than code. [1]") {
		t.Errorf("body lost bare citation marker")
	}
	if !strings.Contains(out, "Adopted rate limiting in [PR #152][2]") {
		t.Errorf("body lost ref-link citation")
	}

	// Sources section present
	if !strings.Contains(out, "## Sources") {
		t.Errorf("missing Sources section")
	}
	if !strings.Contains(out, "1. [Discussion: 2026-03-10 — Architecture sync][1]") {
		t.Errorf("missing numbered citation 1")
	}
	if !strings.Contains(out, "2. [sageox/ox#152][2]") {
		t.Errorf("missing numbered citation 2")
	}

	// Reference link definitions present so [1] / [PR #152][2] resolve when rendered
	if !strings.Contains(out, "[1]: https://sageox.ai/team/team_x/media/recordings/rec_y") {
		t.Errorf("missing ref definition for [1]")
	}
	if !strings.Contains(out, "[2]: https://github.com/sageox/ox/pull/152") {
		t.Errorf("missing ref definition for [2]")
	}

	// Footer is still last
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), "*Distilled from 5 facts*") {
		t.Errorf("footer missing or not last, got tail:\n%s", out[max(0, len(out)-200):])
	}
}

// TestFormatDailyMemory_OmitsSourcesSectionWhenNoCites — when no facts are
// citable, no Sources section is appended. Don't print an empty heading.
func TestFormatDailyMemory_OmitsSourcesSectionWhenNoCites(t *testing.T) {
	out := formatDailyMemory("2026-03-10", "Just narrative.", 3, 0, nil, nil)
	if strings.Contains(out, "## Sources") {
		t.Errorf("expected no Sources section when no cites:\n%s", out)
	}
}

// --- B. Pipeline: facts → prompt-input ---

// TestBuildFactCitationsForPrompt_RoundTrip ensures that flat facts → citations
// → prompt-input attaches the right citation number to each fact, and that
// uncitable facts are excluded entirely.
//
// Failure prevented: a fact-with-no-source silently becomes a [N] marker
// pointing at someone else's source, OR an uncitable fact gets a [0] number.
func TestBuildFactCitationsForPrompt_RoundTrip(t *testing.T) {
	flat := []facts.Fact{
		{
			Headline:    "Decision A",
			SourceType:  facts.SourceDiscussion,
			SourceURL:   "https://sageox.ai/team/t/media/recordings/rec_a",
			SourceTitle: "Discussion A",
			Timestamp:   "2026-03-10T00:00:00Z",
		},
		{
			// uncitable: no URL, no ref
			Headline:   "Stray observation",
			SourceType: facts.SourceObservation,
		},
		{
			Headline:    "Decision B",
			SourceType:  facts.SourceGitHub,
			SourceURL:   "https://github.com/sageox/ox/pull/152",
			SourceTitle: "Add rate limiting",
			Timestamp:   "2026-03-11T00:00:00Z",
		},
	}
	cites, factToNum := buildCitationsFromFacts(flat)
	citedFacts := buildFactCitationsForPrompt(flat, factToNum)

	if len(cites) != 2 {
		t.Fatalf("expected 2 citations (uncitable excluded), got %d", len(cites))
	}
	if len(citedFacts) != 2 {
		t.Fatalf("expected 2 cited facts in prompt input, got %d", len(citedFacts))
	}

	// fact[0] (Decision A) should map to citation 1
	if citedFacts[0].Num != 1 || citedFacts[0].Headline != "Decision A" {
		t.Errorf("fact[0] mapping wrong: num=%d headline=%q", citedFacts[0].Num, citedFacts[0].Headline)
	}
	// fact[2] (Decision B) — fact[1] was skipped — should map to citation 2
	if citedFacts[1].Num != 2 || citedFacts[1].Headline != "Decision B" {
		t.Errorf("fact[1] mapping wrong: num=%d headline=%q", citedFacts[1].Num, citedFacts[1].Headline)
	}
	// SourceTitle and Date should be threaded through to the prompt
	if citedFacts[0].SourceTitle != "Discussion A" {
		t.Errorf("SourceTitle not threaded: got %q", citedFacts[0].SourceTitle)
	}
	if citedFacts[0].Date != "2026-03-10" {
		t.Errorf("Date not extracted: got %q", citedFacts[0].Date)
	}
}

// --- C. Backwards compatibility with legacy fact files ---

// TestBuildCitationsFromFacts_LegacyFactsHaveNoSourceURL covers fact files
// written before #476 (no source_url field). They should still produce
// citations using source_ref as the dedup key — just without clickable URLs.
//
// Failure prevented: an existing team context with old fact files breaks
// catastrophically when distilled with the new pipeline.
func TestBuildCitationsFromFacts_LegacyFactsHaveNoSourceURL(t *testing.T) {
	// pre-#476 fact: source_ref only, no source_url, no source_title
	legacy := []facts.Fact{
		{
			Headline:   "old decision",
			SourceType: facts.SourceDiscussion,
			SourceRef:  "discussions/2026-01-15-1000-alice",
			Timestamp:  "2026-01-15T10:00:00Z",
		},
	}
	cites, factToNum := buildCitationsFromFacts(legacy)
	if len(cites) != 1 {
		t.Fatalf("expected 1 citation for legacy fact, got %d", len(cites))
	}
	if cites[0].URL != "" {
		t.Errorf("legacy fact should have empty URL (graceful), got %q", cites[0].URL)
	}
	// label should still be human-readable
	if !strings.Contains(cites[0].Label, "Discussion") {
		t.Errorf("legacy fact label should still mention source type, got %q", cites[0].Label)
	}
	if factToNum[0] != 1 {
		t.Errorf("legacy fact should still get citation number")
	}
}

// --- D. Multi-layer citation propagation ---

// TestMergeCitationsFromMarkdown_RealisticDailies simulates the weekly distill
// reading two daily summary files. Each has its own per-day numbering, and a
// shared discussion appears in both. After mergeCitationsFromMarkdown:
//   - the shared discussion appears ONCE in the merged list
//   - both dailies' prose markers point at the same global number
//
// This is the gh #476 cross-layer property the weekly stage relies on.
func TestMergeCitationsFromMarkdown_RealisticDailies(t *testing.T) {
	day1 := `# Daily Memory — 2026-03-10

## What We Learned

- Natural language is more merge-friendly than code. [1]
- Adopted rate limiting in [PR #152][2].

## Sources

1. [Discussion: 2026-03-10 — Architecture sync][1]
2. [sageox/ox#152][2]

[1]: https://sageox.ai/team/t/media/recordings/rec_arch
[2]: https://github.com/sageox/ox/pull/152

---
*Distilled from 0 observations and 2 facts*
`
	day2 := `# Daily Memory — 2026-03-11

## What Shipped

- Following the [architecture decision][1], rolled out the new auth layer.

## Sources

1. [Discussion: 2026-03-10 — Architecture sync][1]

[1]: https://sageox.ai/team/t/media/recordings/rec_arch

---
*Distilled from 0 observations and 1 facts*
`

	rewritten, merged := mergeCitationsFromMarkdown([]string{day1, day2})

	// Architecture sync was [1] in day1 and [1] in day2 — should dedupe to ONE entry
	archCount := 0
	prCount := 0
	for _, c := range merged {
		if strings.Contains(c.Label, "Architecture sync") {
			archCount++
		}
		if strings.Contains(c.Label, "sageox/ox#152") {
			prCount++
		}
	}
	if archCount != 1 {
		t.Errorf("expected Architecture sync to appear once in merged, got %d", archCount)
	}
	if prCount != 1 {
		t.Errorf("expected PR #152 to appear once in merged, got %d", prCount)
	}
	if len(merged) != 2 {
		t.Errorf("expected exactly 2 merged citations, got %d", len(merged))
	}

	// both dailies' arch-discussion markers should now point at the same global number.
	// In day1 the arch was [1]; in day2 it was [1] too — they should match in the rewritten output.
	if !strings.Contains(rewritten[0], "Natural language is more merge-friendly than code. [1]") {
		t.Errorf("day1 marker should still be [1] after merge, got:\n%s", rewritten[0])
	}
	if !strings.Contains(rewritten[1], "[architecture decision][1]") {
		t.Errorf("day2 should reference the merged citation [1], got:\n%s", rewritten[1])
	}
	// PR #152 was [2] in day1 and absent in day2 — stays as [2] in day1
	if !strings.Contains(rewritten[0], "[PR #152][2]") {
		t.Errorf("day1 PR marker should still be [2], got:\n%s", rewritten[0])
	}
}

// --- E. Cache index regression: frontmatter still parseable ---

// TestFormatDailyMemory_FrontmatterStillParseable — gh #476 explicitly preserved
// the YAML frontmatter for cache-index dedup. Make sure parseDailySources can
// still read it back after we add the Sources section in the body.
//
// Failure prevented: cache index can't track inputs → infinite re-distillation
// loops that re-process every fact on every run.
func TestFormatDailyMemory_FrontmatterStillParseable(t *testing.T) {
	cites := []citation{
		{Num: 1, Label: "Discussion: x", URL: "https://example.com/x", Key: "https://example.com/x"},
	}
	sources := []string{
		"memory/.discussion-facts/2026-03-10-abc.jsonl",
		"memory/.github-facts/2026-03-10-def.jsonl",
		"memory/.observations/2026-03-10/ghi.jsonl",
	}
	out := formatDailyMemory("2026-03-10", "Body. [1]", 1, 2, sources, cites)

	parsed := parseDailySources(out)
	if len(parsed) != 3 {
		t.Fatalf("parseDailySources lost entries: got %v", parsed)
	}
	for i, want := range sources {
		if parsed[i] != want {
			t.Errorf("source[%d] = %q, want %q", i, parsed[i], want)
		}
	}
}

// --- F. Observations in frontmatter, not in citations (per #476 design) ---

// TestObservationSourcePaths_ConvertsToRelative ensures observation source
// files are recorded in the frontmatter as relative paths (so the cache index
// keys match across machines), but observations don't appear in the citation
// list (per design — they're weaved into prose without [N] markers).
func TestObservationSourcePaths_ConvertsToRelative(t *testing.T) {
	tcPath := "/team/context"
	obs := []distillObservation{
		{Content: "obs 1", SourceFile: "/team/context/memory/.observations/2026-03-10/abc.jsonl"},
		{Content: "obs 2", SourceFile: "/team/context/memory/.observations/2026-03-10/abc.jsonl"}, // duplicate file
		{Content: "obs 3", SourceFile: "/team/context/memory/.observations/2026-03-10/def.jsonl"},
	}
	got := observationSourcePaths(tcPath, obs)
	if len(got) != 2 {
		t.Fatalf("expected 2 unique source paths, got %d: %v", len(got), got)
	}
	if got[0] != "memory/.observations/2026-03-10/abc.jsonl" {
		t.Errorf("got[0] = %q, want relative path", got[0])
	}
	if got[1] != "memory/.observations/2026-03-10/def.jsonl" {
		t.Errorf("got[1] = %q, want relative path", got[1])
	}
}

// --- G. mirrorGitHubSourceURL (CodeRabbit round-1 #4) ---

// TestMirrorGitHubSourceURL covers the cases mirrorGitHubSourceURL is
// expected to handle: http(s) URLs are mirrored, non-URL refs are skipped,
// existing source_url is never overwritten, and leading/trailing whitespace
// in source_ref is trimmed before the scheme check.
//
// Failure prevented: github fact files with stray whitespace in source_ref
// (LLM extractor occasionally emits these) silently produce linkless citations.
func TestMirrorGitHubSourceURL(t *testing.T) {
	tests := []struct {
		name           string
		in             facts.Fact
		wantURL        string
		wantNormalized string // expected source_ref after the function returns (may be trimmed)
	}{
		{
			name:           "https url mirrored",
			in:             facts.Fact{SourceType: facts.SourceGitHub, SourceRef: "https://github.com/sageox/ox/pull/152"},
			wantURL:        "https://github.com/sageox/ox/pull/152",
			wantNormalized: "https://github.com/sageox/ox/pull/152",
		},
		{
			name:           "http url mirrored",
			in:             facts.Fact{SourceType: facts.SourceGitHub, SourceRef: "http://example.com/x"},
			wantURL:        "http://example.com/x",
			wantNormalized: "http://example.com/x",
		},
		{
			name:           "leading whitespace trimmed and mirrored",
			in:             facts.Fact{SourceType: facts.SourceGitHub, SourceRef: "  https://github.com/sageox/ox/pull/1"},
			wantURL:        "https://github.com/sageox/ox/pull/1",
			wantNormalized: "https://github.com/sageox/ox/pull/1",
		},
		{
			name:           "trailing whitespace trimmed and mirrored",
			in:             facts.Fact{SourceType: facts.SourceGitHub, SourceRef: "https://github.com/sageox/ox/pull/1\n"},
			wantURL:        "https://github.com/sageox/ox/pull/1",
			wantNormalized: "https://github.com/sageox/ox/pull/1",
		},
		{
			name:           "non-url ref skipped",
			in:             facts.Fact{SourceType: facts.SourceGitHub, SourceRef: "github.com/sageox/ox/pull/1"},
			wantURL:        "",
			wantNormalized: "github.com/sageox/ox/pull/1",
		},
		{
			name:           "empty ref skipped",
			in:             facts.Fact{SourceType: facts.SourceGitHub, SourceRef: ""},
			wantURL:        "",
			wantNormalized: "",
		},
		{
			name:           "existing source_url not overwritten",
			in:             facts.Fact{SourceType: facts.SourceGitHub, SourceRef: "https://github.com/a/b/pull/1", SourceURL: "https://override.example.com"},
			wantURL:        "https://override.example.com",
			wantNormalized: "https://github.com/a/b/pull/1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := []facts.Fact{tt.in}
			mirrorGitHubSourceURL(fs)
			if fs[0].SourceURL != tt.wantURL {
				t.Errorf("SourceURL = %q, want %q", fs[0].SourceURL, tt.wantURL)
			}
			if fs[0].SourceRef != tt.wantNormalized {
				t.Errorf("SourceRef = %q, want %q", fs[0].SourceRef, tt.wantNormalized)
			}
		})
	}
}

// --- H. Type-check sentinel: agentcli.FactCitation roundtrip ---

// TestAgentcliFactCitation_FieldNames is a sentinel test that the FactCitation
// struct exposes the fields buildFactCitationsForPrompt populates. If the
// struct is renamed without updating callers, this test pins the contract.
func TestAgentcliFactCitation_FieldNames(t *testing.T) {
	fc := agentcli.FactCitation{
		Num:         1,
		Headline:    "h",
		Summary:     "s",
		Rationale:   "r",
		Who:         "w",
		SourceType:  "discussion",
		SourceTitle: "t",
		Date:        "2026-03-10",
		Category:    "decision",
	}
	if fc.Num != 1 || fc.Headline != "h" {
		t.Fatal("FactCitation field check failed")
	}
}
