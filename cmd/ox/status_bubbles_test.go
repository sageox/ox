package main

// status_bubbles_test.go — coverage for the `ox status` knowledge-bubbles
// summary line. Tests the two pure functions that produce the user-visible
// strings (formatBubblesLine, summarizeBubbles) and the JSON envelope
// construction. See ox-gzp.14.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/kb"
	"github.com/sageox/ox/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeBubblesMerger is a minimal stand-in for *kb.Merger used by tests
// that only care about how status renders the result. Production wires
// the real merger; tests use this seam to skip the API + auth dance.
type fakeBubblesMerger struct {
	res kb.MergeResult
	err error
}

func (f *fakeBubblesMerger) Merge(_ context.Context) (kb.MergeResult, error) {
	return f.res, f.err
}

// TestFormatBubblesLine_MixedTypes verifies the canonical example from the
// plan: total + per-type breakdown in the documented order.
//
// Failure prevented: per-type counts render in the wrong order or under
// the wrong total, drifting the user-visible format away from the spec
// other docs and skills will quote.
func TestFormatBubblesLine_MixedTypes(t *testing.T) {
	t.Parallel()

	s := statusBubblesSummary{
		Total: 5,
		ByType: map[string]int{
			"personal": 1,
			"profile":  1,
			"team":     2,
			"repo":     1,
		},
	}

	got := formatBubblesLine(s)
	want := "Knowledge bubbles: 5 (1 personal, 1 profile, 2 team, 1 repo)"
	assert.Equal(t, want, got)
}

// TestFormatBubblesLine_Empty verifies the zero-bubble shape: no parens,
// no per-type breakdown.
//
// Failure prevented: an empty list rendering as "Knowledge bubbles: 0 ()"
// or hiding the line entirely — the first leaves dangling parens, the
// second confuses users who expect to see the field at all times.
func TestFormatBubblesLine_Empty(t *testing.T) {
	t.Parallel()

	s := statusBubblesSummary{Total: 0, ByType: map[string]int{}}
	assert.Equal(t, "Knowledge bubbles: 0", formatBubblesLine(s))
}

// TestFormatBubblesLine_AllOneType verifies the line collapses to a single
// breakdown segment when only one bucket has entries.
//
// Failure prevented: the renderer pads zero buckets ("3 team, 0 personal")
// instead of skipping them, making the line longer than the plan spec.
func TestFormatBubblesLine_AllOneType(t *testing.T) {
	t.Parallel()

	s := statusBubblesSummary{
		Total:  3,
		ByType: map[string]int{"team": 3},
	}
	assert.Equal(t, "Knowledge bubbles: 3 (3 team)", formatBubblesLine(s))
}

// TestFormatBubblesLine_SkipsZeroBuckets verifies that a per-type map
// containing explicit zero entries does NOT contribute a "0 X" segment.
// summarizeBubbles drops zeros, but renderers must also defend against
// callers that pre-populate zero buckets.
//
// Failure prevented: a future caller stores all known types up front
// (with zeros for unused buckets) and the line bloats to "1 personal,
// 0 profile, 2 team" — exactly the pre-collapse format the plan
// rejected.
func TestFormatBubblesLine_SkipsZeroBuckets(t *testing.T) {
	t.Parallel()

	s := statusBubblesSummary{
		Total: 3,
		ByType: map[string]int{
			"personal": 1,
			"profile":  0, // must be skipped, not rendered as "0 profile"
			"team":     2,
		},
	}
	got := formatBubblesLine(s)
	assert.Equal(t, "Knowledge bubbles: 3 (1 personal, 2 team)", got)
	assert.NotContains(t, got, "profile")
}

// TestFormatBubblesLine_Unavailable verifies the merger-error fallback
// renders without per-type breakdown so the rest of `ox status` can still
// surround it.
//
// Failure prevented: a transient merger failure breaks the entire status
// command instead of degrading to a single "(unavailable)" cell.
func TestFormatBubblesLine_Unavailable(t *testing.T) {
	t.Parallel()

	s := statusBubblesSummary{Unavailable: true}
	assert.Equal(t, "Knowledge bubbles: (unavailable)", formatBubblesLine(s))
}

