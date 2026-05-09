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

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/kb"
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