// TestFormatBubblesLine_ForwardCompatType verifies an unknown server-side
// type slug still renders rather than being silently dropped from the
// breakdown.
//
// Failure prevented: server rolls out a sixth type before the CLI knows
// about it; the row counts toward Total but vanishes from the breakdown,
// confusing users about why the numbers don't add up.
func TestFormatBubblesLine_ForwardCompatType(t *testing.T) {
	t.Parallel()

	s := statusBubblesSummary{
		Total: 2,
		ByType: map[string]int{
			"team":    1,
			"unknown": 1,
		},
	}
	got := formatBubblesLine(s)
	assert.Equal(t, "Knowledge bubbles: 2 (1 team, 1 unknown)", got)
}

// TestSummarizeBubbles_Counts verifies the merger-result-to-counts mapping
// includes legacy rows (which carry KBTypeTeam / KBTypeRepo synthesized
// by the merger) under their type bucket — no separate "legacy" tally,
// per the bead.
//
// Failure prevented: legacy rows surface as a separate "legacy" count
// (or worse, vanish from the total) instead of folding cleanly into
// their type bucket.
func TestSummarizeBubbles_Counts(t *testing.T) {
	t.Parallel()

	res := kb.MergeResult{
		Bubbles: []kb.Bubble{
			{Type: api.KBTypePersonal},
			{Type: api.KBTypeTeam, Legacy: true}, // legacy still counts as team
			{Type: api.KBTypeTeam},
			{Type: api.KBTypeRepo, Legacy: true}, // legacy still counts as repo
		},
	}
	s := summarizeBubbles(res)
	assert.Equal(t, 4, s.Total)
	assert.Equal(t, 1, s.ByType["personal"])
	assert.Equal(t, 2, s.ByType["team"])
	assert.Equal(t, 1, s.ByType["repo"])
	_, hasLegacy := s.ByType["legacy"]
	assert.False(t, hasLegacy, "no separate 'legacy' bucket — legacy folds into its type")
}

// TestSummarizeBubbles_EmptyOrUnknownTypeCollapses verifies forward-compat:
// rows with an empty Type field bucket as "unknown" rather than crashing
// or vanishing from the total.
//
// Failure prevented: a malformed server response with an empty kb_type
// produces silently-dropped rows or a "" key in the output JSON.
func TestSummarizeBubbles_EmptyOrUnknownTypeCollapses(t *testing.T) {
	t.Parallel()

	res := kb.MergeResult{
		Bubbles: []kb.Bubble{
			{Type: ""},                   // collapses to unknown
			{Type: api.KBTypeUnknown},    // already unknown
			{Type: api.KBType("future")}, // a future type — kept as-is
		},
	}
	s := summarizeBubbles(res)
	assert.Equal(t, 3, s.Total)
	assert.Equal(t, 2, s.ByType["unknown"])
	assert.Equal(t, 1, s.ByType["future"])
	_, hasEmpty := s.ByType[""]
	assert.False(t, hasEmpty, "empty type must not appear as a literal '' key")
}

// TestSummarizeBubbles_PassesWarnings verifies per-source warnings flow
// through the summary unmodified so renderers can decide what to show.
//
// Failure prevented: the renderer never receives the warnings slice and
// silently swallows partial-failure notifications the user needs to act
// on.
func TestSummarizeBubbles_PassesWarnings(t *testing.T) {
	t.Parallel()

	res := kb.MergeResult{
		Warnings: []kb.SourceWarning{
			{Source: kb.SourceTeamLegacy, Err: "boom"},
		},
	}
	s := summarizeBubbles(res)
	require.Len(t, s.Warnings, 1)
	assert.Equal(t, kb.SourceTeamLegacy, s.Warnings[0].Source)
	assert.Equal(t, "boom", s.Warnings[0].Err)
}

// TestRenderBubblesLine_AppendsWarningHint verifies the human renderer
// appends "(warnings: see ox doctor)" when the merger flagged any
// per-source warnings, but suppresses it for the unavailable case
// (which already shows its own muted message).
//
// Failure prevented: warnings are silently dropped from human output
// (users don't know to run `ox doctor`), or the unavailable case
// double-warns confusingly.
func TestRenderBubblesLine_AppendsWarningHint(t *testing.T) {
	t.Parallel()

	withWarn := statusBubblesSummary{
		Total:  1,
		ByType: map[string]int{"team": 1},
		Warnings: []kb.SourceWarning{
			{Source: kb.SourceTeamLegacy, Err: "boom"},
		},
	}
	got := renderBubblesLine(withWarn)
	assert.Contains(t, got, "warnings: see ox doctor",
		"warnings hint must appear when merger reports per-source errors")

	unavail := statusBubblesSummary{
		Unavailable: true,
		Warnings: []kb.SourceWarning{
			{Source: kb.SourceTeamLegacy, Err: "boom"},
		},
	}
	got = renderBubblesLine(unavail)
	assert.NotContains(t, got, "warnings: see ox doctor",
		"unavailable case has its own messaging — must not double-warn")
}

// TestBuildBubblesJSON_PopulatesByType verifies the JSON envelope mirrors
// the human output: total + by_type map keyed by type slug.
//
// Failure prevented: scriptable consumers parsing the JSON see a missing
// or differently-shaped bubbles field after a merger refactor.
func TestBuildBubblesJSON_PopulatesByType(t *testing.T) {
	t.Parallel()

	s := statusBubblesSummary{
		Total: 5,
		ByType: map[string]int{
			"personal": 1,
			"profile":  1,
			"team":     2,
			"repo":     1,
		},
	}
	js := buildBubblesJSON(s)
	require.NotNil(t, js)
	assert.Equal(t, 5, js.Total)
	assert.Equal(t, 1, js.ByType["personal"])
	assert.Equal(t, 1, js.ByType["profile"])
	assert.Equal(t, 2, js.ByType["team"])
	assert.Equal(t, 1, js.ByType["repo"])
	assert.Empty(t, js.Warnings)
}

// TestBuildBubblesJSON_Unavailable verifies the unavailable case surfaces
// a synthetic warning rather than omitting the field entirely. JSON
// consumers should see "the merger ran but produced nothing", not a
// silently-missing key.
//
// Failure prevented: bubbles field is absent on merger error, leaving
// callers unable to distinguish "no bubbles" from "ox can't tell you
// right now".
func TestBuildBubblesJSON_Unavailable(t *testing.T) {
	t.Parallel()

	s := statusBubblesSummary{Unavailable: true}
	js := buildBubblesJSON(s)
	require.NotNil(t, js)
	assert.Equal(t, 0, js.Total)
	require.Len(t, js.Warnings, 1)
	assert.Equal(t, "merger", js.Warnings[0].Source)
}

// TestBuildStatusJSON_BubblesAndLegacyMirrorsCoexist verifies the kb plan's
// "deprecated mirrors stay one release" rule: bubbles is populated
// alongside the existing ledger / team_contexts fields, not in place of
// them.
//
// Failure prevented: a regression that drops Ledger or TeamContexts from
// the JSON output the moment Bubbles is added, breaking pre-migration
// consumers that haven't switched fields yet.
func TestBuildStatusJSON_BubblesAndLegacyMirrorsCoexist(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	localCfg := &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: tmpDir},
	}

	bubbles := statusBubblesSummary{
		Total:  3,
		ByType: map[string]int{"team": 2, "repo": 1},
	}

	output := buildStatusJSON(
		false, nil, nil, "test.sageox.ai", "/tmp/auth.json", false,
		"/tmp/config", "/tmp/cwd", "/tmp/cwd/.sageox", false,
		localCfg, "", nil, nil,
		nil, nil,
		bubbles,
	)

	require.NotNil(t, output.Bubbles, "bubbles section must be populated")
	assert.Equal(t, 3, output.Bubbles.Total)
	assert.Equal(t, 2, output.Bubbles.ByType["team"])

	require.NotNil(t, output.Ledger, "ledger mirror must remain populated for one release")
	assert.True(t, output.Ledger.Configured)
}

// TestCollectBubblesSummary_MergerErrorMarksUnavailable verifies a merger
// error degrades to Unavailable=true — the calling status renderer must
// keep working.
//
// Failure prevented: a network blip during merge propagates as an error
// out of `ox status`, hiding all the other status info the user needs.
func TestCollectBubblesSummary_MergerErrorMarksUnavailable(t *testing.T) {
	t.Parallel()

	merger := &fakeBubblesMerger{err: errors.New("boom")}
	s := collectBubblesSummary(merger)
	assert.True(t, s.Unavailable, "merger error must collapse to Unavailable=true")
	assert.Equal(t, 0, s.Total)
}

// TestCollectBubblesSummary_NilMergerIsUnavailable verifies the defensive
// nil-merger path. Tests don't need to pass real plumbing.
//
// Failure prevented: a nil-pointer panic in the rare path where the
// merger constructor returns nil (e.g., during early-init).
func TestCollectBubblesSummary_NilMergerIsUnavailable(t *testing.T) {
	t.Parallel()

	s := collectBubblesSummary(nil)
	assert.True(t, s.Unavailable)
}

// TestRenderBubblesLine_FormatStringMatchesPlan verifies the rendered
// human line — once styling is stripped — matches the literal shape
// quoted in the plan and other docs. Anchors the format string so
// downstream skills/agents can pattern-match it.
//
// Failure prevented: a styling refactor sneaks a stray space or
// punctuation change into the line, breaking grep-based skills.
func TestRenderBubblesLine_FormatStringMatchesPlan(t *testing.T) {
	t.Parallel()

	s := statusBubblesSummary{
		Total: 5,
		ByType: map[string]int{
			"personal": 1,
			"profile":  1,
			"team":     2,
			"repo":     1,
		},
	}
	got := renderBubblesLine(s)
	clean := stripANSIBubbles(got)
	assert.Contains(t, clean, "Knowledge bubbles")
	assert.Contains(t, clean, "5 (1 personal, 1 profile, 2 team, 1 repo)")
}

// stripANSIBubbles removes ANSI escape sequences so tests assert on textual
// content, not styling. lipgloss may emit colors when the test harness
// detects a TTY-like env.
func stripANSIBubbles(s string) string {
	for {
		i := strings.Index(s, "\x1b[")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "m")
		if j < 0 {
			return s
		}
		s = s[:i] + s[i+j+1:]
	}
}

// --- Dense knowledge-bubble listing (owner-grouped @slug tree) ---

// TestBubblesCountSummary verifies the section header reports the merger
// total and the rendered count truthfully, without claiming a precise
// in-repo split the two data sources can't guarantee.
//
// Failure prevented: a "N in this repo" claim drifts from reality because
// the merger total counts kb-API/personal bubbles the team-context list omits.
func TestBubblesCountSummary(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "13 total · 7 owners",
		bubblesCountSummary(statusBubblesSummary{Total: 13}, 7))

	// merger total unavailable → fall back to the rendered count alone
	assert.Equal(t, "7 owners listed",
		bubblesCountSummary(statusBubblesSummary{Unavailable: true}, 7))
	assert.Equal(t, "1 owner listed",
		bubblesCountSummary(statusBubblesSummary{Total: 0}, 1))
}

// TestSortOtherBubbleRows_NeedsAttentionFirst verifies dirty/wedged/not-cloned
// rows float to the top, then alphabetical by name.
//
// Failure prevented: a team with uncommitted work is buried mid-list where
// the user scrolls past it.
func TestSortOtherBubbleRows_NeedsAttentionFirst(t *testing.T) {
	t.Parallel()

	rows := []otherTeamRow{
		{name: "Zulu", cloned: true},                      // clean
		{name: "Alpha", cloned: true},                     // clean
		{name: "Bravo", cloned: true, attention: true},    // dirty
		{name: "Charlie", cloned: false, attention: true}, // not cloned
	}
	sortOtherBubbleRows(rows)

	order := []string{rows[0].name, rows[1].name, rows[2].name, rows[3].name}
	// attention rows first (Bravo, Charlie alphabetical), then clean (Alpha, Zulu)
	assert.Equal(t, []string{"Bravo", "Charlie", "Alpha", "Zulu"}, order)
}

// TestRenderOtherBubbleRows verifies the owner-grouped tree: each owner is a
// parent node `@slug  (Display Name)` with its bubble nested under a tree glyph
// as `(team)` + status. Slugs are never truncated, the shared on-disk prefix
// prints once, private is the silent default while PUBLIC is flagged, and full
// paths appear only under verbose.
//
// Failure prevented: the section regresses to repeating a full path per row,
// hides per-bubble status, truncates a slug with "…", buries the owner↔bubble
// relationship, or clutters every row with a redundant "private" marker.
func TestRenderOtherBubbleRows(t *testing.T) {
	t.Parallel()

	base := "/home/u/.local/share/sageox/sageox.ai/teams"
	longSlug := "dry-run-ice-cream-2026-05-01" // would be truncated under a 1-line layout
	rows := []otherTeamRow{
		{
			name: "SageOx Internal", slug: "sageox-internal", bubbleType: "team", visibility: "private",
			path: base + "/team_aaa", cloned: true, attention: true,
			st: gitRepoStatus{Exists: true, UncommittedCount: 6},
		},
		{
			name: "Dry Run - Ice Cream 2026-05-01", slug: longSlug, bubbleType: "team", visibility: "private",
			path: base + "/team_bbb", cloned: true,
			st: gitRepoStatus{Exists: true, HasLastSync: true, LastSync: time.Now().Add(-2 * time.Hour)},
		},
		{
			name: "Open Docs", slug: "open-docs", bubbleType: "team", visibility: "public",
			path: base + "/team_ccc", cloned: true,
			st: gitRepoStatus{Exists: true, HasLastSync: true, LastSync: time.Now().Add(-5 * time.Minute)},
		},
	}

	out := stripANSIBubbles(renderOtherBubbleRows(rows, false, false))

	// owner parent node: full @slug (never truncated) + display name in parens
	assert.Contains(t, out, "@sageox-internal  (SageOx Internal)")
	assert.Contains(t, out, "@"+longSlug+"  (Dry Run - Ice Cream 2026-05-01)", "slug must render in full — no ellipsis")
	assert.NotContains(t, out, "…", "slugs are never shortened with an ellipsis")
	assert.NotContains(t, out, "team_aaa", "opaque ids hidden unless --verbose")

	// the bubble nests under the owner as (team) with a tree glyph
	assert.Contains(t, out, ui.TreeLast+"(team)")

	// shared prefix printed once as a header, never per row
	assert.Equal(t, 1, strings.Count(out, "on disk"))

	// per-bubble status: crisp age for clean, count for dirty
	assert.Contains(t, out, "✓ 5m", "clean repo shows freshness age, not the word synced")
	assert.Contains(t, out, "⚠ 6 uncommitted")

	// visibility: private is silent, only PUBLIC is flagged
	assert.NotContains(t, out, "private", "private is the silent default — no marker")
	assert.Contains(t, out, "PUBLIC", "public bubbles are flagged")

	// verbose reveals the full on-disk path
	verbose := stripANSIBubbles(renderOtherBubbleRows(rows, false, true))
	assert.Contains(t, verbose, base+"/team_aaa")
}

// TestRenderBubbleStatus covers each status cell variant and that the freshness
// age (not the word "synced") is the clean-repo signal.
func TestRenderBubbleStatus(t *testing.T) {
	t.Parallel()

	clean := stripANSIBubbles(renderBubbleStatus(
		gitRepoStatus{Exists: true, HasLastSync: true, LastSync: time.Now().Add(-3 * 24 * time.Hour)}, true, false))
	assert.Equal(t, "✓ 3d", clean)

	dirty := stripANSIBubbles(renderBubbleStatus(gitRepoStatus{Exists: true, UncommittedCount: 18}, true, false))
	assert.Equal(t, "⚠ 18 uncommitted", dirty)

	wedged := stripANSIBubbles(renderBubbleStatus(gitRepoStatus{Exists: true, RebaseInProgress: true}, true, false))
	assert.Equal(t, "⚠ rebase wedged", wedged)

	// the other IsWedged() branch: diverged (both ahead and behind)
	diverged := stripANSIBubbles(renderBubbleStatus(gitRepoStatus{Exists: true, AheadCount: 2, BehindCount: 1}, true, false))
	assert.Equal(t, "⚠ diverged", diverged)

	notCloned := stripANSIBubbles(renderBubbleStatus(gitRepoStatus{}, false, false))
	assert.Equal(t, "⚠ not cloned", notCloned)
}

// TestRenderSlugRef verifies the sigil and slug are rendered as distinct
// segments so the muted sigil + bright slug styling can apply per the design.
func TestRenderSlugRef(t *testing.T) {
	t.Parallel()

	out := stripANSIBubbles(renderSlugRef("@", "sageox"))
	assert.Equal(t, "@sageox", out)
	out = stripANSIBubbles(renderSlugRef("#", "marketing"))
	assert.Equal(t, "#marketing", out)
}

// TestCommonBubbleBase verifies the "on disk" prefix is the directory shared by
// ALL rows, so it stays accurate if bubbles ever live under different parents
// (e.g. teams/ and kb/) — not just the first attention-sorted row's parent.
func TestCommonBubbleBase(t *testing.T) {
	t.Parallel()

	d := "/home/u/.local/share/sageox/sageox.ai"
	// all under teams/ → that shared parent
	same := []otherTeamRow{
		{path: d + "/teams/team_a"},
		{path: d + "/teams/team_b"},
	}
	assert.Equal(t, d+"/teams", commonBubbleBase(same))

	// mixed teams/ and kb/ → the endpoint dir they share, not teams/
	mixed := []otherTeamRow{
		{path: d + "/teams/team_a"},
		{path: d + "/kb/kb_x"},
	}
	assert.Equal(t, d, commonBubbleBase(mixed))
}
